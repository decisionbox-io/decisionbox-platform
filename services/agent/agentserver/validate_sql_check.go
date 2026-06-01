package agentserver

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// This file holds the pure, unit-testable logic of the validate-sql mode
// (the batch loop, the size cap, and the cap-env parsing). The Mongo /
// warehouse wiring lives in validate_sql.go, which is exercised by the
// integration test. Keeping the two apart lets coverage track the logic
// here without masking it behind the wiring file's codecov ignore.

// sqlValidationMaxStatementsEnv caps how many statements a single job may
// carry. A batch over the cap is failed at the job level (a runaway or
// abusive caller shouldn't be able to fire unbounded compile round-trips
// at the warehouse). Empty / invalid → defaultSQLValidationMaxStatements;
// a value <= 0 disables the cap entirely.
const sqlValidationMaxStatementsEnv = "SQL_VALIDATION_MAX_STATEMENTS"

// defaultSQLValidationMaxStatements is generous enough for any realistic
// batch while still bounding a pathological job.
const defaultSQLValidationMaxStatements = 500

// sqlValidationResult mirrors the API's models.SQLValidationResult wire
// shape ({sql, ok, error}).
type sqlValidationResult struct {
	SQL   string `bson:"sql"`
	OK    bool   `bson:"ok"`
	Error string `bson:"error,omitempty"`
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
// (an INSERT/DELETE) compiles but mutates nothing. Read-only safety rests
// on the project's warehouse credentials being read-only, which the
// platform expects of every connection — this mode does no SQL parsing or
// statement linting of its own (issue non-goal: validation is delegated to
// each warehouse's native compile path).
func validateStatements(ctx context.Context, v sqlValidator, statements []string) []sqlValidationResult {
	results := make([]sqlValidationResult, 0, len(statements))
	for _, stmt := range statements {
		r := sqlValidationResult{SQL: stmt, OK: true}
		if err := v.ValidateSQL(ctx, stmt); err != nil {
			r.OK = false
			r.Error = err.Error()
		}
		results = append(results, r)
	}
	return results
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
