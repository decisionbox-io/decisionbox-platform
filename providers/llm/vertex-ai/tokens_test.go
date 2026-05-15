package vertexai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// --- TokenCounter routing -----------------------------------------

func newVertexForTest(model string) *VertexAIProvider {
	return &VertexAIProvider{
		projectID:  "test-project",
		location:   "us-central1",
		model:      model,
		auth:       &gcpAuth{tokenSource: &mockTokenSource{token: "test-token"}},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestVertexProvider_TokenCounter_GeminiUsesCountTokensEndpoint(t *testing.T) {
	p := newVertexForTest("gemini-2.5-flash")
	c, err := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("Gemini counter must register as exact")
	}
	if _, ok := c.(*vertexGeminiTokenCounter); !ok {
		t.Fatalf("got %T, want *vertexGeminiTokenCounter", c)
	}
}

func TestVertexProvider_TokenCounter_ClaudeFallsBackToApproximate(t *testing.T) {
	p := newVertexForTest("claude-sonnet-4-6")
	c, err := p.TokenCounter(context.Background(), "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter (no public count_tokens for Claude on Vertex)", c)
	}
}

func TestVertexProvider_TokenCounter_OpenAICompatMaaSFallsBackToApproximate(t *testing.T) {
	// MaaS Llama uses SentencePiece, not BPE — tiktoken would be
	// systematically wrong, so the provider falls back to approximate.
	p := newVertexForTest("meta/llama-3.3-70b-instruct-maas")
	c, err := p.TokenCounter(context.Background(), "meta/llama-3.3-70b-instruct-maas")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for MaaS Llama", c)
	}
}

func TestVertexProvider_TokenCounter_UnknownModelFallsBackToApproximate(t *testing.T) {
	p := newVertexForTest("custom-deployment-xyz")
	c, err := p.TokenCounter(context.Background(), "custom-deployment-xyz")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for unknown model", c)
	}
}

func TestVertexProvider_TokenCounter_EmptyModelFallsToProviderModel(t *testing.T) {
	p := newVertexForTest("gemini-2.5-flash")
	c, err := p.TokenCounter(context.Background(), "")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("empty model should fall through to provider model (gemini-2.5-flash → exact)")
	}
}

// --- buildCountTokensURL host selection ---------------------------

func TestBuildCountTokensURL_RegionalHost(t *testing.T) {
	got := buildCountTokensURL("proj", "us-central1", "gemini-2.5-flash")
	want := "https://us-central1-aiplatform.googleapis.com/v1/projects/proj/locations/us-central1/publishers/google/models/gemini-2.5-flash:countTokens"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestBuildCountTokensURL_GlobalHostHasNoLocationPrefix(t *testing.T) {
	// The "global" location uses the regionless host. A bug here
	// (e.g. always prefixing) would 404 on every global-region
	// Vertex deployment.
	got := buildCountTokensURL("proj", "global", "gemini-2.5-flash")
	if !strings.HasPrefix(got, "https://aiplatform.googleapis.com/") {
		t.Fatalf("global location should use unprefixed host; got %q", got)
	}
	if strings.Contains(got, "global-aiplatform") {
		t.Fatalf("global location should not have 'global-' prefix; got %q", got)
	}
}

// --- vertexGeminiTokenCounter.Count -------------------------------

// newCountTokensServer returns an httptest.Server that responds to
// /publishers/google/models/<model>:countTokens with the given
// totalTokens. recordedReq, when non-nil, is filled with the first
// parsed request body so tests can inspect what got sent.
func newCountTokensServer(t *testing.T, totalTokens int, recordedReq *geminiCountTokensRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Path, ":countTokens") {
			http.Error(w, "wrong path: "+r.URL.Path, http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if recordedReq != nil {
			_ = json.Unmarshal(body, recordedReq)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(geminiCountTokensResponse{TotalTokens: totalTokens})
	}))
}

// withCountTokensURL redirects buildCountTokensURL to point at the
// given server. Restored on test cleanup.
func withCountTokensURL(t *testing.T, server *httptest.Server) {
	t.Helper()
	orig := buildCountTokensURL
	buildCountTokensURL = func(projectID, location, model string) string {
		return server.URL + "/v1/projects/" + projectID + "/locations/" + location + "/publishers/google/models/" + model + ":countTokens"
	}
	t.Cleanup(func() { buildCountTokensURL = orig })
}

