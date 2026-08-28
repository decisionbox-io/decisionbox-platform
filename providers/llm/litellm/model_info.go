package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// rootURL strips the trailing /v1 from the normalised base so proxy-root
// endpoints (/model/info) can be reached. baseURL always ends in /v1
// (normalizeBaseURL), so this yields the LiteLLM proxy root.
func rootURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

// modelInfoResponse mirrors the shape of LiteLLM's GET /model/info. Each row's
// model_info carries the per-model context window / output cap from LiteLLM's
// model-cost map. All three token fields are nullable — a custom model the
// proxy admin did not annotate reports null, which we treat as "unknown".
type modelInfoResponse struct {
	Data []struct {
		ModelName string `json:"model_name"`
		ModelInfo struct {
			MaxInputTokens  *int `json:"max_input_tokens"`
			MaxOutputTokens *int `json:"max_output_tokens"`
			MaxTokens       *int `json:"max_tokens"`
		} `json:"model_info"`
	} `json:"data"`
}

// fetchModelInfo calls GET /model/info and returns the per-model capabilities
// keyed by model name. Best-effort: a non-200 or parse failure returns an error
// the caller may ignore (falling through to catalog/default). Runs through the
// custom-TLS transport + Bearer auth like the other endpoints.
func (p *LiteLLMProvider) fetchModelInfo(ctx context.Context) (map[string]gollm.ModelCapabilities, error) {
	// /model/info sits at the proxy root, not under /v1.
	url := rootURL(p.baseURL) + "/model/info"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("litellm: model info: build request: %w", err)
	}
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm: model info: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("litellm: model info: read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm: model info: status %d: %s", resp.StatusCode, gollm.SanitizeErrorBody(body, 500))
	}

	var decoded modelInfoResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("litellm: model info: parse: %w", err)
	}

	out := make(map[string]gollm.ModelCapabilities, len(decoded.Data))
	for _, m := range decoded.Data {
		if m.ModelName == "" {
			continue
		}
		caps := gollm.ModelCapabilities{}
		if m.ModelInfo.MaxInputTokens != nil && *m.ModelInfo.MaxInputTokens > 0 {
			caps.MaxInputTokens = *m.ModelInfo.MaxInputTokens
		}
		// Prefer the explicit output cap; fall back to max_tokens (LiteLLM sets
		// max_tokens as the combined/output figure for some model families).
		switch {
		case m.ModelInfo.MaxOutputTokens != nil && *m.ModelInfo.MaxOutputTokens > 0:
			caps.MaxOutputTokens = *m.ModelInfo.MaxOutputTokens
		case m.ModelInfo.MaxTokens != nil && *m.ModelInfo.MaxTokens > 0:
			caps.MaxOutputTokens = *m.ModelInfo.MaxTokens
		}
		out[m.ModelName] = caps
	}
	return out, nil
}

// ResolveModelInfo implements gollm.ModelInfoResolver: it returns the model's
// context window / output cap from the proxy's /model/info map, or (zero, nil)
// when the proxy does not report them (unknown). A call failure returns an
// error the caller treats as "unknown" and never fails the run on.
func (p *LiteLLMProvider) ResolveModelInfo(ctx context.Context, model string) (gollm.ModelCapabilities, error) {
	if model == "" {
		return gollm.ModelCapabilities{}, nil
	}
	byName, err := p.fetchModelInfo(ctx)
	if err != nil {
		return gollm.ModelCapabilities{}, err
	}
	return byName[model], nil
}
