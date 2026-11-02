package database

import (
	"testing"
	"time"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fatStep is a step as the exploration engine leaves it: every row, every
// SQL-fix prompt, the lot. What a checkpoint keeps of it is the contract
// checkpointPayload owns.
func fatStep() models.ExplorationStep {
	rows := make([]map[string]interface{}, 0, 5000)
	for i := 0; i < 5000; i++ {
		rows = append(rows, map[string]interface{}{"id": int64(i)})
	}
	compact := gomodels.BuildCompactResult(rows)
	return models.ExplorationStep{
		Step:            17,
		Timestamp:       time.Now(),
		WarehouseID:     "crm",
		Action:          "query_data",
		Thinking:        "checking retention",
		QueryPurpose:    "retention by cohort",
		Query:           "SELECT cohort, COUNT(*) FROM ds.users GROUP BY 1",
		QueryResult:     rows,
		RowCount:        5000,
		ExecutionTimeMs: 1234,
		CompactResult:   &compact,
		Quality: []gowarehouse.QualityCaveat{
			{Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld"},
		},
		Error:       "",
		FixAttempts: 2,
		Fixed:       true,
		FixHistory: []models.FixAttempt{
			{Step: 17, Attempt: 0, PromptIn: "a very long fix prompt", ResponseOut: "a very long fix response"},
			{Step: 17, Attempt: 1, PromptIn: "another long prompt", ResponseOut: "another long response"},
		},
		LLMRequest:  "the entire exploration prompt",
		LLMResponse: "the entire model response",
		TokensIn:    900,
		TokensOut:   120,
		DurationMs:  777,
		IsInsight:   true,
	}
}

// TestCheckpointPayload_DropsWhatMakesAStepUnbounded pins the three things a
// checkpoint deliberately does not keep. Each is dropped for its own reason,
// and all three are what would otherwise make the document grow with the
// size of the result or the number of fix attempts.
func TestCheckpointPayload_DropsWhatMakesAStepUnbounded(t *testing.T) {
	sample := []map[string]interface{}{{"id": int64(0)}, {"id": int64(1)}}
	got := checkpointPayload(fatStep(), sample)

	// The full result set is replaced by the bounded sample the caller cut.
	if len(got.QueryResult) != len(sample) {
		t.Errorf("QueryResult len = %d, want the %d-row sample — the full result is what makes a step unbounded",
			len(got.QueryResult), len(sample))
	}
	// FixHistory carries a whole SQL-fix prompt and response per attempt. It
	// is audit data, written once at the tail; replay never reads it.
	if got.FixHistory != nil {
		t.Errorf("FixHistory must be dropped, got %d entries", len(got.FixHistory))
	}
	// Unpopulated by the engine today; stripped so they stay that way here
	// rather than silently becoming the largest field in the document.
	if got.LLMRequest != "" || got.LLMResponse != "" {
		t.Errorf("LLM dialog must be dropped, got request=%q response=%q", got.LLMRequest, got.LLMResponse)
	}
}

// TestCheckpointPayload_KeepsEverythingLoadBearing is the other half, and the
// more important one: anything replay or the analysis phase reads has to
// survive. Quality is called out because it is the one field whose loss
// would be silent AND wrong — attachSourceQuality derives every insight's
// evidence label from it, and it is knowable nowhere else, so a resumed run
// without it would relabel findings computed over withheld rows as sound.
func TestCheckpointPayload_KeepsEverythingLoadBearing(t *testing.T) {
	in := fatStep()
	got := checkpointPayload(in, []map[string]interface{}{{"id": int64(0)}})

	if got.Step != in.Step {
		t.Errorf("Step = %d, want %d", got.Step, in.Step)
	}
	if got.Action != in.Action || got.Thinking != in.Thinking {
		t.Errorf("the action and its reasoning must survive: %q / %q", got.Action, got.Thinking)
	}
	if got.Query != in.Query || got.QueryPurpose != in.QueryPurpose {
		t.Errorf("the query and its purpose must survive: %q / %q", got.Query, got.QueryPurpose)
	}
	// RowCount is what keeps the rebuilt evidence bundle honest about the
	// size of the result once the rows are a sample.
	if got.RowCount != 5000 {
		t.Errorf("RowCount = %d, want 5000 — the authoritative size of the result", got.RowCount)
	}
	if got.CompactResult == nil {
		t.Fatal("CompactResult must survive — it is what the analysis phase renders")
	}
	if got.CompactResult.RowCount != in.CompactResult.RowCount {
		t.Errorf("digest row count = %d, want %d", got.CompactResult.RowCount, in.CompactResult.RowCount)
	}
	if len(got.Quality) != 1 || got.Quality[0].Kind != gowarehouse.QualityWithheld {
		t.Errorf("Quality must survive; got %+v", got.Quality)
	}
	if got.WarehouseID != "crm" {
		t.Errorf("WarehouseID = %q, want crm — a multi-warehouse step must stay attributed", got.WarehouseID)
	}
	if got.ExecutionTimeMs != 1234 || got.TokensIn != 900 || got.TokensOut != 120 {
		t.Errorf("per-step metrics must survive: %dms, %d in, %d out",
			got.ExecutionTimeMs, got.TokensIn, got.TokensOut)
	}
	// FixAttempts is the scalar; only the verbose per-attempt log goes.
	if !got.Fixed || got.FixAttempts != 2 {
		t.Errorf("the self-heal summary must survive: fixed=%v attempts=%d", got.Fixed, got.FixAttempts)
	}
	if !got.IsInsight {
		t.Error("IsInsight must survive")
	}
}

// TestCheckpointPayload_DoesNotMutateTheLiveStep is load-bearing in a way
// that is easy to miss: the step it is handed is the SAME struct the
// orchestrator keeps in explorationResult.Steps, which the verifier reads
// from memory at validation time. Stripping in place would delete the live
// run's rows and fix history out from under the validation phase.
func TestCheckpointPayload_DoesNotMutateTheLiveStep(t *testing.T) {
	in := fatStep()
	wantRows := len(in.QueryResult)
	wantFixes := len(in.FixHistory)

	checkpointPayload(in, []map[string]interface{}{{"id": int64(0)}})

	if len(in.QueryResult) != wantRows {
		t.Errorf("the caller's rows were mutated: %d, want %d — the live verifier reads this slice",
			len(in.QueryResult), wantRows)
	}
	if len(in.FixHistory) != wantFixes {
		t.Errorf("the caller's fix history was mutated: %d, want %d", len(in.FixHistory), wantFixes)
	}
	if in.LLMRequest == "" {
		t.Error("the caller's LLM dialog was cleared")
	}
}

// TestCheckpointPayload_NilSampleIsLegitimate covers the steps that never had
// rows: a schema action, and a query that failed.
func TestCheckpointPayload_NilSampleIsLegitimate(t *testing.T) {
	lookup := models.ExplorationStep{Step: 3, Action: "lookup_schema", Thinking: "inspect"}
	if got := checkpointPayload(lookup, nil); got.QueryResult != nil {
		t.Errorf("a schema step has no rows, got %v", got.QueryResult)
	}

	failed := models.ExplorationStep{Step: 4, Action: "query_data", Query: "SELECT bad", Error: "boom"}
	got := checkpointPayload(failed, nil)
	if got.Error != "boom" {
		t.Errorf("Error = %q, want boom — a replayed failure must render as a failure", got.Error)
	}
	if got.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0", got.RowCount)
	}
}
