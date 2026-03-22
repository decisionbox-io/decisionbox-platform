package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// mockBedrockClient implements bedrockClient for unit testing.
type mockBedrockClient struct {
	invokeErr    error
	responseBody []byte
}

func (m *mockBedrockClient) InvokeModel(_ context.Context, params *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	if m.invokeErr != nil {
		return nil, m.invokeErr
	}
	return &bedrockruntime.InvokeModelOutput{
		Body: m.responseBody,
	}, nil
}

// newMockBedrockProvider creates a BedrockProvider with a mock client.
func newMockBedrockProvider(mock *mockBedrockClient) *BedrockProvider {
	return &BedrockProvider{
		client:     mock,
		region:     "us-east-1",
		model:      "anthropic.claude-sonnet-4-20250514-v1:0",
		httpClient: &http.Client{},
	}
}

// buildAnthropicResponse builds a JSON response body matching the Anthropic Messages API format.
func buildAnthropicResponse(content, model, stopReason string, inputTokens, outputTokens int) []byte {
	resp := map[string]interface{}{
		"content": []map[string]string{
			{"type": "text", "text": content},
		},
		"model":       model,
		"stop_reason": stopReason,
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	body, _ := json.Marshal(resp)
	return body
}

func TestBedrockProvider_Chat_Success(t *testing.T) {
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse(
			"Hello from Bedrock",
			"anthropic.claude-sonnet-4-20250514-v1:0",
			"end_turn",
			15, 8,
		),
	}
	p := newMockBedrockProvider(mock)

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello from Bedrock" {
		t.Errorf("content = %q, want %q", resp.Content, "Hello from Bedrock")
	}
	if resp.Model != "anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Errorf("model = %q", resp.Model)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.InputTokens != 15 {
		t.Errorf("input_tokens = %d, want 15", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 8 {
		t.Errorf("output_tokens = %d, want 8", resp.Usage.OutputTokens)
	}
}

func TestBedrockProvider_Chat_APIError(t *testing.T) {
	mock := &mockBedrockClient{
		invokeErr: fmt.Errorf("AccessDeniedException: You don't have access to the model"),
	}
	p := newMockBedrockProvider(mock)

	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "Hello"}},
	})
	if err == nil {
		t.Fatal("expected error for API failure")
	}
	if !contains(err.Error(), "InvokeModel failed") {
		t.Errorf("error = %q, should mention InvokeModel failed", err.Error())
	}
}

func TestBedrockProvider_Chat_SystemPrompt(t *testing.T) {
	var capturedBody []byte
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse("4", "anthropic.claude-sonnet-4-20250514-v1:0", "end_turn", 20, 3),
	}
	// Wrap to capture the request body
	wrappedMock := &capturingMockBedrockClient{
		delegate: mock,
	}
	p := &BedrockProvider{
		client:     wrappedMock,
		region:     "us-east-1",
		model:      "anthropic.claude-sonnet-4-20250514-v1:0",
		httpClient: &http.Client{},
	}

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		SystemPrompt: "You are a calculator. Only respond with numbers.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:    10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "4" {
		t.Errorf("content = %q, want %q", resp.Content, "4")
	}

	// Verify system prompt was included in the request body
	capturedBody = wrappedMock.lastBody
	var reqBody map[string]interface{}
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("failed to parse request body: %v", err)
	}
	system, ok := reqBody["system"]
	if !ok {
		t.Error("system prompt not included in request body")
	}
	if system != "You are a calculator. Only respond with numbers." {
		t.Errorf("system = %q", system)
	}
}

func TestBedrockProvider_Chat_TokenCounting(t *testing.T) {
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse(
			"Response text",
			"anthropic.claude-sonnet-4-20250514-v1:0",
			"max_tokens",
			100, 50,
		),
	}
	p := newMockBedrockProvider(mock)

	resp, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Tell me a long story"}},
		MaxTokens: 50,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 100 {
		t.Errorf("input_tokens = %d, want 100", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 50 {
		t.Errorf("output_tokens = %d, want 50", resp.Usage.OutputTokens)
	}
	if resp.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens", resp.StopReason)
	}
}

func TestBedrockProvider_Validate_Success(t *testing.T) {
	mock := &mockBedrockClient{
		responseBody: buildAnthropicResponse(
			"hi",
			"anthropic.claude-sonnet-4-20250514-v1:0",
			"end_turn",
			5, 1,
		),
	}
	p := newMockBedrockProvider(mock)

	err := p.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate should succeed with valid mock: %v", err)
	}
}

// capturingMockBedrockClient wraps a mockBedrockClient and captures the request body.
type capturingMockBedrockClient struct {
	delegate bedrockClient
	lastBody []byte
}

func (c *capturingMockBedrockClient) InvokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	c.lastBody = params.Body
	return c.delegate.InvokeModel(ctx, params, optFns...)
}
