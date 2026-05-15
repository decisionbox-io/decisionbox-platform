//go:build integration

package claude

import (
	"context"
	"os"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Real /v1/messages/count_tokens integration. Skips when the
// Anthropic key is absent so CI can run the rest of the suite
// without provisioning Anthropic credentials.

func TestIntegration_ClaudeTokenCounter_ReturnsNonZeroForRealPrompt(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_ANTHROPIC_API_KEY not set")
	}

	provider, err := gollm.NewProvider("claude", gollm.ProviderConfig{
		"api_key": apiKey,
		"model":   "claude-haiku-4-5",
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	tcp, ok := provider.(gollm.TokenCounterProvider)
	if !ok {
		t.Fatal("claude provider should implement TokenCounterProvider")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := tcp.TokenCounter(ctx, "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}
	if !gollm.IsExact(c) {
		t.Fatal("real Claude counter should register as exact")
	}

	tokens, err := c.Count(ctx, "The rain in Spain stays mainly in the plain.")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if tokens <= 0 {
		t.Fatalf("Count returned %d; expected > 0", tokens)
	}
	if tokens > 50 {
		// Sanity bound — that sentence should not tokenize past ~15
		// tokens. A wildly large value points at a parsing bug.
		t.Errorf("Count returned %d; expected ≤ 50 for a short English sentence", tokens)
	}
}

func TestIntegration_ClaudeTokenCounter_LongerInputsCountHigher(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_ANTHROPIC_API_KEY not set")
	}
	provider, err := gollm.NewProvider("claude", gollm.ProviderConfig{
		"api_key": apiKey,
		"model":   "claude-haiku-4-5",
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	c, err := provider.(gollm.TokenCounterProvider).TokenCounter(context.Background(), "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	short, err := c.Count(ctx, "hello")
	if err != nil {
		t.Fatalf("short: %v", err)
	}
	long, err := c.Count(ctx, "hello world this is a much longer sentence used for comparison")
	if err != nil {
		t.Fatalf("long: %v", err)
	}
	if long <= short {
		t.Fatalf("long=%d short=%d — long must be strictly greater", long, short)
	}
}
