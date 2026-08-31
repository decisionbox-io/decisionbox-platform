package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestOllamaProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("ollama not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" {
		t.Error("missing description")
	}

	if len(meta.Models) == 0 {
		t.Fatal("catalog is empty")
	}
	if meta.DefaultMaxOutputTokens != ollamaDefaultMaxOutputTokens {
		t.Errorf("DefaultMaxOutputTokens = %d, want %d", meta.DefaultMaxOutputTokens, ollamaDefaultMaxOutputTokens)
	}

	// Per-model caps for the biggest Qwen / Gemma / DeepSeek / Meta models.
	cases := []struct {
		model string
		want  int
	}{
		// Qwen 3.6 / 3.5 — hosted-Plus-tier 64K generation.
		{"qwen3.6", 65536},
		{"qwen3.6:latest", 65536},
		{"qwen3.6:35b-a3b", 65536},
		{"qwen3.5", 65536},
		{"qwen3.5:122b", 65536},

		// Qwen 3 — reasoning-capable; cap raised to fit thinking +
		// answer in a single budget.
		{"qwen3", ollamaDefaultMaxOutputTokens},
		{"qwen3:32b", ollamaDefaultMaxOutputTokens},
		{"qwen3:235b", ollamaDefaultMaxOutputTokens},

		// DeepSeek R1 reasoning — cap raised for the same reason.
		{"deepseek-r1", ollamaDefaultMaxOutputTokens},
		{"deepseek-r1:70b", ollamaDefaultMaxOutputTokens},
		{"deepseek-r1:671b", ollamaDefaultMaxOutputTokens},

		// Qwen 2.5 — non-reasoning, 16K.
		{"qwen2.5:72b", 16384},
		{"qwen2.5-coder:32b", 16384},

		// DeepSeek V3 — non-reasoning, 16K.
		{"deepseek-v3", 16384},

		// Gemma 3 — reasoning-capable; cap raised.
		{"gemma3:27b", ollamaDefaultMaxOutputTokens},

		// Llama 4 / Llama 3.x — 8K practical generation cap.
		{"llama4:maverick", 8192},
		{"llama3.3:70b", 8192},
		{"llama3.1:8b", 8192}, // documented in docs/guides/configuring-llm.md
		{"llama3.1:405b", 8192},
		{"llama3.2:3b", 8192},
		{"llama3:8b", 8192},

		// Gemma 2 — 8K context.
		{"gemma2:9b", 8192},
		{"gemma2:27b", 8192},

		// Fallback to the provider default for unrecognized model tags.
		{"some-unknown-model:42b", ollamaDefaultMaxOutputTokens},
		{"qwen2.5:0.5b", ollamaDefaultMaxOutputTokens}, // small Qwen not in the focused list — falls to default
	}
	for _, tc := range cases {
		if got := gollm.GetMaxOutputTokens("ollama", tc.model); got != tc.want {
			t.Errorf("GetMaxOutputTokens(ollama, %q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

func TestOllamaProvider_ConfigFields(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	if !keys["host"] {
		t.Error("missing host config field")
	}
	if !keys["model"] {
		t.Error("missing model config field")
	}
	// "Enable reasoning" is now a model-agnostic per-project setting (Settings →
	// Advanced), not a provider ConfigField, so it must NOT appear in any
	// provider's config_fields.
	if keys[gollm.ReasoningEnabledKey] {
		t.Errorf("%q must not be a provider config field (it is a per-project setting now)", gollm.ReasoningEnabledKey)
	}
	// Should NOT have api_key — local models
	if keys["api_key"] {
		t.Error("ollama should not have api_key field")
	}
}

// TestOllamaProvider_CatalogModels_Dispatchable confirms that the
// JSON shape exposed via /api/v1/providers/llm marks every Ollama
// catalog row as a real model — even though entries leave Wire blank
// (single-wire provider, no dispatch switch). The handler's
// `Dispatchable` derivation must not assume "no wire == not
// dispatchable".
func TestOllamaProvider_CatalogModels_HaveBlankWire(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")
	for _, m := range meta.CatalogModels() {
		// Ollama models intentionally leave Wire as "" (WireUnknown)
		// because Chat() does not dispatch on wire — the provider
		// has only one path through ollamaapi.
		if m.Wire != "" {
			t.Errorf("%s: Wire = %q, expected empty for Ollama", m.ID, m.Wire)
		}
	}
}

func TestOllamaProvider_ZeroPricing(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")
	// Ollama runs locally — every catalog entry must carry zero
	// pricing so the dashboard's cost estimate row stays at $0.
	for _, e := range meta.Models {
		if e.Pricing.InputPerMillion != 0 || e.Pricing.OutputPerMillion != 0 {
			t.Errorf("%s: pricing should be zero, got in=%f out=%f",
				e.ID, e.Pricing.InputPerMillion, e.Pricing.OutputPerMillion)
		}
	}
}

// TestOllamaProvider_FactoryEmptyModelReturnsListOnlyProvider verifies
// the factory accepts an empty model (list-only construction) so the
// dashboard's "Load models" flow can call ListModels() before the user
// has picked a model. Chat() / Validate() on a list-only provider must
// error clearly when called without a model — pinned in separate
// tests.
func TestOllamaProvider_FactoryEmptyModelReturnsListOnlyProvider(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host": "http://localhost:11434",
	})
	if err != nil {
		t.Fatalf("list-only construction must succeed without model, got %v", err)
	}
	if p == nil {
		t.Fatal("provider should not be nil in list-only mode")
	}
}

func TestOllamaProvider_ChatFailsWithoutModel(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{"host": "http://localhost:11434"})
	if err != nil {
		t.Fatalf("list-only factory: %v", err)
	}
	_, err = p.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("Chat must error in list-only mode")
	}
	if !strings.Contains(err.Error(), "list-only") && !strings.Contains(err.Error(), "model") {
		t.Errorf("error should mention missing model, got %v", err)
	}
}

