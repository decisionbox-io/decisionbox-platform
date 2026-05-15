//go:build integration

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"

	// Real LLM provider registrations.
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/openai"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/vertex-ai"

	"go.mongodb.org/mongo-driver/bson"
)

// Real-provider integration coverage for the Ask handler's
// token-aware trim + typed-error paths. Each provider's tests skip
// cleanly when its credentials are missing, so non-cloud CI can run
// the rest of the suite without any provider being available.
//
// Cost note: these tests consume real LLM invocations. Use the
// cheapest model the provider exposes (Haiku on Bedrock, gpt-4o-mini
// on OpenAI, Gemini Flash on Vertex) and keep prompts short.

// --- Shared scaffolding -------------------------------------------

// skipOnRateLimit short-circuits a test with t.Skip when the upstream
// returned a throttle / unavailable error. Cloud providers can rate-
// limit even authenticated callers; skipping in that case keeps CI
// green when the issue is provider quota, not our code.
func skipOnRateLimit(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "throttling") ||
		strings.Contains(low, "rate limit") ||
		strings.Contains(low, "rate_limit") ||
		strings.Contains(low, "429") ||
		strings.Contains(low, "service unavailable") {
		t.Skipf("upstream rate-limited / unavailable; skipping: %v", err)
	}
}

// askMockVectorStore returns a fixed search result so the integration
// test exercises the LLM path without depending on a real vector
// store. The Ask handler still goes through enrichResults + LLM call.
type askMockVectorStore struct{ results []vectorstore.SearchResult }

func (m *askMockVectorStore) Search(_ context.Context, _ []float64, _ vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	return m.results, nil
}
func (m *askMockVectorStore) Upsert(_ context.Context, _ []vectorstore.Point) error { return nil }
func (m *askMockVectorStore) FindDuplicates(_ context.Context, _ []float64, _, _, _ string, _ float64) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (m *askMockVectorStore) Delete(_ context.Context, _ []string) error      { return nil }
func (m *askMockVectorStore) HealthCheck(_ context.Context) error             { return nil }
func (m *askMockVectorStore) EnsureCollection(_ context.Context, _ int) error { return nil }
func (m *askMockVectorStore) SearchSchemaIndex(_ context.Context, _ string, _ []float64, _ int) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

// askMockEmbedding fakes a deterministic embedding so the Ask flow
// has search results to feed the LLM without needing real embedding
// credentials per provider.
type askMockEmbedding struct{}

