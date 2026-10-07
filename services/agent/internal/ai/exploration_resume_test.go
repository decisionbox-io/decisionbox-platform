package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// --- scaffolding -----------------------------------------------------------

// resumeHarness is one scripted engine plus everything a test asserts
// against: what the model was asked, what the warehouse was asked, and what
// the live-UI / checkpoint hooks saw.
type resumeHarness struct {
	engine   *ExplorationEngine
	llm      *testutil.MockLLMProvider
	wh       *testutil.MockWarehouseProvider
	schema   *fakeSchemaProvider
	onSteps  []int
	perssted []models.ExplorationStep
	args     []models.CheckpointArgs
}

func newResumeHarness(t *testing.T, opts ExplorationEngineOptions, scripted ...string) *resumeHarness {
	t.Helper()
	h := &resumeHarness{
		llm:    testutil.NewMockLLMProvider(),
		wh:     testutil.NewMockWarehouseProvider("ds"),
		schema: &fakeSchemaProvider{},
	}
	for _, s := range scripted {
		h.llm.ResponseQueue = append(h.llm.ResponseQueue, &gollm.ChatResponse{
			Content: s, Model: "mock", StopReason: "end_turn",
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 10},
		})
	}
	// A scripted run that over-runs its queue must fail loudly rather than
	// quietly completing: "the engine made one more call than expected" is
	// exactly the bug these tests are looking for.
	h.llm.DefaultResponse = &gollm.ChatResponse{
		Content: `{"done": true, "summary": "UNSCRIPTED CALL"}`,
		Model:   "mock", StopReason: "end_turn",
		Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}

	client, err := New(h.llm, "mock")
	if err != nil {
		t.Fatal(err)
	}
	opts.Client = client
	if opts.Executor == nil {
		opts.Executor = queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: h.wh, MaxRetries: 1})
	}
	if opts.SchemaProvider == nil {
		opts.SchemaProvider = h.schema
	}
	if opts.Dataset == "" {
		opts.Dataset = "ds"
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = 20
	}
	opts.OnStep = func(stepNum int, _, _, _ string, _ int, _ int64, _ bool, _ string, _, _ int, _ string) {
		h.onSteps = append(h.onSteps, stepNum)
	}
	opts.PersistStep = func(_ context.Context, step models.ExplorationStep, args models.CheckpointArgs) error {
		h.perssted = append(h.perssted, step)
		h.args = append(h.args, args)
		return nil
	}
	h.engine = NewExplorationEngine(opts)
	return h
}

func (h *resumeHarness) run(t *testing.T) *ExplorationResult {
	t.Helper()
	res, err := h.engine.Explore(context.Background(), ExplorationContext{
		ProjectID: "proj-resume", Dataset: "ds", InitialPrompt: "Explore.",
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	return res
}

// warehouseQueries counts the statements that actually reached the warehouse.
func (h *resumeHarness) warehouseQueries() int {
	n := 0
	for _, c := range h.wh.Calls {
		if c.Method == "Query" || c.Method == "RunQuery" {
			n++
		}
	}
	return n
}

// queryStep builds a checkpointed query_data step as the checkpoint writer
// would have left it: a bounded row sample with the TRUE row count beside it.
func queryStep(n int, rowCount int, sample []map[string]interface{}) models.ExplorationCheckpoint {
	compact := gomodels.BuildCompactResult(sample)
	return models.ExplorationCheckpoint{
		Step: models.ExplorationStep{
			Step:            n,
			Action:          "query_data",
			Thinking:        fmt.Sprintf("step %d thinking", n),
			QueryPurpose:    fmt.Sprintf("purpose %d", n),
			Query:           fmt.Sprintf("SELECT %d FROM ds.t", n),
			QueryResult:     sample,
			RowCount:        rowCount,
			ExecutionTimeMs: 42,
			CompactResult:   &compact,
			TokensIn:        111,
			TokensOut:       222,
		},
	}
}

func rows(n int) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, map[string]interface{}{"id": int64(i), "label": fmt.Sprintf("row-%d", i)})
	}
	return out
}

// --- the regression guard for every non-resumed run ------------------------

// TestExplore_NoResumeIsUnchanged is the test that matters most to the
// deployments that will never resume anything: an absent and an empty
// ResumeState must both behave exactly as no resume wiring at all.
func TestExplore_NoResumeIsUnchanged(t *testing.T) {
	for name, resume := range map[string]*ResumeState{
		"nil":        nil,
		"empty":      {},
		"nil-slice":  {Steps: nil},
		"zero-slice": {Steps: []models.ExplorationCheckpoint{}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newResumeHarness(t, ExplorationEngineOptions{Resume: resume},
				`{"thinking": "look", "query": "SELECT 1 FROM ds.t"}`,
				`{"done": true, "summary": "done"}`,
			)
			res := h.run(t)
			if !res.Completed {
				t.Fatalf("run did not complete: %+v", res)
			}
			if res.TotalSteps != 2 {
				t.Errorf("TotalSteps = %d, want 2", res.TotalSteps)
			}
			if res.Steps[0].Step != 1 {
				t.Errorf("first step number = %d, want 1", res.Steps[0].Step)
			}
			// Every executed step is reported live AND checkpointed.
			if got := len(h.onSteps); got != 2 {
				t.Errorf("onStep calls = %d, want 2", got)
			}
			if got := len(h.perssted); got != 2 {
				t.Errorf("checkpoint writes = %d, want 2", got)
			}
		})
	}
}

