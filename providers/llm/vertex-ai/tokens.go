package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TokenCounter implements gollm.TokenCounterProvider for Vertex AI.
// Routing:
//
//  1. Gemini (Google-native wire) → Vertex's `:countTokens` REST
//     endpoint on `publishers/google/models/<model>` — exact, one
//     extra RTT per call. The endpoint is read-only (does not
//     consume generation quota) and uses the same ADC bearer the
//     Chat path uses.
//  2. Claude on Vertex (Anthropic wire) → ApproximateCounter.
//     Vertex's Anthropic publisher does not expose a public
//     count_tokens REST API; the only way to get exact counts is
//     via Anthropic's direct `/messages/count_tokens` which lives
//     on a different surface and needs separate credentials.
//  3. OpenAI-compat MaaS models (Llama / Qwen / DeepSeek /
//     Mistral) → ApproximateCounter. The tokenizers are
//     SentencePiece-based, not BPE, so tiktoken would be
//     systematically wrong; the approximation + 15% margin
//     absorbs the drift.
//
// The wire is resolved via ResolveWire so the catalog + any
// per-project wire_override drives the routing.
func (p *VertexAIProvider) TokenCounter(_ context.Context, model string) (gollm.TokenCounter, error) {
	if model == "" {
		model = p.model
	}
	meta, ok := gollm.GetProviderMeta("vertex-ai")
	if !ok {
		return gollm.ApproximateCounter{}, nil
	}
	wire, err := meta.ResolveWire(model, p.wireOverride)
	if err != nil || wire != gollm.WireGoogleNative {
		// Either an unknown model (no actionable wire) or a wire
		// we don't have an exact counter for. Approximate is the
		// safe choice — the safety-margin layer compensates.
		return gollm.ApproximateCounter{}, nil
	}
	return &vertexGeminiTokenCounter{
		auth:       p.auth,
		httpClient: p.httpClient,
		projectID:  p.projectID,
		location:   p.location,
		model:      model,
	}, nil
}

// vertexGeminiTokenCounter is the per-model exact counter for
// Gemini on Vertex.
type vertexGeminiTokenCounter struct {
	auth       *gcpAuth
	httpClient *http.Client
	projectID  string
	location   string
	model      string
}

// Count posts a single-message body to the Gemini countTokens
// endpoint and returns `totalTokens`. The request shape mirrors a
// real generateContent call so per-message overhead the model
// counts is reflected in the result.
//
// On any error — network blip, 4xx, malformed body — returns the
// error so the caller can fall back to gollm.ApproximateCounter
// rather than failing the user's request.
func (c *vertexGeminiTokenCounter) Count(ctx context.Context, text string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if text == "" {
		return 0, nil
	}

	body, err := json.Marshal(geminiCountTokensRequest{
		Contents: []geminiContent{
			{
				Role:  "user",
				Parts: []geminiPart{{Text: text}},
			},
		},
	})
	if err != nil {
		return 0, fmt.Errorf("vertex-ai countTokens: marshal: %w", err)
	}

	endpoint := buildCountTokensURL(c.projectID, c.location, c.model)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("vertex-ai countTokens: new request: %w", err)
	}
	token, err := c.auth.token(ctx)
	if err != nil {
		return 0, fmt.Errorf("vertex-ai countTokens: auth: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("vertex-ai countTokens: http: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return 0, fmt.Errorf("vertex-ai countTokens: read body: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("vertex-ai countTokens: status %d: %s",
			httpResp.StatusCode, gollm.SanitizeErrorBody(respBody, 300))
	}

	var parsed geminiCountTokensResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return 0, fmt.Errorf("vertex-ai countTokens: parse: %w", err)
	}
	return parsed.TotalTokens, nil
}

// geminiCountTokensRequest is the body shape for the Gemini
// countTokens endpoint. Mirrors generateContent's `contents` field
// (reusing geminiContent + geminiPart from google_native.go); system
// instructions / tools could also be sent here but the Ask handler
// counts pieces individually so we keep it minimal.
type geminiCountTokensRequest struct {
	Contents []geminiContent `json:"contents"`
}

// buildCountTokensURL returns the canonical Vertex countTokens URL
// for the given (projectID, location, model). Exposed as a
// package-level variable so unit tests can redirect to an httptest
// server. Production code never reassigns it.
var buildCountTokensURL = func(projectID, location, model string) string {
	host := fmt.Sprintf("%s-aiplatform.googleapis.com", location)
	if location == "global" {
		host = "aiplatform.googleapis.com"
	}
	return fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/google/models/%s:countTokens",
		host, projectID, location, model,
	)
}

// geminiCountTokensResponse is the success payload. Vertex also
// returns `totalBillableCharacters`, which we ignore — the budget
// layer is tokens-based.
type geminiCountTokensResponse struct {
	TotalTokens int `json:"totalTokens"`
}
