package agentserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// validationJobsCollection is the on-disk Mongo collection name.
// Mirrored from services/api/database/validation_job_repo.go — the
// agent cannot import the api package, so the constant lives in both
// places. Keep in sync.
const validationJobsCollection = "validation_jobs"

// heartbeatInterval is how often the agent writes heartbeat_at to the
// job doc so the API-side stale-recovery sweep doesn't requeue it
// while a long LLM round is in flight. 60 seconds matches the
// schema-index pattern and sits comfortably under the API's 5-minute
// stale threshold.
const heartbeatInterval = 60 * time.Second

// runValidateDoc is the entrypoint for `agent --mode=validate-doc`.
// It loads the ValidationJob row, hydrates the discovery + cited
// exploration steps, runs the verifier + refuter pair against the
// single doc via discovery.ValidateSingleDoc, persists the verdict
// back, and marks the job complete (or failed). Heartbeats fire on
// a separate goroutine the whole time.
//
// Exit contract:
//   - returns nil on success (job marked completed)
//   - returns a non-nil error on any failure that prevents completing
//     the run — the agent caller (Run) propagates the exit code so
//     the API-side worker / runner sees the non-zero status and the
//     job's `error` field surfaces in the dashboard
//
// The function takes care of setting the job's status itself; the
// caller does NOT also need to call MarkFailed.
func runValidateDoc(cfg *config.Config, jobID string) error {
	if jobID == "" {
		return errors.New("--job-id is required for --mode=validate-doc")
	}
	applog.WithField("job_id", jobID).Info("Validate-doc agent starting")

	ctx := context.Background()
	mongoClient, err := initMongoDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer func() { _ = mongoClient.Disconnect(ctx) }()
	db := database.New(mongoClient)
	jobsCol := db.Collection(validationJobsCollection)

	// Load the job. The API's enqueue handler wrote the row before
	// the worker claimed it; the worker called RunValidateDoc which
	// got us here. We expect status=running at this point.
	job, err := loadValidationJob(ctx, jobsCol, jobID)
	if err != nil {
		return fmt.Errorf("load validation_job: %w", err)
	}
	if job == nil {
		return fmt.Errorf("validation_job %s not found", jobID)
	}
	if job.Status != "running" {
		// The worker flipped status to running before spawning; if we
		// see anything else, another worker or cancellation got
		// there first.
		return fmt.Errorf("validation_job %s status is %q (expected running)", jobID, job.Status)
	}
	attempt := job.Attempt

	logger := applog.WithFields(applog.Fields{
		"job_id":       jobID,
		"project_id":   job.ProjectID,
		"discovery_id": job.DiscoveryID,
		"doc_kind":     job.DocKind,
		"doc_id":       job.DocID,
		"attempt":      attempt,
	})

	// Heartbeat goroutine — writes every heartbeatInterval until the
	// run finishes. Filter on (id, attempt, status=running) so a
	// stale-recovered job's orphan agent silently no-ops instead of
	// smearing progress across requeued rows.
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go heartbeatLoop(heartbeatCtx, jobsCol, jobID, attempt)

	// Mark started_at (one-time) so the dashboard knows the agent
	// has begun the work — distinct from "claimed but not started".
	if err := setStartedAt(ctx, jobsCol, jobID, attempt); err != nil {
		logger.WithError(err).Warn("set started_at failed")
	}

	// Run the validation. Any error path falls through to MarkFailed.
	if rErr := runValidateDocInner(ctx, cfg, db, job, attempt); rErr != nil {
		stopHeartbeat()
		_ = markJobFailed(context.Background(), jobsCol, jobID, attempt, rErr.Error())
		logger.WithError(rErr).Warn("Validate-doc agent failed")
		return rErr
	}

	stopHeartbeat()
	if err := markJobCompleted(context.Background(), jobsCol, jobID, attempt); err != nil {
		logger.WithError(err).Warn("mark completed failed")
		return err
	}
	logger.Info("Validate-doc agent completed")
	return nil
}

