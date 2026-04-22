//go:build integration

package vertexai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// The user asks us not to integration-test Claude on Vertex, so we only
// exercise the Google-native (Gemini) path here. The Anthropic path is
// covered by the api.anthropic.com integration test and by the Bedrock
// Anthropic-wire integration test.

func vertexGeminiModel() string {
	if m := os.Getenv("INTEGRATION_TEST_VERTEX_GEMINI_MODEL"); m != "" {
		return m
	}
	return "gemini-2.5-flash"
}

func vertexProject(t *testing.T) string {
	t.Helper()
	p := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if p == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}
	return p
}

func vertexLocation() string {
	if l := os.Getenv("INTEGRATION_TEST_VERTEX_LOCATION"); l != "" {
		return l
	}
	return "us-central1"
}

// --- Google-native (Gemini) wire ---

func TestIntegration_Gemini_BasicChat(t *testing.T) {
	projectID := vertexProject(t)
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   vertexLocation(),
		"model":      vertexGeminiModel(),
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		SystemPrompt: "You are a helpful assistant. Respond concisely.",
		Messages:     []gollm.Message{{Role: "user", Content: "Reply with 'pong'."}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	// Gemini 2.5 thinking models may return empty content for very simple
	// prompts without system instructions; with the system prompt above
	// content is reliable.
	if resp.Usage.InputTokens == 0 {
		t.Error("should report input tokens")
	}
	t.Logf("Gemini on Vertex: %q (model=%s, tokens: in=%d out=%d)",
		resp.Content, resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestIntegration_Gemini_SystemPrompt(t *testing.T) {
	projectID := vertexProject(t)
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   vertexLocation(),
		"model":      vertexGeminiModel(),
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
	t.Logf("Gemini system prompt: %q", resp.Content)
}

// --- Error paths ---

func TestIntegration_UncataloguedModelActionable(t *testing.T) {
	// Does not require GCP auth — dispatch fails before any HTTP call.
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": "any",
		"location":   "us-central1",
		"model":      "vendor/future-unknown-model",
	})
	if err != nil {
		// Vertex factory probes ADC at construction. If ADC is missing,
		// skip — the dispatch-error test below depends on a valid factory.
		t.Skipf("factory error (no ADC?): %v", err)
	}

	_, err = provider.Chat(context.Background(), gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"wire_override", "vendor/future-unknown-model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestIntegration_InvalidProjectID(t *testing.T) {
	// Always runs — uses a bogus project ID against a catalogued model.
	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": "nonexistent-project-xyz-999",
		"location":   "us-central1",
		"model":      vertexGeminiModel(),
	})
	if err != nil {
		t.Skipf("factory error (no ADC?): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for invalid project ID")
	}
	t.Logf("Invalid project error: %v", err)
}

func TestIntegration_Gemini_ContextCancellation(t *testing.T) {
	projectID := vertexProject(t)

	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   vertexLocation(),
		"model":      vertexGeminiModel(),
	})
	if err != nil {
		t.Fatalf("Provider creation failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for cancelled context")
	}
	t.Logf("Cancelled context error: %v", err)
}
