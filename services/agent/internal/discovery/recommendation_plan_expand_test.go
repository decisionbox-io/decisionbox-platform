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
			// Dense prose in proportion to the tokens it cost — a couple of
			// characters per token, which is what a real document cut off at
			// the limit looks like. A response far shorter than its token
			// count is a different failure with a different repair.
			Content:    `{"recommendations":[{"title":"Recover high-value carts","description":"` + strings.Repeat("long ", 1800),
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
	// Distinct titles per entry: two entries answering with the same
	// recommendation is a duplicate, which the selection refuses on purpose,
	// and this test is about the bound rather than about duplicates.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  fourEntries,
		"Write entry 1": expandedRec("One"),
		"Write entry 2": expandedRec("Two"),
	})

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

func TestGenerateRecommendations_ExpansionReturningTheWholeSetKeepsItsOwnEntry(t *testing.T) {
	// The costly way to get this wrong: asked for the second entry and handed
	// the whole set, taking the first object would save the first entry twice,
	// lose the second, and stamp the second's planned citations onto the
	// duplicate.
	wholeSet := `{"recommendations":[
		{"title":"Recover high-value carts","priority":1,"expected_impact":{"metric":"m"}},
		{"title":"Reactivate lapsed buyers","priority":2,"expected_impact":{"metric":"m"}}
	]}`
	o, _ := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("Recover high-value carts"),
		"Write entry 2": wholeSet,
	})

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if recs[1].Title != "Reactivate lapsed buyers" {
		t.Errorf("entry 2 kept %q, want its own planned entry", recs[1].Title)
	}
	if recs[0].Title == recs[1].Title {
		t.Errorf("both entries carry %q — the first was saved twice", recs[0].Title)
	}
	// The second entry cites one insight, the first cites two. A first-object
	// pick would have given the duplicate the second's single citation.
	if len(recs[1].RelatedInsightIDs) != 1 || recs[1].RelatedInsightIDs[0] != "i-2" {
		t.Errorf("entry 2 citations = %v, want its own planned [i-2]", recs[1].RelatedInsightIDs)
	}
	if step.RecommendationsDroppedParse != 1 {
		t.Errorf("RecommendationsDroppedParse = %d, want 1 (the extra object)", step.RecommendationsDroppedParse)
	}
}

func TestGenerateRecommendations_AmbiguousExpansionIsReAsked(t *testing.T) {
	// Several objects and no way to tell which is the planned one: refused and
	// re-asked rather than guessed at.
	ambiguous := `{"recommendations":[
		{"title":"Something else entirely","priority":1,"expected_impact":{"metric":"m"}},
		{"title":"Another thing","priority":2,"expected_impact":{"metric":"m"}}
	]}`
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: validPlanEnvelope, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: ambiguous, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, _ := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 (the ambiguous answer re-asked and recovered)", len(recs))
	}
	if recs[0].Title != "Recover high-value carts" {
		t.Errorf("entry 1 kept %q, want the retry's answer", recs[0].Title)
	}
}

func TestGenerateRecommendations_EmptyPlanIsAnAnswerNotAFailure(t *testing.T) {
	// The model is entitled to decide the insights support nothing actionable,
	// and the single document has always been allowed to say so. Re-asking
	// would spend one long generation arguing with it and another on the
	// fallback.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker: `{"plan":[]}`,
	})

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 0 {
		t.Fatalf("recs = %d, want 0", len(recs))
	}
	if len(provider.Calls) != 1 {
		t.Errorf("provider called %d times, want 1 (no re-ask, no fallback)", len(provider.Calls))
	}
	if step.RecommendationPlanRetries != 0 {
		t.Errorf("RecommendationPlanRetries = %d, want 0", step.RecommendationPlanRetries)
	}
	if step.RecommendationsSingleCallFallback {
		t.Error("an answer the model meant must not trigger the fallback")
	}
	if step.Status != "" || step.Error != "" {
		t.Errorf("an empty plan is not an error: status=%q err=%q", step.Status, step.Error)
	}
}

