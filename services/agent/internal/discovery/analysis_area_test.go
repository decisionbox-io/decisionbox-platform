package discovery

import (
	"context"
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// The Phase 4 loop's analysis half, which the per-insight resume work lifted
// out of RunDiscovery so the loop could choose between running it and
// restoring a draft of it.
//
// It had no test before, because it had no name before: it was ~250 lines in
// the middle of RunDiscovery, which nothing in this repository drives (it
// needs a warehouse, an LLM client, a schema cache and a prompt set). Having
// extracted it, these cover what the loop actually branches on — the four
// dispositions — because a wrong disposition is the one failure mode that
// would silently change which areas a run analyses.

// areaFixture wires the smallest orchestrator that can run analyseArea: a mock
// LLM, a disabled-but-non-nil status reporter (its methods no-op when runID is
// empty, which is what keeps the counters out of these tests), and a picker
// whose vector search is injected.
func areaFixture(t *testing.T, content string, chatErr error, hits []RunStepIndexHit) (*Orchestrator, areaAnalysisInput) {
	t.Helper()
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content: content,
		Usage:   gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}
	provider.Error = chatErr
	client, err := ai.New(provider, "mock-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}
	o := &Orchestrator{aiClient: client, statusReporter: &StatusReporter{}}

	picker := NewAnalysisStepPicker(func(context.Context, string, RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
		return hits, nil
	})
	picker.EstimateRenderedSize = EstimateCompactedRenderedSize

	steps := []models.ExplorationStep{{
		Step: 1, Action: "query_data", QueryPurpose: "sessions by device",
		Query:       "SELECT device, count(*) FROM shop.sessions GROUP BY 1",
		QueryResult: []map[string]any{{"device": "mobile", "n": 1200}},
		RowCount:    1,
	}}
	return o, areaAnalysisInput{
		area:             AnalysisArea{ID: "session_behavior", Name: "Session Behaviour", Description: "how sessions convert"},
		prompts:          ResolvedPrompts{AnalysisAreas: map[string]string{"session_behavior": "Find session patterns."}},
		baseContext:      "the base context",
		datasetsStr:      "shop",
		refDataset:       "shop",
		picker:           picker,
		explorationSteps: steps,
		stepByID:         buildStepIndex(steps),
		currencies:       newCurrencyPlan(nil, nil),
	}
}

// TestAnalyseArea_SkipsAnAreaTheDomainPackHasNoPromptFor — the first thing the
// function does, and the reason it must be a disposition rather than a bare
// `continue`: the area contributes no step at all, so it is absent from the
// analysis log rather than counted as a failed area.
func TestAnalyseArea_SkipsAnAreaTheDomainPackHasNoPromptFor(t *testing.T) {
	o, in := areaFixture(t, `{"insights":[]}`, nil, nil)
	in.prompts = ResolvedPrompts{AnalysisAreas: map[string]string{"something_else": "..."}}

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaSkipped {
		t.Fatalf("disposition = %v, want areaSkipped", out.disposition)
	}
	if out.step.AreaID != "" || len(out.insights) != 0 {
		t.Error("a skipped area must hand back nothing for the loop to append")
	}
}

// TestAnalyseArea_SkipsWhenStepRetrievalFails — the picker is how an area
// finds its evidence; if that fails the remaining areas must still run, so
// this is a skip rather than an abort.
func TestAnalyseArea_SkipsWhenStepRetrievalFails(t *testing.T) {
	o, in := areaFixture(t, `{"insights":[]}`, nil, nil)
	// A picker with no Search is the one failure Pick reports itself.
	in.picker = NewAnalysisStepPicker(nil)

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaSkipped {
		t.Fatalf("disposition = %v, want areaSkipped", out.disposition)
	}
}

// TestAnalyseArea_SkipsWhenNoRelevantStepsWereFound — sending an empty
// evidence block to the model is worse than not asking: it invites invention.
func TestAnalyseArea_SkipsWhenNoRelevantStepsWereFound(t *testing.T) {
	o, in := areaFixture(t, `{"insights":[]}`, nil, nil) // no hits -> nothing picked
	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaSkipped {
		t.Fatalf("disposition = %v, want areaSkipped", out.disposition)
	}
}

// TestAnalyseArea_FailedAnalysisCarriesTheErrorOnTheStep is the disposition the
// caller treats differently from a skip: the step IS appended, which is what
// makes the area count toward the run's failed-area tally and so toward its
// partial/failed classification. It is also the case the per-area checkpoint
// deliberately does not mark, so a resume retries it.
func TestAnalyseArea_FailedAnalysisCarriesTheErrorOnTheStep(t *testing.T) {
	o, in := areaFixture(t, "", errors.New("llm provider 503"), []RunStepIndexHit{{Step: 1, Score: 0.9}})

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaFailed {
		t.Fatalf("disposition = %v, want areaFailed", out.disposition)
	}
	if out.step.Error == "" {
		t.Error("a failed area must carry its error on the step, or it is counted as a success with no insights")
	}
	if out.step.AreaID != "session_behavior" {
		t.Errorf("step area = %q, want the area that failed", out.step.AreaID)
	}
}

// TestAnalyseArea_UnparseableResponseIsAFailureNotAnEmptyArea — a response
// that yields nothing parseable must not look like an area that legitimately
// found nothing, or the resume would skip it as done.
func TestAnalyseArea_UnparseableResponseIsAFailureNotAnEmptyArea(t *testing.T) {
	o, in := areaFixture(t, "not json at all", nil, []RunStepIndexHit{{Step: 1, Score: 0.9}})

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaFailed {
		t.Fatalf("disposition = %v, want areaFailed", out.disposition)
	}
	if out.step.Error == "" {
		t.Error("a parse failure must be recorded on the step")
	}
}

// TestAnalyseArea_LegitimatelyEmptyAreaIsAnalysedNotFailed — the mirror of the
// test above, and the distinction the whole disposition enum exists for: a
// valid envelope with no insights is an area that found nothing, which is a
// result rather than an error.
func TestAnalyseArea_LegitimatelyEmptyAreaIsAnalysedNotFailed(t *testing.T) {
	o, in := areaFixture(t, `{"insights":[]}`, nil, []RunStepIndexHit{{Step: 1, Score: 0.9}})

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaAnalysed {
		t.Fatalf("disposition = %v, want areaAnalysed", out.disposition)
	}
	if out.step.Error != "" {
		t.Errorf("step carries error %q; an empty area is not a failure", out.step.Error)
	}
	if len(out.insights) != 0 {
		t.Errorf("got %d insights, want none", len(out.insights))
	}
}

// TestAnalyseArea_AnalysedAreaCarriesWhatTheLoopAndTheCheckpointNeed is the
// happy path, and it asserts the fields the rest of the pipeline reads off the
// step — because the draft checkpoint persists exactly this struct, so a field
// the function forgets to set is a field a resumed run never gets back.
func TestAnalyseArea_AnalysedAreaCarriesWhatTheLoopAndTheCheckpointNeed(t *testing.T) {
	const resp = `{"insights":[
      {"name":"Mobile converts worse","severity":"high","description":"Mobile sessions convert at a third of desktop.","affected_count":1200,"source_steps":[1]},
      {"name":"Weekend dip","severity":"medium","description":"Weekend sessions convert below weekdays.","affected_count":300,"source_steps":[1]}
    ]}`
	o, in := areaFixture(t, resp, nil, []RunStepIndexHit{{Step: 1, Score: 0.9}})

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaAnalysed {
		t.Fatalf("disposition = %v, want areaAnalysed", out.disposition)
	}
	if len(out.insights) != 2 {
		t.Fatalf("got %d insights, want 2", len(out.insights))
	}
	s := out.step
	if s.AreaID != "session_behavior" || s.AreaName != "Session Behaviour" {
		t.Errorf("area identity lost: %q / %q", s.AreaID, s.AreaName)
	}
	if s.RunAt.IsZero() {
		t.Error("RunAt unset — the analysis-generation fingerprint is built from it, so a zero value would make every attempt's fingerprint differ")
	}
	if s.Prompt == "" || s.Response == "" {
		t.Error("the audit dialog is not recorded")
	}
	if s.RelevantQueries != 1 || s.QueryResultsChars == 0 {
		t.Errorf("picker sizing not recorded: queries=%d chars=%d", s.RelevantQueries, s.QueryResultsChars)
	}
	if len(s.SelectedSteps) != 1 {
		t.Errorf("selected-step telemetry = %d entries, want 1", len(s.SelectedSteps))
	}
	if s.TokensIn == 0 || s.TokensOut == 0 {
		t.Errorf("token accounting not recorded: %d/%d", s.TokensIn, s.TokensOut)
	}
	// The insights must be the same objects the step carries, because the
	// draft checkpoint stores step.Insights and the loop validates `insights`.
	if len(s.Insights) != len(out.insights) {
		t.Fatalf("step carries %d insights, outcome carries %d", len(s.Insights), len(out.insights))
	}
	for i := range out.insights {
		if s.Insights[i].ID != out.insights[i].ID {
			t.Errorf("insight %d differs between the step and the outcome", i)
		}
		if out.insights[i].ID == "" {
			t.Error("insight has no id — it keys the standalone row and the Qdrant point")
		}
		if out.insights[i].AnalysisArea != "session_behavior" {
			t.Errorf("insight %d not stamped with its area; the fingerprint groups by it", i)
		}
	}
	// Produced before validation, which is what a draft represents.
	if len(s.ValidationResults) != 0 {
		t.Error("analyseArea must not produce validation results; Phase 4.5 does")
	}
}

// TestAnalyseArea_ReportsSupersessionRatherThanContinuing — between the
// analysis call returning and validation starting, this attempt may have lost
// the run. Carrying on would spend the per-insight validation on a result that
// is about to be discarded, and would write run-step rows under another
// attempt's run.
func TestAnalyseArea_ReportsSupersessionRatherThanContinuing(t *testing.T) {
	o, in := areaFixture(t, `{"insights":[]}`, nil, []RunStepIndexHit{{Step: 1, Score: 0.9}})
	doc := newFakeRunDoc()
	doc.owns = false
	o.statusReporter = reporterFor(doc, 2)

	out := o.analyseArea(context.Background(), in)
	if out.disposition != areaSuperseded {
		t.Fatalf("disposition = %v, want areaSuperseded", out.disposition)
	}
}
