package azurefoundry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels calls the Azure Foundry deployments list behind
// {endpoint}/openai/v1/models using the api-key header. This returns
// the user's deployed OpenAI-shape models. Claude deployments on Azure
// Foundry are listed separately via the "models" endpoint on the
// control-plane, which is less accessible; for now we list the
// OpenAI-side catalog and rely on our shipped catalog for the Anthropic
// Claude deployments (which the user sees in the dropdown already).
func (p *AzureFoundryProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	url := p.endpoint + "/openai/v1/models"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry: list models: build request: %w", err)
	}
	req.Header.Set("api-key", p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azure-foundry: list models: request failed: %w", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("azure-foundry: list models: read: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("azure-foundry: list models: status %d: %s", resp.StatusCode, string(body))
	}

	var decoded struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("azure-foundry: list models: parse: %w", err)
	}

	out := make([]gollm.RemoteModel, 0, len(decoded.Data))
	for _, m := range decoded.Data {
		out = append(out, gollm.RemoteModel{ID: m.ID, DisplayName: m.ID})
	}
	return out, nil
}
