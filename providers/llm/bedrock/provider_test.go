package bedrock

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestIsAnthropic(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"anthropic.claude-sonnet-4-20250514-v1:0", true},
		{"us.anthropic.claude-sonnet-4-20250514-v1:0", true},
		{"anthropic.claude-3-haiku-20240307-v1:0", true},
		{"meta.llama3-70b-instruct-v1:0", false},
		{"mistral.mistral-large-2407-v1:0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAnthropic(tt.model); got != tt.want {
			t.Errorf("isAnthropic(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestBedrockProvider_UnsupportedModel(t *testing.T) {
	p := &BedrockProvider{model: "meta.llama3-70b-instruct-v1:0"}

	_, err := p.Chat(nil, gollm.ChatRequest{
		Model:    "meta.llama3-70b-instruct-v1:0",
		Messages: []gollm.Message{{Role: "user", Content: "test"}},
	})

	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !contains(err.Error(), "unsupported model") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestBedrockProvider_DefaultModel(t *testing.T) {
	p := &BedrockProvider{model: "anthropic.claude-sonnet-4-20250514-v1:0"}

	// Empty model should default to provider's model and route to anthropic
	req := gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "test"}},
	}
	if req.Model == "" {
		req.Model = p.model
	}
	if !isAnthropic(req.Model) {
		t.Error("default model should route to anthropic")
	}
}

func TestBedrockProvider_Registered(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("bedrock")
	if !ok {
		t.Fatal("bedrock not registered")
	}
	if contains(meta.Description, "coming soon") {
		t.Error("description still says coming soon")
	}
	if len(meta.DefaultPricing) == 0 {
		t.Error("no default pricing")
	}

	// MaxOutputTokens
	if meta.MaxOutputTokens == nil {
		t.Fatal("MaxOutputTokens should not be nil")
	}
	if len(meta.MaxOutputTokens) != 5 {
		t.Errorf("MaxOutputTokens has %d entries, want 5", len(meta.MaxOutputTokens))
	}
	if meta.MaxOutputTokens["claude-sonnet-4"] != 16384 {
		t.Errorf("MaxOutputTokens[claude-sonnet-4] = %d, want 16384", meta.MaxOutputTokens["claude-sonnet-4"])
	}
	if meta.MaxOutputTokens["claude-haiku-4-5"] != 8192 {
		t.Errorf("MaxOutputTokens[claude-haiku-4-5] = %d, want 8192", meta.MaxOutputTokens["claude-haiku-4-5"])
	}

	// Verify GetMaxOutputTokens helper
	if got := gollm.GetMaxOutputTokens("bedrock", "claude-opus-4-6"); got != 16384 {
		t.Errorf("GetMaxOutputTokens(bedrock, claude-opus-4-6) = %d, want 16384", got)
	}
}

func TestBedrockProvider_Validate_UnsupportedModel(t *testing.T) {
	p := &BedrockProvider{model: "meta.llama3-70b-instruct-v1:0"}
	err := p.Validate(nil)
	if err == nil {
		t.Error("Validate should fail for unsupported model")
	}
}

func TestBedrockProvider_ConfigFields(t *testing.T) {
	meta, _ := gollm.GetProviderMeta("bedrock")

	keys := make(map[string]bool)
	for _, f := range meta.ConfigFields {
		keys[f.Key] = true
	}
	if !keys["region"] {
		t.Error("missing region config field")
	}
	if !keys["model"] {
		t.Error("missing model config field")
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
