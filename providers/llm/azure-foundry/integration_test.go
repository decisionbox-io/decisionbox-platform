//go:build integration

package azurefoundry

import (
	"context"
	"fmt"
	"os"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

var (
	testEndpoint   string
	testAPIKey     string
	testClaudeModel string
	testOpenAIModel string
)

func TestMain(m *testing.M) {
	testEndpoint = os.Getenv("INTEGRATION_TEST_AZURE_FOUNDRY_ENDPOINT")
	testAPIKey = os.Getenv("INTEGRATION_TEST_AZURE_FOUNDRY_API_KEY")
	if testEndpoint == "" || testAPIKey == "" {
		fmt.Println("INTEGRATION_TEST_AZURE_FOUNDRY_ENDPOINT and _API_KEY not set, skipping Azure AI Foundry integration tests")
		os.Exit(0)
	}

	testClaudeModel = os.Getenv("INTEGRATION_TEST_AZURE_FOUNDRY_CLAUDE_MODEL")
	if testClaudeModel == "" {
		testClaudeModel = "claude-haiku-4-5"
	}

	testOpenAIModel = os.Getenv("INTEGRATION_TEST_AZURE_FOUNDRY_OPENAI_MODEL")
	if testOpenAIModel == "" {
		testOpenAIModel = "gpt-4o-mini"
	}

	os.Exit(m.Run())
}

func TestIntegration_ClaudeChat(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testClaudeModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     testClaudeModel,
		Messages:  []gollm.Message{{Role: "user", Content: "What is 2+2? Reply with just the number."}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Content == "" {
		t.Error("empty response content")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("input_tokens should be > 0")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("output_tokens should be > 0")
	}
}

func TestIntegration_ClaudeSystemPrompt(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testClaudeModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        testClaudeModel,
		SystemPrompt: "You are a calculator. Only respond with numbers, nothing else.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("Chat with system prompt failed: %v", err)
	}
	if resp.Content == "" {
		t.Error("empty response content")
	}
}

func TestIntegration_OpenAIChat(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testOpenAIModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:     testOpenAIModel,
		Messages:  []gollm.Message{{Role: "user", Content: "What is 2+2? Reply with just the number."}},
		MaxTokens: 10,
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Content == "" {
		t.Error("empty response content")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("input_tokens should be > 0")
	}
}

func TestIntegration_OpenAISystemPrompt(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testOpenAIModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Model:        testOpenAIModel,
		SystemPrompt: "You are a calculator. Only respond with numbers.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("Chat with system prompt failed: %v", err)
	}
	if resp.Content == "" {
		t.Error("empty response content")
	}
}

func TestIntegration_ContextCancellation(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testClaudeModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = p.Chat(ctx, gollm.ChatRequest{
		Model:     testClaudeModel,
		Messages:  []gollm.Message{{Role: "user", Content: "Hello"}},
		MaxTokens: 10,
	})
	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestIntegration_Validate_Claude(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testClaudeModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	if err := p.Validate(context.Background()); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestIntegration_Validate_OpenAI(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  testAPIKey,
		"model":    testOpenAIModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}
	if err := p.Validate(context.Background()); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestIntegration_InvalidAPIKey(t *testing.T) {
	p, err := gollm.NewProvider("azure-foundry", gollm.ProviderConfig{
		"endpoint": testEndpoint,
		"api_key":  "invalid-key-xyz",
		"model":    testOpenAIModel,
	})
	if err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	_, err = p.Chat(context.Background(), gollm.ChatRequest{
		Model:     testOpenAIModel,
		Messages:  []gollm.Message{{Role: "user", Content: "Hello"}},
		MaxTokens: 10,
	})
	if err == nil {
		t.Error("expected error for invalid API key")
	}
}
