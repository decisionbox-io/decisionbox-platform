package discovery

import (
	"context"
	"reflect"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

const validRecEnvelope = `{"recommendations":[
	{"title":"A","category":"churn","description":"d","priority":1,"target_segment":"s","segment_size":10,
	 "expected_impact":{"metric":"m","estimated_improvement":"+5%","reasoning":"r"},"actions":["x"],
	 "related_insight_ids":["i-1"],"confidence":0.8},
	{"title":"B","priority":2,"expected_impact":"prose impact string"}
]}`

// --- parseRecommendations (Fix 2 + Fix 1 compose) ---

func TestParseRecommendations_Envelope(t *testing.T) {
	recs, dropped, err := parseRecommendations(validRecEnvelope)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 2 || dropped != 0 {
		t.Fatalf("got %d recs, %d dropped; want 2, 0", len(recs), dropped)
	}
	// The second rec's prose expected_impact must be coerced, not dropped.
	if recs[1].ExpectedImpact.Reasoning != "prose impact string" {
		t.Errorf("string impact not coerced: %+v", recs[1].ExpectedImpact)
	}
}

func TestParseRecommendations_TopLevelArray(t *testing.T) {
	// Observed real failure on the dev test project: the model emits a bare
	// top-level array instead of the {"recommendations": [...]} envelope.
	const arr = `[{"title":"A","priority":1,"expected_impact":{"metric":"m"}},
	              {"title":"B","priority":2,"expected_impact":"prose"}]`
	recs, dropped, err := parseRecommendations(arr)
	if err != nil {
		t.Fatalf("err = %v, want nil (bare array must be accepted)", err)
	}
	if len(recs) != 2 || dropped != 0 {
		t.Fatalf("got %d recs, %d dropped; want 2, 0", len(recs), dropped)
	}
}

func TestParseRecommendations_SkipsMalformedItem(t *testing.T) {
	// Second item has a type-wrong scalar field (priority as a string) that the
	// tolerant Impact decoder can't rescue — it must be skipped, not fatal.
	const in = `{"recommendations":[
		{"title":"Good","priority":1,"expected_impact":{"metric":"m"}},
		{"title":"Bad","priority":"high","expected_impact":{"metric":"m"}}
	]}`
	recs, dropped, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 1 || dropped != 1 {
		t.Fatalf("got %d recs, %d dropped; want 1, 1", len(recs), dropped)
	}
	if recs[0].Title != "Good" {
		t.Errorf("kept the wrong rec: %+v", recs[0])
	}
}

func TestParseRecommendations_Garbage(t *testing.T) {
	if _, _, err := parseRecommendations("this is not json at all"); err == nil {
		t.Error("want error on unparseable response, got nil")
	}
}

func TestParseRecommendations_EmptyEnvelope(t *testing.T) {
	recs, dropped, err := parseRecommendations(`{"recommendations":[]}`)
	if err != nil {
		t.Fatalf("err = %v, want nil for legitimately empty result", err)
	}
	if len(recs) != 0 || dropped != 0 {
		t.Fatalf("got %d recs, %d dropped; want 0, 0", len(recs), dropped)
	}
}

// --- generateRecommendations wiring (Fixes 3/4/5) ---

func newRecOrchestrator(content string) (*Orchestrator, *testutil.MockLLMProvider) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content: content,
		Usage:   gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}
	client, _ := ai.New(provider, "mock-model")
	return &Orchestrator{aiClient: client}, provider
}

var recInsights = []models.Insight{{ID: "i-1", Name: "Churn", AnalysisArea: "churn"}}

func TestGenerateRecommendations_StringImpactKept(t *testing.T) {
	o, _ := newRecOrchestrator(validRecEnvelope)
	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds")
	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2", len(recs))
	}
	if step.Error != "" || step.Status != "" {
		t.Errorf("clean parse must leave Error/Status empty: err=%q status=%q", step.Error, step.Status)
	}
	if step.RecommendationParseRetries != 0 {
		t.Errorf("no retry expected, got %d", step.RecommendationParseRetries)
	}
}

func TestGenerateRecommendations_RetryRecovers(t *testing.T) {
	o, provider := newRecOrchestrator("")
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: "not json", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: validRecEnvelope, Usage: gollm.Usage{InputTokens: 2, OutputTokens: 3}},
	}
	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds")
	if len(recs) != 2 {
		t.Fatalf("recs = %d, want 2 after retry", len(recs))
	}
	if step.RecommendationParseRetries != 1 {
		t.Errorf("RecommendationParseRetries = %d, want 1", step.RecommendationParseRetries)
	}
	if step.Error != "" || step.Status != "" {
		t.Errorf("recovered run must have empty Error/Status: err=%q status=%q", step.Error, step.Status)
	}
	if len(provider.Calls) != 2 {
		t.Errorf("provider called %d times, want 2 (initial + 1 retry)", len(provider.Calls))
	}
}

func TestGenerateRecommendations_AllFailSetsStatus(t *testing.T) {
	o, provider := newRecOrchestrator("")
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: "garbage one", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: "garbage two", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds")
	if len(recs) != 0 {
		t.Fatalf("recs = %d, want 0", len(recs))
	}
	if step.Status != statusRecommendationParseError {
		t.Errorf("Status = %q, want %q", step.Status, statusRecommendationParseError)
	}
	if step.Error == "" {
		t.Error("Error must be set when all attempts fail to parse")
	}
	if step.RecommendationParseRetries != 1 {
		t.Errorf("RecommendationParseRetries = %d, want 1", step.RecommendationParseRetries)
	}
}

