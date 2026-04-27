// Package llmprovider provides LLM provider implementations.
// The Claude provider registers itself via init() so services can
// select it with LLM_PROVIDER=claude and llm.NewProvider("claude", cfg).
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
)

func init() {
	gollm.RegisterWithMeta("claude", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
		maxRetries, _ := strconv.Atoi(cfg["max_retries"])
		if maxRetries == 0 {
			maxRetries = 3
		}
		timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
		if timeoutSec == 0 {
			timeoutSec = 60
		}
		delayMs, _ := strconv.Atoi(cfg["request_delay_ms"])

		return NewClaudeProvider(ClaudeConfig{
			APIKey:         cfg["api_key"],
			Model:          cfg["model"],
			MaxRetries:     maxRetries,
			Timeout:        time.Duration(timeoutSec) * time.Second,
			RequestDelayMs: delayMs,
		})
	}, gollm.ProviderMeta{
		Name:        "Claude (Anthropic)",
		Description: "Anthropic Claude API - direct access",
		ConfigFields: []gollm.ConfigField{
			{Key: "api_key", Label: "API Key", Required: true, Type: "string", Placeholder: "sk-ant-..."},
			{
				Key:         "model",
				Label:       "Model",
				Required:    true,
				Type:        "string",
				FreeText:    true,
				Default:     "claude-sonnet-4-6",
				Description: "Pick a catalogued Claude model or type any Anthropic model ID.",
			},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4-6": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4-5":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-1":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-sonnet-4":   {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4":     {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
		},
		MaxOutputTokens: map[string]int{
			"claude-opus-4-6":   128000,
			"claude-sonnet-4-6": 64000,
			"claude-sonnet-4-5": 64000,
			"claude-opus-4-5":   64000,
			"claude-opus-4-1":   32000,
			"claude-sonnet-4":   64000,
			"claude-opus-4":     32000,
			"claude-haiku-4-5":  64000,
			"_default":          16384,
		},
		// Claude supports tool_use natively. Enables function-calling on
		// /ask and any other tool-dependent flow.
		SupportsTools: true,
	})
}

// ClaudeConfig holds Claude-specific configuration.
type ClaudeConfig struct {
	APIKey         string
	Model          string
	MaxRetries     int
	Timeout        time.Duration
	RequestDelayMs int
}

// ClaudeProvider implements llm.Provider for Anthropic Claude.
type ClaudeProvider struct {
	apiKey     string
	model      string
	httpClient *http.Client
	maxRetries int
	delayMs    int
}

// NewClaudeProvider creates a Claude LLM provider.
func NewClaudeProvider(cfg ClaudeConfig) (*ClaudeProvider, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("claude: API key is required")
	}
	if cfg.Model == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}

	return &ClaudeProvider{
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		httpClient: &http.Client{Timeout: cfg.Timeout},
		maxRetries: cfg.MaxRetries,
		delayMs:    cfg.RequestDelayMs,
	}, nil
}

// Validate checks that the API key is valid by sending a minimal Chat request.
// Uses max_tokens=1 to minimize token consumption.
func (p *ClaudeProvider) Validate(ctx context.Context) error {
	_, err := p.sendRequest(ctx, &claudeRequest{
		Model:     p.model,
		MaxTokens: 1,
		Messages:  []claudeMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		return fmt.Errorf("claude: validation failed: %w", err)
	}
	return nil
}

// Chat sends a conversation to Claude and returns a response. When
// ChatRequest.Tools is non-empty the request includes Anthropic's
// tool_use blocks and ChatResponse.ToolCalls will be populated whenever
// the model decides to invoke one (StopReason == "tool_use").
func (p *ClaudeProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if p.delayMs > 0 {
		time.Sleep(time.Duration(p.delayMs) * time.Millisecond)
	}

	model := req.Model
	if model == "" {
		model = p.model
	}

	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	claudeMessages, err := convertMessagesForClaude(req.Messages)
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}

	apiReq := claudeRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  claudeMessages,
		System:    req.SystemPrompt,
	}
	if len(req.Tools) > 0 {
		apiReq.Tools = convertToolsForClaude(req.Tools)
	}
	if tc := convertToolChoiceForClaude(req.ToolChoice); tc != nil {
		apiReq.ToolChoice = tc
	}

	var lastErr error
	for attempt := 1; attempt <= p.maxRetries; attempt++ {
		resp, err := p.sendRequest(ctx, &apiReq)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if attempt < p.maxRetries {
			backoff := time.Duration(attempt*attempt) * time.Second
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			time.Sleep(backoff)
		}
	}

	return nil, fmt.Errorf("claude: failed after %d attempts: %w", p.maxRetries, lastErr)
}

