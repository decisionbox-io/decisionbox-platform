package discovery

import (
	"context"
	"strconv"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// The markers the two passes are recognised by, taken from the instruction
// blocks themselves so a reworded block fails these tests rather than
// silently routing every call to the same canned response.
const (
	planCallMarker   = "This response: the plan only"
	expandCallMarker = "This response: one recommendation"
)

var planExpandInsights = []models.Insight{
	{ID: "i-1", Name: "Cart abandonment", AnalysisArea: "conversion"},
	{ID: "i-2", Name: "Lapsed buyers", AnalysisArea: "retention"},
}

// expandedRec is one recommendation document, as an expansion call answers.
func expandedRec(title string) string {
	return `{"recommendations":[{"title":"` + title + `","description":"d","priority":1,` +
		`"expected_impact":{"metric":"m","estimated_improvement":"+5%","reasoning":"r"},` +
		`"actions":["step one"]}]}`
}

// newRoutedOrchestrator wires an orchestrator whose provider answers by which
// pass is asking, keyed on prompt substrings.
func newRoutedOrchestrator(byMarker map[string]string) (*Orchestrator, *testutil.MockLLMProvider) {
	provider := testutil.NewMockLLMProvider()
	for marker, content := range byMarker {
		provider.Responses[marker] = &gollm.ChatResponse{
			Content:    content,
			StopReason: "end_turn",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
		}
	}
	client, _ := ai.New(provider, "mock-model")
	return &Orchestrator{aiClient: client}, provider
}

func callKinds(step *models.RecommendationStep) []string {
	kinds := make([]string, 0, len(step.Calls))
	for _, c := range step.Calls {
		kinds = append(kinds, c.Kind)
	}
	return kinds
}

func TestGenerateRecommendations_PlanThenExpandWritesEachEntry(t *testing.T) {
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("Recover high-value carts"),
		"Write entry 2": expandedRec("Reactivate lapsed buyers"),
	})

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 (one per planned entry)", len(recs))
	}
	if len(provider.Calls) != 3 {
		t.Fatalf("provider called %d times, want 3 (one plan + two expansions)", len(provider.Calls))
	}
	if step.RecommendationsPlanned != 2 || step.RecommendationsExpanded != 2 || step.RecommendationsExpandFailed != 0 {
		t.Errorf("planned/expanded/failed = %d/%d/%d, want 2/2/0",
			step.RecommendationsPlanned, step.RecommendationsExpanded, step.RecommendationsExpandFailed)
	}
	if step.RecommendationsSingleCallFallback {
		t.Error("a working two-pass run must not touch the single-document fallback")
	}
	if step.Status != "" || step.Error != "" {
		t.Errorf("a clean run must leave Status/Error empty: status=%q err=%q", step.Status, step.Error)
	}
	if got := callKinds(step); strings.Join(got, ",") != "plan,expand,expand" {
		t.Errorf("call kinds = %v, want plan,expand,expand", got)
	}
	// Each expansion's citation comes from its plan entry, so the downstream
	// citation recovery has nothing to salvage.
	if len(recs[0].RelatedInsightIDs) != 2 || len(recs[1].RelatedInsightIDs) != 1 {
		t.Errorf("citations not taken from the plan: %v / %v", recs[0].RelatedInsightIDs, recs[1].RelatedInsightIDs)
	}
	// The cost of every call lands on the step, not just the last one.
	if step.TokensOut != 60 {
		t.Errorf("TokensOut = %d, want 60 (three calls at 20)", step.TokensOut)
	}
}