func TestVertexGeminiTokenCounter_Empty(t *testing.T) {
	srv := newCountTokensServer(t, 999, nil)
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	got, err := c.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count(\"\") errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0 (must short-circuit)", got)
	}
}

func TestVertexGeminiTokenCounter_Success(t *testing.T) {
	var seen geminiCountTokensRequest
	srv := newCountTokensServer(t, 42, &seen)
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")

	got, err := c.Count(context.Background(), "the rain in spain")
	if err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if got != 42 {
		t.Fatalf("Count = %d, want 42", got)
	}
	if len(seen.Contents) != 1 || seen.Contents[0].Role != "user" {
		t.Errorf("expected single user content, got %+v", seen.Contents)
	}
	if len(seen.Contents[0].Parts) != 1 || seen.Contents[0].Parts[0].Text != "the rain in spain" {
		t.Errorf("expected single text part with input, got %+v", seen.Contents[0].Parts)
	}
}

func TestVertexGeminiTokenCounter_Non200ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":429,"message":"quota exceeded"}}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 429")
	}
	if !strings.Contains(err.Error(), "429") && !strings.Contains(err.Error(), "status") {
		t.Errorf("error %q missing status detail", err.Error())
	}
}

func TestVertexGeminiTokenCounter_NonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("<html>upstream broken</html>"))
	}))
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected error on 500 with HTML body")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error %q missing 'status 500'", err.Error())
	}
}

func TestVertexGeminiTokenCounter_MalformedSuccessResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json-at-all"))
	}))
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected parse error on malformed JSON response")
	}
}

func TestVertexGeminiTokenCounter_HTTPTransportFailure(t *testing.T) {
	// Point at a closed server to force a transport-level error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	p.httpClient = &http.Client{Timeout: 1 * time.Second}
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected transport error against a closed server")
	}
}

func TestVertexGeminiTokenCounter_AuthFailure(t *testing.T) {
	srv := newCountTokensServer(t, 1, nil)
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	p.auth = &gcpAuth{tokenSource: &mockTokenSource{err: errors.New("ADC unavailable")}}
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("expected auth error when token source fails")
	}
	if !strings.Contains(err.Error(), "ADC unavailable") &&
		!strings.Contains(err.Error(), "access token") {
		t.Errorf("error %q missing auth detail", err.Error())
	}
}

func TestVertexGeminiTokenCounter_ContextCancelled(t *testing.T) {
	srv := newCountTokensServer(t, 1, nil)
	defer srv.Close()
	withCountTokensURL(t, srv)

	p := newVertexForTest("gemini-2.5-flash")
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Count(ctx, "blocked")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Count(cancelled ctx) returned err=%v, want context.Canceled", err)
	}
}

func TestVertexGeminiTokenCounter_BuildURLUsesProviderLocation(t *testing.T) {
	// Confirms the counter passes the provider's location through.
	// Without this, a bug that hardcoded us-central1 would silently
	// route every project to the same region (failing for
	// europe-/asia-/global-deployed customers).
	srv := newCountTokensServer(t, 5, nil)
	defer srv.Close()

	var seenURL string
	orig := buildCountTokensURL
	buildCountTokensURL = func(projectID, location, model string) string {
		seenURL = orig(projectID, location, model)
		// Redirect the actual call to the test server so we don't
		// hit the network; we only care that the URL produced by
		// the production builder carried the right location.
		return srv.URL + "/v1/projects/" + projectID + "/locations/" + location + "/publishers/google/models/" + model + ":countTokens"
	}
	t.Cleanup(func() { buildCountTokensURL = orig })

	p := newVertexForTest("gemini-2.5-flash")
	p.location = "europe-west4"
	c, _ := p.TokenCounter(context.Background(), "gemini-2.5-flash")
	if _, err := c.Count(context.Background(), "ping"); err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if !strings.Contains(seenURL, "europe-west4-aiplatform") {
		t.Fatalf("URL didn't carry the provider's location: %s", seenURL)
	}
}