func runValidateDocInner(ctx context.Context, cfg *config.Config, db *database.DB, job *validationJobDoc, attempt int) error {
	// Mark which agent step we're in so the dashboard's progress card
	// can render "Verifier running" / "Refuter running". The verifier
	// + refuter live behind discovery.ValidateSingleDoc; we mark the
	// label here, the inner function handles both agents internally.
	jobsCol := db.Collection(validationJobsCollection)
	_ = markJobStep(ctx, jobsCol, job.ID, attempt, "verifier")

	projectRepo := database.NewProjectRepository(db)
	project, err := projectRepo.GetByID(ctx, job.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	if project == nil {
		return fmt.Errorf("project %s not found", job.ProjectID)
	}
	if !project.EffectiveValidationEnabled() {
		return fmt.Errorf("validation is disabled for this project")
	}

	discoveryRepo := database.NewDiscoveryRepository(db)
	discDoc, err := discoveryRepo.GetByID(ctx, job.DiscoveryID)
	if err != nil {
		return fmt.Errorf("load discovery: %w", err)
	}
	if discDoc == nil {
		return fmt.Errorf("discovery %s not found", job.DiscoveryID)
	}

	// Pull exploration-step rows from the split-log collection — the
	// embedded array on the discovery doc was retired. validateSingleDoc
	// indexes these by step number internally.
	logRepo := database.NewDiscoveryLogRepository(db)
	steps, err := logRepo.ListExplorationStepsByDiscovery(ctx, job.DiscoveryID, 0)
	if err != nil {
		return fmt.Errorf("list exploration steps: %w", err)
	}

	secretProvider, err := initSecretProvider(db.Client())
	if err != nil {
		return err
	}
	// Verify the doc against the datasource its discovery ran on
	// (multi-warehouse); a legacy discovery carries no warehouse_id and
	// resolves to the primary. docWH supplies the tenant filter so the
	// provider and its filter stay on the same warehouse.
	docWHID := discDoc.WarehouseID
	if docWHID == "" {
		docWHID = project.PrimaryWarehouseID
	}
	warehouseProvider, err := initWarehouseProvider(ctx, project, docWHID, secretProvider, project.ID)
	if err != nil {
		return err
	}
	defer warehouseProvider.Close()
	docWH, _ := project.WarehouseByID(docWHID)

	llmProvider, err := initLLMProvider(ctx, cfg, project, secretProvider, project.ID)
	if err != nil {
		return err
	}
	aiClient, err := ai.New(llmProvider, project.LLM.Model)
	if err != nil {
		return fmt.Errorf("init ai client: %w", err)
	}

	// Schema provider for the verifier's lookup_schema tool is
	// deliberately omitted in v1 of the manual-validation path: the
	// schema_retrieve.Retriever exposes a different shape
	// (LookupResult vs the verifier's table->columns interface) and
	// wiring an adapter is non-trivial. Without it the verifier
	// still operates on the catalog already attached to cited
	// source steps; cross-table refutations get marked
	// unverifiable. The orchestrator's full-run validation has the
	// same fall-back behaviour (see validation_phase.go) — it's not
	// a regression.
	var schemaProvider ai.SchemaProvider

	vCfg, vCaps := verifier.LoadConfigFromEnv()
	stepByID := indexExplorationSteps(steps)

	exec := &verifier.DefaultExecutor{
		SchemaProvider:      schemaProvider,
		QueryExec:           queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: warehouseProvider, FilterField: docWH.FilterField, FilterValue: docWH.FilterValue}),
		StepByID:            stepByID,
		Cfg:                 vCfg.Bundle,
		MaxReadStepRowsCall: vCaps.MaxReadStepRowsCall,
	}

	input := discovery.ValidateSingleDocInput{
		AIClient: aiClient,
		Cfg:      vCfg,
		Caps:     vCaps,
		WH: verifier.WarehouseInfo{
			Dialect:     warehouseProvider.SQLDialect(),
			Dataset:     warehouseProvider.GetDataset(),
			FilterField: docWH.FilterField,
			FilterValue: docWH.FilterValue,
		},
		Disc: verifier.DiscoveryContext{
			ProjectID: project.ID,
			RunID:     "", // manual run; no discovery RunID
			Domain:    project.Domain,
			Language:  project.EffectiveLanguage(),
		},
		Executor: exec,
		DocKind:  valmodels.DocKind(job.DocKind),
		DocID:    job.DocID,
		Insights: discDoc.Insights,
		Recs:     discDoc.Recommendations,
		Steps:    steps,
	}
	result, err := discovery.ValidateSingleDoc(ctx, input)
	if err != nil {
		return fmt.Errorf("validate single doc: %w", err)
	}

	// Stamp run/discovery/project IDs that ValidateSingleDoc
	// deliberately doesn't know (they belong to the persistence
	// layer, not the verifier).
	result.DocKind = valmodels.DocKind(job.DocKind)
	result.ValidatedAt = time.Now()

	// Persist the embedded-array update + the ValidationResult row.
	_ = markJobStep(ctx, jobsCol, job.ID, attempt, "combining")

	// Re-check the job is still ours before mutating the discovery
	// doc. If another API replica cancelled it (status=cancelled),
	// or stale recovery requeued it (attempt bumped), or the user
	// retry created a fresh attempt, we MUST NOT overwrite the
	// discovery with this orphan's verdict — the user could see a
	// validation they explicitly cancelled, or a stale verdict could
	// clobber the later attempt's results.
	//
	// The job-row writes (markJobCompleted etc.) already filter on
	// (id, attempt, status=running), so they silently no-op on
	// orphan agents — but the discovery write didn't have any such
	// guard until now.
	current, lookupErr := loadValidationJob(ctx, jobsCol, job.ID)
	if lookupErr != nil {
		return fmt.Errorf("recheck job before persist: %w", lookupErr)
	}
	if current == nil || current.Status != "running" || current.Attempt != attempt {
		return fmt.Errorf("job %s no longer owned by this attempt (status=%s, attempt=%d, want running/%d) — refusing to persist verdict", job.ID, currentOrEmpty(current), currentAttemptOrZero(current), attempt)
	}

	switch valmodels.DocKind(job.DocKind) {
	case valmodels.DocInsight:
		// Find the matching insight on the loaded discovery slice (
		// ValidateSingleDoc stamped Validation on it).
		var matched *models.Insight
		for i := range discDoc.Insights {
			if discDoc.Insights[i].ID == job.DocID {
				matched = &discDoc.Insights[i]
				break
			}
		}
		if matched == nil || matched.Validation == nil {
			return fmt.Errorf("validation result was not stamped on insight %s", job.DocID)
		}
		if err := discoveryRepo.UpdateInsightValidation(ctx, discDoc.ID, job.DocID, matched.Validation); err != nil {
			return fmt.Errorf("persist insight validation: %w", err)
		}
	case valmodels.DocRecommendation:
		var matched *models.Recommendation
		for i := range discDoc.Recommendations {
			if discDoc.Recommendations[i].ID == job.DocID {
				matched = &discDoc.Recommendations[i]
				break
			}
		}
		if matched == nil || matched.Validation == nil {
			return fmt.Errorf("validation result was not stamped on recommendation %s", job.DocID)
		}
		if err := discoveryRepo.UpdateRecommendationValidation(ctx, discDoc.ID, job.DocID, matched.Validation); err != nil {
			return fmt.Errorf("persist recommendation validation: %w", err)
		}
	}

	// Insert the ValidationResult log row.
	if err := logRepo.SaveValidationResults(ctx, project.ID, discDoc.ID, "", []models.ValidationResult{result}); err != nil {
		// Best-effort — the verdict is already on the embedded doc,
		// so the dashboard sees the new state. Log the log-write
		// failure but don't fail the job.
		applog.WithError(err).Warn("save validation result row failed")
	}
	return nil
}