// --- conversation shape ----------------------------------------------------

// TestReplay_ConversationShapeAndFirstCall pins the whole point of replay:
// the model must see the transcript it would have seen had the process never
// died, and its FIRST call must be for the next step — not step 1.
func TestReplay_ConversationShapeAndFirstCall(t *testing.T) {
	prefix := []models.ExplorationCheckpoint{
		queryStep(1, 3, rows(3)),
		queryStep(2, 7, rows(7)),
		queryStep(3, 2, rows(2)),
	}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 10,
		Resume:   &ResumeState{Steps: prefix},
	}, `{"done": true, "summary": "nothing left"}`)

	res := h.run(t)

	if len(h.llm.Calls) != 1 {
		t.Fatalf("LLM calls = %d, want exactly 1 (the replayed prefix must not be re-asked)", len(h.llm.Calls))
	}
	if h.warehouseQueries() != 0 {
		t.Errorf("warehouse queries = %d, want 0 — a replayed query_data must never re-execute", h.warehouseQueries())
	}

	msgs := h.llm.Calls[0].Request.Messages
	// 1 initial user message + 2 per replayed step.
	wantMsgs := 1 + 2*len(prefix)
	if len(msgs) != wantMsgs {
		t.Fatalf("messages on the first call = %d, want %d", len(msgs), wantMsgs)
	}
	for i, m := range msgs {
		want := "user"
		if i%2 == 1 {
			want = "assistant"
		}
		if m.Role != want {
			t.Errorf("message[%d].Role = %q, want %q (turns must strictly alternate)", i, m.Role, want)
		}
	}
	// The last message is step 3's RESULT, so the model answers with step 4.
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "Rows returned: 2") {
		t.Errorf("last message is not step 3's result: %q", last.Content)
	}
	// And the assistant turn before it is step 3's action.
	prev := msgs[len(msgs)-2]
	if !strings.Contains(prev.Content, "SELECT 3 FROM ds.t") {
		t.Errorf("penultimate message is not step 3's action: %q", prev.Content)
	}

	// Step numbering continues rather than restarting.
	if res.TotalSteps != 4 {
		t.Errorf("TotalSteps = %d, want 4 (3 replayed + the done on step 4)", res.TotalSteps)
	}
	if len(res.Steps) != 4 {
		t.Fatalf("Steps len = %d, want 4", len(res.Steps))
	}
	for i, s := range res.Steps {
		if s.Step != i+1 {
			t.Errorf("Steps[%d].Step = %d, want %d", i, s.Step, i+1)
		}
	}
	// Replayed steps keep their ORIGINAL token counts; they are not re-billed.
	if res.Steps[0].TokensIn != 111 || res.Steps[0].TokensOut != 222 {
		t.Errorf("replayed step tokens = (%d, %d), want the originals (111, 222)",
			res.Steps[0].TokensIn, res.Steps[0].TokensOut)
	}
}

// TestReplay_DoesNotRefireTheLiveStepFeed guards against a double-counted
// run. The previous attempt already wrote these steps' run-step rows and
// already incremented the run document's query / schema-action counters, so
// re-emitting them would duplicate the dashboard's feed and inflate the
// run's totals.
func TestReplay_DoesNotRefireTheLiveStepFeed(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 10,
		Resume:   &ResumeState{Steps: []models.ExplorationCheckpoint{queryStep(1, 3, rows(3)), queryStep(2, 3, rows(3))}},
	}, `{"done": true, "summary": "done"}`)

	h.run(t)

	// Only the new step (3, the completion) is reported live.
	if len(h.onSteps) != 1 || h.onSteps[0] != 3 {
		t.Errorf("onStep calls = %v, want exactly [3] — replayed steps must not re-fire the live feed", h.onSteps)
	}
	// But the prefix IS re-checkpointed under this attempt, so a run that
	// dies twice still resumes from the same place.
	if len(h.perssted) != 3 {
		t.Fatalf("checkpoint writes = %d, want 3 (2 replayed + 1 new)", len(h.perssted))
	}
	for i, s := range h.perssted {
		if s.Step != i+1 {
			t.Errorf("checkpoint[%d] is step %d, want %d", i, s.Step, i+1)
		}
	}
}

// --- budget integrity: the cost-correctness test ---------------------------