func TestGenerateRecommendations_NoRetryOnPartialSuccess(t *testing.T) {
	const partial = `{"recommendations":[
		{"title":"Good","priority":1,"expected_impact":{"metric":"m"}},
		{"title":"Bad","priority":"high"}
	]}`
	o, provider := newRecOrchestrator(partial)
	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds")
	if len(recs) != 1 {
		t.Fatalf("recs = %d, want 1 (keep the good one)", len(recs))
	}
	if step.RecommendationsDroppedParse != 1 {
		t.Errorf("RecommendationsDroppedParse = %d, want 1", step.RecommendationsDroppedParse)
	}
	if step.RecommendationParseRetries != 0 {
		t.Errorf("must not retry when >=1 rec survived, got %d retries", step.RecommendationParseRetries)
	}
	if len(provider.Calls) != 1 {
		t.Errorf("provider called %d times, want 1 (no retry)", len(provider.Calls))
	}
}

func TestGenerateRecommendations_LegitEmptyNoRetry(t *testing.T) {
	o, provider := newRecOrchestrator(`{"recommendations":[]}`)
	recs, step := o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds")
	if len(recs) != 0 {
		t.Fatalf("recs = %d, want 0", len(recs))
	}
	if step.Error != "" || step.Status != "" {
		t.Errorf("a legitimately empty result is not an error: err=%q status=%q", step.Error, step.Status)
	}
	if len(provider.Calls) != 1 {
		t.Errorf("provider called %d times, want 1 (no retry on legit empty)", len(provider.Calls))
	}
}

func TestGenerateRecommendations_StructuredOutputGating(t *testing.T) {
	const pn = "test-rec-structured"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) {
		return nil, nil
	}, gollm.ProviderMeta{
		ID:                       pn,
		Name:                     "rec structured test",
		SupportsStructuredOutput: true,
		Models:                   []gollm.ModelEntry{{ID: "mock-model", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 8000}},
	})

	// Supported provider: ResponseFormat is attached.
	o, provider := newRecOrchestrator(validRecEnvelope)
	o.aiClient.SetProvenance("p", "r", pn)
	if _, _ = o.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds"); len(provider.Calls) == 0 {
		t.Fatal("provider was not called")
	}
	if rf := provider.Calls[0].Request.ResponseFormat; rf == nil {
		t.Error("ResponseFormat must be set for a structured-output provider")
	} else if rf.Name != recommendationResponseFormatName {
		t.Errorf("ResponseFormat.Name = %q, want %q", rf.Name, recommendationResponseFormatName)
	}

	// Unsupported provider (no provenance → unknown provider): no format sent.
	o2, provider2 := newRecOrchestrator(validRecEnvelope)
	_, _ = o2.generateRecommendations(context.Background(), "{{INSIGHTS_DATA}}", recInsights, "", "ds")
	if provider2.Calls[0].Request.ResponseFormat != nil {
		t.Error("ResponseFormat must be nil for a provider without structured-output support")
	}
}

// --- applyRecommendationDropStats composition ---

func TestApplyRecommendationDropStats_ComposesParseAndRelated(t *testing.T) {
	step := &models.RecommendationStep{RecommendationsDroppedParse: 2}
	kept := []models.Recommendation{{ID: "a"}}
	applyRecommendationDropStats(step, kept, RecommendationDropStats{Total: 3, MissingIDs: 1, UnknownOrIneligibleID: 2})
	if step.RecommendationsDropped != 5 {
		t.Errorf("RecommendationsDropped = %d, want 5 (2 parse + 3 related)", step.RecommendationsDropped)
	}
	if step.RecommendationsDroppedMissingIDs != 1 || step.RecommendationsDroppedUnknownID != 2 {
		t.Errorf("per-reason counters wrong: %+v", step)
	}
	if step.RecommendationsDroppedParse != 2 {
		t.Errorf("parse count must be preserved, got %d", step.RecommendationsDroppedParse)
	}
}

// --- schema drift guard (Fix 5) ---

func jsonTagSet(typ reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

func TestRecommendationSchema_MatchesStructTags(t *testing.T) {
	recTags := jsonTagSet(reflect.TypeOf(models.Recommendation{}))
	impTags := jsonTagSet(reflect.TypeOf(models.Impact{}))

	schema := recommendationResponseSchema()
	props := schema["properties"].(map[string]interface{})
	recItems := props["recommendations"].(map[string]interface{})["items"].(map[string]interface{})
	recProps := recItems["properties"].(map[string]interface{})

	for k := range recProps {
		if !recTags[k] {
			t.Errorf("schema recommendation property %q has no matching json tag on models.Recommendation", k)
		}
	}
	impProps := recProps["expected_impact"].(map[string]interface{})["properties"].(map[string]interface{})
	for k := range impProps {
		if !impTags[k] {
			t.Errorf("schema expected_impact property %q has no matching json tag on models.Impact", k)
		}
	}
	// Server-assigned / internal fields must never appear in the generation contract.
	for _, internal := range []string{"id", "created_at", "validation", "description_md"} {
		if _, ok := recProps[internal]; ok {
			t.Errorf("schema must not expose internal field %q to the model", internal)
		}
	}
}
