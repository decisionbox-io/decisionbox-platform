package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- Test LLM providers with controllable behaviour ---------------

// smallWindowLLM is a Provider whose ProviderMeta declares a tiny
// MaxInputTokens. Lets the Ask handler hit the trim / 413 paths
// without needing real provider context windows. Captures every
// ChatRequest so tests can assert on the trimmed message slice.
type smallWindowLLM struct {
	mu       sync.Mutex
	requests []gollm.ChatRequest
	response *gollm.ChatResponse
	err      error
}

func (s *smallWindowLLM) Chat(_ context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	if s.response != nil {
		return s.response, nil
	}
	return &gollm.ChatResponse{Content: "ok", Model: "small-llm", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}
func (s *smallWindowLLM) Validate(_ context.Context) error { return nil }

func (s *smallWindowLLM) lastRequest() gollm.ChatRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		return gollm.ChatRequest{}
	}
	return s.requests[len(s.requests)-1]
}

var (
	smallLLMMu   sync.Mutex
	smallLLMLast *smallWindowLLM

	failingLLMMu   sync.Mutex
	failingLLMLast *smallWindowLLM
)

// flakyCounterLLM is a provider whose TokenCounter implementation
// errors on its first call, letting us exercise the handler's
// counter-error fallback path.
type flakyCounterLLM struct {
	smallWindowLLM
}

func (f *flakyCounterLLM) TokenCounter(_ context.Context, _ string) (gollm.TokenCounter, error) {
	return &countOnceThenError{}, nil
}

type countOnceThenError struct{ calls int }

func (c *countOnceThenError) Count(_ context.Context, _ string) (int, error) {
	c.calls++
	if c.calls == 1 {
		return 0, errors.New("first-call boom")
	}
	return 1, nil
}

// hugeExactLLM is a provider whose exact verifier always reports a
// count that exceeds the model's window. Lets us exercise the
// verifyExactPromptFits → 413 path without needing a real upstream.
type hugeExactLLM struct {
	smallWindowLLM
	reportedTokens int
}

func (h *hugeExactLLM) TokenCounter(_ context.Context, _ string) (gollm.TokenCounter, error) {
	return &fixedExactCounter{count: h.reportedTokens}, nil
}

type fixedExactCounter struct{ count int }

func (c *fixedExactCounter) Count(_ context.Context, _ string) (int, error) {
	return c.count, nil
}

func init() {
	// Provider with a deliberately tiny context window so trim is
	// triggered by a few normal-length messages. MaxOutputTokens stays
	// large enough to subtract askMaxOutputTokens without going
	// negative (NewBudget clamps to 0 — which surfaces as 413).
	gollm.RegisterWithMeta("test-llm-small-window", func(_ gollm.ProviderConfig) (gollm.Provider, error) {
		p := &smallWindowLLM{
			response: &gollm.ChatResponse{
				Content: "trimmed answer",
				Model:   "small-llm",
				Usage:   gollm.Usage{InputTokens: 10, OutputTokens: 2},
			},
		}
		smallLLMMu.Lock()
		smallLLMLast = p
		smallLLMMu.Unlock()
		return p, nil
	}, gollm.ProviderMeta{
		Name: "test-llm-small-window",
		Models: []gollm.ModelEntry{
			{
				ID:              "small-llm",
				Wire:            gollm.WireAnthropic,
				MaxOutputTokens: 256,
				MaxInputTokens:  4000, // ~1000 tokens of room after reserves/margin
			},
		},
	})

	// Provider that surfaces an upstream context-overflow error so
	// classifyLLMError can be exercised end-to-end.
	gollm.RegisterWithMeta("test-llm-overflow", func(_ gollm.ProviderConfig) (gollm.Provider, error) {
		p := &smallWindowLLM{err: errors.New("anthropic: prompt is too long: 250000 tokens > 200000 max")}
		failingLLMMu.Lock()
		failingLLMLast = p
		failingLLMMu.Unlock()
		return p, nil
	}, gollm.ProviderMeta{
		Name: "test-llm-overflow",
		Models: []gollm.ModelEntry{
			{
				ID:              "overflow-llm",
				Wire:            gollm.WireAnthropic,
				MaxOutputTokens: 1024,
				MaxInputTokens:  200000,
			},
		},
	})

	// Provider with an absurdly small window — used to verify the
	// "question alone exceeds the model's input window" 413 path.
	gollm.RegisterWithMeta("test-llm-tiny", func(_ gollm.ProviderConfig) (gollm.Provider, error) {
		return &smallWindowLLM{response: &gollm.ChatResponse{Content: "ok"}}, nil
	}, gollm.ProviderMeta{
		Name: "test-llm-tiny",
		Models: []gollm.ModelEntry{
			{
				ID:              "tiny-llm",
				Wire:            gollm.WireAnthropic,
				MaxOutputTokens: 16,
				MaxInputTokens:  64, // reserves blow this away → Available()==0
			},
		},
	})

	// Provider whose TokenCounter implementation errors on its first
	// call, letting us exercise the counter-error fallback in the
	// Ask handler (it must drop to ApproximateCounter without
	// failing the request).
	gollm.RegisterWithMeta("test-llm-flaky-counter", func(_ gollm.ProviderConfig) (gollm.Provider, error) {
		return &flakyCounterLLM{
			smallWindowLLM: smallWindowLLM{response: &gollm.ChatResponse{Content: "flaky ok", Model: "flaky-llm"}},
		}, nil
	}, gollm.ProviderMeta{
		Name: "test-llm-flaky-counter",
		Models: []gollm.ModelEntry{
			{
				ID:              "flaky-llm",
				Wire:            gollm.WireAnthropic,
				MaxOutputTokens: 256,
				MaxInputTokens:  4000,
			},
		},
	})

	// Provider whose exact verifier reports 250000 tokens — always
	// over the model's 200000 window. Exercises the
	// verifyExactPromptFits 413 path.
	gollm.RegisterWithMeta("test-llm-huge-exact", func(_ gollm.ProviderConfig) (gollm.Provider, error) {
		return &hugeExactLLM{
			smallWindowLLM: smallWindowLLM{response: &gollm.ChatResponse{Content: "should never see this", Model: "huge-llm"}},
			reportedTokens: 250000,
		}, nil
	}, gollm.ProviderMeta{
		Name: "test-llm-huge-exact",
		Models: []gollm.ModelEntry{
			{
				ID:              "huge-llm",
				Wire:            gollm.WireAnthropic,
				MaxOutputTokens: 1024,
				MaxInputTokens:  200000,
			},
		},
	})
}