// TestReplay_RestoresSchemaBudgets is why replay re-executes the cheap
// actions instead of seeding counters. A resumed run handed a fresh
// lookup budget would quietly let an operator pay for N extra schema
// lookups per resume, and the "K remaining" lines the engine shows the model
// would be wrong.
func TestReplay_RestoresSchemaBudgets(t *testing.T) {
	lookup := func(n int, refs []string) models.ExplorationCheckpoint {
		return models.ExplorationCheckpoint{
			Step: models.ExplorationStep{
				Step: n, Action: "lookup_schema", Thinking: "inspect",
			},
			Args: models.CheckpointArgs{LookupSchema: refs},
		}
	}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps:         10,
		MaxLookupsPerRun: 2,
		Resume: &ResumeState{Steps: []models.ExplorationCheckpoint{
			lookup(1, []string{"ds.users"}),
			lookup(2, []string{"ds.orders"}),
		}},
	},
		`{"thinking": "one more lookup", "lookup_schema": ["ds.payments"]}`,
		`{"done": true, "summary": "done"}`,
	)

	res := h.run(t)

	// Both replayed lookups went through the real provider, which is what
	// consumed the budget.
	if h.schema.lookupCalls != 2 {
		t.Errorf("provider lookup calls during replay = %d, want 2", h.schema.lookupCalls)
	}
	if h.engine.lookupsUsed != 2 {
		t.Errorf("lookupsUsed after replay = %d, want 2", h.engine.lookupsUsed)
	}
	// Step 3's lookup must be refused: the budget is spent.
	if len(res.Steps) < 3 {
		t.Fatalf("expected at least 3 steps, got %d", len(res.Steps))
	}
	msgs := h.llm.Calls[1].Request.Messages
	refusal := msgs[len(msgs)-1].Content
	if !strings.Contains(strings.ToLower(refusal), "budget") {
		t.Errorf("step 3's lookup was not refused for budget; result message was %q", refusal)
	}
	if h.schema.lookupCalls != 2 {
		t.Errorf("provider was called again past the budget: %d calls", h.schema.lookupCalls)
	}
}

// TestReplay_RestoresSearchAndCorrelationBudgets is the same cost-integrity
// argument for the other two re-executed actions.
func TestReplay_RestoresSearchAndCorrelationBudgets(t *testing.T) {
	correlations := 0
	// get_correlations compares two of the RUN's datasources, so the engine
	// needs both registered or it (correctly) refuses the pair as unknown.
	wh := testutil.NewMockWarehouseProvider("ds")
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps:                    10,
		MaxSearchesPerRun:           1,
		MaxCorrelationLookupsPerRun: 1,
		PrimaryDatasource:           "ds",
		Executors:                   map[string]*queryexec.QueryExecutor{"ds": exec, "crm": exec},
		CorrelationLookup: func(context.Context, string, string) ([]agentplugin.CorrelationKey, error) {
			correlations++
			return nil, nil
		},
		Resume: &ResumeState{Steps: []models.ExplorationCheckpoint{
			{
				Step: models.ExplorationStep{Step: 1, Action: "search_tables", Thinking: "find"},
				Args: models.CheckpointArgs{SearchTables: "audit log", SearchTopK: 5},
			},
			{
				Step: models.ExplorationStep{Step: 2, Action: "get_correlations", Thinking: "ask"},
				Args: models.CheckpointArgs{CorrelationA: "ds", CorrelationB: "crm"},
			},
		}},
	}, `{"done": true, "summary": "done"}`)

	h.run(t)

	if h.schema.searchCalls != 1 {
		t.Errorf("search calls during replay = %d, want 1", h.schema.searchCalls)
	}
	if h.engine.searchesUsed != 1 {
		t.Errorf("searchesUsed after replay = %d, want 1 (budget must be consumed, not reset)", h.engine.searchesUsed)
	}
	if correlations != 1 {
		t.Errorf("correlation lookups during replay = %d, want 1", correlations)
	}
	if h.engine.correlationLookupsUsed != 1 {
		t.Errorf("correlationLookupsUsed after replay = %d, want 1", h.engine.correlationLookupsUsed)
	}
}

// TestReplay_FetchedTableDedupeSurvives pins the other piece of state
// re-execution restores for free: a resumed run that re-asks for a table it
// already looked up gets the "you already have this" short-circuit rather
// than paying for the lookup twice.
func TestReplay_FetchedTableDedupeSurvives(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 10,
		Resume: &ResumeState{Steps: []models.ExplorationCheckpoint{{
			Step: models.ExplorationStep{Step: 1, Action: "lookup_schema", Thinking: "inspect"},
			Args: models.CheckpointArgs{LookupSchema: []string{"ds.users"}},
		}}},
	},
		`{"thinking": "re-ask", "lookup_schema": ["ds.users"]}`,
		`{"done": true, "summary": "done"}`,
	)

	h.run(t)

	if h.schema.lookupCalls != 1 {
		t.Errorf("provider lookup calls = %d, want 1 — the re-ask must be deduped, not re-fetched", h.schema.lookupCalls)
	}
}

