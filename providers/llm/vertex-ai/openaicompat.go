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

// chatOpenAICompat sends a request to a Vertex AI model that speaks the
// OpenAI /chat/completions wire. Two endpoint shapes are supported,
// selected by whether endpoint_id is configured:
//
//   - Shared Model Garden MaaS (endpoint_id blank): Llama MaaS, Qwen
//     MaaS, DeepSeek MaaS, Mistral MaaS, and Gemini's OpenAI-compatible
//     surface, all served under .../endpoints/openapi/chat/completions.
//     The model ID is namespaced with the publisher
//     (e.g. "meta/llama-3.3-70b-instruct-maas"); Vertex strips the slash
//     internally. We pass it through verbatim because every documented
//     example from Google uses this form.
//   - User-deployed endpoint (endpoint_id set): a model the operator
//     deployed themselves (self-fine-tuned, quantised, …), served under
//     .../endpoints/{endpoint_id}/chat/completions with no /openapi/
//     segment. The deployed model is named by its serving container, so
//     the request body's model is passed through verbatim with no
//     publisher prefix.
//
// Both shapes authenticate with the same GCP bearer token as Gemini.
func (p *VertexAIProvider) chatOpenAICompat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	body := openaicompat.BuildRequestBody(req.Model, req)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai/openai-compat: failed to marshal request: %w", err)
	}

	endpoint := p.openAICompatURL()

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

// openAICompatURL builds the chat-completions URL for the configured
// path. The region-scoped host mirrors the rest of the provider:
// regional except for the "global" location.
//
//   - endpoint_id set → .../endpoints/{endpoint_id}/chat/completions
//     (user-deployed model, no /openapi/ segment).
//   - endpoint_id blank → .../endpoints/openapi/chat/completions
//     (shared Model Garden MaaS).
func (p *VertexAIProvider) openAICompatURL() string {
	host := "aiplatform.googleapis.com"
	if p.location != "global" {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", p.location)
	}
	if p.endpointID != "" {
		return fmt.Sprintf(
			"https://%s/v1beta1/projects/%s/locations/%s/endpoints/%s/chat/completions",
			host, p.projectID, p.location, p.endpointID,
		)
	}
	return fmt.Sprintf(
		"https://%s/v1beta1/projects/%s/locations/%s/endpoints/openapi/chat/completions",
		host, p.projectID, p.location,
	)
}
