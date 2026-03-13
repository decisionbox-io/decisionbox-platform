//go:build integration

package vertexai

import (
	"context"
	"os"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func vertexClaudeModel() string {
	if m := os.Getenv("INTEGRATION_TEST_VERTEX_CLAUDE_MODEL"); m != "" {
		return m
	}
	return "claude-haiku-4-5@20251001"
}

func vertexGeminiModel() string {
	if m := os.Getenv("INTEGRATION_TEST_VERTEX_GEMINI_MODEL"); m != "" {
		return m
	}
	return "gemini-2.5-flash"
}

func TestIntegration_ClaudeChat(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	model := vertexClaudeModel()
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   "us-east5",
		"model":      model,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	if resp.Usage.InputTokens == 0 {
		t.Error("should report input tokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("should report output tokens")
	}
	t.Logf("Claude on Vertex: %q (model=%s, tokens: in=%d out=%d)",
		resp.Content, resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestIntegration_GeminiChat(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	model := vertexGeminiModel()
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   "us-central1",
		"model":      model,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		SystemPrompt: "You are a helpful assistant. Respond concisely.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2? Reply with just the number."}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	// Note: Gemini 2.5 thinking models may return empty content for very simple
	// prompts without system instructions. With system prompt, content is reliable.
	t.Logf("Gemini on Vertex: %q (model=%s, tokens: in=%d out=%d)",
		resp.Content, resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestIntegration_ClaudeSystemPrompt(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	model := vertexClaudeModel()
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   "us-east5",
		"model":      model,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	t.Logf("Claude system prompt: %q", resp.Content)
}

func TestIntegration_GeminiSystemPrompt(t *testing.T) {
	projectID := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if projectID == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}

	model := vertexGeminiModel()
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   "us-central1",
		"model":      model,
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	t.Logf("Gemini system prompt: %q", resp.Content)
}
