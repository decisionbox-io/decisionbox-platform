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

	// Vertex's publisher-list endpoint is region-scoped and each region
	// advertises only a subset of each publisher's catalog — for example
	// us-east5 only surfaces claude-3-opus and claude-sonnet-4-5 even
	// though claude-opus-4-6 is available there via rawPredict. The
	// `global` host returns the complete publisher catalog, so we
	// always query it. If the user's configured location is something
	// other than global we additionally query that regional host and
	// merge, so region-only model IDs (rare) still appear.
	hosts := []string{"aiplatform.googleapis.com"}
	if p.location != "" && p.location != "global" {
		hosts = append(hosts, fmt.Sprintf("%s-aiplatform.googleapis.com", p.location))
	}

	publishers := []struct {
		name  string
		idFmt func(string) string // e.g. "google" publishes bare IDs; others use "{pub}/{id}"
	}{
		{name: "google", idFmt: func(id string) string { return id }},
		{name: "anthropic", idFmt: func(id string) string { return id }},
		{name: "meta", idFmt: func(id string) string { return "meta/" + id }},
		{name: "mistral-ai", idFmt: func(id string) string { return "mistral-ai/" + id }},
		{name: "qwen", idFmt: func(id string) string { return "qwen/" + id }},
		{name: "deepseek-ai", idFmt: func(id string) string { return "deepseek-ai/" + id }},
	}

	// Dedup by model ID across (host × publisher) — the same model can
	// appear in both global and regional lists.
	seen := make(map[string]struct{}, 256)
	out := make([]gollm.RemoteModel, 0, 64)
	var firstErr error
	for _, host := range hosts {
		for _, pub := range publishers {
			models, err := p.listPublisherModels(ctx, host, pub.name, token)
			if err != nil {
				// Non-fatal individually, but track the first failure
				// so we can surface it when every call fails.
				if firstErr == nil {
					firstErr = fmt.Errorf("publisher %s@%s: %w", pub.name, host, err)
				}
				continue
			}
			for _, m := range models {
				id := pub.idFmt(m.ID)
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				name := m.DisplayName
				if name == "" {
					name = id
				}
				out = append(out, gollm.RemoteModel{ID: id, DisplayName: name})
			}
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, fmt.Errorf("vertex-ai: list models: all publishers failed; first error: %w", firstErr)
	}
	return out, nil
}

type vertexPublisherModel struct {
	ID          string `json:"-"`
	Name        string `json:"name"` // e.g. "publishers/google/models/gemini-2.5-pro"
	DisplayName string `json:"versionId"`
}

func (p *VertexAIProvider) listPublisherModels(ctx context.Context, host, publisher, token string) ([]vertexPublisherModel, error) {
	// The publisher-models list endpoint is only available under
	// v1beta1 on Vertex AI. The v1 surface returns Google's generic
	// 404 HTML page because the REST path simply doesn't exist.
	url := fmt.Sprintf("https://%s/v1beta1/publishers/%s/models", host, publisher)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// Required when ADC is a user account: aiplatform.googleapis.com
	// needs a quota project, which the bearer token alone doesn't
	// carry. Send the project as the quota project so a gcloud-login
	// user with no `gcloud auth application-default set-quota-project`
	// doesn't get a 403. Harmless for service-account tokens.
	req.Header.Set("X-Goog-User-Project", p.projectID)

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
