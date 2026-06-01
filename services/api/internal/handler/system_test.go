package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/systeminfo"
)

// decodeSystem unwraps the standard {"data": {...}} envelope into the
// system response shape.
func decodeSystem(t *testing.T, body []byte) systemInfoResponse {
	t.Helper()
	var env struct {
		Data systemInfoResponse `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, body)
	}
	return env.Data
}

func TestSystemInfo_ReturnsRegistryInventory(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	systeminfo.Register(systeminfo.Descriptor{
		Name: "API", Kind: systeminfo.KindService, Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-06-01T00:00:00Z",
	})
	systeminfo.Register(systeminfo.Descriptor{
		Name: "Schema indexing", Kind: systeminfo.KindWorker, RunsIn: "API", Version: "1.2.3",
		Note: "runs in-process inside the API service; shares its image version",
	})
	systeminfo.Register(systeminfo.Descriptor{
		Name: "Agent", Kind: systeminfo.KindService, Version: "latest", Note: "agent image the API is configured to launch",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	w := httptest.NewRecorder()
	SystemInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}

	resp := decodeSystem(t, w.Body.Bytes())
	if len(resp.Components) != 3 {
		t.Fatalf("components = %d, want 3: %+v", len(resp.Components), resp.Components)
	}

	byName := map[string]systeminfo.Descriptor{}
	for _, c := range resp.Components {
		byName[c.Name] = c
	}

	api, ok := byName["API"]
	if !ok || api.Kind != systeminfo.KindService || api.Version != "1.2.3" || api.Commit != "abc1234" {
		t.Fatalf("API descriptor wrong: %+v", api)
	}

	// In-process worker must carry parent + explanatory note.
	worker, ok := byName["Schema indexing"]
	if !ok || worker.Kind != systeminfo.KindWorker || worker.RunsIn != "API" || worker.Note == "" {
		t.Fatalf("worker descriptor must report runs_in + note: %+v", worker)
	}

	// Agent must carry a clarifying note.
	agent, ok := byName["Agent"]
	if !ok || agent.Note == "" {
		t.Fatalf("agent descriptor must carry a note: %+v", agent)
	}
}

func TestSystemInfo_EmptyRegistryReturnsEmptyArray(t *testing.T) {
	systeminfo.ResetForTest()
	t.Cleanup(systeminfo.ResetForTest)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system", nil)
	w := httptest.NewRecorder()
	SystemInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// components must serialize as [] (not null) so the dashboard can map over it.
	if body := w.Body.String(); !jsonHasEmptyComponents(body) {
		t.Fatalf("expected components:[] in body, got: %s", body)
	}
}

func jsonHasEmptyComponents(body string) bool {
	var env struct {
		Data struct {
			Components []systeminfo.Descriptor `json:"components"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return false
	}
	return env.Data.Components != nil && len(env.Data.Components) == 0
}
