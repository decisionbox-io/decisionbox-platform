package validation

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// userCountCaptureExecutor records the FixOpts forwarded by the user-count
// validator's probe queries. Tests assert the rendered VerificationContext
// reaches the SQL fixer so the column-grounding evidence flows through to
// retry attempts on warehouses with non-`user_id` user-id columns.
type userCountCaptureExecutor struct {
	rows     []map[string]interface{}
	err      error
	calls    []userCountCall
	failOn   func(query string) bool
	failErr  error
	failRows []map[string]interface{}
}

type userCountCall struct {
	query string
	opts  queryexec.FixOpts
}

func (e *userCountCaptureExecutor) Execute(_ context.Context, query string, _ string, opts queryexec.FixOpts) ([]map[string]interface{}, error) {
	e.calls = append(e.calls, userCountCall{query: query, opts: opts})
	if e.failOn != nil && e.failOn(query) {
		if e.failRows != nil {
			return e.failRows, e.failErr
		}
		return nil, e.failErr
	}
	return e.rows, e.err
}

// TestUserCountValidator_SetExplorationLogPanicsOnNil mirrors the
// InsightValidator contract — passing nil where an empty slice is meant is a
// wiring bug, not a runtime condition.
func TestUserCountValidator_SetExplorationLogPanicsOnNil(t *testing.T) {
	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected SetExplorationLog(nil) to panic")
		}
	}()
	v.SetExplorationLog(nil)
}

// TestUserCountValidator_ProbeRoutesThroughExecutorWhenWired — the executor
// must receive each probe query so its self-healing path (FixOpts forwarded
// to the SQL fixer) covers user_id-column hallucinations on retry.
func TestUserCountValidator_ProbeRoutesThroughExecutorWhenWired(t *testing.T) {
	exec := &userCountCaptureExecutor{
		rows: []map[string]interface{}{{"total_users": int64(1234)}},
	}
	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
		Executor:  exec,
		Dataset:   "ds",
	})
	v.SetExplorationLog([]models.ExplorationStep{
		{Step: 1, Action: "query_data", QueryPurpose: "scan", Query: "SELECT KULLANICI_ID FROM dbo.SESSIONS"},
	})

	total, err := v.GetTotalUsers(context.Background())
	if err != nil {
		t.Fatalf("GetTotalUsers: %v", err)
	}
	if total != 1234 {
		t.Errorf("total = %d, want 1234", total)
	}
	if len(exec.calls) == 0 {
		t.Fatal("executor should have been called at least once")
	}
}

// TestUserCountValidator_FixOptsCarriesSourceStepEvidence — FixOpts on every
// probe call must include the rendered source-step SQL so the SQL fixer has
// the column-grounding evidence to substitute the real user-id column when
// the hardcoded `user_id` probe fails.
func TestUserCountValidator_FixOptsCarriesSourceStepEvidence(t *testing.T) {
	exec := &userCountCaptureExecutor{
		rows: []map[string]interface{}{{"total_users": int64(99)}},
	}
	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
		Executor:  exec,
		Dataset:   "ds",
	})
	v.SetExplorationLog([]models.ExplorationStep{
		{Step: 7, Action: "query_data", QueryPurpose: "broad", Query: "SELECT KULLANICI_ID, STHAR_TARIH FROM dbo.STHAR"},
		{Step: 8, Action: "query_data", QueryPurpose: "narrow", Query: "SELECT COUNT(*) FROM dbo.STHAR WHERE KULLANICI_ID IS NOT NULL"},
	})

	if _, err := v.GetTotalUsers(context.Background()); err != nil {
		t.Fatalf("GetTotalUsers: %v", err)
	}
	if len(exec.calls) == 0 {
		t.Fatal("executor should have been called")
	}
	first := exec.calls[0]
	if first.opts.VerificationContext == "" {
		t.Fatal("FixOpts.VerificationContext should be populated when explorationLog has query_data steps")
	}
	for _, want := range []string{"KULLANICI_ID", "STHAR_TARIH", "Step 7", "Step 8"} {
		if !strings.Contains(first.opts.VerificationContext, want) {
			t.Errorf("VerificationContext missing %q, got:\n%s", want, first.opts.VerificationContext)
		}
	}
}