// --- the stop rule across a resume ----------------------------------------

// TestReplay_FloorIsSatisfiedByReplayedSteps pins that the floor counts the
// run's absolute progress, not this process's. A run resumed past its floor
// must be allowed to finish: re-imposing the floor on the new attempt would
// force it to explore its whole budget again, which is the opposite of what
// resume is for.
func TestReplay_FloorIsSatisfiedByReplayedSteps(t *testing.T) {
	prefix := make([]models.ExplorationCheckpoint, 0, 6)
	for i := 1; i <= 6; i++ {
		prefix = append(prefix, queryStep(i, 3, rows(3)))
	}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 20,
		MinSteps: 6,
		Resume:   &ResumeState{Steps: prefix},
	}, `{"done": true, "summary": "enough"}`)

	res := h.run(t)

	if !res.Completed {
		t.Fatalf("a done on step 7 under a floor of 6 was refused: %+v", res)
	}
	if len(h.llm.Calls) != 1 {
		t.Errorf("LLM calls = %d, want 1 — a rejected completion would have forced another", len(h.llm.Calls))
	}
	for _, s := range res.Steps {
		if s.Action == "complete_rejected" {
			t.Errorf("step %d was rejected for the floor even though replay had already cleared it", s.Step)
		}
	}
}

// TestReplay_FloorStillBitesBelowIt is the other half: resume must not
// become a way to shortcut the floor. A prefix of 2 under a floor of 6 still
// refuses a completion.
func TestReplay_FloorStillBitesBelowIt(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 20,
		MinSteps: 6,
		Resume:   &ResumeState{Steps: []models.ExplorationCheckpoint{queryStep(1, 3, rows(3)), queryStep(2, 3, rows(3))}},
	},
		`{"done": true, "summary": "too early"}`,
		`{"thinking": "ok, more", "query": "SELECT 9 FROM ds.t"}`,
		`{"done": true, "summary": "still early"}`,
	)

	res := h.run(t)

	if res.Steps[2].Action != "complete_rejected" {
		t.Errorf("step 3's action = %q, want complete_rejected", res.Steps[2].Action)
	}
	if !strings.Contains(res.Steps[2].Error, "3 < 6") {
		t.Errorf("rejection reason = %q, want it to name the floor as 3 < 6", res.Steps[2].Error)
	}
}

// --- per-action replay fidelity -------------------------------------------

// TestReplay_QueryResultMessageTellsTheTruthAboutRowCount is the row-count
// honesty test for the replay path, and the mirror of the verifier one.
//
// The checkpoint keeps a bounded sample, so the slice the engine holds is
// shorter than the result. Deriving the total from the slice would tell the
// model a 50 000-row query returned 50 rows — a number it would then compute
// shares and conclusions from.
func TestReplay_QueryResultMessageTellsTheTruthAboutRowCount(t *testing.T) {
	sampled := queryStep(1, 50_000, rows(50)) // what the checkpoint retained
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 5,
		Resume:   &ResumeState{Steps: []models.ExplorationCheckpoint{sampled}},
	}, `{"done": true, "summary": "done"}`)

	h.run(t)

	msg := h.llm.Calls[0].Request.Messages[2].Content
	if !strings.Contains(msg, "Rows returned: 50000") {
		t.Errorf("replayed message must report the TRUE row count; got %q", msg)
	}
	if !strings.Contains(msg, "(Showing 10 of 50000 rows)") {
		t.Errorf("replayed message must say which slice of the real total is shown; got %q", msg)
	}
	if strings.Contains(msg, "Rows returned: 50\n") || strings.Contains(msg, "of 50 rows") {
		t.Errorf("replayed message reported the SAMPLE size as the result size: %q", msg)
	}
	// The rows the model sees are real rows from the result, not a digest.
	if !strings.Contains(msg, `"label": "row-0"`) && !strings.Contains(msg, `"label":"row-0"`) {
		t.Errorf("replayed message does not contain the retained rows: %q", msg)
	}
}

// TestReplay_SmallResultIsRenderedVerbatim pins the common case: a result
// that fitted inside the sample replays byte-for-byte as it was shown live,
// with no "showing N of M" note at all.
func TestReplay_SmallResultIsRenderedVerbatim(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 5,
		Resume:   &ResumeState{Steps: []models.ExplorationCheckpoint{queryStep(1, 3, rows(3))}},
	}, `{"done": true, "summary": "done"}`)

	h.run(t)

	msg := h.llm.Calls[0].Request.Messages[2].Content
	if !strings.Contains(msg, "Rows returned: 3") {
		t.Errorf("want the true count; got %q", msg)
	}
	if strings.Contains(msg, "Showing") {
		t.Errorf("a fully retained 3-row result must not claim truncation: %q", msg)
	}
	for i := 0; i < 3; i++ {
		if !strings.Contains(msg, fmt.Sprintf("row-%d", i)) {
			t.Errorf("row %d missing from the replayed message: %q", i, msg)
		}
	}
}

