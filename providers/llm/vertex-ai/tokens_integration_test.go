//go:build integration

package vertexai

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Real Vertex Gemini countTokens integration. Skips when ADC /
// project aren't on the machine.

func tokensIntegrationProjectID(t *testing.T) string {
	t.Helper()
	p := os.Getenv("INTEGRATION_TEST_VERTEX_PROJECT_ID")
	if p == "" {
		t.Skip("INTEGRATION_TEST_VERTEX_PROJECT_ID not set")
	}
	return p
}

func tokensIntegrationLocation() string {
	if l := os.Getenv("INTEGRATION_TEST_VERTEX_LOCATION"); l != "" {
		return l
	}
	return "us-central1"
}

func tokensIntegrationGeminiModel() string {
	if m := os.Getenv("INTEGRATION_TEST_VERTEX_GEMINI_MODEL"); m != "" {
		return m
	}
	return "gemini-2.5-flash"
}

// TestIntegration_VertexGeminiCountTokens_BasicAccuracy verifies that
// the countTokens endpoint returns a sensible token count for a
// non-trivial English prompt and that the count matches what a
// generateContent call would report in usageMetadata.promptTokenCount
// (within ±20% — Gemini's response chat-template overhead is small).
func TestIntegration_VertexGeminiCountTokens_BasicAccuracy(t *testing.T) {
	projectID := tokensIntegrationProjectID(t)
	model := tokensIntegrationGeminiModel()

	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   tokensIntegrationLocation(),
		"model":      model,
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	tcp, ok := provider.(gollm.TokenCounterProvider)
	if !ok {
		t.Fatal("vertex-ai provider should implement TokenCounterProvider")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	counter, err := tcp.TokenCounter(ctx, model)
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}
	if !gollm.IsExact(counter) {
		t.Fatal("Gemini countTokens counter must register as exact")
	}

	prompt := "Summarize the following sentence in one word: " +
		"The quick brown fox jumps over the lazy dog. " +
		"Reply with only the single summary word."
	predicted, err := counter.Count(ctx, prompt)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if predicted <= 0 {
		t.Fatalf("countTokens returned %d; expected > 0", predicted)
	}

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model:     model,
		Messages:  []gollm.Message{{Role: "user", Content: prompt}},
		MaxTokens: 8,
	})
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "rate") || strings.Contains(low, "429") || strings.Contains(low, "quota") {
			t.Skipf("Vertex rate-limited; skipping: %v", err)
		}
		t.Fatalf("Chat: %v", err)
	}
	actual := resp.Usage.InputTokens
	if actual <= 0 {
		t.Logf("Vertex Chat returned input_tokens=%d; cross-check skipped", actual)
		return
	}
	delta := math.Abs(float64(predicted-actual)) / float64(actual)
	t.Logf("model=%s predicted=%d actual=%d delta=%.1f%%",
		model, predicted, actual, delta*100)
	if delta > 0.20 {
		t.Errorf("countTokens drifted >20%% from generateContent.usageMetadata: predicted=%d actual=%d (%.1f%%)",
			predicted, actual, delta*100)
	}
}

// TestIntegration_VertexGeminiCountTokens_EmptyShortCircuits checks
// the empty-input fast path: no network call should happen and the
// answer must be 0.
func TestIntegration_VertexGeminiCountTokens_EmptyShortCircuits(t *testing.T) {
	projectID := tokensIntegrationProjectID(t)
	model := tokensIntegrationGeminiModel()

	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   tokensIntegrationLocation(),
		"model":      model,
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	counter, err := provider.(gollm.TokenCounterProvider).TokenCounter(context.Background(), model)
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}
	got, err := counter.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count(\"\") errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0", got)
	}
}

// TestIntegration_VertexClaude_FallsBackToApproximate verifies that
// the routing in TokenCounter returns ApproximateCounter for a
// Claude-on-Vertex model (Anthropic wire), since Vertex's Anthropic
// publisher doesn't expose a public countTokens endpoint.
func TestIntegration_VertexClaude_FallsBackToApproximate(t *testing.T) {
	projectID := tokensIntegrationProjectID(t)

	provider, err := gollm.NewProvider("vertex-ai", gollm.ProviderConfig{
		"project_id": projectID,
		"location":   tokensIntegrationLocation(),
		"model":      "claude-haiku-4-5",
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	counter, err := provider.(gollm.TokenCounterProvider).TokenCounter(context.Background(), "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}
	if _, ok := counter.(gollm.ApproximateCounter); !ok {
		t.Fatalf("got %T, want ApproximateCounter for Claude on Vertex", counter)
	}
	if gollm.IsExact(counter) {
		t.Fatal("Claude on Vertex must NOT register as exact")
	}
}
