package ai

import (
	"context"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func TestNewClient(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Model = "test-model"

	provider := testutil.NewMockLLMProvider()

	client, err := New(cfg, provider)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if client == nil {
		t.Fatal("client should not be nil")
	}
}

func TestChat(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Model = "test-model"

	provider := testutil.NewMockLLMProvider()
	client, _ := New(cfg, provider)

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
	cfg := &config.Config{}
	cfg.LLM.Model = "test-model"

	provider := testutil.NewMockLLMProvider()
	provider.Error = context.DeadlineExceeded

	client, _ := New(cfg, provider)

	_, err := client.Chat(context.Background(), "test", "", 1000)
	if err == nil {
		t.Error("should return error when provider fails")
	}
}

func TestSetTestMode(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLM.Model = "test-model"

	provider := testutil.NewMockLLMProvider()
	client, _ := New(cfg, provider)

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
	cfg := &config.Config{}
	cfg.LLM.Model = "test-model"

	provider := testutil.NewMockLLMProvider()
	client, _ := New(cfg, provider)

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
