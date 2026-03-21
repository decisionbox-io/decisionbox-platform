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
			{Key: "model", Label: "Model", Required: true, Type: "string", Default: "claude-sonnet-4-20250514"},
		},
		DefaultPricing: map[string]gollm.TokenPricing{
			"claude-sonnet-4":   {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-sonnet-4-5": {InputPerMillion: 3.0, OutputPerMillion: 15.0},
			"claude-opus-4":     {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-opus-4-6":   {InputPerMillion: 15.0, OutputPerMillion: 75.0},
			"claude-haiku-4-5":  {InputPerMillion: 0.80, OutputPerMillion: 4.0},
		},
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
		cfg.Model = "claude-sonnet-4-20250514"
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

// Chat sends a conversation to Claude and returns a response.
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

	claudeMessages := make([]claudeMessage, len(req.Messages))
	for i, m := range req.Messages {
		claudeMessages[i] = claudeMessage{Role: m.Role, Content: m.Content}
	}

	apiReq := claudeRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  claudeMessages,
		System:    req.SystemPrompt,
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
	for _, c := range apiResp.Content {
		if c.Type == "text" {
			content += c.Text
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
	}, nil
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []claudeMessage `json:"messages"`
	System    string          `json:"system,omitempty"`
}

type claudeResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
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
