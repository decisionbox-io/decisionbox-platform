package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

// chatOpenAICompat sends a request to a Model-Garden / Model-as-a-Service
// model on Vertex AI that speaks the OpenAI /chat/completions wire
// (Llama MaaS, Qwen MaaS, DeepSeek MaaS, Mistral MaaS, and Gemini's
// OpenAI-compatible surface). Vertex exposes those via a dedicated
// endpoint under /v1beta1/.../endpoints/openapi/chat/completions,
// authenticated with the same GCP bearer token as Gemini.
//
// The model ID in the request body is namespaced with the publisher
// (e.g. "meta/llama-3.3-70b-instruct-maas"); Vertex strips the slash
// internally. We pass it through verbatim because every documented
// example from Google uses this form.
func (p *VertexAIProvider) chatOpenAICompat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	body := openaicompat.BuildRequestBody(req.Model, req)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to marshal request: %w", err)
	}

	host := "aiplatform.googleapis.com"
	if p.location != "global" {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", p.location)
	}
	endpoint := fmt.Sprintf(
		"https://%s/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions",
		host, p.projectID, p.location,
	)

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to create request: %w", err)
	}

	token, err := p.auth.token(ctx)
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		if apiErr := openaicompat.ExtractAPIError(respBody); apiErr != nil {
			return nil, fmt.Errorf("vertex-ai/openai-compat: API error (%d): %s - %s", httpResp.StatusCode, apiErr.Type, apiErr.Message)
		}
		// Collapse whitespace so Vertex's multi-line HTML error pages stay
		// readable in one-line log output.
		snippet := strings.TrimSpace(string(respBody))
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}
		return nil, fmt.Errorf("vertex-ai/openai-compat: API error (%d): %s", httpResp.StatusCode, snippet)
	}

	resp, err := openaicompat.ParseResponseBody(respBody)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: %w", err)
	}
	return resp, nil
}
