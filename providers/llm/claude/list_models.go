package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// anthropicModelsURL is the list endpoint; the /v1/messages URL has
// /messages at the end and the base is https://api.anthropic.com/v1.
const anthropicModelsURL = "https://api.anthropic.com/v1/models"

// ListModels returns every model the configured API key can see, via
// Anthropic's GET /v1/models. Pagination is handled by following the
// last_id cursor until has_more=false.
func (p *ClaudeProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	out := make([]gollm.RemoteModel, 0, 32)
	cursor := ""

	for i := 0; i < 10; i++ { // hard cap in case the upstream misbehaves
		url := anthropicModelsURL + "?limit=100"
		if cursor != "" {
			url += "&after_id=" + cursor
		}

		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("claude: list models: build request: %w", err)
		}
		req.Header.Set("x-api-key", p.apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("claude: list models: request failed: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("claude: list models: read: %w", readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("claude: list models: status %d: %s", resp.StatusCode, string(body))
		}

		var decoded struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
				Type        string `json:"type"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("claude: list models: parse: %w", err)
		}
		for _, m := range decoded.Data {
			name := m.DisplayName
			if name == "" {
				name = m.ID
			}
			out = append(out, gollm.RemoteModel{ID: m.ID, DisplayName: name})
		}
		if !decoded.HasMore || decoded.LastID == "" {
			break
		}
		cursor = decoded.LastID
	}
	return out, nil
}
