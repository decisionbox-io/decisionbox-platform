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
		got, extra, err := selectPlannedRecommendation(c.parsed, plan, c.idx)
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