func currentSmallLLM() *smallWindowLLM {
	smallLLMMu.Lock()
	defer smallLLMMu.Unlock()
	return smallLLMLast
}

// --- Trim path ------------------------------------------------------

func TestAsk_TrimsHistoryWhenSessionTooLarge(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Name:      "Tiny window project",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm-small-window", Model: "small-llm"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high", DiscoveredAt: time.Now()},
	}}

	// 30 message pairs, each ~400 chars total → way past the 4K
	// MaxInputTokens. Trim must drop most.
	big := strings.Repeat("padding ", 50) // ~400 chars
	msgs := make([]commonmodels.AskSessionMessage, 0, 30)
	for i := 0; i < 30; i++ {
		msgs = append(msgs, commonmodels.AskSessionMessage{
			Question:  "Q" + turnLabel(i) + " " + big,
			Answer:    "A" + turnLabel(i) + " " + big,
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	sessionRepo := &mockAskSessionRepo{
		session: &commonmodels.AskSession{ID: "sess-1", ProjectID: "proj-1", Messages: msgs},
	}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, sessionRepo, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "latest question", SessionID: "sess-1"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := currentSmallLLM().lastRequest()
	// The current question is always sent. Anything else is history.
	if len(got.Messages) < 1 {
		t.Fatalf("ChatRequest had %d messages — current question is missing", len(got.Messages))
	}
	// Trim must have dropped the vast majority of the 30-pair (60-msg)
	// history. Even a generous budget would not fit all 60.
	if len(got.Messages) >= 60 {
		t.Fatalf("trim did not run — got %d messages, expected far fewer than 60", len(got.Messages))
	}
	// And the last user message must be the current question, not an
	// ancient one — so the trim preserved order.
	last := got.Messages[len(got.Messages)-1]
	if last.Role != "user" || last.Content == "" || !strings.Contains(last.Content, "latest question") {
		t.Fatalf("last message lost the current question; got %+v", last)
	}
}

// --- Upstream overflow detection ----------------------------------

func TestAsk_UpstreamOverflowSurfacesAsTyped413(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm-overflow", Model: "overflow-llm"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high"},
	}}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "q"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 on upstream overflow, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeContextOverflow {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeContextOverflow)
	}
}

// --- Missing LLM provider -----------------------------------------

func TestAsk_NoLLMProviderReturns412Typed(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "definitely-not-registered", Model: "x"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d"},
	}}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "q"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 for missing LLM provider, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeLLMNotConfigured {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeLLMNotConfigured)
	}
}

