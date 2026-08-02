package agentserver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeValidator is a minimal sqlValidator for testing the batch loop
// without a real warehouse. fn decides each statement's verdict; calls
// records the statements it was asked to compile (so a test can prove the
// loop calls ValidateSQL exactly once per statement and nothing else).
type fakeValidator struct {
	fn    func(sql string) error
	calls []string
}

func (f *fakeValidator) ValidateSQL(_ context.Context, sql string) error {
	f.calls = append(f.calls, sql)
	return f.fn(sql)
}

// --job-id is required — the check must fire before any Mongo / warehouse
// work so an unattended dispatcher gets a clear, fast error.
func TestRunValidateSQL_RequiresJobID(t *testing.T) {
	err := runValidateSQL(&config.Config{}, "")
	if err == nil {
		t.Fatal("runValidateSQL with empty job id should error")
	}
	if !strings.Contains(err.Error(), "--job-id") {
		t.Errorf("error = %q, want it to mention --job-id", err.Error())
	}
}

// Per-statement verdict shape: order preserved, SQL echoed, valid →
// ok:true with no error, invalid → ok:false with the validator's own
// message round-tripped. The loop must touch ValidateSQL exactly once per
// statement (and never execute anything else).
func TestValidateStatements_VerdictShape(t *testing.T) {
	fv := &fakeValidator{fn: func(sql string) error {
		if strings.Contains(sql, "BAD") {
			return errors.New(`syntax error at or near "BAD"`)
		}
		return nil
	}}
	stmts := []string{"SELECT 1", "SELECT BAD FROM x", "SELECT 2"}

	got := validateStatements(context.Background(), fv, stmts)

	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	want := []sqlValidationResult{
		{SQL: "SELECT 1", OK: true},
		{SQL: "SELECT BAD FROM x", OK: false, Error: `syntax error at or near "BAD"`},
		{SQL: "SELECT 2", OK: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("results = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(fv.calls, stmts) {
		t.Errorf("ValidateSQL calls = %v, want one per statement in order %v", fv.calls, stmts)
	}
}

// An empty batch is a valid no-op: zero results, and ValidateSQL is never
// called. The result must be a non-nil empty slice so it serialises as
// [] rather than null.
func TestValidateStatements_EmptyBatch(t *testing.T) {
	fv := &fakeValidator{fn: func(string) error {
		t.Fatal("ValidateSQL must not be called for an empty batch")
		return nil
	}}

	got := validateStatements(context.Background(), fv, nil)
	if got == nil {
		t.Fatal("empty batch result must be non-nil")
	}
	if len(got) != 0 {
		t.Errorf("empty batch produced %d results, want 0", len(got))
	}
}

// A whitespace-only statement is not special-cased by the loop — it flows
// through ValidateSQL and is recorded as whatever verdict the warehouse
// returns (warehouses reject empty SQL at their boundary).
func TestValidateStatements_BlankStatementPassesThrough(t *testing.T) {
	fv := &fakeValidator{fn: func(sql string) error {
		if strings.TrimSpace(sql) == "" {
			return errors.New("postgres: empty SQL")
		}
		return nil
	}}
	got := validateStatements(context.Background(), fv, []string{"   "})
	if len(got) != 1 || got[0].OK {
		t.Fatalf("blank statement verdict = %+v, want ok:false", got)
	}
	if got[0].Error != "postgres: empty SQL" {
		t.Errorf("error = %q, want the warehouse message passed through", got[0].Error)
	}
}

// An oversized batch fails the job; at-or-under the cap passes; a
// disabled cap (0) lets any size through.
func TestCheckSQLBatchSize(t *testing.T) {
	t.Run("under and at the cap pass", func(t *testing.T) {
		t.Setenv(sqlValidationMaxStatementsEnv, "3")
		for _, n := range []int{0, 1, 3} {
			if err := checkSQLBatchSize(n); err != nil {
				t.Errorf("checkSQLBatchSize(%d) = %v, want nil", n, err)
			}
		}
	})
	t.Run("over the cap fails with an actionable message", func(t *testing.T) {
		t.Setenv(sqlValidationMaxStatementsEnv, "3")
		err := checkSQLBatchSize(4)
		if err == nil {
			t.Fatal("checkSQLBatchSize(4) with cap 3 = nil, want error")
		}
		if !strings.Contains(err.Error(), sqlValidationMaxStatementsEnv) {
			t.Errorf("error %q should name the env var so the operator can raise the cap", err.Error())
		}
	})
	t.Run("zero disables the cap", func(t *testing.T) {
		t.Setenv(sqlValidationMaxStatementsEnv, "0")
		if err := checkSQLBatchSize(1_000_000); err != nil {
			t.Errorf("cap disabled should accept any size, got %v", err)
		}
	})
}

func TestSQLValidationMaxStatements(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{name: "unset uses default", set: false, want: defaultSQLValidationMaxStatements},
		{name: "blank uses default", set: true, val: "  ", want: defaultSQLValidationMaxStatements},
		{name: "explicit value", set: true, val: "25", want: 25},
		{name: "zero disables cap", set: true, val: "0", want: 0},
		{name: "negative disables cap", set: true, val: "-3", want: -3},
		{name: "invalid uses default", set: true, val: "lots", want: defaultSQLValidationMaxStatements},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv(sqlValidationMaxStatementsEnv, c.val)
			} else {
				// Ensure a leftover ambient value can't leak in.
				t.Setenv(sqlValidationMaxStatementsEnv, "")
			}
			if got := sqlValidationMaxStatements(); got != c.want {
				t.Errorf("sqlValidationMaxStatements() = %d, want %d", got, c.want)
			}
		})
	}
}

