package azurefoundry

import (
	"context"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// newFoundryForTest builds a minimal AzureFoundryProvider for unit
// tests. The endpoint / key are placeholders — TokenCounter never
// makes a network call.
func newFoundryForTest(model string) *AzureFoundryProvider {
	return &AzureFoundryProvider{
		endpoint: "https://test.openai.azure.com",
		apiKey:   "test-key",
		model:    model,
	}
}

func TestFoundryProvider_TokenCounter_OpenAIWireUsesTiktoken(t *testing.T) {
	p := newFoundryForTest("gpt-4o")
	c, err := p.TokenCounter(context.Background(), "gpt-4o")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("gpt-4o on Foundry must register as exact (tiktoken)")
	}
}

func TestFoundryProvider_TokenCounter_ClaudeWireIsApproximate(t *testing.T) {
	// Anthropic-wire entries on Foundry have no Encoding declared
	// (Foundry fronts Claude through its own wire; no count_tokens
	// API is exposed). The counter must fall back to approximate.
	p := newFoundryForTest("claude-sonnet-4-6")
	c, err := p.TokenCounter(context.Background(), "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for Claude on Foundry", c)
	}
	if gollm.IsExact(c) {
		t.Fatal("Claude on Foundry must NOT register as exact")
	}
}

func TestFoundryProvider_TokenCounter_UnknownModelIsApproximate(t *testing.T) {
	// Custom deployment name with no catalog entry → no Encoding →
	// approximate. We can't assume the deployment's tokenizer.
	p := newFoundryForTest("my-custom-deployment-xyz")
	c, err := p.TokenCounter(context.Background(), "my-custom-deployment-xyz")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if _, ok := c.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for unknown deployment", c)
	}
}

func TestFoundryProvider_TokenCounter_EmptyModelUsesProviderModel(t *testing.T) {
	p := newFoundryForTest("gpt-4o")
	c, err := p.TokenCounter(context.Background(), "")
	if err != nil {
		t.Fatalf("TokenCounter errored: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("empty model should fall back to provider model (gpt-4o → exact)")
	}
}

func TestFoundryProvider_TokenCounter_CountReturnsPositive(t *testing.T) {
	p := newFoundryForTest("gpt-4o")
	c, _ := p.TokenCounter(context.Background(), "gpt-4o")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := c.Count(ctx, "hello world")
	if err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if got <= 0 {
		t.Fatalf("Count = %d, want > 0", got)
	}
}