// turnLabel produces a stable, short label for a synthesized turn
// index. Using a letter (vs strconv) keeps the test fixture diffs
// readable when something goes wrong.
func turnLabel(n int) string {
	return string(rune('A' + (n % 26)))
}

// --- Question-alone overflow (test-llm-tiny) ----------------------

func TestAsk_QuestionAloneOverflowReturns413(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm-tiny", Model: "tiny-llm"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high"},
	}}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	// 4K-char question — even with rune/4 approximation this is 1K
	// tokens, way past the 64-token tiny window.
	body, _ := json.Marshal(askRequest{Question: strings.Repeat("alpha ", 800)})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for question-alone overflow, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeContextOverflow {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeContextOverflow)
	}
	if !strings.Contains(resp.Error, "question alone") {
		t.Errorf("Error message should call out 'question alone'; got %q", resp.Error)
	}
}

// --- Assembled-prompt overflow (test-llm-tiny + small RAG) --------

// TestAsk_AssembledPromptOverflowReturns413 covers the gap between
// the "question alone overflows" check and the "RAG+knowledge >
// available" check: the prompt scaffolding wrapper itself
// ("Context from N relevant insights/recommendations:\n\n…
// Question: …") adds tokens that aren't measured by either earlier
// guard. Without the explicit promptTokens > Available() check the
// handler would let an oversized prompt through to the provider.
func TestAsk_AssembledPromptOverflowReturns413(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm-tiny", Model: "tiny-llm"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high"},
	}}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	// Question short enough to slip past the question-alone guard,
	// but combined with the scaffolding wrapper + 1 insight it
	// overshoots the tiny-llm 64-token window.
	body, _ := json.Marshal(askRequest{Question: "what is happening with retention and churn here"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for assembled-prompt overflow, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeContextOverflow {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeContextOverflow)
	}
}

// --- Exact-verifier failure is non-fatal (test-llm-flaky-counter) ---

// TestAsk_ExactVerifierOverflowReturns413 exercises the final
// verifyExactPromptFits gate: even when the approximate budget walk
// cleared the prompt, the exact counter can reveal that the
// upstream-tokenized request still exceeds the model's window.
// Handler must return typed 413 with details naming the exact count.
func TestAsk_ExactVerifierOverflowReturns413(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm-huge-exact", Model: "huge-llm"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high"},
	}}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "small question"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 from exact-verifier overflow, got %d: %s", w.Code, w.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeContextOverflow {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeContextOverflow)
	}
	if !strings.Contains(resp.Error, "verified via exact counter") {
		t.Errorf("Error message should call out 'verified via exact counter'; got %q", resp.Error)
	}
	if !strings.Contains(resp.Details, "exact_tokens=250000") {
		t.Errorf("Details should carry exact_tokens=250000; got %q", resp.Details)
	}
}

// TestAsk_FlakyExactVerifierIsNonFatal covers the rare case where
// the provider's exact counter errors on the final verification
// call (transient /count_tokens 503, e.g.). The Ask handler must
// fall through silently and let the request proceed — the
// approximate walk's 15% safety margin already cleared the prompt,
// and a flaky upstream verifier should never block a user's
// otherwise-valid request.
func TestAsk_FlakyExactVerifierIsNonFatal(t *testing.T) {
	insightID := "11111111-1111-4111-8111-111111111111"
	projectRepo := &mockProjectRepoForSearch{
		project: &models.Project{
			ID:        "proj-1",
			Embedding: goembedding.ProjectConfig{Provider: "test-embedding", Model: "test-model"},
			LLM:       models.LLMConfig{Provider: "test-llm-flaky-counter", Model: "flaky-llm"},
		},
	}
	vs := &mockVectorStoreForSearch{results: []vectorstore.SearchResult{
		{ID: insightID, Score: 0.9, Payload: map[string]interface{}{"type": "insight"}},
	}}
	insightRepo := &mockInsightRepo{insights: []*commonmodels.StandaloneInsight{
		{ID: insightID, ProjectID: "proj-1", DiscoveryID: "disc-1", Name: "n", Description: "d", Severity: "high", DiscoveredAt: time.Now()},
	}}

	h := NewSearchHandler(projectRepo, insightRepo, &mockRecommendationRepo{}, &mockSearchHistoryRepo{}, &mockAskSessionRepo{}, &mockSecretProviderForSearch{}, vs)
	body, _ := json.Marshal(askRequest{Question: "what is happening?"})
	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/ask", bytes.NewReader(body))
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.Ask(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 (flaky exact verifier should not block request), got %d: %s", w.Code, w.Body.String())
	}
}
