//go:build integration

package openai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestIntegration_BasicChat(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(apiKey, "gpt-4o-mini", "")

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
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(apiKey, "gpt-4o-mini", "")

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
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(apiKey, "gpt-4o", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model:     "gpt-4o-mini",
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
	t.Logf("Response: %q (model=%s, stop=%s)", resp.Content, resp.Model, resp.StopReason)
}

func TestIntegration_ViaFactory(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key": apiKey,
		"model":   "gpt-4o-mini",
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

// --- Error path tests ---

func TestIntegration_InvalidAPIKey(t *testing.T) {
	provider := NewOpenAIProvider("sk-invalid-key-12345", "gpt-4o-mini", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for invalid API key")
	}
	if !strings.Contains(err.Error(), "API error") {
		t.Errorf("error should mention API error, got: %v", err)
	}
	t.Logf("Invalid key error: %v", err)
}

func TestIntegration_InvalidModel(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(apiKey, "gpt-nonexistent-999", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for invalid model")
	}
	t.Logf("Invalid model error: %v", err)
}

func TestIntegration_Validate_Success(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(apiKey, "gpt-4o-mini", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := provider.Validate(ctx); err != nil {
		t.Fatalf("Validate should succeed with valid API key: %v", err)
	}
	t.Log("OpenAI Validate succeeded")
}

func TestIntegration_Validate_InvalidKey(t *testing.T) {
	provider := NewOpenAIProvider("sk-invalid-key-12345", "gpt-4o-mini", "")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := provider.Validate(ctx); err == nil {
		t.Fatal("Validate should fail with invalid API key")
	}
}

func TestIntegration_Validate_ViaFactory(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"api_key": apiKey,
		"model":   "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("Factory error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := provider.Validate(ctx); err != nil {
		t.Fatalf("Validate via factory should succeed: %v", err)
	}
	t.Log("OpenAI Validate via factory succeeded")
}

func TestIntegration_ContextCancellation(t *testing.T) {
	apiKey := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}

	provider := NewOpenAIProvider(apiKey, "gpt-4o-mini", "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for cancelled context")
	}
	t.Logf("Cancelled context error: %v", err)
}
