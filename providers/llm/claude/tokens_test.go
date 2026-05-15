package claude

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

// newCountingTokensServer returns an httptest.Server that returns
// `tokens` from /v1/messages/count_tokens. recordedReq, when
// non-nil, is populated with the first parsed request so tests can
// inspect what the counter sent.
func newCountingTokensServer(t *testing.T, tokens int, recordedReq *countTokensRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == "" {
			http.Error(w, `{"error":{"type":"authentication_error","message":"missing key"}}`, 401)
			return
		}
		if r.Header.Get("anthropic-version") == "" {
			http.Error(w, `{"error":{"type":"invalid_request_error","message":"missing version"}}`, 400)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if recordedReq != nil {
			_ = json.Unmarshal(body, recordedReq)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(countTokensResponse{InputTokens: tokens})
	}))
}

// withCountTokensURL temporarily redirects the package-level
// anthropicCountTokensURL to point at the test server. The original
// value is restored on cleanup so tests stay isolated.
func withCountTokensURL(t *testing.T, url string) {
	t.Helper()
	orig := anthropicCountTokensURL
	anthropicCountTokensURL = url
	t.Cleanup(func() { anthropicCountTokensURL = orig })
}

func TestClaudeTokenCounter_Empty(t *testing.T) {
	srv := newCountingTokensServer(t, 999, nil)
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, err := p.TokenCounter(context.Background(), "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	got, err := c.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count(\"\") errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0 (must short-circuit)", got)
	}
}

func TestClaudeTokenCounter_Success(t *testing.T) {
	var seen countTokensRequest
	srv := newCountingTokensServer(t, 42, &seen)
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")

	got, err := c.Count(context.Background(), "the rain in spain")
	if err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if got != 42 {
		t.Fatalf("Count = %d, want 42", got)
	}
	if seen.Model != "claude-haiku-4-5" {
		t.Errorf("server saw model=%q, want %q", seen.Model, "claude-haiku-4-5")
	}
	if len(seen.Messages) != 1 || seen.Messages[0].Role != "user" {
		t.Errorf("expected single user message, got %+v", seen.Messages)
	}
}

func TestClaudeTokenCounter_ServerErrorReturnsTypedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("Count returned nil error on 429")
	}
	if !strings.Contains(err.Error(), "rate_limit_error") {
		t.Errorf("error %q missing upstream type", err.Error())
	}
}

func TestClaudeTokenCounter_MalformedSuccessResponse(t *testing.T) {
	// 200 OK with a body that isn't a valid countTokensResponse —
	// e.g. an upstream proxy that returns HTML on success. The
	// counter must return a parse error rather than silently
	// returning 0 (which would under-count and let the prompt
	// overshoot).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("Count returned nil error on malformed 200 body")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error %q missing 'parse' detail", err.Error())
	}
}

func TestClaudeTokenCounter_NonJSONErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("<html>upstream broken</html>"))
	}))
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("Count returned nil error on 500 with non-JSON body")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("error %q missing status detail", err.Error())
	}
}

func TestClaudeTokenCounter_HTTPFailure(t *testing.T) {
	// Point at a closed server to force a transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 1 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")
	_, err := c.Count(context.Background(), "anything")
	if err == nil {
		t.Fatal("Count returned nil error against a closed server")
	}
}

func TestClaudeTokenCounter_ContextCancelled(t *testing.T) {
	srv := newCountingTokensServer(t, 1, nil)
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Count(ctx, "blocked")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Count(cancelled ctx) returned err=%v, want context.Canceled", err)
	}
}

func TestClaudeTokenCounter_RegistersAsExact(t *testing.T) {
	srv := newCountingTokensServer(t, 1, nil)
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-haiku-4-5", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "claude-haiku-4-5")
	if !gollm.IsExact(c) {
		t.Fatal("Claude TokenCounter must register as exact (non-ApproximateCounter)")
	}
}

func TestClaudeTokenCounter_EmptyModelFallsToProvider(t *testing.T) {
	var seen countTokensRequest
	srv := newCountingTokensServer(t, 1, &seen)
	defer srv.Close()
	withCountTokensURL(t, srv.URL)

	p := &ClaudeProvider{apiKey: "k", model: "claude-sonnet-4-6", httpClient: &http.Client{Timeout: 5 * time.Second}}
	c, _ := p.TokenCounter(context.Background(), "")
	if _, err := c.Count(context.Background(), "ping"); err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if seen.Model != "claude-sonnet-4-6" {
		t.Fatalf("expected fallback to provider model claude-sonnet-4-6, server saw %q", seen.Model)
	}
}
