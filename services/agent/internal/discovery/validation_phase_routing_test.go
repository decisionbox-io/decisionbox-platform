package discovery

import (
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// Multi-warehouse validation routing: an insight/recommendation is verified
// against the datasource it is about (resolved from the warehouse_ids of the
// steps it cites), falling back to the primary on a single-warehouse run.

func testValPhaseMultiWH() *validationPhase {
	return &validationPhase{
		wh:       verifier.WarehouseInfo{Dialect: "primary"},
		executor: &verifier.DefaultExecutor{},
		whByDS: map[string]verifier.WarehouseInfo{
			"default":   {Dialect: "postgres"},
			"wh_oracle": {Dialect: "oracle"},
		},
		executorByDS: map[string]verifier.Executor{
			"default":   &verifier.DefaultExecutor{},
			"wh_oracle": &verifier.DefaultExecutor{},
		},
		primaryDS: "default",
	}
}

func TestValidationPhase_DatasourceForSteps(t *testing.T) {
	p := testValPhaseMultiWH()
	stepByID := map[int]*models.ExplorationStep{
		1: {Step: 1, WarehouseID: "wh_oracle"},
		2: {Step: 2, WarehouseID: "wh_oracle"},
		3: {Step: 3, WarehouseID: "default"},
	}
	// Cites mostly Oracle steps → routes to Oracle.
	if got := p.datasourceForSteps([]int{1, 2, 3}, stepByID); got != "wh_oracle" {
		t.Errorf("dominant datasource = %q, want wh_oracle", got)
	}
	// No cited steps (or steps without a datasource) → the primary.
	if got := p.datasourceForSteps(nil, stepByID); got != "default" {
		t.Errorf("no steps → %q, want default", got)
	}
	if got := p.datasourceForSteps([]int{3}, stepByID); got != "default" {
		t.Errorf("default-only steps → %q, want default", got)
	}
}

func TestValidationPhase_ForDatasource(t *testing.T) {
	p := testValPhaseMultiWH()
	wh, ex := p.forDatasource("wh_oracle")
	if wh.Dialect != "oracle" || ex != p.executorByDS["wh_oracle"] {
		t.Errorf("forDatasource(wh_oracle) = dialect %q (want oracle) / wrong executor", wh.Dialect)
	}
	// Unknown datasource falls back to the primary wh + executor.
	wh2, ex2 := p.forDatasource("nope")
	if wh2.Dialect != "primary" || ex2 != p.executor {
		t.Errorf("unknown datasource should fall back to primary; got dialect %q", wh2.Dialect)
	}
}

func TestValidationPhase_SingleWarehouseFallback(t *testing.T) {
	// No per-datasource wiring (single-warehouse run): everything resolves
	// to primaryDS and routes through the fallback wh/executor, unchanged.
	p := &validationPhase{
		wh:        verifier.WarehouseInfo{Dialect: "primary"},
		executor:  &verifier.DefaultExecutor{},
		primaryDS: "default",
	}
	stepByID := map[int]*models.ExplorationStep{1: {WarehouseID: "wh_oracle"}}
	if got := p.datasourceForSteps([]int{1}, stepByID); got != "default" {
		t.Errorf("single-warehouse → %q, want default", got)
	}
	wh, ex := p.forDatasource("wh_oracle")
	if wh.Dialect != "primary" || ex != p.executor {
		t.Error("single-warehouse forDatasource must return the primary fallback")
	}
}