func TestGenerateRecommendations_PlanThatLostEveryEntryIsStillAFailure(t *testing.T) {
	// An empty plan is only an answer when the model meant it. One that
	// arrived with entries and lost every one to the decoder is a failure like
	// any other, and is re-asked and then fallen back from.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker: `{"plan":[{"category":"conversion"},{"title":"  "}]}`,
	})
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    validRecEnvelope,
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if step.RecommendationPlanRetries != 1 {
		t.Errorf("RecommendationPlanRetries = %d, want 1", step.RecommendationPlanRetries)
	}
	if !step.RecommendationsSingleCallFallback {
		t.Error("a plan that lost every entry must fall back")
	}
	if len(recs) == 0 {
		t.Error("the fallback document should have produced recommendations")
	}
}

func TestSelectPlannedRecommendation(t *testing.T) {
	plan := []recommendationPlanItem{
		{Title: "First thing"},
		{Title: "Second thing"},
	}
	rec := func(title string) models.Recommendation { return models.Recommendation{Title: title} }

	cases := []struct {
		name      string
		parsed    []models.Recommendation
		idx       int
		wantTitle string
		wantExtra int
		wantErr   bool
	}{
		{
			name:      "one object is that object",
			parsed:    []models.Recommendation{rec("Reworded entirely")},
			idx:       1,
			wantTitle: "Reworded entirely",
		},
		{
			name:      "the whole set, asked for the second",
			parsed:    []models.Recommendation{rec("First thing"), rec("Second thing")},
			idx:       1,
			wantTitle: "Second thing",
			wantExtra: 1,
		},
		{
			name:      "the whole set, asked for the first",
			parsed:    []models.Recommendation{rec("First thing"), rec("Second thing")},
			idx:       0,
			wantTitle: "First thing",
			wantExtra: 1,
		},
		{
			name:      "matched past case and spacing",
			parsed:    []models.Recommendation{rec("first   THING"), rec("Second thing")},
			idx:       0,
			wantTitle: "first   THING",
			wantExtra: 1,
		},
		{
			name:      "reworded target found by elimination",
			parsed:    []models.Recommendation{rec("First thing"), rec("Lapsed buyers, reactivated")},
			idx:       1,
			wantTitle: "Lapsed buyers, reactivated",
			wantExtra: 1,
		},
		{
			name:    "two copies of the planned title",
			parsed:  []models.Recommendation{rec("Second thing"), rec("second thing")},
			idx:     1,
			wantErr: true,
		},
		{
			name:    "nothing identifiable",
			parsed:  []models.Recommendation{rec("One"), rec("Two")},
			idx:     1,
			wantErr: true,
		},
		{
			name:    "no objects",
			parsed:  nil,
			idx:     0,
			wantErr: true,
		},
	}

	for _, c := range cases {
		got, extra, err := selectPlannedRecommendation(c.parsed, plan, c.idx, nil)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: want an error, got %+v", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got.Title != c.wantTitle {
			t.Errorf("%s: picked %q, want %q", c.name, got.Title, c.wantTitle)
		}
		if extra != c.wantExtra {
			t.Errorf("%s: extra = %d, want %d", c.name, extra, c.wantExtra)
		}
	}
}

func TestGenerateRecommendations_PlanIDsThatNameNoInsightNeverReachTheExpansion(t *testing.T) {
	// The expansion prompt restates the planned ids verbatim for the entry to
	// copy. An id the model invented in the plan would be handed straight back
	// to it as the citation to use — teaching the slug-shaped failure instead
	// of catching it.
	const planWithASlug = `{"plan":[
		{"title":"Recover high-value carts","related_insight_ids":["i-1","conversion:critical:invented-slug"]}
	]}`
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  planWithASlug,
		"Write entry 1": expandedRec("Recover high-value carts"),
	})

	recs, _ := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(provider.Calls) != 2 {
		t.Fatalf("provider called %d times, want 2", len(provider.Calls))
	}
	expansionPrompt := provider.Calls[1].Request.Messages[0].Content
	if strings.Contains(expansionPrompt, "conversion:critical:invented-slug") {
		t.Error("the expansion prompt restated an id that names no insight")
	}
	if !strings.Contains(expansionPrompt, "i-1") {
		t.Error("the expansion prompt dropped the real planned id along with the invented one")
	}
	if len(recs) != 1 || len(recs[0].RelatedInsightIDs) != 1 || recs[0].RelatedInsightIDs[0] != "i-1" {
		t.Errorf("citations = %v, want only the real planned id", recs[0].RelatedInsightIDs)
	}
}

