package ai

import (
	"context"
	"errors"
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

func TestSetReasoning(t *testing.T) {
	// Default (off) leaves ReasoningEffortDefault on the request — byte-identical
	// to today for every provider.
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")
	if _, err := client.Chat(context.Background(), "p", "", 100); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := provider.Calls[0].Request.ReasoningEffort; got != gollm.ReasoningEffortDefault {
		t.Errorf("default ReasoningEffort = %q, want %q", got, gollm.ReasoningEffortDefault)
	}

	// Enabled → every request carries ReasoningEffort=on (providers that don't
	// wire reasoning ignore it; Ollama capability-gates it).
	client.SetReasoning(true)
	if _, err := client.Chat(context.Background(), "p", "", 100); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := provider.Calls[1].Request.ReasoningEffort; got != gollm.ReasoningEffortOn {
		t.Errorf("enabled ReasoningEffort = %q, want %q", got, gollm.ReasoningEffortOn)
	}

	// Toggling back off returns to the default.
	client.SetReasoning(false)
	if _, err := client.Chat(context.Background(), "p", "", 100); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := provider.Calls[2].Request.ReasoningEffort; got != gollm.ReasoningEffortDefault {
		t.Errorf("re-disabled ReasoningEffort = %q, want %q", got, gollm.ReasoningEffortDefault)
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

// --- Structured output plumbing (issue #342 / #341 shared) ---

func TestSupportsStructuredOutput(t *testing.T) {
	const pn = "test-struct-cap"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
		gollm.ProviderMeta{ID: pn, Name: "struct cap", SupportsStructuredOutput: true,
			Models: []gollm.ModelEntry{{ID: "m", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 8000}}})

	c, _ := New(testutil.NewMockLLMProvider(), "m")
	if c.SupportsStructuredOutput() {
		t.Error("no provenance set → provider unknown → must be false")
	}
	c.SetProvenance("p", "r", pn)
	if !c.SupportsStructuredOutput() {
		t.Error("registered provider declares SupportsStructuredOutput=true")
	}
	c.SetProvenance("p", "r", "totally-unknown-provider")
	if c.SupportsStructuredOutput() {
		t.Error("unknown provider → false")
	}
}

func TestCreateMessageWithFormat_GatesOnSupport(t *testing.T) {
	const pn = "test-struct-attach"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
		gollm.ProviderMeta{ID: pn, Name: "struct attach", SupportsStructuredOutput: true,
			Models: []gollm.ModelEntry{{ID: "m", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 8000}}})

	format := &gollm.ResponseFormat{Name: "x", Schema: map[string]interface{}{"type": "object"}}
	msgs := []gollm.Message{{Role: "user", Content: "hi"}}

	// Supported → format threaded onto the request.
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{Content: "{}", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}}
	c, _ := New(provider, "m")
	c.SetProvenance("p", "r", pn)
	if _, err := c.CreateMessageWithFormat(context.Background(), msgs, "", 100, format); err != nil {
		t.Fatalf("CreateMessageWithFormat: %v", err)
	}
	if provider.Calls[0].Request.ResponseFormat == nil {
		t.Error("supported provider must carry ResponseFormat on the wire")
	}

	// Unsupported → format dropped (byte-identical to a plain call).
	provider2 := testutil.NewMockLLMProvider()
	provider2.DefaultResponse = &gollm.ChatResponse{Content: "{}", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}}
	c2, _ := New(provider2, "m")
	if _, err := c2.CreateMessageWithFormat(context.Background(), msgs, "", 100, format); err != nil {
		t.Fatalf("CreateMessageWithFormat: %v", err)
	}
	if provider2.Calls[0].Request.ResponseFormat != nil {
		t.Error("unsupported provider must not carry ResponseFormat")
	}
}

func TestChatPassesTheStopReasonThrough(t *testing.T) {
	// A caller that asks for a structured document decides what to do next
	// from this: a reply cut off at the output cap needs a smaller request,
	// not a re-ask. Dropping it here left every such caller guessing from the
	// text, which cannot be done.
	for _, reason := range []string{"end_turn", "max_tokens", "length"} {
		provider := testutil.NewMockLLMProvider()
		provider.DefaultResponse = &gollm.ChatResponse{
			Content:    "{}",
			StopReason: reason,
			Usage:      gollm.Usage{InputTokens: 7, OutputTokens: 11},
		}
		client, _ := New(provider, "test-model")

		res, err := client.Chat(context.Background(), "prompt", "", 100)
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if res.StopReason != reason {
			t.Errorf("Chat StopReason = %q, want %q", res.StopReason, reason)
		}

		res, err = client.ChatWithFormat(context.Background(), "prompt", "", 100, nil)
		if err != nil {
			t.Fatalf("ChatWithFormat: %v", err)
		}
		if res.StopReason != reason {
			t.Errorf("ChatWithFormat StopReason = %q, want %q", res.StopReason, reason)
		}
	}
}

func TestChatReportsTheBudgetTheRequestFinallyCarried(t *testing.T) {
	// The adaptive context-overflow retry lowers max_tokens and tries again,
	// so a caller that judges a reply against "the budget it was given" has to
	// be given the budget the request finally carried. Reporting the original
	// makes a reply that spent the reduced budget to the last token look like
	// one that had room to spare.
	const overflow = `openai: API error (400): invalid_request_error - This model's maximum ` +
		`context length is 8192 tokens. However, you requested 9000 tokens (5000 in the messages, ` +
		`4000 in the completion). Please reduce the length of the messages or completion.`

	provider := testutil.NewMockLLMProvider()
	provider.ErrorOnCall = map[int]error{0: errors.New(overflow)}
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    "{}",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 5000, OutputTokens: 100},
	}
	client, _ := New(provider, "test-model")

	res, err := client.Chat(context.Background(), "prompt", "", 4000)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(provider.Calls) != 2 {
		t.Fatalf("provider called %d times, want 2 (the overflow and the reduced retry)", len(provider.Calls))
	}

	retried := provider.Calls[1].Request.MaxTokens
	if retried >= 4000 {
		t.Fatalf("the retry carried max_tokens = %d, want it lowered below the 4000 asked for", retried)
	}
	if res.MaxTokens != retried {
		t.Errorf("ChatResult.MaxTokens = %d, want the %d the request finally carried", res.MaxTokens, retried)
	}
}

func TestChatReportsTheBudgetWhenNothingWasRetried(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	client, _ := New(provider, "test-model")

	res, err := client.ChatWithFormat(context.Background(), "prompt", "", 2048, nil)
	if err != nil {
		t.Fatalf("ChatWithFormat: %v", err)
	}
	if res.MaxTokens != 2048 {
		t.Errorf("ChatResult.MaxTokens = %d, want 2048", res.MaxTokens)
	}
}