// TestReplay_FailedStepRendersAsFailure is the step that would be most
// dangerous to get wrong: rendering a failed query as a success block would
// invite the model to reason over rows that never existed.
func TestReplay_FailedStepRendersAsFailure(t *testing.T) {
	failed := models.ExplorationCheckpoint{Step: models.ExplorationStep{
		Step: 1, Action: "query_data", Thinking: "try",
		Query: "SELECT bad FROM ds.t",
		Error: "Unrecognized name: bad",
	}}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 5,
		Resume:   &ResumeState{Steps: []models.ExplorationCheckpoint{failed}},
	}, `{"done": true, "summary": "done"}`)

	h.run(t)

	msg := h.llm.Calls[0].Request.Messages[2].Content
	if !strings.Contains(msg, "Query failed: Unrecognized name: bad") {
		t.Errorf("a replayed failed step must render as a failure; got %q", msg)
	}
	if strings.Contains(msg, "Query executed successfully") {
		t.Errorf("a replayed failed step rendered as a success: %q", msg)
	}
}

// TestReplay_CarriesQualityCaveatsIntoTheMessage pins that a step whose rows
// were WITHHELD still says so on replay. The caveat is knowable only at
// execution time, so if it were dropped the resumed run would reason over a
// degraded result believing it complete.
func TestReplay_CarriesQualityCaveatsIntoTheMessage(t *testing.T) {
	cp := queryStep(1, 5, rows(5))
	cp.Step.Quality = []gowarehouse.QualityCaveat{{
		Kind: gowarehouse.QualityWithheld, Detail: "37 of 412 rows withheld",
	}}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 5,
		Resume:   &ResumeState{Steps: []models.ExplorationCheckpoint{cp}},
	}, `{"done": true, "summary": "done"}`)

	h.run(t)

	msg := h.llm.Calls[0].Request.Messages[2].Content
	want := gowarehouse.CaveatInstruction(cp.Step.Quality)
	if want == "" {
		t.Fatal("test setup produced no caveat instruction")
	}
	if !strings.Contains(msg, strings.TrimSpace(want)) {
		t.Errorf("replayed message dropped the quality caveat.\ngot:  %q\nwant it to contain: %q", msg, want)
	}
}

// TestReplay_CompleteRejectedRederivesTheNudge covers each reason class. The
// nudge is re-derived rather than stored so a replayed run shows the current
// wording, and the model has to see that it already tried to finish here.
func TestReplay_CompleteRejectedRederivesTheNudge(t *testing.T) {
	cases := map[string]string{
		"floor":      "required minimum",
		"productive": "has not covered everything",
		"unproven":   "has not covered everything",
	}
	for reason, wantIn := range cases {
		t.Run(reason, func(t *testing.T) {
			cp := models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 1, Action: "complete_rejected", Thinking: "i think im done"},
				Args: models.CheckpointArgs{RejectReason: reason},
			}
			h := newResumeHarness(t, ExplorationEngineOptions{
				MaxSteps: 5, MinSteps: 4,
				Resume: &ResumeState{Steps: []models.ExplorationCheckpoint{cp}},
			},
				`{"thinking": "fine", "query": "SELECT 1 FROM ds.t"}`,
				`{"thinking": "more", "query": "SELECT 2 FROM ds.t"}`,
				`{"thinking": "more", "query": "SELECT 3 FROM ds.t"}`,
				`{"done": true, "summary": "done"}`,
			)

			h.run(t)

			msgs := h.llm.Calls[0].Request.Messages
			nudge := msgs[len(msgs)-1].Content
			if !strings.Contains(nudge, wantIn) {
				t.Errorf("replayed nudge for %q = %q, want it to contain %q", reason, nudge, wantIn)
			}
			// And the assistant turn shows what the model actually emitted:
			// a completion signal, not a synthetic placeholder.
			action := msgs[len(msgs)-2].Content
			if !strings.Contains(action, `"done":true`) && !strings.Contains(action, `"done": true`) {
				t.Errorf("a replayed complete_rejected must show the model's own completion signal; got %q", action)
			}
		})
	}
}