func TestGenerateRecommendations_ReasoningModelGetsThinkingHeadroom(t *testing.T) {
	// On a thinking model the requested output covers the hidden reasoning as
	// well as the answer, so a ceiling sized from the visible JSON alone can
	// be spent before the answer begins — which asking for shorter prose
	// cannot fix. The exploration engine hit exactly this on Qwen3 and
	// DeepSeek-R1 (#341) and the fix there was headroom.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("A"),
		"Write entry 2": expandedRec("B"),
	})
	o.reasoningEnabled = true

	if _, _ = o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds"); len(provider.Calls) != 3 {
		t.Fatalf("provider called %d times, want 3", len(provider.Calls))
	}
	if got, want := provider.Calls[0].Request.MaxTokens, defaultRecommendationPlanOutputTokens+recommendationReasoningHeadroomTokens; got != want {
		t.Errorf("plan call max_tokens = %d, want %d (its own ceiling plus thinking room)", got, want)
	}
	for i, call := range provider.Calls[1:] {
		if got, want := call.Request.MaxTokens, defaultRecommendationExpandOutputTokens+recommendationReasoningHeadroomTokens; got != want {
			t.Errorf("expansion %d max_tokens = %d, want %d", i+1, got, want)
		}
	}

	// A model with no hidden channel is unaffected.
	plain, plainProvider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("A"),
		"Write entry 2": expandedRec("B"),
	})
	_, _ = plain.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")
	if got := plainProvider.Calls[0].Request.MaxTokens; got != defaultRecommendationPlanOutputTokens {
		t.Errorf("non-reasoning plan call max_tokens = %d, want %d unchanged", got, defaultRecommendationPlanOutputTokens)
	}
}

func TestGenerateRecommendations_UncitedExpansionIsReAskedForItsCitation(t *testing.T) {
	// An expanded recommendation normally takes its citation from the plan
	// entry it was written for. When the plan has none either — it cited
	// nothing, or cited ids that name no insight — the only thing left is the
	// downstream backfill, which links the recommendation to every eligible
	// insight and so says nothing about where it came from. The
	// single-document path has always re-asked here (#347); the two-pass path
	// re-asks per entry.
	const planWithoutIDs = `{"plan":[{"title":"Recover high-value carts","category":"conversion","priority":1}]}`
	uncited := expandedRec("Recover high-value carts")
	cited := `{"recommendations":[{"title":"Recover high-value carts","priority":1,` +
		`"expected_impact":{"metric":"m"},"related_insight_ids":["i-2"]}]}`

	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: planWithoutIDs, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: uncited, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: cited, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(provider.Calls) != 3 {
		t.Fatalf("provider called %d times, want 3 (plan, expansion, citation re-ask)", len(provider.Calls))
	}
	if step.RecommendationCitationRetries != 1 {
		t.Errorf("RecommendationCitationRetries = %d, want 1", step.RecommendationCitationRetries)
	}
	if len(recs) != 1 || len(recs[0].RelatedInsightIDs) != 1 || recs[0].RelatedInsightIDs[0] != "i-2" {
		t.Errorf("citations = %v, want the recovered [i-2]", recs[0].RelatedInsightIDs)
	}
	// The re-ask must name the ids it wants copied.
	if !strings.Contains(provider.Calls[2].Request.Messages[0].Content, "i-1") {
		t.Error("the citation re-ask did not spell out the eligible ids")
	}
}

func TestGenerateRecommendations_CitedExpansionIsNotReAsked(t *testing.T) {
	// The plan supplies the citation on the normal path, so the re-ask must
	// not fire — it would be a whole extra generation per entry.
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("Recover high-value carts"),
		"Write entry 2": expandedRec("Reactivate lapsed buyers"),
	})

	_, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(provider.Calls) != 3 {
		t.Errorf("provider called %d times, want 3 (no citation re-ask)", len(provider.Calls))
	}
	if step.RecommendationCitationRetries != 0 {
		t.Errorf("RecommendationCitationRetries = %d, want 0", step.RecommendationCitationRetries)
	}
}

func TestGenerateRecommendations_UncitedExpansionSurvivesAFailedReAsk(t *testing.T) {
	// A recommendation is never thrown away for lacking a citation: the
	// downstream recovery grounds it.
	const planWithoutIDs = `{"plan":[{"title":"Recover high-value carts","priority":1}]}`
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: planWithoutIDs, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: "still no citation here", StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 1 || recs[0].Title != "Recover high-value carts" {
		t.Fatalf("recs = %+v, want the uncited recommendation kept", recs)
	}
	if step.Status != "" {
		t.Errorf("Status = %q, want empty — a recommendation was produced", step.Status)
	}
}