func TestGenerateRecommendations_EveryCallAsksForASmallOutput(t *testing.T) {
	// The fix, stated as the assertion: no call in the phase asks for the
	// model's whole output budget any more. The old single call asked for all
	// of it and the model spent all of it.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("A"),
		"Write entry 2": expandedRec("B"),
	})

	if _, _ = o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds"); len(provider.Calls) != 3 {
		t.Fatalf("provider called %d times, want 3", len(provider.Calls))
	}
	if got := provider.Calls[0].Request.MaxTokens; got != defaultRecommendationPlanOutputTokens {
		t.Errorf("plan call max_tokens = %d, want %d", got, defaultRecommendationPlanOutputTokens)
	}
	for i, call := range provider.Calls[1:] {
		if got := call.Request.MaxTokens; got != defaultRecommendationExpandOutputTokens {
			t.Errorf("expansion %d max_tokens = %d, want %d", i+1, got, defaultRecommendationExpandOutputTokens)
		}
	}
}

func TestGenerateRecommendations_OneFailedExpansionCostsOneEntry(t *testing.T) {
	// The point of the split: a call that fails costs its own entry and
	// nothing else. Entry 1 is unreadable on both of its attempts; entry 2
	// still ships.
	o, _ := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": "I could not do that.",
		"Write entry 2": expandedRec("Reactivate lapsed buyers"),
	})

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 1 || recs[0].Title != "Reactivate lapsed buyers" {
		t.Fatalf("recs = %+v, want only the entry that could be written", recs)
	}
	if step.RecommendationsExpandFailed != 1 || step.RecommendationsExpanded != 1 {
		t.Errorf("expanded/failed = %d/%d, want 1/1", step.RecommendationsExpanded, step.RecommendationsExpandFailed)
	}
	if step.RecommendationsSingleCallFallback {
		t.Error("a partial set is a result; it must not trigger the fallback")
	}
	if step.Status != "" {
		t.Errorf("Status = %q, want empty — the phase produced a recommendation", step.Status)
	}
}

func TestGenerateRecommendations_TruncatedExpansionIsReAskedShorter(t *testing.T) {
	// The retry the old phase did not have. A reply cut off at the cap is
	// re-asked for shorter, not re-asked identically.
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: validPlanEnvelope, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{
			Content:    `{"recommendations":[{"title":"Recover high-value carts","description":"` + strings.Repeat("long ", 200),
			StopReason: "length",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: defaultRecommendationExpandOutputTokens},
		},
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 (the truncated entry recovered on the retry)", len(recs))
	}
	if !step.Calls[1].Truncated {
		t.Errorf("the truncated call was not recorded as truncated: %+v", step.Calls[1])
	}
	if step.Calls[1].StopReason != "length" {
		t.Errorf("stop reason = %q, want it recorded verbatim", step.Calls[1].StopReason)
	}
	retry := provider.Calls[2].Request.Messages[0].Content
	if !strings.Contains(retry, "shorter") {
		t.Error("the retry after a truncation must ask for a shorter answer")
	}
	if strings.Contains(retry, "could not be parsed as JSON") {
		t.Error("a truncated reply must not be re-asked as if it were malformed")
	}
}

func TestGenerateRecommendations_EverythingTruncatedStampsTheTruncatedStatus(t *testing.T) {
	// Zero recommendations because every reply was cut off is a different
	// report from zero because the model wrote nonsense, and the operator-
	// facing fix is different too.
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"plan":[{"title":"cut off here`,
		StopReason: "length",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: defaultRecommendationPlanOutputTokens},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 0 {
		t.Fatalf("recs = %d, want 0", len(recs))
	}
	if step.Status != statusRecommendationTruncated {
		t.Errorf("Status = %q, want %q", step.Status, statusRecommendationTruncated)
	}
	if step.Error == "" {
		t.Error("Error must say why the phase produced nothing")
	}
	if !step.RecommendationsSingleCallFallback {
		t.Error("an unusable plan must still have tried the single-document fallback")
	}
}

func TestGenerateRecommendations_FallbackWhenThePlanIsUnreadable(t *testing.T) {
	// A model that answers the plan call with the full document is not
	// half-accepted: the phase falls back to the shape it used to ask for, so
	// the run ends where it used to rather than worse.
	o, provider := newRoutedOrchestrator(nil)
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    validRecEnvelope,
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 from the fallback document", len(recs))
	}
	if !step.RecommendationsSingleCallFallback {
		t.Error("RecommendationsSingleCallFallback must record that the fallback carried the run")
	}
	if step.RecommendationPlanRetries != 1 {
		t.Errorf("RecommendationPlanRetries = %d, want 1 before giving up on the plan", step.RecommendationPlanRetries)
	}
	if kinds := callKinds(step); kinds[len(kinds)-1] != models.RecommendationCallSingleDocument {
		t.Errorf("call kinds = %v, want the fallback last", kinds)
	}
	if step.Status != "" || step.Error != "" {
		t.Errorf("the fallback succeeded, so Status/Error must be empty: status=%q err=%q", step.Status, step.Error)
	}
}