func TestOllamaProvider_ValidateFailsWithoutModel(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{"host": "http://localhost:11434"})
	if err != nil {
		t.Fatalf("list-only factory: %v", err)
	}
	if err := p.Validate(context.Background()); err == nil {
		t.Fatal("Validate must error in list-only mode")
	}
}

func TestOllamaProvider_FactorySuccess(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host":  "http://localhost:11434",
		"model": "qwen2.5:0.5b",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}

func TestOllamaProvider_DefaultHost(t *testing.T) {
	p, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"model": "qwen2.5:0.5b",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Error("provider should not be nil")
	}
}

func TestOllamaProvider_Validate_ServerDown(t *testing.T) {
	p, err := NewOllamaProvider("http://localhost:1", "qwen2.5:0.5b", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(context.Background()); err == nil {
		t.Error("Validate should fail when server is unreachable")
	}
}

func TestNewOllamaProvider_InvalidURL(t *testing.T) {
	_, err := NewOllamaProvider("://invalid", "model", 0, 0)
	if err == nil {
		t.Error("should error on invalid URL")
	}
}

func TestOllamaProvider_SupportsStructuredOutput(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("ollama")
	if !meta.SupportsStructuredOutput {
		t.Error("ollama should advertise SupportsStructuredOutput (grammar-constrained decoding via `format`)")
	}
}

// TestOllamaProvider_Chat_SetsFormatFromResponseFormat drives a real
// (hermetic) Ollama /api/chat round-trip against an httptest server and
// asserts the request carries the ResponseFormat schema in the `format`
// field — that is how llama.cpp grammar-constrained decoding is engaged.
// The schema includes an open-ended (additionalProperties) object to
// prove dynamic-key maps survive onto the wire.
func TestOllamaProvider_Chat_SetsFormatFromResponseFormat(t *testing.T) {
	var capturedFormat json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Format json.RawMessage `json:"format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedFormat = body.Format
		w.Header().Set("Content-Type", "application/x-ndjson")
		// A single terminal NDJSON line is a complete non-streamed reply.
		_, _ = w.Write([]byte(`{"model":"qwen3","done":true,"message":{"role":"assistant","content":"{}"}}` + "\n"))
	}))
	defer srv.Close()

	p, err := NewOllamaProvider(srv.URL, "qwen3", 0, 0)
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"slug": map[string]interface{}{"type": "string"}},
		"categories": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": map[string]interface{}{"type": "string"},
		},
	}
	_, err = p.Chat(context.Background(), gollm.ChatRequest{
		Messages:       []gollm.Message{{Role: "user", Content: "go"}},
		ResponseFormat: &gollm.ResponseFormat{Name: "domain_pack", Schema: schema},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(capturedFormat) == 0 {
		t.Fatal("format field not sent to Ollama")
	}
	var gotSchema map[string]interface{}
	if err := json.Unmarshal(capturedFormat, &gotSchema); err != nil {
		t.Fatalf("format is not a JSON schema object: %v", err)
	}
	cats, ok := gotSchema["categories"].(map[string]interface{})
	if !ok || cats["additionalProperties"] == nil {
		t.Errorf("open-ended 'categories' object dropped from format: %v", gotSchema)
	}
}

// TestOllamaProvider_Chat_NoFormatWhenUnset locks the non-regression:
// without a ResponseFormat, no `format` field is sent.
func TestOllamaProvider_Chat_NoFormatWhenUnset(t *testing.T) {
	sawFormat := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, sawFormat = body["format"]
		_, _ = w.Write([]byte(`{"model":"qwen3","done":true,"message":{"role":"assistant","content":"hi"}}` + "\n"))
	}))
	defer srv.Close()

	p, _ := NewOllamaProvider(srv.URL, "qwen3", 0, 0)
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "go"}},
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if sawFormat {
		t.Error("format field must be omitted when no ResponseFormat is set")
	}
}
