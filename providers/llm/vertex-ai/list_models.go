package vertexai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ListModels queries Vertex AI's publisher model endpoints using GCP ADC.
// Vertex has no single "list everything" call — each publisher
// (google, anthropic, meta, mistral-ai, qwen, deepseek-ai) exposes its
// own publishers/{pub}/models list under the aiplatform service.
// We query the well-known publishers in parallel and merge the results.
//
// The handler surfaces failures per-publisher but does not fail the
// whole request — if one publisher is unreachable or the caller lacks
// access, the others still come back. We attach the publisher prefix
// to each ID so the resulting string is exactly what dispatch expects
// (e.g. "meta/llama-3.3-70b-instruct-maas").
func (p *VertexAIProvider) ListModels(ctx context.Context) ([]gollm.RemoteModel, error) {
	token, err := p.auth.token(ctx)
	if err != nil {
		return nil, fmt.Errorf("vertex-ai: list models: auth: %w", err)
	}

	host := "aiplatform.googleapis.com"
	if p.location != "global" {
		host = fmt.Sprintf("%s-aiplatform.googleapis.com", p.location)
	}

	publishers := []struct {
		name   string
		idFmt  func(string) string // e.g. "google" publishes bare IDs; others use "{pub}/{id}"
	}{
		{name: "google", idFmt: func(id string) string { return id }},
		{name: "anthropic", idFmt: func(id string) string { return id }},
		{name: "meta", idFmt: func(id string) string { return "meta/" + id }},
		{name: "mistral-ai", idFmt: func(id string) string { return "mistral-ai/" + id }},
		{name: "qwen", idFmt: func(id string) string { return "qwen/" + id }},
		{name: "deepseek-ai", idFmt: func(id string) string { return "deepseek-ai/" + id }},
	}

	out := make([]gollm.RemoteModel, 0, 64)
	for _, pub := range publishers {
		models, err := p.listPublisherModels(ctx, host, pub.name, token)
		if err != nil {
			// non-fatal; continue to next publisher
			continue
		}
		for _, m := range models {
			id := pub.idFmt(m.ID)
			name := m.DisplayName
			if name == "" {
				name = id
			}
			out = append(out, gollm.RemoteModel{ID: id, DisplayName: name})
		}
	}
	return out, nil
}

type vertexPublisherModel struct {
	ID          string `json:"-"`
	Name        string `json:"name"` // e.g. "publishers/google/models/gemini-2.5-pro"
	DisplayName string `json:"versionId"`
}

func (p *VertexAIProvider) listPublisherModels(ctx context.Context, host, publisher, token string) ([]vertexPublisherModel, error) {
	url := fmt.Sprintf("https://%s/v1/publishers/%s/models", host, publisher)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var decoded struct {
		PublisherModels []struct {
			Name        string `json:"name"`
			VersionID   string `json:"versionId"`
			DisplayName string `json:"displayName"`
		} `json:"publisherModels"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}

	out := make([]vertexPublisherModel, 0, len(decoded.PublisherModels))
	for _, m := range decoded.PublisherModels {
		// Extract the trailing model ID from "publishers/.../models/{id}".
		id := ""
		if idx := lastSegment(m.Name, "/models/"); idx != "" {
			id = idx
		}
		if id == "" {
			continue
		}
		displayName := m.DisplayName
		if displayName == "" {
			displayName = id
		}
		out = append(out, vertexPublisherModel{ID: id, Name: m.Name, DisplayName: displayName})
	}
	return out, nil
}

// lastSegment returns the substring after `sep` in `s`, or empty if sep
// does not appear. Used to extract the model ID from the Vertex resource
// name "publishers/google/models/gemini-2.5-pro".
func lastSegment(s, sep string) string {
	i := len(s) - 1
	for ; i >= len(sep)-1; i-- {
		if s[i-len(sep)+1:i+1] == sep {
			return s[i+1:]
		}
	}
	return ""
}