func TestGenerateRecommendations_PlanFanOutIsBounded(t *testing.T) {
	// The bound exists so a runaway plan cannot become a runaway number of
	// calls.
	t.Setenv(recommendationPlanMaxItemsEnv, "2")
	const fourEntries = `{"plan":[
		{"title":"One","related_insight_ids":["i-1"]},
		{"title":"Two","related_insight_ids":["i-1"]},
		{"title":"Three","related_insight_ids":["i-2"]},
		{"title":"Four","related_insight_ids":["i-2"]}
	]}`
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker: fourEntries,
	})
	provider.Responses[expandCallMarker] = &gollm.ChatResponse{
		Content:    expandedRec("Written"),
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if step.RecommendationsPlanned != 2 {
		t.Errorf("RecommendationsPlanned = %d, want the bound of 2", step.RecommendationsPlanned)
	}
	if len(recs) != 2 {
		t.Errorf("recs = %d, want 2", len(recs))
	}
	if len(provider.Calls) != 3 {
		t.Errorf("provider called %d times, want 3 (plan + two expansions)", len(provider.Calls))
	}
	if !strings.Contains(provider.Calls[0].Request.Messages[0].Content, "At most "+strconv.Itoa(2)+" entries") {
		t.Error("the plan call must tell the model the bound it is held to")
	}
}

func TestGenerateRecommendations_ExpansionKeepsOnlyThePlannedEntry(t *testing.T) {
	// A model that answers one expansion with the rest of the plan would
	// duplicate entries their own calls are about to write.
	twoInOne := `{"recommendations":[
		{"title":"Recover high-value carts","priority":1,"expected_impact":{"metric":"m"}},
		{"title":"Reactivate lapsed buyers","priority":2,"expected_impact":{"metric":"m"}}
	]}`
	o, _ := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": twoInOne,
		"Write entry 2": expandedRec("Reactivate lapsed buyers"),
	})

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 (one per entry, not three)", len(recs))
	}
	if recs[0].Title != "Recover high-value carts" {
		t.Errorf("entry 1 kept %q, want the planned one", recs[0].Title)
	}
	if step.RecommendationsDroppedParse != 1 {
		t.Errorf("RecommendationsDroppedParse = %d, want 1 (the extra object)", step.RecommendationsDroppedParse)
	}
}

func TestGenerateRecommendations_CancelledContextStopsExpanding(t *testing.T) {
	// Each remaining expansion would wait out a full generation before
	// failing the same way, so a finished context ends the pass.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker: validPlanEnvelope,
	})
	provider.Responses[expandCallMarker] = &gollm.ChatResponse{
		Content:    expandedRec("Written"),
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	recs, step := o.generateRecommendations(ctx, "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 0 {
		t.Fatalf("recs = %d, want 0", len(recs))
	}
	if step.RecommendationsSingleCallFallback {
		t.Error("a cancelled run must not spend another long generation on the fallback")
	}
	if len(provider.Calls) > 1 {
		t.Errorf("provider called %d times, want at most the plan call", len(provider.Calls))
	}
}
