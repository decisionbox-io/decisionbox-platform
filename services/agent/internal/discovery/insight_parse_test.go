package discovery

import (
	"context"
	"reflect"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// --- parseInsights (per-item, tolerant) ---

func TestParseInsights_Envelope(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[
		{"name":"A","severity":"high","affected_count":10},
		{"name":"B","severity":"low","affected_count":"20"}
	]}`
	insights, dropped, err := o.parseInsights(in, "churn")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 2 || dropped != 0 {
		t.Fatalf("got %d insights, %d dropped; want 2, 0", len(insights), dropped)
	}
	// The second insight's string-typed affected_count must be coerced, not dropped.
	if insights[1].AffectedCount != 20 {
		t.Errorf("string affected_count not coerced: %+v", insights[1])
	}
}

func TestParseInsights_TopLevelArray(t *testing.T) {
	// Some models emit a bare top-level array instead of the {"insights": [...]}
	// envelope — the same failure the recommendation parser had to absorb.
	o := &Orchestrator{}
	const arr = `[{"name":"A","severity":"high"},{"name":"B","severity":"low"}]`
	insights, dropped, err := o.parseInsights(arr, "churn")
	if err != nil {
		t.Fatalf("err = %v, want nil (bare array must be accepted)", err)
	}
	if len(insights) != 2 || dropped != 0 {
		t.Fatalf("got %d insights, %d dropped; want 2, 0", len(insights), dropped)
	}
}

func TestParseInsights_SkipsMalformedItem(t *testing.T) {
	// Second item has a genuinely type-wrong field (metrics as an array, not an
	// object) that coercion can't rescue — it must be skipped, not zero the area.
	o := &Orchestrator{}
	const in = `{"insights":[
		{"name":"Good","severity":"high"},
		{"name":"Bad","metrics":[1,2,3]}
	]}`
	insights, dropped, err := o.parseInsights(in, "churn")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || dropped != 1 {
		t.Fatalf("got %d insights, %d dropped; want 1, 1", len(insights), dropped)
	}
	if insights[0].Name != "Good" {
		t.Errorf("kept the wrong insight: %+v", insights[0])
	}
}

func TestParseInsights_WrongEnvelopeKeyIsError(t *testing.T) {
	// An object without the "insights" key is a parse failure, not a
	// legitimately empty result — otherwise it silently yields 0 with no retry.
	o := &Orchestrator{}
	for _, in := range []string{`{"insight":[{"name":"a"}]}`, `{"items":[{"name":"a"}]}`, `{}`, `{"insights":null}`} {
		if _, _, err := o.parseInsights(in, "churn"); err == nil {
			t.Errorf("parseInsights(%s): want error for missing/null insights key, got nil", in)
		}
	}
}

func TestParseInsights_CapitalizedEnvelopeKey(t *testing.T) {
	o := &Orchestrator{}
	insights, _, err := o.parseInsights(`{"Insights":[{"name":"a","severity":"low"}]}`, "churn")
	if err != nil {
		t.Fatalf("err = %v, want nil for capitalized key", err)
	}
	if len(insights) != 1 {
		t.Fatalf("got %d insights, want 1", len(insights))
	}
}

func TestParseInsights_EmptyEnvelopeNoError(t *testing.T) {
	// A legitimately empty area: valid envelope, empty array. Must NOT be an
	// error (that would trigger a pointless retry).
	o := &Orchestrator{}
	insights, dropped, err := o.parseInsights(`{"insights":[]}`, "churn")
	if err != nil {
		t.Fatalf("err = %v, want nil for legitimately empty area", err)
	}
	if len(insights) != 0 || dropped != 0 {
		t.Fatalf("got %d insights, %d dropped; want 0, 0", len(insights), dropped)
	}
}

// --- analyzeAreaInsights (self-heal loop wiring) ---

func newAnalysisOrchestrator(content string) (*Orchestrator, *testutil.MockLLMProvider) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content: content,
		Usage:   gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}
	client, _ := ai.New(provider, "mock-model")
	return &Orchestrator{aiClient: client}, provider
}

const validInsightEnvelope = `{"insights":[
	{"name":"High churn","severity":"critical","affected_count":2847,"risk_score":0.85,"confidence":0.92,"source_steps":[1,3]}
]}`

func TestAnalyzeArea_CleanFirstCallNoRetry(t *testing.T) {
	o, provider := newAnalysisOrchestrator(validInsightEnvelope)
	out := o.analyzeAreaInsights(context.Background(), "churn", "prompt", 1000)
	if out.chatErr != nil || out.parseErr != nil {
		t.Fatalf("clean parse must have no errors: chat=%v parse=%v", out.chatErr, out.parseErr)
	}
	if len(out.insights) != 1 {
		t.Fatalf("insights = %d, want 1", len(out.insights))
	}
	if out.parseRetries != 0 {
		t.Errorf("no retry expected, got %d", out.parseRetries)
	}
	if out.droppedParse != 0 {
		t.Errorf("droppedParse = %d, want 0", out.droppedParse)
	}
	if len(provider.Calls) != 1 {
		t.Errorf("provider called %d times, want 1 (no retry on a clean parse)", len(provider.Calls))
	}
}

func TestAnalyzeArea_LegitEmptyAreaNoRetry(t *testing.T) {
	// A valid-but-empty area (the model genuinely found nothing) must NOT retry.
	o, provider := newAnalysisOrchestrator(`{"insights":[]}`)
	out := o.analyzeAreaInsights(context.Background(), "churn", "prompt", 1000)
	if out.parseErr != nil {
		t.Errorf("empty area must not be a parse error: %v", out.parseErr)
	}
	if len(out.insights) != 0 {
		t.Errorf("insights = %d, want 0", len(out.insights))
	}
	if out.parseRetries != 0 || len(provider.Calls) != 1 {
		t.Errorf("empty area must not retry: retries=%d calls=%d", out.parseRetries, len(provider.Calls))
	}
}

func TestAnalyzeArea_RetryRecovers(t *testing.T) {
	o, provider := newAnalysisOrchestrator("")
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: "not json", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: validInsightEnvelope, Usage: gollm.Usage{InputTokens: 2, OutputTokens: 3}},
	}
	out := o.analyzeAreaInsights(context.Background(), "churn", "prompt", 1000)
	if len(out.insights) != 1 {
		t.Fatalf("insights = %d, want 1 after retry", len(out.insights))
	}
	if out.parseRetries != 1 {
		t.Errorf("parseRetries = %d, want 1", out.parseRetries)
	}
	if out.parseErr != nil {
		t.Errorf("recovered run must have nil parseErr, got %v", out.parseErr)
	}
	if len(provider.Calls) != 2 {
		t.Errorf("provider called %d times, want 2 (initial + 1 retry)", len(provider.Calls))
	}
	// Tokens accumulate across attempts (mirrors the recommendation path).
	if out.tokensIn != 3 || out.tokensOut != 4 {
		t.Errorf("tokens not accumulated: in=%d out=%d, want 3/4", out.tokensIn, out.tokensOut)
	}
}

func TestAnalyzeArea_AllAttemptsFailSetsParseErr(t *testing.T) {
	o, provider := newAnalysisOrchestrator("")
	provider.ResponseQueue = []*gollm.ChatResponse{
		{Content: "garbage one", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		{Content: "garbage two", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
	}
	out := o.analyzeAreaInsights(context.Background(), "churn", "prompt", 1000)
	if len(out.insights) != 0 {
		t.Fatalf("insights = %d, want 0", len(out.insights))
	}
	if out.parseErr == nil {
		t.Error("parseErr must be set when all attempts fail to parse")
	}
	if out.parseRetries != 1 {
		t.Errorf("parseRetries = %d, want 1", out.parseRetries)
	}
	if len(provider.Calls) != 2 {
		t.Errorf("provider called %d times, want 2", len(provider.Calls))
	}
}

func TestAnalyzeArea_OneBadItemNoRetry(t *testing.T) {
	// One salvageable batch with a single unparseable item: keep the good one,
	// count the drop, and do NOT retry (>=1 insight survived).
	o, provider := newAnalysisOrchestrator(`{"insights":[
		{"name":"Good","severity":"high"},
		{"name":"Bad","metrics":[1,2,3]}
	]}`)
	out := o.analyzeAreaInsights(context.Background(), "churn", "prompt", 1000)
	if len(out.insights) != 1 || out.droppedParse != 1 {
		t.Fatalf("got %d insights, %d dropped; want 1, 1", len(out.insights), out.droppedParse)
	}
	if out.parseRetries != 0 || len(provider.Calls) != 1 {
		t.Errorf("a surviving insight must not trigger a retry: retries=%d calls=%d", out.parseRetries, len(provider.Calls))
	}
}

// --- schema drift guard (R2) ---

func TestInsightSchema_MatchesStructTags(t *testing.T) {
	insTags := jsonTagSet(reflect.TypeOf(models.Insight{}))

	schema := insightResponseSchema()
	props := schema["properties"].(map[string]interface{})
	insItems := props["insights"].(map[string]interface{})["items"].(map[string]interface{})
	insProps := insItems["properties"].(map[string]interface{})

	for k := range insProps {
		if !insTags[k] {
			t.Errorf("schema insight property %q has no matching json tag on models.Insight", k)
		}
	}
	// Server-assigned / internal fields must never appear in the generation contract.
	for _, internal := range []string{"id", "analysis_area", "discovered_at", "validation", "description_md", "sql_metadata"} {
		if _, ok := insProps[internal]; ok {
			t.Errorf("schema must not expose internal field %q to the model", internal)
		}
	}
}