func TestGenerateRecommendations_CitationReAskCannotSubstituteAnotherEntry(t *testing.T) {
	// The citation re-ask is as able to answer with the whole set as the first
	// attempt was, and adopting whichever object carried a valid citation
	// would save another entry twice and lose this one — the protection
	// selectPlannedRecommendation exists to give, bypassed by a second way in.
	const planWithIDsOnOne = `{"plan":[
		{"title":"Recover high-value carts","priority":1,"related_insight_ids":["i-1"]},
		{"title":"Reactivate lapsed buyers","priority":2}
	]}`
	// The re-ask for entry 2 answers with both entries; only entry 1's carries
	// a citation.
	wholeSetCitingOnlyTheFirst := `{"recommendations":[
		{"title":"Recover high-value carts","priority":1,"expected_impact":{"metric":"m"},"related_insight_ids":["i-1"]},
		{"title":"Reactivate lapsed buyers","priority":2,"expected_impact":{"metric":"m"}}
	]}`
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: planWithIDsOnOne, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: wholeSetCitingOnlyTheFirst, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, _ := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if recs[0].Title == recs[1].Title {
		t.Fatalf("both entries carry %q — the re-ask substituted another entry", recs[0].Title)
	}
	if recs[1].Title != "Reactivate lapsed buyers" {
		t.Errorf("entry 2 = %q, want its own planned entry", recs[1].Title)
	}
}

func TestGenerateRecommendations_EmptyPlanFromATruncatedResponseIsRecovered(t *testing.T) {
	// The condition "the model meant it" needs more than a clean parse. Every
	// top-level JSON value is read and the first that produces a plan wins,
	// and this pipeline really does see an empty placeholder followed by the
	// real answer — so a response that was cut off may have had its real plan
	// in the part that never arrived, behind an empty one that did. Accepting
	// that as "nothing to recommend" is the silent zero this whole change
	// exists to remove.
	for _, content := range []string{
		`{"plan":[]}`,
		`{"plan":[]} {"plan":[{"title":"Recover high-value carts"`,
	} {
		provider := testutil.NewMockLLMProvider()
		provider.ResponseQueue = []*gollm.ChatResponse{
			{Content: content, StopReason: "length", Usage: gollm.Usage{InputTokens: 10, OutputTokens: defaultRecommendationPlanOutputTokens}},
			{Content: content, StopReason: "length", Usage: gollm.Usage{InputTokens: 10, OutputTokens: defaultRecommendationPlanOutputTokens}},
		}
		provider.DefaultResponse = &gollm.ChatResponse{
			Content:    validRecEnvelope,
			StopReason: "end_turn",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
		}
		client, _ := ai.New(provider, "mock-model")
		o := &Orchestrator{aiClient: client}

		recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

		if step.RecommendationPlanRetries != 1 {
			t.Errorf("%s: RecommendationPlanRetries = %d, want 1", content, step.RecommendationPlanRetries)
		}
		if !step.RecommendationsSingleCallFallback {
			t.Errorf("%s: a truncated response must not be read as an empty plan", content)
		}
		if len(recs) == 0 {
			t.Errorf("%s: the fallback should have produced recommendations", content)
		}
	}
}

