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
// context window / output cap from the proxy's /model/info map. When
// /model/info is blocked (common for non-admin virtual keys) or reports nothing
// for the model, it falls back to /v1/models — which is usually still
// accessible and carries the output cap — so a model whose output cap is below
// the 64K default is still detected and not over-requested. Returns (zero, nil)
// when neither source reports anything; only a total failure of both surfaces
// an error (which the caller treats as "unknown" and never fails the run on).
func (p *LiteLLMProvider) ResolveModelInfo(ctx context.Context, model string) (gollm.ModelCapabilities, error) {
	if model == "" {
		return gollm.ModelCapabilities{}, nil
	}
	byName, infoErr := p.fetchModelInfo(ctx)
	var caps gollm.ModelCapabilities
	if infoErr == nil {
		caps = byName[model]
	}
	// Both known → done. Otherwise consult /v1/models to fill whatever
	// /model/info omitted (notably the output cap, which /v1/models carries and
	// which is accessible to non-admin keys). This covers both a fully-blocked
	// /model/info and a partial one that reports only the input window.
	if caps.MaxInputTokens > 0 && caps.MaxOutputTokens > 0 {
		return caps, nil
	}
	basic, _, v1Err := p.fetchV1Models(ctx)
	if v1Err != nil {
		if caps.MaxInputTokens > 0 || caps.MaxOutputTokens > 0 {
			return caps, nil // return what /model/info did give
		}
		if infoErr != nil {
			return gollm.ModelCapabilities{}, infoErr // both endpoints failed
		}
		return gollm.ModelCapabilities{}, nil
	}
	b := basic[model]
	if caps.MaxInputTokens == 0 {
		caps.MaxInputTokens = b.MaxInputTokens
	}
	if caps.MaxOutputTokens == 0 {
		caps.MaxOutputTokens = b.MaxOutputTokens
	}
	return caps, nil
}
