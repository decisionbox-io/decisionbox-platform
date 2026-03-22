package ai

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func TestNewClient(t *testing.T) {

	provider := testutil.NewMockLLMProvider()

	client, err := New(provider, "test-model")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if client == nil {
		t.Fatal("client should not be nil")
	}
}

func TestChat(t *testing.T) {

	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	result, err := client.Chat(context.Background(), "test prompt", "system prompt", 1000)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	if result.Content == "" {
		t.Error("Content should not be empty")
	}
	if result.TokensIn == 0 {
		t.Error("TokensIn should be captured")
	}
	if result.TokensOut == 0 {
		t.Error("TokensOut should be captured")
	}
	// DurationMs may be 0 for very fast mock calls — just check it's non-negative
	if result.DurationMs < 0 {
		t.Error("DurationMs should be non-negative")
	}

	if len(provider.Calls) != 1 {
		t.Errorf("provider should be called once, got %d", len(provider.Calls))
	}
}

func TestChatError(t *testing.T) {

	provider := testutil.NewMockLLMProvider()
	provider.Error = context.DeadlineExceeded

	client, _ := New(provider, "test-model")

	_, err := client.Chat(context.Background(), "test", "", 1000)
	if err == nil {
		t.Error("should return error when provider fails")
	}
}

func TestSetTestMode(t *testing.T) {

	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	client.SetTestMode(true)
	if !client.testMode {
		t.Error("test mode should be enabled")
	}

	client.SetTestMode(false)
	if client.testMode {
		t.Error("test mode should be disabled")
	}
}

func TestSetDebugLogger(t *testing.T) {

	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	client.SetDebugLogger(nil)
	if client.debugLogger != nil {
		t.Error("debug logger should be nil")
	}
}

func TestChatResultFields(t *testing.T) {
	result := &ChatResult{
		Content:    "test response",
		TokensIn:   500,
		TokensOut:  200,
		DurationMs: 1500,
	}

	if result.Content != "test response" {
		t.Error("Content mismatch")
	}
	if result.TokensIn != 500 {
		t.Error("TokensIn mismatch")
	}
}

func TestClient_ExtractText(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	resp := &gollm.ChatResponse{
		Content: "This is the response text",
	}

	text := client.ExtractText(resp)
	if text != "This is the response text" {
		t.Errorf("ExtractText = %q, want 'This is the response text'", text)
	}
}

func TestClient_ExtractText_Empty(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	// Nil response
	text := client.ExtractText(nil)
	if text != "" {
		t.Errorf("ExtractText(nil) = %q, want empty", text)
	}

	// Empty content response
	resp := &gollm.ChatResponse{Content: ""}
	text = client.ExtractText(resp)
	if text != "" {
		t.Errorf("ExtractText(empty) = %q, want empty", text)
	}
}

func TestClient_ModelName(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "claude-sonnet-4-20250514")

	if client.ModelName() != "claude-sonnet-4-20250514" {
		t.Errorf("ModelName = %q, want claude-sonnet-4-20250514", client.ModelName())
	}
}

func TestClient_SetStepAndPhase(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	client.SetStep(5)
	if client.currentStep != 5 {
		t.Errorf("currentStep = %d, want 5", client.currentStep)
	}

	client.SetPhase("analysis")
	if client.currentPhase != "analysis" {
		t.Errorf("currentPhase = %q, want analysis", client.currentPhase)
	}
}

func TestChat_DefaultMaxTokens(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	// Call CreateMessage with maxTokens=0, should default to 4096
	_, err := client.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "test"}}, "", 0)
	if err != nil {
		t.Fatalf("CreateMessage error: %v", err)
	}

	if len(provider.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(provider.Calls))
	}
	if provider.Calls[0].Request.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want 4096 (default)", provider.Calls[0].Request.MaxTokens)
	}
}

func TestChat_CustomMaxTokens(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	_, err := client.CreateMessage(context.Background(), []gollm.Message{{Role: "user", Content: "test"}}, "system", 8000)
	if err != nil {
		t.Fatalf("CreateMessage error: %v", err)
	}

	if provider.Calls[0].Request.MaxTokens != 8000 {
		t.Errorf("MaxTokens = %d, want 8000", provider.Calls[0].Request.MaxTokens)
	}
	if provider.Calls[0].Request.SystemPrompt != "system" {
		t.Errorf("SystemPrompt = %q, want 'system'", provider.Calls[0].Request.SystemPrompt)
	}
}