// TestReplay_PrefixEndingInCompleteDoesNotExploreFurther pins the window
// between a completion's checkpoint and the exploration summary's.
//
// If the process died in it, the prefix carries an accepted `complete` step
// with no summary beside it. Falling through left the model an "Unknown
// action: complete" message it never saw and carried on exploring past a
// completion the run had already earned — paying again for exactly the work
// resume exists to protect.
func TestReplay_PrefixEndingInCompleteDoesNotExploreFurther(t *testing.T) {
	prefix := []models.ExplorationCheckpoint{
		queryStep(1, 3, rows(3)),
		queryStep(2, 5, rows(5)),
		{
			Step: models.ExplorationStep{Step: 3, Action: "complete", Thinking: "covered it"},
			Args: models.CheckpointArgs{CompletionReason: "covered every area"},
		},
	}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 20,
		Resume:   &ResumeState{Steps: prefix},
	}) // no scripted responses: any LLM call is a failure

	res := h.run(t)

	if len(h.llm.Calls) != 0 {
		t.Errorf("LLM calls = %d, want 0 — the exploration had already finished", len(h.llm.Calls))
	}
	if h.warehouseQueries() != 0 {
		t.Errorf("warehouse queries = %d, want 0", h.warehouseQueries())
	}
	if !res.Completed {
		t.Error("the replayed completion must mark the result completed")
	}
	if res.CompletionMsg != "covered every area" {
		t.Errorf("CompletionMsg = %q, want the model's own summary", res.CompletionMsg)
	}
	if res.TotalSteps != 3 || len(res.Steps) != 3 {
		t.Errorf("steps = %d / total = %d, want 3 / 3", len(res.Steps), res.TotalSteps)
	}
	if res.Steps[2].Action != "complete" {
		t.Errorf("the final replayed step's action = %q, want complete", res.Steps[2].Action)
	}
}

