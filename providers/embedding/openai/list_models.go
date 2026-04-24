package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// ListModels hits OpenAI's GET /v1/models and filters to the rows that
// look like embedding models (by ID prefix). The endpoint is free and
// doesn't consume embedding quota — safe to call from the dashboard's
// "Load models" button.
//
// Why filter by prefix and not by a server-side capability flag? OpenAI
// doesn't expose a capability field on /v1/models — it's a flat list
// that returns chat, embedding, audio, and moderation models all
// together. The ID-prefix filter (`text-embedding-*`) matches every
// embedding model OpenAI ships today and any future one they'd name
// consistently. Anything else gets dropped.
func (p *provider) ListModels(ctx context.Context) ([]goembedding.RemoteModel, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", p.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("openai embedding: build list req: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai embedding: list models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai embedding: read list body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Try the structured error first so a 401/429 has a clear
		// message in the dashboard; fall through to the raw body
		// snippet for anything unstructured.
		var apiErr apiErrorResponse
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
			return nil, fmt.Errorf("openai embedding: list models: %s (%s)", apiErr.Error.Message, apiErr.Error.Type)
		}
		return nil, fmt.Errorf("openai embedding: list models: status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var listResp struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("openai embedding: parse list body: %w", err)
	}

	out := make([]goembedding.RemoteModel, 0, len(listResp.Data))
	for _, m := range listResp.Data {
		if !isEmbeddingModelID(m.ID) {
			continue
		}
		dims := modelDimensions[m.ID] // 0 when unknown; dashboard falls back to catalog
		out = append(out, goembedding.RemoteModel{
			ID:          m.ID,
			DisplayName: m.ID,
			Dimensions:  dims,
		})
	}
	return out, nil
}

// isEmbeddingModelID keeps the filter in one place so compliance with
// OpenAI's naming convention is explicit. Any future embedding model
// OpenAI ships under `text-embedding-*` is picked up automatically.
func isEmbeddingModelID(id string) bool {
	return strings.HasPrefix(id, "text-embedding-")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
