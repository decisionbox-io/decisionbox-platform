package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gocatalog "github.com/decisionbox-io/decisionbox/libs/go-common/llm/catalog"
)

type fakeCatalogExtender struct {
	entries []gollm.ModelEntry
	err     error
}

func (f *fakeCatalogExtender) Extend(_ context.Context, _ string) ([]gollm.ModelEntry, error) {
	return f.entries, f.err
}
func (f *fakeCatalogExtender) Resolve(_ context.Context, _ string) (*gollm.ModelEntry, error) {
	return nil, gocatalog.ErrModelNotFound
}

func TestProvidersHandler_ListExtendedLLMModels_EmptyByDefault(t *testing.T) {
	t.Cleanup(gocatalog.ResetForTest)
	gocatalog.ResetForTest()
	h := NewProvidersHandler()

	req := httptest.NewRequest("GET", "/api/v1/projects/p1/llm/extended-models", nil)
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()

	h.ListExtendedLLMModels(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env struct {
		Data []gollm.ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not JSON: %v — %s", err, w.Body.String())
	}
	if len(env.Data) != 0 {
		t.Errorf("default community build must return zero extended entries, got %d", len(env.Data))
	}
}

func TestProvidersHandler_ListExtendedLLMModels_FlattensExtenderEntries(t *testing.T) {
	t.Cleanup(gocatalog.ResetForTest)
	gocatalog.ResetForTest()
	gocatalog.RegisterExtender(&fakeCatalogExtender{
		entries: []gollm.ModelEntry{
			{
				ID:              "ext:custom-1",
				DisplayName:     "Custom 1",
				Wire:            gollm.WireOpenAICompat,
				MaxOutputTokens: 4096,
				MaxInputTokens:  32000,
				Pricing:         gollm.TokenPricing{InputPerMillion: 1.5, OutputPerMillion: 7.5},
			},
			{
				// DisplayName omitted — must default to ID in the response.
				ID:              "ext:custom-2",
				Wire:            gollm.WireAnthropic,
				MaxOutputTokens: 8192,
			},
		},
	})
	h := NewProvidersHandler()

	req := httptest.NewRequest("GET", "/api/v1/projects/p1/llm/extended-models", nil)
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListExtendedLLMModels(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var env struct {
		Data []gollm.ModelInfo `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body not JSON: %v — %s", err, w.Body.String())
	}
	got := env.Data
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// Sorted alphabetically by ID, deterministic.
	if got[0].ID != "ext:custom-1" {
		t.Errorf("got[0].ID = %q, want ext:custom-1", got[0].ID)
	}
	if got[0].DisplayName != "Custom 1" {
		t.Errorf("got[0].DisplayName = %q, want %q", got[0].DisplayName, "Custom 1")
	}
	if got[0].MaxOutputTokens != 4096 || got[0].MaxInputTokens != 32000 {
		t.Errorf("max tokens not propagated: %+v", got[0])
	}
	if got[0].InputPricePerMillion != 1.5 || got[0].OutputPricePerMillion != 7.5 {
		t.Errorf("pricing not propagated: %+v", got[0])
	}
	if got[0].Wire != string(gollm.WireOpenAICompat) {
		t.Errorf("wire = %q, want %q", got[0].Wire, gollm.WireOpenAICompat)
	}
	if got[1].DisplayName != "ext:custom-2" {
		t.Errorf("got[1].DisplayName should fall back to ID when empty, got %q", got[1].DisplayName)
	}
}

func TestProvidersHandler_ListExtendedLLMModels_RejectsEmptyProjectID(t *testing.T) {
	t.Cleanup(gocatalog.ResetForTest)
	gocatalog.ResetForTest()
	h := NewProvidersHandler()
	req := httptest.NewRequest("GET", "/api/v1/projects//llm/extended-models", nil)
	// PathValue is unset — simulates a malformed route.
	w := httptest.NewRecorder()
	h.ListExtendedLLMModels(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for empty project id", w.Code)
	}
}

func TestProvidersHandler_ListExtendedLLMModels_PropagatesExtenderError(t *testing.T) {
	t.Cleanup(gocatalog.ResetForTest)
	gocatalog.ResetForTest()
	gocatalog.RegisterExtender(&fakeCatalogExtender{err: errors.New("upstream down")})
	h := NewProvidersHandler()

	req := httptest.NewRequest("GET", "/api/v1/projects/p1/llm/extended-models", nil)
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	h.ListExtendedLLMModels(w, req)
	if w.Code != 500 {
		t.Errorf("status = %d, want 500 when extender errors", w.Code)
	}
}