func (p *ClaudeProvider) sendRequest(ctx context.Context, req *claudeRequest) (*gollm.ChatResponse, error) {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("claude: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("claude: failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("claude: HTTP request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("claude: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		var errResp claudeErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return nil, fmt.Errorf("claude: API error: %s - %s", errResp.Error.Type, errResp.Error.Message)
		}
		return nil, fmt.Errorf("claude: API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	var apiResp claudeResponse
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("claude: failed to parse response: %w", err)
	}

	var content string
	var toolCalls []gollm.ToolCall
	for _, c := range apiResp.Content {
		switch c.Type {
		case "text":
			content += c.Text
		case "tool_use":
			// Anthropic returns Input as an inline JSON object; unmarshal
			// into a map so the caller can inspect arguments by key.
			var input map[string]interface{}
			if len(c.Input) > 0 {
				_ = json.Unmarshal(c.Input, &input)
			}
			toolCalls = append(toolCalls, gollm.ToolCall{
				ID:    c.ID,
				Name:  c.Name,
				Input: input,
			})
		}
	}

	return &gollm.ChatResponse{
		Content:    content,
		Model:      apiResp.Model,
		StopReason: apiResp.StopReason,
		Usage: gollm.Usage{
			InputTokens:  apiResp.Usage.InputTokens,
			OutputTokens: apiResp.Usage.OutputTokens,
		},
		ToolCalls: toolCalls,
	}, nil
}

// convertMessagesForClaude flattens the provider-neutral Message shape
// into Anthropic's content-block format. Plain text messages stay as a
// single string (Claude accepts content as a string OR as an array of
// blocks). User messages carrying ToolResults get expanded into an array
// of tool_result blocks.
func convertMessagesForClaude(msgs []gollm.Message) ([]claudeMessage, error) {
	out := make([]claudeMessage, 0, len(msgs))
	for _, m := range msgs {
		if len(m.ToolResults) > 0 {
			if m.Role != "user" {
				return nil, fmt.Errorf("tool_results may only accompany role=user, got %q", m.Role)
			}
			blocks := make([]claudeContentBlock, 0, len(m.ToolResults)+1)
			for _, r := range m.ToolResults {
				block := claudeContentBlock{
					Type:      "tool_result",
					ToolUseID: r.CallID,
					Content:   r.Content,
				}
				if r.IsError {
					t := true
					block.IsError = &t
				}
				blocks = append(blocks, block)
			}
			if m.Content != "" {
				blocks = append(blocks, claudeContentBlock{Type: "text", Text: m.Content})
			}
			out = append(out, claudeMessage{Role: m.Role, Content: blocks})
			continue
		}

		// Assistant turn that issued tool_use blocks — replay them as
		// content blocks so Anthropic can correlate the next turn's
		// tool_result.tool_use_id.
		if len(m.ToolCalls) > 0 {
			if m.Role != "assistant" {
				return nil, fmt.Errorf("tool_calls may only accompany role=assistant, got %q", m.Role)
			}
			blocks := make([]claudeContentBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, claudeContentBlock{Type: "text", Text: m.Content})
			}
			for _, call := range m.ToolCalls {
				input := call.Input
				if input == nil {
					input = map[string]interface{}{}
				}
				blocks = append(blocks, claudeContentBlock{
					Type:       "tool_use",
					ID:         call.ID,
					Name:       call.Name,
					Input:      input,
				})
			}
			out = append(out, claudeMessage{Role: m.Role, Content: blocks})
			continue
		}

		// Fast path: plain text. Keeping it as a raw string preserves wire
		// compatibility with the old shape so no server-side regressions
		// from migrating to the richer union.
		out = append(out, claudeMessage{Role: m.Role, Content: m.Content})
	}
	return out, nil
}

func convertToolsForClaude(tools []gollm.ToolDefinition) []claudeTool {
	out := make([]claudeTool, len(tools))
	for i, t := range tools {
		out[i] = claudeTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return out
}

// convertToolChoiceForClaude maps the wire-neutral tool_choice string to
// Anthropic's structured form ({"type":"auto"|"any"|"none"|"tool","name":"X"}).
// Returns nil for "" or "auto" so the request omits the field, which is
// Anthropic's own default.
func convertToolChoiceForClaude(choice string) map[string]interface{} {
	switch choice {
	case "", "auto":
		return nil
	case "any", "required":
		return map[string]interface{}{"type": "any"}
	case "none":
		return map[string]interface{}{"type": "none"}
	default:
		return map[string]interface{}{"type": "tool", "name": choice}
	}
}

// claudeMessage's Content field is a json.Marshaler-friendly union:
// either a plain string or a []claudeContentBlock. We use interface{} at
// the struct field so encoding picks the correct shape at marshal time.
type claudeMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type claudeContentBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// tool_use (assistant-side echo for multi-turn correlation)
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   *bool  `json:"is_error,omitempty"`
}

type claudeTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type claudeRequest struct {
	Model      string                 `json:"model"`
	MaxTokens  int                    `json:"max_tokens"`
	Messages   []claudeMessage        `json:"messages"`
	System     string                 `json:"system,omitempty"`
	Tools      []claudeTool           `json:"tools,omitempty"`
	ToolChoice map[string]interface{} `json:"tool_choice,omitempty"`
}

type claudeResponseContent struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type claudeResponse struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model"`
	Content    []claudeResponseContent `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type claudeErrorResponse struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