func TestGenerateRecommendations_EmptyPlanIsAnAnswerWhenTheResponseIsComplete(t *testing.T) {
	// The other side of the same condition, and the one that must not regress:
	// a complete empty plan is the model saying the insights support nothing
	// actionable. The token count here would trip the missing-output
	// arithmetic if a complete JSON document were not exempt from it — which
	// is how a structured-output provider's good empty answer got argued with,
	// since the folded tool call discards the scaffolding its tokens went on.
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"plan":[]}`,
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 900},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 0 {
		t.Fatalf("recs = %d, want 0", len(recs))
	}
	if len(provider.Calls) != 1 {
		t.Errorf("provider called %d times, want 1 (no re-ask, no fallback)", len(provider.Calls))
	}
	if step.RecommendationsSingleCallFallback {
		t.Error("an answer the model meant must not trigger the fallback")
	}
	if step.Status != "" || step.Error != "" {
		t.Errorf("a complete empty plan is not an error: status=%q err=%q", step.Status, step.Error)
	}
}

func TestGenerateRecommendations_OutputThatWentMissingIsReAskedWithMoreRoom(t *testing.T) {
	// The repair that is the opposite of the others. A reply that spent its
	// budget without the answer reaching us did not write too much — it never
	// got to the answer — so the next attempt needs more room, not less. This
	// is what an uncatalogued thinking model looks like from the outside: the
	// OpenAI catalog flags no model as reasoning, so an o-series or GPT-5
	// project reaches this phase with effectiveReasoning() false.
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: validPlanEnvelope, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// Entry 1: the whole budget spent, almost nothing delivered, and what
		// did arrive is not a complete document.
		{
			Content:    `{"recommendations":[`,
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
		t.Fatalf("recs = %d, want 2 (the entry recovered on the retry)", len(recs))
	}
	if !step.Calls[1].Degenerate {
		t.Errorf("the vanished reply was not recorded as degenerate: %+v", step.Calls[1])
	}

	first := provider.Calls[1].Request.MaxTokens
	retry := provider.Calls[2].Request.MaxTokens
	if retry <= first {
		t.Errorf("retry max_tokens = %d, want more than the %d the vanished attempt had", retry, first)
	}
	if want := defaultRecommendationExpandOutputTokens + recommendationReasoningHeadroomTokens; retry != want {
		t.Errorf("retry max_tokens = %d, want %d", retry, want)
	}
	retryPrompt := provider.Calls[2].Request.Messages[0].Content
	if !strings.Contains(retryPrompt, "did not reach us") {
		t.Error("the retry must name what went wrong: the response did not reach us")
	}
	if strings.Contains(retryPrompt, "Write the same recommendation again, shorter") {
		t.Error("a reply that never produced an answer must not be asked for a shorter one")
	}
}

func TestGenerateRecommendations_ThinkingModelOutOfBudgetIsReAskedWithMoreRoom(t *testing.T) {
	// A catalogued thinking model that spends its ceiling and still gets cut
	// off mid-answer is not writing too much prose — the thinking and the
	// answer share one budget. More room, and less deliberation, is the only
	// lever there is.
	truncated := `{"recommendations":[{"title":"Recover high-value carts","description":"` + strings.Repeat("long ", 7000)
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: validPlanEnvelope, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{
			Content:    truncated,
			StopReason: "length",
			Usage: gollm.Usage{
				InputTokens:  10,
				OutputTokens: defaultRecommendationExpandOutputTokens + recommendationReasoningHeadroomTokens,
			},
		},
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client, reasoningEnabled: true}

	recs, _ := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 (the entry recovered on the retry)", len(recs))
	}
	first := provider.Calls[1].Request.MaxTokens
	retry := provider.Calls[2].Request.MaxTokens
	if retry <= first {
		t.Errorf("retry max_tokens = %d, want more than the %d the first attempt had", retry, first)
	}
	retryPrompt := provider.Calls[2].Request.Messages[0].Content
	if !strings.Contains(retryPrompt, "Do not plan or deliberate at length") {
		t.Error("a thinking model out of budget must be asked to deliberate less, not to write less")
	}
}

func TestGenerateRecommendations_EveryCallsBudgetReachesTheClassifier(t *testing.T) {
	// A zero budget silently disables the token signal, so the phase would
	// lose its provider-independent truncation check without anything
	// failing. The value itself comes from the client (see
	// TestChatReportsTheBudgetTheRequestFinallyCarried, which covers the
	// adaptive retry lowering it).
	o, provider := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": expandedRec("A"),
		"Write entry 2": expandedRec("B"),
	})

	if _, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds"); len(step.Calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(step.Calls))
	}
	for i := range provider.Calls {
		if provider.Calls[i].Request.MaxTokens <= 0 {
			t.Errorf("call %d carried no output budget", i)
		}
	}
}

func TestClassifyResponse_JudgesAgainstTheReducedBudget(t *testing.T) {
	// The adaptive context-overflow retry lowers max_tokens and tries again.
	// Judged against the original ceiling a reply that spent the lowered
	// budget to the last token reads as one that had room to spare — and a
	// provider that reports no usable stop reason leaves nothing else to go
	// on, so the wrong repair gets chosen in silence.
	const originalCeiling = 4096
	const reducedBudget = 1200

	if h := classifyResponse(facts("", reducedBudget, reducedBudget, strings.Repeat("a", 4000))); !h.truncated {
		t.Error("a reply that spent the budget the request carried is truncated")
	}
	if h := classifyResponse(facts("", reducedBudget, originalCeiling, strings.Repeat("a", 4000))); h.truncated {
		t.Error("judged against the original ceiling the same reply looks healthy — which is the bug")
	}
}