// A validation job routes to its own warehouse_id (multi-warehouse); a legacy
// job with none falls back to the project's primary (resolved through the
// accessor so a stale primary id doesn't fail), preserving the old
// single-warehouse behaviour.
func TestClaimWarehouseID(t *testing.T) {
	proj := &models.Project{
		PrimaryWarehouseID: "wh_primary",
		Warehouses: []models.WarehouseConfig{
			{ID: "wh_primary", Provider: "postgres", Datasets: []string{"public"}},
			{ID: "wh_b", Provider: "redshift", Datasets: []string{"crm"}},
		},
	}
	cases := []struct {
		name string
		job  string
		want string
	}{
		{name: "explicit secondary wins", job: "wh_b", want: "wh_b"},
		{name: "explicit primary honoured", job: "wh_primary", want: "wh_primary"},
		{name: "empty falls back to primary", job: "", want: "wh_primary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := claimWarehouseID(&sqlValidationJobClaim{WarehouseID: c.job}, proj)
			if got != c.want {
				t.Errorf("claimWarehouseID(job=%q) = %q, want %q", c.job, got, c.want)
			}
		})
	}

	// A stale primary_warehouse_id (points at a removed warehouse, no default
	// entry) must fall back to the first configured warehouse via the accessor.
	stale := &models.Project{
		PrimaryWarehouseID: "wh_gone",
		Warehouses:         []models.WarehouseConfig{{ID: "wh_a", Provider: "postgres", Datasets: []string{"public"}}},
	}
	if got := claimWarehouseID(&sqlValidationJobClaim{}, stale); got != "wh_a" {
		t.Errorf("stale primary should resolve to the first warehouse, got %q", got)
	}

	// A legacy job (no warehouse_id) whose project grew to multi-warehouse and
	// moved its primary must still compile against the original default warehouse
	// (where its statements came from), not the new primary.
	moved := &models.Project{
		PrimaryWarehouseID: "wh_b",
		Warehouses: []models.WarehouseConfig{
			{ID: models.DefaultWarehouseID, Provider: "postgres", Datasets: []string{"public"}},
			{ID: "wh_b", Provider: "redshift", Datasets: []string{"crm"}},
		},
	}
	if got := claimWarehouseID(&sqlValidationJobClaim{}, moved); got != models.DefaultWarehouseID {
		t.Errorf("legacy job w/ moved primary = %q, want %q (must not follow the new primary)", got, models.DefaultWarehouseID)
	}

	// A legacy single-warehouse project (legacy `warehouse` field) resolves to
	// the synthesised default id.
	legacy := &models.Project{Warehouse: models.WarehouseConfig{Provider: "bigquery", Datasets: []string{"ds"}}}
	if got := claimWarehouseID(&sqlValidationJobClaim{}, legacy); got != models.DefaultWarehouseID {
		t.Errorf("legacy project + legacy job = %q, want %q", got, models.DefaultWarehouseID)
	}
}
