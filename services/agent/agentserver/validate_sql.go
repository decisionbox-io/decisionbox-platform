package agentserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// sqlValidationJobsCollection is the on-disk Mongo collection name.
// Mirrored from services/api/database/sql_validation_job_repo.go — the
// agent cannot import the api package, so the constant lives in both
// places. Keep in sync.
const sqlValidationJobsCollection = "sql_validation_jobs"

// sqlValidationMaxStatementsEnv caps how many statements a single job may
// carry. A batch over the cap is failed at the job level (a runaway or
// abusive caller shouldn't be able to fire unbounded compile round-trips
// at the warehouse). Empty / invalid → defaultSQLValidationMaxStatements;
// a value <= 0 disables the cap entirely.
const sqlValidationMaxStatementsEnv = "SQL_VALIDATION_MAX_STATEMENTS"

// defaultSQLValidationMaxStatements is generous enough for any realistic
// batch while still bounding a pathological job.
const defaultSQLValidationMaxStatements = 500

// runValidateSQL is the entrypoint for `agent --mode=validate-sql`.
// It claims the SQLValidationJob row (pending → running), builds the
// project's warehouse provider via the same initWarehouseProvider path
// discovery uses, compile-checks each statement through
// warehouse.Provider.ValidateSQL (compile-only, no execution), writes the
// per-statement verdicts back to the job doc, and marks the job complete
// (or failed). A heartbeat goroutine writes heartbeat_at the whole time,
// same as validate-doc.
//
// Unlike validate-doc — which the API worker claims (pending → running)
// before spawning the agent — SQL validation has no in-API worker: a
// caller enqueues the job and dispatches the agent directly. So the agent
// claims the job itself here. The claim is atomic (FindOneAndUpdate on
// status=pending), so a double-dispatch resolves to exactly one runner.
//
// Stale recovery is therefore the dispatcher's responsibility, not this
// package's: if the agent process is killed mid-run (OOM, pod eviction)
// the row stays `running` until the dispatcher acts. The model carries
// the same heartbeat_at / claimed_at / attempt fields validate-doc's
// RequeueStale uses, so a caller can detect a cold heartbeat and re-claim
// or fail the row. An in-API stale-recovery worker for
// sql_validation_jobs is intentionally not part of this primitive (it
// adds no HTTP/worker surface). Terminal rows TTL out after 7 days (see
// services/api/database/init.go).
//
// Exit contract (mirrors runValidateDoc):
//   - returns nil on success (job marked completed)
//   - returns a non-nil error on any failure that prevents completing the
//     run — Run() propagates the exit code so the caller sees the
//     non-zero status and the job's `error` field surfaces the reason
//
// Per-statement compile failures are NOT run failures: they are recorded
// as {ok:false, error:<warehouse message>} in the results.
func runValidateSQL(cfg *config.Config, jobID string) error {
	if jobID == "" {
		return errors.New("--job-id is required for --mode=validate-sql")
	}
	applog.WithField("job_id", jobID).Info("Validate-sql agent starting")

	ctx := context.Background()
	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()
	db := database.New(mongoClient)
	jobsCol := db.Collection(sqlValidationJobsCollection)

	// Atomically claim the job (pending → running, bump attempt). The
	// returned doc reflects the post-claim state.
	claim, err := claimSQLValidationJob(ctx, jobsCol, jobID)
	if err != nil {
		return fmt.Errorf("claim sql_validation_job: %w", err)
	}
	if claim == nil {
		// Nothing was claimable — disambiguate not-found from
		// already-claimed/terminal so the error is actionable.
		existing, lookupErr := loadSQLValidationJobClaim(ctx, jobsCol, jobID)
		if lookupErr != nil {
			return fmt.Errorf("lookup sql_validation_job: %w", lookupErr)
		}
		if existing == nil {
			return fmt.Errorf("sql_validation_job %s not found", jobID)
		}
		return fmt.Errorf("sql_validation_job %s status is %q (expected pending)", jobID, existing.Status)
	}
	attempt := claim.Attempt

	// Set the project id in the context so warehouse middleware (e.g.
	// governance) sees it, the same way discovery / test-connection do.
	ctx = gowarehouse.WithProjectID(ctx, claim.ProjectID)

	logger := applog.WithFields(applog.Fields{
		"job_id":     jobID,
		"project_id": claim.ProjectID,
		"attempt":    attempt,
	})

	// Heartbeat goroutine — writes every heartbeatInterval until the run
	// finishes. Filtered on (id, attempt, status=running) so a
	// stale-recovered job's orphan agent silently no-ops. Reuses the
	// validate-doc heartbeat loop (it is collection-agnostic).
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go heartbeatLoop(heartbeatCtx, jobsCol, jobID, attempt)

	if err := setStartedAt(ctx, jobsCol, jobID, attempt); err != nil {
		logger.WithError(err).Warn("set started_at failed")
	}

	if rErr := runValidateSQLInner(ctx, db, jobsCol, claim, attempt); rErr != nil {
		stopHeartbeat()
		_ = markJobFailed(context.Background(), jobsCol, jobID, attempt, rErr.Error())
		logger.WithError(rErr).Warn("Validate-sql agent failed")
		return rErr
	}

	stopHeartbeat()
	if err := markJobCompleted(context.Background(), jobsCol, jobID, attempt); err != nil {
		logger.WithError(err).Warn("mark completed failed")
		return err
	}
	logger.Info("Validate-sql agent completed")
	return nil
}