func TestGenerateRecommendations_CitationReAsksAreCountedAcrossEntries(t *testing.T) {
	// The field is the phase's total re-asks, and every planned entry can need
	// one. Assigning the local attempt number instead of counting recorded a
	// dozen re-asks as one, which is the number an operator would use to
	// decide whether the plan is citing badly.
	const planWithoutIDs = `{"plan":[
		{"title":"Recover high-value carts","priority":1},
		{"title":"Reactivate lapsed buyers","priority":2}
	]}`
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: planWithoutIDs, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// Entry 1: written uncited, then re-asked and still uncited.
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// Entry 2: the same.
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 — an uncited recommendation is still kept", len(recs))
	}
	if step.RecommendationCitationRetries != 2 {
		t.Errorf("RecommendationCitationRetries = %d, want 2 (one re-ask per entry)", step.RecommendationCitationRetries)
	}
	// The per-call record keeps the local attempt number, which is where it
	// means something.
	reasks := 0
	for _, c := range step.Calls {
		if c.Kind == models.RecommendationCallExpand && c.Attempt == 1 {
			reasks++
		}
	}
	if reasks != 2 {
		t.Errorf("per-call re-asks = %d, want 2", reasks)
	}
}

func TestGenerateRecommendations_TwoEntriesCannotWriteTheSameRecommendation(t *testing.T) {
	// The expansion may reword its planned title, so two entries can both
	// come back as the same recommendation. Accepting both puts a duplicate in
	// the set, the recommendations page deduplicates by title, and one entry
	// is lost — while the counters report both as written, which is the part
	// that makes it invisible.
	const duplicate = `{"recommendations":[{"title":"One single idea","priority":1,` +
		`"expected_impact":{"metric":"m"},"related_insight_ids":["i-1"]}]}`
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: validPlanEnvelope, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// Entry 1 rewords to "One single idea".
		{Content: duplicate, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// Entry 2 answers with the same thing, then recovers on the re-ask.
		{Content: duplicate, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		{Content: expandedRec("Reactivate lapsed buyers"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if recs[0].Title == recs[1].Title {
		t.Fatalf("the set holds two copies of %q", recs[0].Title)
	}
	if step.RecommendationsExpanded != 2 || step.RecommendationsExpandFailed != 0 {
		t.Errorf("expanded/failed = %d/%d, want 2/0", step.RecommendationsExpanded, step.RecommendationsExpandFailed)
	}
}

func TestGenerateRecommendations_APersistentDuplicateCostsItsOwnEntryOnly(t *testing.T) {
	// When the re-ask duplicates too, that entry fails and is counted — the
	// set is short by one and says so, rather than carrying two copies of
	// another entry.
	const duplicate = `{"recommendations":[{"title":"One single idea","priority":1,` +
		`"expected_impact":{"metric":"m"},"related_insight_ids":["i-1"]}]}`
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    duplicate,
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: validPlanEnvelope, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1", len(recs))
	}
	if step.RecommendationsExpanded != 1 || step.RecommendationsExpandFailed != 1 {
		t.Errorf("expanded/failed = %d/%d, want 1/1", step.RecommendationsExpanded, step.RecommendationsExpandFailed)
	}
	if step.RecommendationsSingleCallFallback {
		t.Error("a partial set is a result; it must not trigger the fallback")
	}
}

func TestGenerateRecommendations_CancellationAccountsForTheEntriesNeverAttempted(t *testing.T) {
	// planned, expanded, failed and skipped have to add up to the plan.
	// Otherwise a cancelled run reads as a run whose model quietly produced
	// less, and the investigation starts from the wrong place.
	o, _ := newRoutedOrchestrator(map[string]string{
		planCallMarker: validPlanEnvelope,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, step := o.generateRecommendations(ctx, "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if step.RecommendationsPlanned != 2 {
		t.Fatalf("RecommendationsPlanned = %d, want 2", step.RecommendationsPlanned)
	}
	if step.RecommendationsExpandSkipped != 2 {
		t.Errorf("RecommendationsExpandSkipped = %d, want 2", step.RecommendationsExpandSkipped)
	}
	total := step.RecommendationsExpanded + step.RecommendationsExpandFailed + step.RecommendationsExpandSkipped
	if total != step.RecommendationsPlanned {
		t.Errorf("expanded+failed+skipped = %d, want the %d planned", total, step.RecommendationsPlanned)
	}
}

func TestGenerateRecommendations_IndependentExpansionsCannotCollideOnAnID(t *testing.T) {
	// Each recommendation is written by its own call, so every model that
	// numbers its output calls the first one "r-1" every time — a single
	// document at least numbered within itself. The id becomes the standalone
	// `recommendations._id` and the Qdrant point id, so a repeat makes the
	// insert fail on a duplicate key and abandons the rest of the indexing.
	withID := func(id, title string) string {
		return `{"recommendations":[{"id":"` + id + `","title":"` + title + `","priority":1,` +
			`"expected_impact":{"metric":"m"},"related_insight_ids":["i-1"]}]}`
	}
	o, _ := newRoutedOrchestrator(map[string]string{
		planCallMarker:  validPlanEnvelope,
		"Write entry 1": withID("r-1", "Recover high-value carts"),
		"Write entry 2": withID("r-1", "Reactivate lapsed buyers"),
	})

	recs, _ := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if recs[0].ID == recs[1].ID {
		t.Fatalf("both recommendations carry id %q", recs[0].ID)
	}
	// The first claim on an id the model supplied is still honoured.
	if recs[0].ID != "r-1" {
		t.Errorf("recs[0].ID = %q, want the model's own r-1 kept", recs[0].ID)
	}
	for i := range recs {
		if recs[i].ID == "" {
			t.Errorf("recs[%d] has no id", i)
		}
	}
}

func TestGenerateRecommendations_CitationReAskKeepsWhatMadeTheExpansionWork(t *testing.T) {
	// An expansion that only succeeded at a raised ceiling, with "deliberate
	// less" attached, must not then be asked to do it again at the base
	// ceiling without it — that reproduces the budget failure in the name of
	// fixing a citation, and the downstream backfill then links the
	// recommendation to every eligible insight.
	const planWithoutIDs = `{"plan":[{"title":"Recover high-value carts","priority":1}]}`
	provider := testutil.NewMockLLMProvider()
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: planWithoutIDs, StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// Attempt 1: the budget goes somewhere the caller cannot see.
		{
			Content:    `{"recommendations":[`,
			StopReason: "length",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: defaultRecommendationExpandOutputTokens},
		},
		// Attempt 2 works, at the raised ceiling — but cites nothing.
		{Content: expandedRec("Recover high-value carts"), StopReason: "end_turn", Usage: gollm.Usage{InputTokens: 10, OutputTokens: 20}},
		// The citation re-ask.
		{
			Content:    `{"recommendations":[{"title":"Recover high-value carts","priority":1,"expected_impact":{"metric":"m"},"related_insight_ids":["i-1"]}]}`,
			StopReason: "end_turn",
			Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
		},
	}
	client, _ := ai.New(provider, "mock-model")
	o := &Orchestrator{aiClient: client}

	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", planExpandInsights, "", "ds")

	if len(provider.Calls) != 4 {
		t.Fatalf("provider called %d times, want 4 (plan, vanished attempt, retry, citation re-ask)", len(provider.Calls))
	}
	recovered := provider.Calls[2].Request
	citation := provider.Calls[3].Request
	if citation.MaxTokens != recovered.MaxTokens {
		t.Errorf("citation re-ask max_tokens = %d, want the %d the expansion succeeded with", citation.MaxTokens, recovered.MaxTokens)
	}
	if !strings.Contains(citation.Messages[0].Content, "Do not plan or deliberate at length") {
		t.Error("the citation re-ask dropped the corrective instruction the expansion needed")
	}
	if !strings.Contains(citation.Messages[0].Content, "related_insight_ids") {
		t.Error("the citation re-ask must still ask for the citation")
	}
	if len(recs) != 1 || len(recs[0].RelatedInsightIDs) != 1 || recs[0].RelatedInsightIDs[0] != "i-1" {
		t.Errorf("citations = %v, want the recovered [i-1]", recs[0].RelatedInsightIDs)
	}
	if step.RecommendationCitationRetries != 1 {
		t.Errorf("RecommendationCitationRetries = %d, want 1", step.RecommendationCitationRetries)
	}
}
