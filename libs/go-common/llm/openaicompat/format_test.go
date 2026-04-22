package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestBuildRequestBody_BasicMessages(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		Messages: []gollm.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
			{Role: "user", Content: "how are you"},
		},
	})

	if body.Model != "gpt-4o" {
		t.Errorf("model = %q", body.Model)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(body.Messages))
	}
	if body.Messages[0].Role != "user" || body.Messages[0].Content != "hello" {
		t.Errorf("first message = %+v", body.Messages[0])
	}
	if body.Messages[2].Content != "how are you" {
		t.Errorf("third message = %+v", body.Messages[2])
	}
	if body.MaxTokens != 0 {
		t.Errorf("max_tokens should be 0 when unset, got %d", body.MaxTokens)
	}
}

func TestBuildRequestBody_SystemPromptLeads(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		SystemPrompt: "You are helpful",
		Messages:     []gollm.Message{{Role: "user", Content: "hi"}},
	})

	if len(body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system + user)", len(body.Messages))
	}
	if body.Messages[0].Role != "system" {
		t.Errorf("first role = %q, want system", body.Messages[0].Role)
	}
	if body.Messages[0].Content != "You are helpful" {
		t.Errorf("system content = %q", body.Messages[0].Content)
	}
	if body.Messages[1].Role != "user" {
		t.Errorf("second role = %q, want user", body.Messages[1].Role)
	}
}

func TestBuildRequestBody_EmptySystemPromptOmitted(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		SystemPrompt: "",
		Messages:     []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if len(body.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(body.Messages))
	}
	if body.Messages[0].Role != "user" {
		t.Errorf("role = %q", body.Messages[0].Role)
	}
}

func TestBuildRequestBody_MaxTokensAndTemperature(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		Messages:    []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens:   2000,
		Temperature: 0.7,
	})
	if body.MaxTokens != 2000 {
		t.Errorf("max_tokens = %d", body.MaxTokens)
	}
	if body.Temperature != 0.7 {
		t.Errorf("temperature = %f", body.Temperature)
	}
}

func TestBuildRequestBody_ZeroTemperatureNotSent(t *testing.T) {
	body := BuildRequestBody("gpt-4o", gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Temperature should be omitted entirely when zero (omitempty).
	if strings.Contains(string(raw), "temperature") {
		t.Errorf("temperature should be omitted when zero; body = %s", raw)
	}
	if strings.Contains(string(raw), "max_tokens") {
		t.Errorf("max_tokens should be omitted when zero; body = %s", raw)
	}
}

func TestBuildRequestBody_RequestModelOverridesDefault(t *testing.T) {
	body := BuildRequestBody("gpt-4o-default", gollm.ChatRequest{
		Model:    "gpt-4o-mini",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if body.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", body.Model)
	}
}

func TestBuildRequestBody_EmptyRequestModelFallsBackToDefault(t *testing.T) {
	body := BuildRequestBody("gpt-4o-default", gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if body.Model != "gpt-4o-default" {
		t.Errorf("model = %q, want gpt-4o-default", body.Model)
	}
}

func TestParseResponseBody_Success(t *testing.T) {
	raw := []byte(`{
		"id":"chatcmpl-xyz",
		"model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
	}`)
	resp, err := ParseResponseBody(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "Hello" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.Model != "gpt-4o" {
		t.Errorf("model = %q", resp.Model)
	}
	if resp.StopReason != "stop" {
		t.Errorf("stop_reason = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 10 {
		t.Errorf("input_tokens = %d", resp.Usage.InputTokens)
	}
	if resp.Usage.OutputTokens != 5 {
		t.Errorf("output_tokens = %d", resp.Usage.OutputTokens)
	}
}

func TestParseResponseBody_NoChoices(t *testing.T) {
	raw := []byte(`{"id":"x","model":"m","choices":[]}`)
	_, err := ParseResponseBody(raw)
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error = %q, should mention 'no choices'", err.Error())
	}
}

func TestParseResponseBody_InvalidJSON(t *testing.T) {
	raw := []byte(`not-json`)
	_, err := ParseResponseBody(raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "parse response") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestParseResponseBody_MissingUsageToleratedAsZero(t *testing.T) {
	raw := []byte(`{
		"id":"x","model":"m",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]
	}`)
	resp, err := ParseResponseBody(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != 0 {
		t.Errorf("usage = %+v, want zeros", resp.Usage)
	}
}

func TestParseResponseBody_MissingModelToleratedAsEmpty(t *testing.T) {
	raw := []byte(`{
		"id":"x",
		"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}
	}`)
	resp, err := ParseResponseBody(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "" {
		t.Errorf("model = %q, want empty", resp.Model)
	}
}

func TestParseResponseBody_FirstChoiceWins(t *testing.T) {
	raw := []byte(`{
		"model":"m",
		"choices":[
			{"index":0,"message":{"role":"assistant","content":"first"},"finish_reason":"stop"},
			{"index":1,"message":{"role":"assistant","content":"second"},"finish_reason":"stop"}
		]
	}`)
	resp, err := ParseResponseBody(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "first" {
		t.Errorf("content = %q, want first", resp.Content)
	}
}

func TestExtractAPIError_Populated(t *testing.T) {
	raw := []byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error","code":"invalid_api_key"}}`)
	apiErr := ExtractAPIError(raw)
	if apiErr == nil {
		t.Fatal("expected APIError, got nil")
	}
	if apiErr.Message != "Invalid API key" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if apiErr.Type != "invalid_request_error" {
		t.Errorf("type = %q", apiErr.Type)
	}
	if apiErr.Code != "invalid_api_key" {
		t.Errorf("code = %q", apiErr.Code)
	}
}

func TestExtractAPIError_NotJSON(t *testing.T) {
	if got := ExtractAPIError([]byte("<html>502 Bad Gateway</html>")); got != nil {
		t.Errorf("expected nil for non-JSON, got %+v", got)
	}
}

func TestExtractAPIError_NoErrorField(t *testing.T) {
	raw := []byte(`{"id":"x","model":"m","choices":[]}`)
	if got := ExtractAPIError(raw); got != nil {
		t.Errorf("expected nil when no error field, got %+v", got)
	}
}

func TestExtractAPIError_EmptyErrorIgnored(t *testing.T) {
	raw := []byte(`{"error":{}}`)
	if got := ExtractAPIError(raw); got != nil {
		t.Errorf("expected nil for empty error, got %+v", got)
	}
}

func TestAPIError_ErrorString(t *testing.T) {
	tests := []struct {
		err  *APIError
		want string
	}{
		{&APIError{Message: "bad", Type: "server_error"}, "server_error: bad"},
		{&APIError{Message: "bad"}, "bad"},
		{nil, ""},
	}
	for _, tt := range tests {
		got := tt.err.Error()
		if got != tt.want {
			t.Errorf("Error() = %q, want %q", got, tt.want)
		}
	}
}