// TestUserCountValidator_FallsThroughToWarehouseWithoutExecutor — preserving
// the legacy direct-warehouse path when no executor is wired (tests that
// don't care about self-healing, plus the orchestrator's New() construction
// before RunDiscovery wires the executor).
func TestUserCountValidator_FallsThroughToWarehouseWithoutExecutor(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"total_users"},
		Rows:    []map[string]interface{}{{"total_users": int64(42)}},
	}
	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: wh,
		Dataset:   "ds",
	})
	v.SetExplorationLog([]models.ExplorationStep{})

	total, err := v.GetTotalUsers(context.Background())
	if err != nil {
		t.Fatalf("GetTotalUsers: %v", err)
	}
	if total != 42 {
		t.Errorf("total = %d, want 42", total)
	}
}

// TestUserCountValidator_EmptyExplorationLogYieldsEmptyVerificationContext —
// the executor should still receive a valid FixOpts even when there are no
// source steps; an empty VerificationContext just means the SQL fixer has
// no special grounding evidence and falls back to its standard prompt.
func TestUserCountValidator_EmptyExplorationLogYieldsEmptyVerificationContext(t *testing.T) {
	exec := &userCountCaptureExecutor{
		rows: []map[string]interface{}{{"total_users": int64(7)}},
	}
	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
		Executor:  exec,
		Dataset:   "ds",
	})
	v.SetExplorationLog([]models.ExplorationStep{})

	if _, err := v.GetTotalUsers(context.Background()); err != nil {
		t.Fatalf("GetTotalUsers: %v", err)
	}
	if len(exec.calls) == 0 {
		t.Fatal("executor should have been called")
	}
	if exec.calls[0].opts.VerificationContext != "" {
		t.Errorf("VerificationContext should be empty for an empty exploration log, got:\n%s", exec.calls[0].opts.VerificationContext)
	}
}

// TestCollectQueryStepIDs — only `query_data` steps with non-empty Query are
// included; lookup_schema / search_tables steps are skipped because they
// have no SQL to render as evidence.
func TestCollectQueryStepIDs(t *testing.T) {
	log := []models.ExplorationStep{
		{Step: 1, Action: "query_data", Query: "SELECT 1"},
		{Step: 2, Action: "lookup_schema", Query: ""},
		{Step: 3, Action: "search_tables", Query: ""},
		{Step: 4, Action: "query_data", Query: "SELECT 2"},
		{Step: 5, Action: "query_data", Query: ""}, // explicit empty
	}
	got := collectQueryStepIDs(log)
	want := []int{1, 4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %d, want %d", i, got[i], w)
		}
	}
}

// TestExtractTotalUsersFromRows — the fallback paths return 0 for empty
// input, missing field, or non-numeric values. The "happy" path is exercised
// by the larger probe tests above.
func TestExtractTotalUsersFromRows(t *testing.T) {
	tests := []struct {
		name string
		rows []map[string]interface{}
		want int
	}{
		{"empty", nil, 0},
		{"missing field", []map[string]interface{}{{"other": int64(5)}}, 0},
		{"int64", []map[string]interface{}{{"total_users": int64(100)}}, 100},
		{"int", []map[string]interface{}{{"total_users": int(100)}}, 100},
		{"float64", []map[string]interface{}{{"total_users": float64(99.7)}}, 99},
		{"non-numeric", []map[string]interface{}{{"total_users": "100"}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTotalUsersFromRows(tt.rows)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

// TestUserCountValidator_SetExecutorReplacesPrior — calling SetExecutor with
// a different executor instance routes subsequent probes through the new
// one. Used by the orchestrator's construct-then-set wiring.
func TestUserCountValidator_SetExecutorReplacesPrior(t *testing.T) {
	first := &userCountCaptureExecutor{
		rows: []map[string]interface{}{{"total_users": int64(1)}},
	}
	v := NewUserCountValidator(UserCountValidatorOptions{
		Warehouse: testutil.NewMockWarehouseProvider("ds"),
		Executor:  first,
		Dataset:   "ds",
	})
	v.SetExplorationLog([]models.ExplorationStep{})

	second := &userCountCaptureExecutor{
		rows: []map[string]interface{}{{"total_users": int64(2)}},
	}
	v.SetExecutor(second)

	total, err := v.GetTotalUsers(context.Background())
	if err != nil {
		t.Fatalf("GetTotalUsers: %v", err)
	}
	if len(first.calls) != 0 {
		t.Errorf("original executor should not be called, got %d calls", len(first.calls))
	}
	if len(second.calls) == 0 {
		t.Errorf("replacement executor should be called")
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (from replacement)", total)
	}
}
