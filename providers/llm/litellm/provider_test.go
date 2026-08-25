package litellm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestProviderRegistered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta(providerName)
	if !ok {
		t.Fatal("litellm provider not registered")
	}
	if meta.Name != "LiteLLM" {
		t.Errorf("Name = %q, want LiteLLM", meta.Name)
	}
	if !meta.DispatchAnyModelID {
		t.Error("DispatchAnyModelID should be true (mirror Ollama)")
	}
	if !meta.PreferLiveModelID {
		t.Error("PreferLiveModelID should be true")
	}
	// base_url must be a declared, required config field.
	var hasBaseURL bool
	for _, f := range meta.ConfigFields {
		if f.Key == "base_url" {
			hasBaseURL = f.Required
		}
	}
	if !hasBaseURL {
		t.Error("base_url should be a required config field")
	}
	// The TLS fields must be surfaced.
	var hasCA, hasSkip bool
	for _, f := range meta.ConfigFields {
		switch f.Key {
		case gollm.TLSCACertKey:
			hasCA = true
		case gollm.TLSSkipVerifyKey:
			hasSkip = true
		}
	}
	if !hasCA || !hasSkip {
		t.Errorf("TLS config fields missing: ca=%v skip=%v", hasCA, hasSkip)
	}
	// An open proxy must be configurable: a credential-less "none" auth
	// method has to exist alongside the api_key one so the dashboard can
	// skip the key entirely.
	var hasAPIKey, hasNone bool
	for _, m := range meta.AuthMethods {
		switch m.ID {
		case "api_key":
			hasAPIKey = true
		case "none":
			hasNone = len(m.Fields) == 0
		}
	}
	if !hasAPIKey || !hasNone {
		t.Errorf("auth methods incomplete: api_key=%v none(no-fields)=%v", hasAPIKey, hasNone)
	}
}

func TestFactory_MissingBaseURL(t *testing.T) {
	_, err := gollm.NewProvider(providerName, gollm.ProviderConfig{"model": "gpt-4o"})
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("want base_url required error, got %v", err)
	}
}

func TestUnknownModelDefaults(t *testing.T) {
	// Unknown model → 128K input / 64K output (#338).
	if got := gollm.GetMaxInputTokens(providerName, "whatever-model"); got != 131072 {
		t.Errorf("GetMaxInputTokens = %d, want 131072", got)
	}
	if got := gollm.GetMaxOutputTokens(providerName, "whatever-model"); got != 65536 {
		t.Errorf("GetMaxOutputTokens = %d, want 65536", got)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://litellm.internal:4000":    "https://litellm.internal:4000/v1",
		"https://litellm.internal:4000/":   "https://litellm.internal:4000/v1",
		"https://litellm.internal:4000/v1": "https://litellm.internal:4000/v1",
		"https://host/v1/":                 "https://host/v1",
		"  https://host  ":                 "https://host/v1",
	}
	for in, want := range cases {
		if got := normalizeBaseURL(in); got != want {
			t.Errorf("normalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChat_SendsBearerAndModel(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	prov := NewLiteLLMProvider("virtual-key", "routed-model", srv.URL, srv.Client())
	resp, err := prov.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hi there" {
		t.Errorf("content = %q", resp.Content)
	}
	if gotAuth != "Bearer virtual-key" {
		t.Errorf("Authorization = %q, want Bearer virtual-key", gotAuth)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(gotBody), &sent)
	if sent["model"] != "routed-model" {
		t.Errorf("request model = %v, want routed-model", sent["model"])
	}
}

func TestChat_NoKeyOmitsAuthHeader(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer srv.Close()

	prov := NewLiteLLMProvider("", "m", srv.URL, srv.Client())
	if _, err := prov.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if hadAuth {
		t.Error("no key configured — Authorization header should be omitted")
	}
}

func TestChat_RequiresModel(t *testing.T) {
	prov := NewLiteLLMProvider("k", "", "https://x:4000", nil)
	_, err := prov.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "requires a model") {
		t.Fatalf("want model-required error, got %v", err)
	}
}

func TestChat_APIErrorSanitised(t *testing.T) {
	// A non-JSON error body (e.g. a gateway 502 page) that echoes the
	// caller's bearer token must be masked before landing in the error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream rejected Authorization: Bearer sk-secret123"))
	}))
	defer srv.Close()

	prov := NewLiteLLMProvider("sk-secret123", "m", srv.URL, srv.Client())
	_, err := prov.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an API error")
	}
	if strings.Contains(err.Error(), "sk-secret123") {
		t.Errorf("error leaked a secret-looking token: %v", err)
	}
}
