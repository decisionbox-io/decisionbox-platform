package vertexai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestIsClaude(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-20250514", true},
		{"claude-opus-4-20250514", true},
		{"claude-haiku-4-5-20251001", true},
		{"gemini-2.5-pro", false},
		{"gpt-4o", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isClaude(tt.model); got != tt.want {
			t.Errorf("isClaude(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestIsGemini(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gemini-2.5-pro", true},
		{"gemini-2.5-flash", true},
		{"gemini-1.5-pro", true},
		{"claude-sonnet-4-20250514", false},
		{"gpt-4o", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isGemini(tt.model); got != tt.want {
			t.Errorf("isGemini(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestVertexAIProvider_ChatUnsupportedModel(t *testing.T) {
	p := &VertexAIProvider{
		projectID: "test-project",
		location:  "us-central1",
		model:     "llama-3",
	}

	_, err := p.Chat(nil, gollm.ChatRequest{
		Model: "llama-3",
	})

	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !contains(err.Error(), "unsupported model") {
		t.Errorf("error = %q, want 'unsupported model'", err.Error())
	}
}

func TestVertexAIProvider_ChatDefaultModel(t *testing.T) {
	p := &VertexAIProvider{
		projectID: "test-project",
		location:  "us-central1",
		model:     "claude-sonnet-4-20250514",
	}

	// Empty model in request should fall back to provider default
	// Will fail on auth (no credentials in test), but verifies routing
	_, err := p.Chat(nil, gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "test"}},
	})

	// Should fail on auth, not on model routing
	if err != nil && contains(err.Error(), "unsupported model") {
		t.Error("should route to claude, not fail on model check")
	}
}

func TestVertexAIProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("vertex-ai")
	if !ok {
		t.Fatal("vertex-ai not registered")
	}
	if meta.Name == "" {
		t.Error("missing provider name")
	}
	if meta.Description == "" || contains(meta.Description, "coming soon") {
		t.Error("description still says 'coming soon' — should be updated")
	}
	if len(meta.DefaultPricing) == 0 {
		t.Error("no default pricing")
	}
	if _, ok := meta.DefaultPricing["claude-sonnet-4"]; !ok {
		t.Error("missing claude-sonnet-4 pricing")
	}
	if _, ok := meta.DefaultPricing["gemini-2.5-pro"]; !ok {
		t.Error("missing gemini-2.5-pro pricing")
	}
}

func TestVertexAIProvider_Validate_UnsupportedModel(t *testing.T) {
	p := &VertexAIProvider{
		projectID: "test-project",
		location:  "us-east5",
		model:     "llama-3",
	}
	err := p.Validate(nil)
	if err == nil {
		t.Error("Validate should fail with unsupported model")
	}
}

func TestVertexAIProvider_ConfigFields(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("vertex-ai")
	if !ok {
		t.Fatal("vertex-ai not registered")
	}

	fieldKeys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		fieldKeys[f.Key] = true
	}

	if !fieldKeys["project_id"] {
		t.Error("missing project_id config field")
	}
	if !fieldKeys["location"] {
		t.Error("missing location config field")
	}
	if !fieldKeys["model"] {
		t.Error("missing model config field")
	}
	// Should NOT have api_key — uses GCP ADC
	if fieldKeys["api_key"] {
		t.Error("vertex-ai should not have api_key field — uses GCP ADC")
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
