package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// anthropicCountTokensURL is the /v1/messages/count_tokens
// endpoint. It accepts the same {model, messages, system, tools}
// shape as /v1/messages and returns {input_tokens} without consuming
// generation quota — Anthropic explicitly designed it for prompt
// budgeting.
//
// Exposed as a package-level var (rather than const) so tests can
// redirect to an httptest server. Production code never reassigns it.
var anthropicCountTokensURL = "https://api.anthropic.com/v1/messages/count_tokens"

// TokenCounter implements gollm.TokenCounterProvider for Claude. The
// counter calls /v1/messages/count_tokens against Anthropic's server
// so callers get an exact tokenization for the given Claude model —
// no client-side BPE library can match Claude's tokenizer, so this
// network round-trip is the only accurate option.
//
// Errors here are non-fatal at the call site: the Ask handler wraps
// every Count call in a context with a deadline and falls back to
// gollm.ApproximateCounter on error.
func (p *ClaudeProvider) TokenCounter(_ context.Context, model string) (gollm.TokenCounter, error) {
	if model == "" {
		model = p.model
	}
	return &claudeTokenCounter{
		apiKey:     p.apiKey,
		model:      model,
		httpClient: p.httpClient,
	}, nil
}

// claudeTokenCounter is the per-model exact counter for Claude.
type claudeTokenCounter struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// Count posts a single-message body to /v1/messages/count_tokens and
// returns the upstream input_tokens. The request shape mirrors a
// real /messages call so any model-specific tokenization (system
// prompt overhead, tool-block metadata) is reflected in the count.
func (c *claudeTokenCounter) Count(ctx context.Context, text string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if text == "" {
		return 0, nil
	}

	body, err := json.Marshal(countTokensRequest{
		Model: c.model,
		Messages: []claudeMessage{
			{Role: "user", Content: text},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("claude count_tokens: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicCountTokensURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("claude count_tokens: new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("claude count_tokens: http: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return 0, fmt.Errorf("claude count_tokens: read body: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		var errResp claudeErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error.Message != "" {
			return 0, fmt.Errorf("claude count_tokens: %s — %s", errResp.Error.Type, errResp.Error.Message)
		}
		return 0, fmt.Errorf("claude count_tokens: status %d: %s",
			httpResp.StatusCode, gollm.SanitizeErrorBody(respBody, 300))
	}

	var parsed countTokensResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return 0, fmt.Errorf("claude count_tokens: parse: %w", err)
	}
	return parsed.InputTokens, nil
}

// countTokensRequest mirrors the count_tokens body shape. We keep it
// minimal (no tools, no system) because the handler counts pieces
// individually — the budget layer adds them with reserves on top.
type countTokensRequest struct {
	Model    string          `json:"model"`
	Messages []claudeMessage `json:"messages"`
}

// countTokensResponse is the count_tokens success payload.
type countTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}