func runValidateSQLInner(ctx context.Context, db *database.DB, jobsCol *mongo.Collection, claim *sqlValidationJobClaim, attempt int) error {
	// Statements are decoded separately from the claim so a malformed
	// `statements` field fails the job (caught here → markJobFailed)
	// instead of breaking the atomic claim that flipped status to
	// running.
	statements, err := loadSQLValidationStatements(ctx, jobsCol, claim.ID)
	if err != nil {
		return fmt.Errorf("decode statements: %w", err)
	}

	if err := checkSQLBatchSize(len(statements)); err != nil {
		return err
	}

	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, claim.ProjectID)
	if err != nil {
		return fmt.Errorf("load project %s: %w", claim.ProjectID, err)
	}
	if project == nil {
		return fmt.Errorf("project %s not found", claim.ProjectID)
	}

	secretProvider, err := initSecretProvider(db.Client())
	if err != nil {
		return err
	}
	warehouseProvider, err := initWarehouseProvider(ctx, project, secretProvider, project.ID)
	if err != nil {
		return err
	}
	defer warehouseProvider.Close()

	results := validateStatements(ctx, warehouseProvider, statements)

	if err := writeSQLValidationResults(ctx, jobsCol, claim.ID, attempt, results); err != nil {
		return fmt.Errorf("write results: %w", err)
	}

	failed := 0
	for _, r := range results {
		if !r.OK {
			failed++
		}
	}
	applog.WithFields(applog.Fields{
		"job_id":     claim.ID,
		"statements": len(statements),
		"failed":     failed,
	}).Info("Validate-sql batch complete")
	return nil
}

// sqlValidator is the slice of warehouse.Provider that the batch loop
// needs. Narrowing to this one method keeps the loop unit-testable with a
// tiny fake (the full warehouse.Provider interface is large).
type sqlValidator interface {
	ValidateSQL(ctx context.Context, sql string) error
}

// validateStatements compile-checks each statement through the
// warehouse's native compile-only path and returns one verdict per
// statement, in the same order. A per-statement compile failure is
// recorded as {ok:false, error:<warehouse message>} — never a batch-level
// error. ValidateSQL is "compile, don't execute", so a single statement
// (an INSERT/DELETE) compiles but mutates nothing.
//
// Multi-statement input is rejected up front (ok:false) and never reaches
// ValidateSQL. The compile-only providers wrap the input — e.g. Postgres
// runs `EXPLAIN <sql>` — and EXPLAIN only covers the FIRST statement; a
// trailing `; DELETE …` would run as a normal command under the driver's
// simple-query protocol, executing despite the "never executes" contract.
// Rejecting batched statements closes that hole regardless of how each
// provider wraps the input.
func validateStatements(ctx context.Context, v sqlValidator, statements []string) []sqlValidationResult {
	results := make([]sqlValidationResult, 0, len(statements))
	for _, stmt := range statements {
		r := sqlValidationResult{SQL: stmt, OK: true}
		if err := ensureSingleStatement(stmt); err != nil {
			r.OK = false
			r.Error = err.Error()
		} else if err := v.ValidateSQL(ctx, stmt); err != nil {
			r.OK = false
			r.Error = err.Error()
		}
		results = append(results, r)
	}
	return results
}

// ensureSingleStatement returns an error when sql contains more than one
// statement. It flags any `;` that sits outside a single-quoted string
// literal or a double-quoted identifier and is followed by further
// non-empty content (a lone trailing `;` is fine). The scan deliberately
// errs toward rejection: it does not parse line/block comments or
// dollar-quoted bodies, so a `;` inside one of those is treated as a
// separator and rejected — the safe direction for a compile-only check
// whose whole point is to never execute a second command.
func ensureSingleStatement(sql string) error {
	const multiStatementErr = "multi-statement SQL is not allowed: submit one statement per batch entry"
	var inSingle, inDouble bool
	runes := []rune(sql)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inSingle:
			if c == '\'' {
				// Doubled '' is an escaped quote inside the literal.
				if i+1 < len(runes) && runes[i+1] == '\'' {
					i++
					continue
				}
				inSingle = false
			}
		case inDouble:
			if c == '"' {
				if i+1 < len(runes) && runes[i+1] == '"' {
					i++
					continue
				}
				inDouble = false
			}
		default:
			switch c {
			case '\'':
				inSingle = true
			case '"':
				inDouble = true
			case ';':
				// Allow only a terminating `;` (optionally repeated)
				// followed by nothing but whitespace.
				rest := strings.TrimLeft(strings.TrimSpace(string(runes[i+1:])), ";")
				if strings.TrimSpace(rest) != "" {
					return errors.New(multiStatementErr)
				}
			}
		}
	}
	return nil
}

