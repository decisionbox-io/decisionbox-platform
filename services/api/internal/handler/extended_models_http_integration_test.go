//go:build integration

package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gocatalog "github.com/decisionbox-io/decisionbox/libs/go-common/llm/catalog"
)

// liveCatalogExtender drives the integration test — exposes whichever
// entries the test installs without leaking state across tests.
type liveCatalogExtender struct {
	entries []gollm.ModelEntry
}

func (l *liveCatalogExtender) Extend(_ context.Context, _ string) ([]gollm.ModelEntry, error) {
	return l.entries, nil
}
func (l *liveCatalogExtender) Resolve(_ context.Context, _ string) (*gollm.ModelEntry, error) {
	return nil, gocatalog.ErrModelNotFound
}

// TestInteg_ExtendedLLMModels_RoundTrip exercises the full
// /api/v1/projects/{id}/llm/extended-models response shape against the
// real handler (which depends on the catalog package's registry) and a
// real MongoDB-backed database. The catalog registry itself is process
// global, so we explicitly reset between cases so a prior test cannot
// leak into a later one.
func TestInteg_ExtendedLLMModels_RoundTrip(t *testing.T) {
	t.Cleanup(gocatalog.ResetForTest)
	gocatalog.ResetForTest()

	// Default (no extenders registered) — endpoint returns empty list.
	{
		h := NewProvidersHandler()
		req := httptest.NewRequest("GET", "/api/v1/projects/proj-extmodels/llm/extended-models", nil)
		req.SetPathValue("id", "proj-extmodels")
		w := httptest.NewRecorder()
		h.ListExtendedLLMModels(w, req)
		if w.Code != 200 {
			t.Fatalf("default: status = %d, want 200", w.Code)
		}
		var env struct {
			Data []gollm.ModelInfo `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("default: body not JSON: %v — %s", err, w.Body.String())
		}
		if len(env.Data) != 0 {
			t.Errorf("default community build must return zero entries, got %d", len(env.Data))
		}
	}

	// One extender registered, multiple project IDs see the same shape.
	gocatalog.RegisterExtender(&liveCatalogExtender{
		entries: []gollm.ModelEntry{
			{ID: "ext:m-a", DisplayName: "M A", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 4096, MaxInputTokens: 32000, Pricing: gollm.TokenPricing{InputPerMillion: 2.0, OutputPerMillion: 10.0}},
			{ID: "ext:m-b", Wire: gollm.WireAnthropic, MaxOutputTokens: 8192},
		},
	})

	for _, projectID := range []string{"proj-extmodels-1", "proj-extmodels-2"} {
		h := NewProvidersHandler()
		req := httptest.NewRequest("GET", "/api/v1/projects/"+projectID+"/llm/extended-models", nil)
		req.SetPathValue("id", projectID)
		w := httptest.NewRecorder()
		h.ListExtendedLLMModels(w, req)

		if w.Code != 200 {
			t.Fatalf("project %s: status = %d, want 200", projectID, w.Code)
		}
		var env struct {
			Data []gollm.ModelInfo `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatalf("project %s: body not JSON: %v — %s", projectID, err, w.Body.String())
		}
		if len(env.Data) != 2 {
			t.Fatalf("project %s: got %d entries, want 2", projectID, len(env.Data))
		}
		// Sort key is by ID — "ext:m-a" first.
		if env.Data[0].ID != "ext:m-a" {
			t.Errorf("project %s: env.Data[0].ID = %q, want ext:m-a", projectID, env.Data[0].ID)
		}
		if env.Data[0].DisplayName != "M A" {
			t.Errorf("project %s: DisplayName not propagated, got %q", projectID, env.Data[0].DisplayName)
		}
		if env.Data[0].MaxOutputTokens != 4096 || env.Data[0].MaxInputTokens != 32000 {
			t.Errorf("project %s: token caps not propagated: %+v", projectID, env.Data[0])
		}
		if env.Data[0].InputPricePerMillion != 2.0 || env.Data[0].OutputPricePerMillion != 10.0 {
			t.Errorf("project %s: pricing not propagated: %+v", projectID, env.Data[0])
		}
		// Second entry — DisplayName omitted → falls back to ID.
		if env.Data[1].DisplayName != "ext:m-b" {
			t.Errorf("project %s: DisplayName fallback broke, got %q", projectID, env.Data[1].DisplayName)
		}
	}

	// Empty project ID is rejected at the handler boundary.
	{
		h := NewProvidersHandler()
		req := httptest.NewRequest("GET", "/api/v1/projects//llm/extended-models", nil)
		// PathValue intentionally not set.
		w := httptest.NewRecorder()
		h.ListExtendedLLMModels(w, req)
		if w.Code != 400 {
			t.Errorf("empty pid: status = %d, want 400", w.Code)
		}
	}
}