// TestReplay_CompleteStepAppendsNoResultMessage pins the conversation shape.
// The live loop breaks BEFORE appending a result message for the completion,
// so replay must too — otherwise the transcript gains a turn the original run
// never had.
func TestReplay_CompleteStepAppendsNoResultMessage(t *testing.T) {
	prefix := []models.ExplorationCheckpoint{
		queryStep(1, 3, rows(3)),
		{
			Step: models.ExplorationStep{Step: 2, Action: "complete", Thinking: "done here"},
			Args: models.CheckpointArgs{CompletionReason: "nothing left"},
		},
	}
	h := newResumeHarness(t, ExplorationEngineOptions{MaxSteps: 20})

	// Driven directly: no LLM call happens on this path, so there is no
	// recorded request to read the conversation back out of.
	conv := NewConversation(ConversationOptions{SystemPrompt: "sys", MaxMessages: 50})
	conv.AddUserMessage("initial")
	out := h.engine.replayPrefix(context.Background(), conv, &ResumeState{Steps: prefix})

	if !out.Completed || out.CompletionMsg != "nothing left" {
		t.Errorf("outcome = %+v, want completed with the model's summary", out)
	}

	msgs := conv.GetMessages()
	// initial user message + (assistant, user) for step 1 + the completion's
	// assistant turn, and nothing after it.
	if len(msgs) != 4 {
		t.Fatalf("messages = %d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[3].Role != "assistant" {
		t.Errorf("the last message is %q, want the completion's assistant turn with no reply after it", msgs[3].Role)
	}
	for _, m := range msgs {
		if strings.Contains(m.Content, "Unknown action") {
			t.Errorf("replay produced an Unknown-action message: %q", m.Content)
		}
	}
}

// --- replayedActionJSON: deterministic rendering --------------------------

// TestReplayedActionJSON_Golden pins the exact assistant turn per action
// type. It is the one thing in replay the model parses as its own prior
// output, so drift here changes what the model believes it did.
func TestReplayedActionJSON_Golden(t *testing.T) {
	cases := []struct {
		name string
		cp   models.ExplorationCheckpoint
		want string
	}{
		{
			name: "query_data",
			cp: models.ExplorationCheckpoint{Step: models.ExplorationStep{
				Step: 1, Action: "query_data", Thinking: "count rows",
				Query: "SELECT 1", QueryPurpose: "sanity",
			}},
			want: `{"thinking":"count rows","action":"query_data","query":"SELECT 1","query_purpose":"sanity"}`,
		},
		{
			name: "query_data with a datasource",
			cp: models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 2, Action: "query_data", Thinking: "crm", Query: "SELECT 2"},
				Args: models.CheckpointArgs{Datasource: "crm"},
			},
			want: `{"thinking":"crm","action":"query_data","query":"SELECT 2","datasource_id":"crm"}`,
		},
		{
			name: "lookup_schema",
			cp: models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 3, Action: "lookup_schema", Thinking: "inspect"},
				Args: models.CheckpointArgs{LookupSchema: []string{"ds.users", "ds.orders"}},
			},
			want: `{"thinking":"inspect","action":"lookup_schema","lookup_schema":["ds.users","ds.orders"]}`,
		},
		{
			name: "search_tables",
			cp: models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 4, Action: "search_tables", Thinking: "find"},
				Args: models.CheckpointArgs{SearchTables: "audit log", SearchTopK: 5},
			},
			want: `{"thinking":"find","action":"search_tables","search_tables":"audit log","search_top_k":5}`,
		},
		{
			name: "get_correlations",
			cp: models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 5, Action: "get_correlations", Thinking: "ask"},
				Args: models.CheckpointArgs{CorrelationA: "ds", CorrelationB: "crm"},
			},
			want: `{"thinking":"ask","action":"get_correlations","get_correlations":{"a":"ds","b":"crm"}}`,
		},
		{
			name: "complete carries the model's summary",
			cp: models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 7, Action: "complete", Thinking: "covered it"},
				Args: models.CheckpointArgs{CompletionReason: "covered every area"},
			},
			want: `{"thinking":"covered it","action":"complete","done":true,"summary":"covered every area"}`,
		},
		{
			name: "complete_rejected renders as the completion it was",
			cp: models.ExplorationCheckpoint{
				Step: models.ExplorationStep{Step: 6, Action: "complete_rejected", Thinking: "done?"},
				Args: models.CheckpointArgs{RejectReason: "floor"},
			},
			want: `{"thinking":"done?","action":"complete","done":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := replayedActionJSON(tc.cp)
			if got != tc.want {
				t.Errorf("replayedActionJSON =\n  %s\nwant\n  %s", got, tc.want)
			}
			// Whatever it renders must be parseable as the action it claims
			// to be — the engine's own parser is the authority.
			var probe map[string]any
			if err := json.Unmarshal([]byte(got), &probe); err != nil {
				t.Errorf("rendered action is not valid JSON: %v", err)
			}
		})
	}
}

// TestCheckpointArgsFor_CapturesOnlyTheActionsOwnArgs pins the capture side
// of the same contract: each action records its own arguments and nothing
// else, so a replayed turn cannot claim an argument the model never sent.
func TestCheckpointArgsFor_CapturesOnlyTheActionsOwnArgs(t *testing.T) {
	// A single action struct carrying every field — the parser normalises
	// one mode per turn, but a captured action must not leak the others.
	full := &ExplorationAction{
		Datasource:      "crm",
		LookupSchema:    []string{"ds.users"},
		SearchTables:    "audit",
		SearchTopK:      7,
		GetCorrelations: &CorrelationPair{A: "ds", B: "crm"},
	}

	t.Run("lookup_schema", func(t *testing.T) {
		full.Action = "lookup_schema"
		got := checkpointArgsFor(full)
		if len(got.LookupSchema) != 1 || got.LookupSchema[0] != "ds.users" {
			t.Errorf("LookupSchema = %v, want [ds.users]", got.LookupSchema)
		}
		if got.SearchTables != "" || got.SearchTopK != 0 || got.CorrelationA != "" {
			t.Errorf("leaked another action's args: %+v", got)
		}
	})
	t.Run("search_tables", func(t *testing.T) {
		full.Action = "search_tables"
		got := checkpointArgsFor(full)
		if got.SearchTables != "audit" || got.SearchTopK != 7 {
			t.Errorf("search args = (%q, %d), want (audit, 7)", got.SearchTables, got.SearchTopK)
		}
		if len(got.LookupSchema) != 0 || got.CorrelationA != "" {
			t.Errorf("leaked another action's args: %+v", got)
		}
	})
	t.Run("get_correlations", func(t *testing.T) {
		full.Action = "get_correlations"
		got := checkpointArgsFor(full)
		if got.CorrelationA != "ds" || got.CorrelationB != "crm" {
			t.Errorf("correlation pair = (%q, %q), want (ds, crm)", got.CorrelationA, got.CorrelationB)
		}
		if len(got.LookupSchema) != 0 || got.SearchTables != "" {
			t.Errorf("leaked another action's args: %+v", got)
		}
	})
	t.Run("query_data keeps only the datasource", func(t *testing.T) {
		full.Action = "query_data"
		got := checkpointArgsFor(full)
		if got.Datasource != "crm" {
			t.Errorf("Datasource = %q, want crm", got.Datasource)
		}
		if len(got.LookupSchema) != 0 || got.SearchTables != "" || got.CorrelationA != "" {
			t.Errorf("leaked another action's args: %+v", got)
		}
	})
	t.Run("nil action", func(t *testing.T) {
		got := checkpointArgsFor(nil)
		if got.Datasource != "" || len(got.LookupSchema) != 0 || got.SearchTables != "" ||
			got.SearchTopK != 0 || got.CorrelationA != "" || got.CorrelationB != "" || got.RejectReason != "" {
			t.Errorf("checkpointArgsFor(nil) = %+v, want zero", got)
		}
	})
}

// --- failure tolerance ----------------------------------------------------

// TestCheckpointFailureDoesNotAbortTheRun pins the contract that makes
// checkpointing safe to add: it is best-effort. A Mongo hiccup must cost the
// ability to resume, never the run itself.
func TestCheckpointFailureDoesNotAbortTheRun(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{MaxSteps: 5},
		`{"thinking": "go", "query": "SELECT 1 FROM ds.t"}`,
		`{"done": true, "summary": "done"}`,
	)
	// Swap in a hook that always fails, after the harness built the engine.
	h.engine.persistStep = func(context.Context, models.ExplorationStep, models.CheckpointArgs) error {
		return fmt.Errorf("mongo is down")
	}

	res := h.run(t)

	if !res.Completed {
		t.Fatalf("a failing checkpoint write aborted the run: %+v", res)
	}
	if res.TotalSteps != 2 {
		t.Errorf("TotalSteps = %d, want 2", res.TotalSteps)
	}
}

// TestCheckpointSupersededStopsTheRun pins the one checkpoint failure that is
// NOT swallowed.
//
// ErrAttemptSuperseded does not mean the write failed; it means this process
// is no longer the run. Continuing would spend LLM and warehouse money on
// results that will be discarded, and — worse — could write a checkpoint for
// a step the live attempt has not reached yet, which a later resume would
// replay as the live attempt's own work.
func TestCheckpointSupersededStopsTheRun(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{MaxSteps: 10},
		`{"thinking": "one", "query": "SELECT 1 FROM ds.t"}`,
		`{"thinking": "two", "query": "SELECT 2 FROM ds.t"}`,
		`{"done": true, "summary": "done"}`,
	)
	// Superseded from the second step onward.
	calls := 0
	h.engine.persistStep = func(_ context.Context, step models.ExplorationStep, _ models.CheckpointArgs) error {
		calls++
		if calls >= 2 {
			return ErrAttemptSuperseded
		}
		return nil
	}

	res, err := h.engine.Explore(context.Background(), ExplorationContext{
		ProjectID: "proj", Dataset: "ds", InitialPrompt: "Explore.",
	})
	if !errors.Is(err, ErrAttemptSuperseded) {
		t.Fatalf("Explore err = %v, want ErrAttemptSuperseded", err)
	}
	if !errors.Is(res.Error, ErrAttemptSuperseded) {
		t.Errorf("result.Error = %v, want ErrAttemptSuperseded", res.Error)
	}
	if res.Completed {
		t.Error("a superseded run must not report itself completed")
	}
	// It stopped at the step that found out, rather than running to the cap.
	if len(h.llm.Calls) != 2 {
		t.Errorf("LLM calls = %d, want 2 — the run kept spending after being superseded", len(h.llm.Calls))
	}
	if res.TotalSteps != 2 {
		t.Errorf("TotalSteps = %d, want 2", res.TotalSteps)
	}
}

// TestCheckpointSupersededDuringReplayStopsBeforeExploring is the same guard
// on the replay path: a resumed run that is itself superseded mid-replay must
// stop without making a single LLM call.
func TestCheckpointSupersededDuringReplayStopsBeforeExploring(t *testing.T) {
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 10,
		Resume: &ResumeState{Steps: []models.ExplorationCheckpoint{
			queryStep(1, 3, rows(3)), queryStep(2, 3, rows(3)),
		}},
	}, `{"done": true, "summary": "done"}`)
	h.engine.persistStep = func(context.Context, models.ExplorationStep, models.CheckpointArgs) error {
		return ErrAttemptSuperseded
	}

	_, err := h.engine.Explore(context.Background(), ExplorationContext{
		ProjectID: "proj", Dataset: "ds", InitialPrompt: "Explore.",
	})
	if !errors.Is(err, ErrAttemptSuperseded) {
		t.Fatalf("Explore err = %v, want ErrAttemptSuperseded", err)
	}
	if len(h.llm.Calls) != 0 {
		t.Errorf("LLM calls = %d, want 0 — a superseded run must not explore", len(h.llm.Calls))
	}
	if h.warehouseQueries() != 0 {
		t.Errorf("warehouse queries = %d, want 0", h.warehouseQueries())
	}
}

// TestReplay_PrefixAtMaxStepsEndsWithoutAnLLMCall is the degenerate edge: an
// operator lowered max-steps below what the previous attempt already did.
// The run must end immediately rather than loop or panic.
func TestReplay_PrefixAtMaxStepsEndsWithoutAnLLMCall(t *testing.T) {
	prefix := []models.ExplorationCheckpoint{queryStep(1, 3, rows(3)), queryStep(2, 3, rows(3)), queryStep(3, 3, rows(3))}
	h := newResumeHarness(t, ExplorationEngineOptions{
		MaxSteps: 2, // lower than the 3 already executed
		Resume:   &ResumeState{Steps: prefix},
	})

	res := h.run(t)

	if len(h.llm.Calls) != 0 {
		t.Errorf("LLM calls = %d, want 0 — there is no budget left to explore with", len(h.llm.Calls))
	}
	if res.Completed {
		t.Error("a run that ran out of step budget must not report itself completed")
	}
	if res.TotalSteps != 3 {
		t.Errorf("TotalSteps = %d, want 3 (what was actually executed)", res.TotalSteps)
	}
	if len(res.Steps) != 3 {
		t.Errorf("Steps len = %d, want the 3 replayed steps", len(res.Steps))
	}
}
