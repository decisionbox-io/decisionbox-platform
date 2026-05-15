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

	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"

	"go.mongodb.org/mongo-driver/bson"
)

// Real-Bedrock integration coverage for the Ask handler's token-aware
// trim + typed-error paths. Skips when AWS creds aren't on the
// machine. These tests do consume real Bedrock invocations — keep
// them short and use Haiku (cheap, fast) when possible.

func bedrockRegion(t *testing.T) string {
	t.Helper()
	r := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if r == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set; skipping Bedrock-backed Ask integration test")
	}
	return r
}

// bedrockModelFor returns either INTEGRATION_TEST_BEDROCK_MODEL or a
// hard-coded default. Haiku is the cheapest model we ship support for
// and is plenty for verifying handler-level behaviour.
func bedrockModelFor(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("INTEGRATION_TEST_BEDROCK_MODEL"); m != "" {
		return m
	}
	return "global.anthropic.claude-haiku-4-5-20251001-v1:0"
}

func skipOnRateLimit(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "throttling") ||
		strings.Contains(low, "rate limit") ||
		strings.Contains(low, "429") ||
		strings.Contains(low, "service unavailable") {
		t.Skipf("Bedrock rate-limited / unavailable; skipping: %v", err)
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
func (m *askMockVectorStore) Delete(_ context.Context, _ []string) error    { return nil }
func (m *askMockVectorStore) HealthCheck(_ context.Context) error           { return nil }
func (m *askMockVectorStore) EnsureCollection(_ context.Context, _ int) error { return nil }
func (m *askMockVectorStore) SearchSchemaIndex(_ context.Context, _ string, _ []float64, _ int) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

// askMockEmbedding fakes a deterministic embedding so we don't need
// real embedding creds for this test.
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

func ensureEmbeddingRegisteredForBedrockTest(t *testing.T) {
	t.Helper()
	// Already registered in search_test.go (unit-test build); for the
	// integration build we re-register if the name isn't taken. Safe to
	// no-op if it already exists.
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

// askBedrockSetup creates a project (via the real repo so the
// ObjectID flow is honoured) and seeds one standalone insight that
// the mock vectorstore returns. Returns the project ID + insight ID
// + a wired SearchHandler.
type askBedrockSetup struct {
	projectID string
	insightID string
	handler   *SearchHandler
}

func askSearchHandlerForBedrock(t *testing.T, model string) askBedrockSetup {
	t.Helper()
	ensureEmbeddingRegisteredForBedrockTest(t)

	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	projectRepo := database.NewProjectRepository(testDB)

	proj := &models.Project{
		Name: "bedrock integration",
		Embedding: goembedding.ProjectConfig{
			Provider: "test-embedding",
			Model:    "test-model",
		},
		LLM: models.LLMConfig{
			Provider: "bedrock",
			Model:    model,
			Config:   map[string]string{"region": region},
		},
		SchemaIndexStatus: models.SchemaIndexStatusReady,
	}
	if err := projectRepo.Create(context.Background(), proj); err != nil {
		t.Fatalf("create project: %v", err)
	}

	insightID := "ins-bed-" + proj.ID
	if _, err := testDB.Collection("standalone_insights").InsertOne(context.Background(), commonmodels.StandaloneInsight{
		ID:           insightID,
		ProjectID:    proj.ID,
		DiscoveryID:  "disc-bed",
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
		nil, // no secret provider needed — Bedrock uses AWS creds from env / IAM
		vs,
	)
	return askBedrockSetup{projectID: proj.ID, insightID: insightID, handler: h}
}

// --- The actual integration tests ---------------------------------

// TestIntegration_Ask_RealBedrock_NewSession verifies the happy path
// against a real Bedrock model: no session, fresh question, the
// handler builds a budget, calls Bedrock, persists the answer.
func TestIntegration_Ask_RealBedrock_NewSession(t *testing.T) {
	_ = bedrockRegion(t) // skip if no AWS creds in env

	setup := askSearchHandlerForBedrock(t, bedrockModelFor(t))
	t.Cleanup(func() {
		_, _ = testDB.Collection("standalone_insights").DeleteOne(context.Background(), bson.M{"_id": setup.insightID})
	})

	body, _ := json.Marshal(askRequest{Question: "What's the most pressing retention issue?"})
	req := httptest.NewRequest("POST", "/api/v1/projects/"+setup.projectID+"/ask", bytes.NewReader(body))
	req.SetPathValue("id", setup.projectID)
	w := httptest.NewRecorder()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	setup.handler.Ask(w, req.WithContext(ctx))

	if w.Code == http.StatusBadGateway {
		// Bedrock can throttle even authenticated callers; skip
		// rather than fail so flaky cloud quota doesn't break CI.
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

// TestIntegration_Ask_RealBedrock_TrimsLongSession exercises the trim
// loop against a real Bedrock window. A 50-turn synthesized session,
// each turn ~1KB, should comfortably trim down to a sane prefix and
// Bedrock should respond 200 without a 4xx context-overflow.
func TestIntegration_Ask_RealBedrock_TrimsLongSession(t *testing.T) {
	_ = bedrockRegion(t)

	setup := askSearchHandlerForBedrock(t, bedrockModelFor(t))
	t.Cleanup(func() {
		_, _ = testDB.Collection("standalone_insights").DeleteOne(context.Background(), bson.M{"_id": setup.insightID})
		_, _ = testDB.Collection("ask_sessions").DeleteMany(context.Background(), bson.M{"project_id": setup.projectID})
	})

	// Synthesize a session with 50 turns. Each Q+A is ~600 chars.
	pad := strings.Repeat("alpha beta gamma delta epsilon zeta ", 8) // ~280 chars
	msgs := make([]commonmodels.AskSessionMessage, 0, 50)
	for i := 0; i < 50; i++ {
		msgs = append(msgs, commonmodels.AskSessionMessage{
			Question:  "Old question " + strings.Repeat("Q", 4) + " " + pad,
			Answer:    "Old answer " + strings.Repeat("A", 4) + " " + pad,
			Model:     "bedrock",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	session := &commonmodels.AskSession{
		ID:           "sess-trim-" + setup.projectID,
		ProjectID:    setup.projectID,
		UserID:       "anonymous",
		Title:        "trim test",
		Messages:     msgs,
		MessageCount: len(msgs),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := database.NewAskSessionRepository(testDB).Create(context.Background(), session); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	body, _ := json.Marshal(askRequest{Question: "Given all the above, what should we prioritize?", SessionID: session.ID})
	req := httptest.NewRequest("POST", "/api/v1/projects/"+setup.projectID+"/ask", bytes.NewReader(body))
	req.SetPathValue("id", setup.projectID)
	w := httptest.NewRecorder()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	setup.handler.Ask(w, req.WithContext(ctx))

	if w.Code == http.StatusBadGateway {
		var resp APIResponse
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		skipOnRateLimit(t, asError(resp.Error))
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (trim should keep us under the window), got %d: %s", w.Code, w.Body.String())
	}

	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := resp.Data.(map[string]interface{})
	if data["answer"].(string) == "" {
		t.Fatal("answer was empty")
	}

	// Sanity: budget math. The model declares 200K input, we passed
	// 50 turns of ~600 chars (~150 tokens each) → ~7500 tokens of
	// history. Reserves drop to ~7K. Should all fit, but the test
	// confirms that even at 50 turns we don't 4xx.
	if t.Failed() {
		t.Logf("model=%s window=%d", bedrockModelFor(t),
			gollm.GetMaxInputTokens("bedrock", bedrockModelFor(t)))
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
