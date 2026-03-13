//go:build integration

package claude

import (
	"context"
	"os"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestIntegration_BasicChat(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_ANTHROPIC_API_KEY not set")
	}

	provider, err := NewClaudeProvider(ClaudeConfig{
		APIKey:     apiKey,
		Model:      "claude-haiku-4-5-20251001",
		MaxRetries: 1,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Say hello in one word."}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response content should not be empty")
	}
	if resp.Model == "" {
		t.Error("response model should not be empty")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("should report input tokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("should report output tokens")
	}
	t.Logf("Response: %q (model=%s, tokens: in=%d out=%d)",
		resp.Content, resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestIntegration_SystemPrompt(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_ANTHROPIC_API_KEY not set")
	}

	provider, err := NewClaudeProvider(ClaudeConfig{
		APIKey:     apiKey,
		Model:      "claude-haiku-4-5-20251001",
		MaxRetries: 1,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		SystemPrompt: "You are a calculator. Only respond with numbers.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response should not be empty")
	}
	t.Logf("Response: %q", resp.Content)
}

func TestIntegration_ModelOverride(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_ANTHROPIC_API_KEY not set")
	}

	provider, err := NewClaudeProvider(ClaudeConfig{
		APIKey:     apiKey,
		Model:      "claude-haiku-4-5-20251001",
		MaxRetries: 1,
		Timeout:    30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Override model in request
	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model:     "claude-haiku-4-5-20251001",
		Messages:  []gollm.Message{{Role: "user", Content: "Say yes."}},
		MaxTokens: 5,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response should not be empty")
	}
	if resp.StopReason == "" {
		t.Error("stop_reason should not be empty")
	}
	t.Logf("Response: %q (stop=%s)", resp.Content, resp.StopReason)
}

func TestIntegration_ViaFactory(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_ANTHROPIC_API_KEY not set")
	}

	provider, err := gollm.NewProvider("claude", gollm.ProviderConfig{
		"api_key": apiKey,
		"model":   "claude-haiku-4-5-20251001",
	})
	if err != nil {
		t.Fatalf("Factory error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Say OK."}},
		MaxTokens: 5,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response should not be empty")
	}
	t.Logf("Factory response: %q", resp.Content)
}
