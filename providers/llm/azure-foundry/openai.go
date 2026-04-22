package azurefoundry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

// openaiChat sends a request to an OpenAI-compatible model on Azure AI
// Foundry. Uses the OpenAI Chat Completions API at
// {endpoint}/openai/v1/chat/completions via the shared openaicompat helper.
//
// Reference:
//
//	https://learn.microsoft.com/en-us/azure/foundry/foundry-models/concepts/endpoints
func (p *AzureFoundryProvider) openaiChat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	body := openaicompat.BuildRequestBody(req.Model, req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to marshal request: %w", err)
	}

	endpoint := p.endpoint + "/openai/v1/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("api-key", p.apiKey)

	httpResp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: failed to read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		if apiErr := openaicompat.ExtractAPIError(respBody); apiErr != nil {
			return nil, fmt.Errorf("azure-foundry/openai: API error (%d): %s - %s", httpResp.StatusCode, apiErr.Type, apiErr.Message)
		}
		return nil, fmt.Errorf("azure-foundry/openai: API error (%d): %s", httpResp.StatusCode, string(respBody))
	}

	resp, err := openaicompat.ParseResponseBody(respBody)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry/openai: %w", err)
	}
	return resp, nil
}