// --- inline validation_jobs reader/writers ---------------------------
//
// The agent's database package doesn't have a ValidationJob repo
// (validation_jobs is owned by the API). We do the writes inline so
// the agent doesn't need a hard dep on the API repo. Wire format
// matches services/api/models/validation_job.go.

type validationJobDoc struct {
	ID          string `bson:"_id"`
	ProjectID   string `bson:"project_id"`
	DiscoveryID string `bson:"discovery_id"`
	DocKind     string `bson:"doc_kind"`
	DocID       string `bson:"doc_id"`
	Status      string `bson:"status"`
	Attempt     int    `bson:"attempt"`
}

// currentOrEmpty + currentAttemptOrZero are tiny nil-safety helpers
// for the prePersistGuard error message — Go's lazy-eval shortcuts
// don't apply across the struct-field arrow, so we centralise the
// nil check here.
func currentOrEmpty(j *validationJobDoc) string {
	if j == nil {
		return "missing"
	}
	return j.Status
}
func currentAttemptOrZero(j *validationJobDoc) int {
	if j == nil {
		return 0
	}
	return j.Attempt
}

func loadValidationJob(ctx context.Context, col *mongo.Collection, jobID string) (*validationJobDoc, error) {
	var doc validationJobDoc
	err := col.FindOne(ctx, bson.M{"_id": jobID}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func setStartedAt(ctx context.Context, col *mongo.Collection, jobID string, attempt int) error {
	now := time.Now().UTC()
	_, err := col.UpdateOne(ctx,
		bson.M{"_id": jobID, "attempt": attempt, "status": "running"},
		bson.M{"$set": bson.M{"started_at": now, "heartbeat_at": now}},
	)
	return err
}

func markJobStep(ctx context.Context, col *mongo.Collection, jobID string, attempt int, step string) error {
	now := time.Now().UTC()
	_, err := col.UpdateOne(ctx,
		bson.M{"_id": jobID, "attempt": attempt, "status": "running"},
		bson.M{"$set": bson.M{"step": step, "heartbeat_at": now}},
	)
	return err
}

func markJobCompleted(ctx context.Context, col *mongo.Collection, jobID string, attempt int) error {
	now := time.Now().UTC()
	_, err := col.UpdateOne(ctx,
		bson.M{"_id": jobID, "attempt": attempt, "status": "running"},
		bson.M{"$set": bson.M{"status": "completed", "completed_at": now, "heartbeat_at": now}},
	)
	return err
}

func markJobFailed(ctx context.Context, col *mongo.Collection, jobID string, attempt int, errMsg string) error {
	now := time.Now().UTC()
	_, err := col.UpdateOne(ctx,
		bson.M{"_id": jobID, "attempt": attempt, "status": "running"},
		bson.M{"$set": bson.M{"status": "failed", "completed_at": now, "heartbeat_at": now, "error": errMsg}},
	)
	return err
}

func heartbeatLoop(ctx context.Context, col *mongo.Collection, jobID string, attempt int) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now().UTC()
			// Use a short timeout so a Mongo hiccup doesn't block
			// the goroutine. Filter on (status=running, attempt) so
			// a stale-recovered job's orphan agent silently no-ops.
			hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = col.UpdateOne(hbCtx,
				bson.M{"_id": jobID, "attempt": attempt, "status": "running"},
				bson.M{"$set": bson.M{"heartbeat_at": now}},
			)
			cancel()
		}
	}
}

// indexExplorationSteps mirrors the orchestrator's buildStepIndex
// without importing the unexported helper. Pointers point INTO the
// caller's slice so step.QueryResult etc. stay live.
func indexExplorationSteps(steps []models.ExplorationStep) map[int]*models.ExplorationStep {
	out := make(map[int]*models.ExplorationStep, len(steps))
	for i := range steps {
		out[steps[i].Step] = &steps[i]
	}
	return out
}