func (askMockEmbedding) Embed(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = []float64{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (askMockEmbedding) Dimensions() int                  { return 3 }
func (askMockEmbedding) ModelName() string                { return "test-model" }
func (askMockEmbedding) Validate(_ context.Context) error { return nil }

func ensureMockEmbeddingRegistered(t *testing.T) {
	t.Helper()
	// Already registered in the unit-test build's init(). For the
	// integration build we register on first call; subsequent calls
	// hit RegisterWithMeta's duplicate-panic and we recover quietly.
	defer func() { _ = recover() }()
	goembedding.RegisterWithMeta("test-embedding", func(_ goembedding.ProviderConfig) (goembedding.Provider, error) {
		return askMockEmbedding{}, nil
	}, goembedding.ProviderMeta{
		ID:   "test-embedding",
		Name: "Test Embedding",
		Models: []goembedding.ModelInfo{
			{ID: "test-model", Dimensions: 3},
		},
	})
}

// realAskSetup wires up the Ask handler against the integration
// TestMain's testDB, with a project pointing at a real LLM provider
// + a seeded standalone insight that the mock vector store returns.
type realAskSetup struct {
	projectID string
	insightID string
	handler   *SearchHandler
}

// askSearchHandlerForProvider creates a project pointing at the
// given (real) LLM provider + model, seeds one insight, and wires
// up a SearchHandler against testDB. The Ask handler then runs the
// full budget walk + typed-error machinery against the real upstream.
func askSearchHandlerForProvider(t *testing.T, providerName, model string, config map[string]string) realAskSetup {
	t.Helper()
	ensureMockEmbeddingRegistered(t)

	projectRepo := database.NewProjectRepository(testDB)
	proj := &models.Project{
		Name: providerName + " integration",
		Embedding: goembedding.ProjectConfig{
			Provider: "test-embedding",
			Model:    "test-model",
		},
		LLM: models.LLMConfig{
			Provider: providerName,
			Model:    model,
			Config:   config,
		},
		SchemaIndexStatus: models.SchemaIndexStatusReady,
	}
	if err := projectRepo.Create(context.Background(), proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insightID := "ins-" + providerName + "-" + proj.ID
	if _, err := testDB.Collection("standalone_insights").InsertOne(context.Background(), commonmodels.StandaloneInsight{
		ID:           insightID,
		ProjectID:    proj.ID,
		DiscoveryID:  "disc-real",
		Name:         "Players churn after onboarding",
		Description:  "Day-7 retention drops 30%.",
		Severity:     "high",
		AnalysisArea: "retention",
		DiscoveredAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed insight: %v", err)
	}

	vs := &askMockVectorStore{
		results: []vectorstore.SearchResult{
			{ID: insightID, Score: 0.91, Payload: map[string]interface{}{"type": "insight"}},
		},
	}
	h := NewSearchHandler(
		projectRepo,
		database.NewInsightRepository(testDB),
		database.NewRecommendationRepository(testDB),
		database.NewSearchHistoryRepository(testDB),
		database.NewAskSessionRepository(testDB),
		nil, // no secret provider — provider creds come from env
		vs,
	)
	return realAskSetup{projectID: proj.ID, insightID: insightID, handler: h}
}

// askCleanup deletes the seeded insight + any session rows the test
// produced. Safe to call as t.Cleanup; missing rows are not errors.
func askCleanup(setup realAskSetup) {
	_, _ = testDB.Collection("standalone_insights").DeleteOne(context.Background(), bson.M{"_id": setup.insightID})
	_, _ = testDB.Collection("ask_sessions").DeleteMany(context.Background(), bson.M{"project_id": setup.projectID})
}

// askLongSession builds an N-turn session whose Q/A pairs are large
// enough to stress the trim walk but small enough that even modest
// context windows (100K+) keep us in OK territory. Same prompt
// shape across providers so the assertions match.
func askLongSession(projectID string, turns int) *commonmodels.AskSession {
	pad := strings.Repeat("alpha beta gamma delta epsilon zeta ", 8) // ~280 chars
	msgs := make([]commonmodels.AskSessionMessage, 0, turns)
	for i := 0; i < turns; i++ {
		msgs = append(msgs, commonmodels.AskSessionMessage{
			Question:  "Old question " + strings.Repeat("Q", 4) + " " + pad,
			Answer:    "Old answer " + strings.Repeat("A", 4) + " " + pad,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	return &commonmodels.AskSession{
		ID:           "sess-trim-" + projectID,
		ProjectID:    projectID,
		UserID:       "anonymous",
		Title:        "trim test",
		Messages:     msgs,
		MessageCount: len(msgs),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
}

func asError(s string) error {
	if s == "" {
		return nil
	}
	return &simpleErr{msg: s}
}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

// runAskHappyPath issues a single Ask request and asserts the
// response carries an answer + session id + non-zero input tokens.
// Used by every provider's "new session" test to keep assertions
// uniform.
func runAskHappyPath(t *testing.T, setup realAskSetup, question string, timeout time.Duration) {
	t.Helper()
	body, _ := json.Marshal(askRequest{Question: question})
	req := httptest.NewRequest("POST", "/api/v1/projects/"+setup.projectID+"/ask", bytes.NewReader(body))
	req.SetPathValue("id", setup.projectID)
	w := httptest.NewRecorder()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	setup.handler.Ask(w, req.WithContext(ctx))

	if w.Code == http.StatusBadGateway {
		var resp APIResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		skipOnRateLimit(t, asError(resp.Error))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := resp.Data.(map[string]interface{})
	if data["answer"].(string) == "" {
		t.Fatal("answer was empty")
	}
	if data["session_id"].(string) == "" {
		t.Fatal("session_id missing")
	}
	if input := data["input_tokens"].(float64); input <= 0 {
		t.Errorf("input_tokens = %v, want > 0", input)
	}
}

// runAskTrim seeds a long session and asserts the Ask handler returns
// 200 (i.e. the trim walk kept the prompt under the provider's input
// window).
func runAskTrim(t *testing.T, setup realAskSetup, providerName, model string, turns int, timeout time.Duration) {
	t.Helper()
	session := askLongSession(setup.projectID, turns)
	if err := database.NewAskSessionRepository(testDB).Create(context.Background(), session); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	body, _ := json.Marshal(askRequest{
		Question:  "Given all the above, what should we prioritize?",
		SessionID: session.ID,
	})
	req := httptest.NewRequest("POST", "/api/v1/projects/"+setup.projectID+"/ask", bytes.NewReader(body))
	req.SetPathValue("id", setup.projectID)
	w := httptest.NewRecorder()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	setup.handler.Ask(w, req.WithContext(ctx))

	if w.Code == http.StatusBadGateway {
		var resp APIResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		skipOnRateLimit(t, asError(resp.Error))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (trim should keep us under the window), got %d: %s\nmodel=%s window=%d",
			w.Code, w.Body.String(), model,
			gollm.GetMaxInputTokens(providerName, model))
	}
}

// --- Bedrock ------------------------------------------------------

func bedrockRegion(t *testing.T) string {
	t.Helper()
	r := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if r == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set; skipping Bedrock-backed Ask integration test")
	}
	return r
}

func bedrockModelFor(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("INTEGRATION_TEST_BEDROCK_MODEL"); m != "" {
		return m
	}
	return "global.anthropic.claude-haiku-4-5-20251001-v1:0"
}

func TestIntegration_Ask_RealBedrock_NewSession(t *testing.T) {
	region := bedrockRegion(t)
	setup := askSearchHandlerForProvider(t, "bedrock", bedrockModelFor(t),
		map[string]string{"region": region})
	t.Cleanup(func() { askCleanup(setup) })
	runAskHappyPath(t, setup, "What's the most pressing retention issue?", 90*time.Second)
}

func TestIntegration_Ask_RealBedrock_TrimsLongSession(t *testing.T) {
	region := bedrockRegion(t)
	model := bedrockModelFor(t)
	setup := askSearchHandlerForProvider(t, "bedrock", model,
		map[string]string{"region": region})
	t.Cleanup(func() { askCleanup(setup) })
	runAskTrim(t, setup, "bedrock", model, 50, 120*time.Second)
}

// --- OpenAI -------------------------------------------------------

func openaiAPIKey(t *testing.T) string {
	t.Helper()
	k := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if k == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set; skipping OpenAI-backed Ask integration test")
	}
	return k
}

func openaiModelFor(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("INTEGRATION_TEST_OPENAI_MODEL"); m != "" {
		return m
	}
	return "gpt-4o-mini"
}

// TestIntegration_Ask_RealOpenAI_NewSession is the happy path against
// real OpenAI. Exercises the OpenAI TokenCounter (tiktoken-go with
// the model's declared encoding) end-to-end: budget walk picks the
// exact-tier 5% safety margin, Chat call succeeds, persisted message
// carries non-zero input_tokens from OpenAI's billing.
func TestIntegration_Ask_RealOpenAI_NewSession(t *testing.T) {
	apiKey := openaiAPIKey(t)
	model := openaiModelFor(t)
	setup := askSearchHandlerForProvider(t, "openai", model,
		map[string]string{"api_key": apiKey})
	t.Cleanup(func() { askCleanup(setup) })
	runAskHappyPath(t, setup, "What's the most pressing retention issue?", 60*time.Second)
}

// TestIntegration_Ask_RealOpenAI_TrimsLongSession verifies the
// tiktoken-driven budget walk keeps a 50-turn session inside the
// model's input window. OpenAI's prompt-too-long surface is
// `context_length_exceeded`; if our trim is wrong the request 4xx's
// and the handler reflects it as a typed 413.
func TestIntegration_Ask_RealOpenAI_TrimsLongSession(t *testing.T) {
	apiKey := openaiAPIKey(t)
	model := openaiModelFor(t)
	setup := askSearchHandlerForProvider(t, "openai", model,
		map[string]string{"api_key": apiKey})
	t.Cleanup(func() { askCleanup(setup) })
	runAskTrim(t, setup, "openai", model, 50, 90*time.Second)
}

// --- Vertex AI Gemini --------------------------------------------

func vertexProjectID(t *testing.T) string {
	t.Helper()
	p := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if p == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set; skipping Vertex-backed Ask integration test")
	}
	return p
}

func vertexLocation() string {
	if l := os.Getenv("INTEGRATION_TEST_VERTEX_LOCATION"); l != "" {
		return l
	}
	return "us-central1"
}

func vertexModelFor(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("INTEGRATION_TEST_VERTEX_GEMINI_MODEL"); m != "" {
		return m
	}
	return "gemini-2.5-flash"
}

// TestIntegration_Ask_RealVertex_NewSession exercises the Ask handler
// against Vertex AI Gemini. Vertex does not yet implement
// TokenCounterProvider (a follow-up will wire countTokens REST), so
// the handler picks gollm.ApproximateCounter with the wider 15%
// safety margin — this test confirms that fallback path actually
// produces a passing request end-to-end on the Google-native wire.
func TestIntegration_Ask_RealVertex_NewSession(t *testing.T) {
	projectID := vertexProjectID(t)
	model := vertexModelFor(t)
	setup := askSearchHandlerForProvider(t, "vertex-ai", model,
		map[string]string{
			"project_id": projectID,
			"location":   vertexLocation(),
		})
	t.Cleanup(func() { askCleanup(setup) })
	runAskHappyPath(t, setup, "What's the most pressing retention issue?", 90*time.Second)
}

// TestIntegration_Ask_RealVertex_TrimsLongSession verifies the
// approximate-counter trim path against Gemini's 1M context window.
// Even though Gemini's window dwarfs our 50-turn fixture, the test
// proves the trim walk doesn't fail surprisingly on the Google-
// native wire (different request shape than Anthropic / OpenAI compat).
func TestIntegration_Ask_RealVertex_TrimsLongSession(t *testing.T) {
	projectID := vertexProjectID(t)
	model := vertexModelFor(t)
	setup := askSearchHandlerForProvider(t, "vertex-ai", model,
		map[string]string{
			"project_id": projectID,
			"location":   vertexLocation(),
		})
	t.Cleanup(func() { askCleanup(setup) })
	runAskTrim(t, setup, "vertex-ai", model, 50, 120*time.Second)
}

// --- Ollama -------------------------------------------------------

func ollamaHost(t *testing.T) string {
	t.Helper()
	h := os.Getenv("INTEGRATION_TEST_OLLAMA_HOST")
	if h == "" {
		t.Skip("INTEGRATION_TEST_OLLAMA_HOST not set; skipping Ollama-backed Ask integration test")
	}
	return h
}

func ollamaModelFor(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("INTEGRATION_TEST_OLLAMA_MODEL"); m != "" {
		return m
	}
	// Catalogued via the qwen3 alias list in providers/llm/ollama/catalog.go.
	return "qwen3:32b"
}

// TestIntegration_Ask_RealOllama_NewSession exercises the Ask handler
// against a remote Ollama endpoint (set via INTEGRATION_TEST_OLLAMA_HOST).
// Ollama does not implement TokenCounterProvider, so this verifies the
// approximate-counter fallback with the 15% safety margin against a
// real model. The 60-second deadline is generous for self-hosted
// large-model inference.
func TestIntegration_Ask_RealOllama_NewSession(t *testing.T) {
	host := ollamaHost(t)
	model := ollamaModelFor(t)
	setup := askSearchHandlerForProvider(t, "ollama", model,
		map[string]string{"host": host})
	t.Cleanup(func() { askCleanup(setup) })
	runAskHappyPath(t, setup, "What's the most pressing retention issue?", 180*time.Second)
}

// TestIntegration_Ask_RealOllama_TrimsLongSession verifies the
// approximate-counter trim path against the model's published native
// context (qwen3 ships 128K). Even with the wider safety margin a
// 50-turn fixture has plenty of room — the test is here to prove the
// budget walk does not interact pathologically with Ollama's
// streaming-by-default API.
func TestIntegration_Ask_RealOllama_TrimsLongSession(t *testing.T) {
	host := ollamaHost(t)
	model := ollamaModelFor(t)
	setup := askSearchHandlerForProvider(t, "ollama", model,
		map[string]string{"host": host})
	t.Cleanup(func() { askCleanup(setup) })
	runAskTrim(t, setup, "ollama", model, 50, 240*time.Second)
}