// checkSQLBatchSize rejects an oversized batch at the job level. Returns
// nil when the batch is within the configured cap (or the cap is
// disabled). Kept separate from runValidateSQLInner so the oversized-batch
// behaviour is unit-testable without a Mongo / warehouse round-trip.
func checkSQLBatchSize(n int) error {
	if maxN := sqlValidationMaxStatements(); maxN > 0 && n > maxN {
		return fmt.Errorf("batch too large: %d statements exceed the limit of %d (set %s to adjust, 0 to disable)", n, maxN, sqlValidationMaxStatementsEnv)
	}
	return nil
}

// sqlValidationMaxStatements returns the per-job statement cap. Empty /
// invalid env → default; a value <= 0 means "no cap".
func sqlValidationMaxStatements() int {
	raw := strings.TrimSpace(os.Getenv(sqlValidationMaxStatementsEnv))
	if raw == "" {
		return defaultSQLValidationMaxStatements
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		applog.WithFields(applog.Fields{
			"env":      sqlValidationMaxStatementsEnv,
			"value":    raw,
			"fallback": defaultSQLValidationMaxStatements,
		}).Warn("invalid SQL_VALIDATION_MAX_STATEMENTS; falling back to default")
		return defaultSQLValidationMaxStatements
	}
	return n
}

// --- inline sql_validation_jobs reader/writers -----------------------
//
// The agent's database package doesn't have a SQLValidationJob repo
// (sql_validation_jobs is owned by the API). We do the reads/writes
// inline so the agent doesn't need a hard dep on the API repo. Wire
// format matches services/api/models/sql_validation_job.go.

// sqlValidationResult mirrors the API's models.SQLValidationResult wire
// shape ({sql, ok, error}).
type sqlValidationResult struct {
	SQL   string `bson:"sql"`
	OK    bool   `bson:"ok"`
	Error string `bson:"error,omitempty"`
}

// sqlValidationJobClaim carries only the fields needed to claim and route
// the job. Decoding a minimal subset means the atomic claim can never be
// broken by a malformed `statements` field (decoded separately, below).
type sqlValidationJobClaim struct {
	ID        string `bson:"_id"`
	ProjectID string `bson:"project_id"`
	Status    string `bson:"status"`
	Attempt   int    `bson:"attempt"`
}

// claimSQLValidationJob atomically transitions the job pending → running
// (bumping attempt, stamping claimed_at + heartbeat_at) and returns the
// post-claim doc. Returns (nil, nil) when the job is absent or not
// pending — the caller disambiguates via loadSQLValidationJobClaim.
func claimSQLValidationJob(ctx context.Context, col *mongo.Collection, jobID string) (*sqlValidationJobClaim, error) {
	now := time.Now().UTC()
	update := bson.M{
		"$set": bson.M{
			"status":       "running",
			"claimed_at":   now,
			"heartbeat_at": now,
		},
		"$inc": bson.M{"attempt": 1},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var c sqlValidationJobClaim
	err := col.FindOneAndUpdate(ctx, bson.M{"_id": jobID, "status": "pending"}, update, opts).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadSQLValidationJobClaim(ctx context.Context, col *mongo.Collection, jobID string) (*sqlValidationJobClaim, error) {
	var c sqlValidationJobClaim
	err := col.FindOne(ctx, bson.M{"_id": jobID}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func loadSQLValidationStatements(ctx context.Context, col *mongo.Collection, jobID string) ([]string, error) {
	var doc struct {
		Statements []string `bson:"statements"`
	}
	if err := col.FindOne(ctx, bson.M{"_id": jobID}).Decode(&doc); err != nil {
		return nil, err
	}
	return doc.Statements, nil
}

func writeSQLValidationResults(ctx context.Context, col *mongo.Collection, jobID string, attempt int, results []sqlValidationResult) error {
	now := time.Now().UTC()
	_, err := col.UpdateOne(ctx,
		bson.M{"_id": jobID, "attempt": attempt, "status": "running"},
		bson.M{"$set": bson.M{"results": results, "heartbeat_at": now}},
	)
	return err
}
