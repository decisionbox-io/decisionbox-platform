//go:build integration && ollama

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaprovider "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/testcontainers/testcontainers-go/modules/ollama"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/ollama"
)

const testOllamaModel = "qwen2.5:0.5b"

func setupOllama(t *testing.T) (gollm.Provider, func()) {
	t.Helper()
	ctx := context.Background()

	applog.Init("ollama-test", "warn")

	// Start Ollama container
	container, err := ollama.Run(ctx, "ollama/ollama:0.18.1")
	if err != nil {
		t.Fatalf("Failed to start Ollama: %v", err)
	}

	// Pull a tiny model
	t.Logf("Pulling model %s...", testOllamaModel)
	code, _, err := container.Exec(ctx, []string{"ollama", "pull", testOllamaModel})
	if err != nil || code != 0 {
		container.Terminate(ctx)
		t.Fatalf("Failed to pull model (code=%d): %v", code, err)
	}
	t.Logf("Model %s pulled successfully", testOllamaModel)

	// Get the endpoint
	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("Failed to get endpoint: %v", err)
	}

	// Create provider
	provider, err := ollamaprovider.NewOllamaProvider(endpoint, testOllamaModel)
	if err != nil {
		container.Terminate(ctx)
		t.Fatalf("Failed to create Ollama provider: %v", err)
	}

	cleanup := func() {
		container.Terminate(ctx)
	}

	return provider, cleanup
}

// =====================================================================
// Ollama Provider Tests
// =====================================================================

func TestOllama_ProviderRegistered(t *testing.T) {
	found := false
	for _, p := range gollm.RegisteredProviders() {
		if p == "ollama" {
			found = true
		}
	}
	if !found {
		t.Fatal("ollama not registered in LLM provider registry")
	}
}

func TestOllama_BasicChat(t *testing.T) {
	provider, cleanup := setupOllama(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model:    testOllamaModel,
		Messages: []gollm.Message{{Role: "user", Content: "Reply with exactly: HELLO"}},
		MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response content should not be empty")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("should report output tokens")
	}
	t.Logf("Response: %s (tokens: in=%d out=%d)", resp.Content, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestOllama_SystemPrompt(t *testing.T) {
	provider, cleanup := setupOllama(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model:        testOllamaModel,
		SystemPrompt: "You are a helpful assistant. Always respond in JSON format.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2? Respond with {\"answer\": N}"}},
		MaxTokens:    100,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response should not be empty")
	}
	t.Logf("Response: %s", resp.Content)
}

func TestOllama_JSONParsing(t *testing.T) {
	provider, cleanup := setupOllama(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model: testOllamaModel,
		SystemPrompt: "You must respond with valid JSON only. No other text.",
		Messages: []gollm.Message{{
			Role:    "user",
			Content: `Respond with this exact JSON: {"thinking": "test", "query": "SELECT 1"}`,
		}},
		MaxTokens: 200,
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	// Check if response contains JSON-like structure
	content := resp.Content
	hasJSON := strings.Contains(content, "{") && strings.Contains(content, "}")
	t.Logf("Response: %s (hasJSON=%v)", content, hasJSON)
}

// =====================================================================
// AI Client with Ollama
// =====================================================================

func TestOllama_AIClient_Chat(t *testing.T) {
	provider, cleanup := setupOllama(t)
	defer cleanup()


	client, err := ai.New(provider, testOllamaModel)
	if err != nil {
		t.Fatalf("AI client error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	result, err := client.Chat(ctx, "What is 1+1? Reply with just the number.", "", 50)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if result.Content == "" {
		t.Error("content should not be empty")
	}
	if result.TokensOut == 0 {
		t.Error("should report tokens")
	}
	t.Logf("AI Client response: %s (tokens: in=%d out=%d, duration=%dms)",
		result.Content, result.TokensIn, result.TokensOut, result.DurationMs)
}

func TestOllama_AIClient_AnalysisPrompt(t *testing.T) {
	provider, cleanup := setupOllama(t)
	defer cleanup()


	client, err := ai.New(provider, testOllamaModel)
	if err != nil {
		t.Fatalf("AI client error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	prompt := `Analyze this data and respond with JSON:

Query results:
- 500 users churned in last 30 days
- Average session duration dropped from 12min to 4min
- 68% quit rate on level 42

Respond with ONLY valid JSON:
{"insights": [{"name": "test", "description": "test", "severity": "high", "affected_count": 500, "risk_score": 0.68, "confidence": 0.8, "indicators": ["session drop"]}]}`

	result, err := client.Chat(ctx, prompt, "You are a data analyst. Respond only with valid JSON.", 500)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}

	t.Logf("Analysis response (%d tokens): %s", result.TokensOut, result.Content)

	// Verify it attempted to return JSON
	if !strings.Contains(result.Content, "{") {
		t.Error("response should contain JSON")
	}
}
