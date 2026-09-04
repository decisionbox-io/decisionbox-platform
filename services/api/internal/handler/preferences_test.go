package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- mock UserPreferenceRepo ---

var _ database.UserPreferenceRepo = (*mockUserPrefRepo)(nil)

type mockUserPrefRepo struct {
	items  map[string]*models.UserPreference
	getErr error
	setErr error
}

func newMockUserPrefRepo() *mockUserPrefRepo {
	return &mockUserPrefRepo{items: make(map[string]*models.UserPreference)}
}

func (m *mockUserPrefRepo) Get(_ context.Context, sub string) (*models.UserPreference, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.items[sub], nil
}

func (m *mockUserPrefRepo) SetLocale(_ context.Context, sub, locale string) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.items[sub] = &models.UserPreference{Sub: sub, Locale: locale}
	return nil
}

func decodePrefsData(t *testing.T, w interface{ String() string }) map[string]interface{} {
	t.Helper()
	var resp APIResponse
	if err := json.Unmarshal([]byte(w.String()), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected error: %q", resp.Error)
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Data = %T, want map", resp.Data)
	}
	return data
}

func TestPreferences_Get_UnsetReturnsEmptyLocale(t *testing.T) {
	h := NewPreferencesHandler(newMockUserPrefRepo())
	req, w := newAuthedRequest("GET", "/api/v1/me/preferences", "", "alice")
	h.Get(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodePrefsData(t, w.Body)["locale"]; got != "" {
		t.Errorf("locale = %v, want empty", got)
	}
}

func TestPreferences_PutThenGet_Roundtrip(t *testing.T) {
	repo := newMockUserPrefRepo()
	h := NewPreferencesHandler(repo)

	req, w := newAuthedRequest("PUT", "/api/v1/me/preferences", `{"locale":"tr"}`, "alice")
	h.Update(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := decodePrefsData(t, w.Body)["locale"]; got != "tr" {
		t.Errorf("PUT locale = %v, want tr", got)
	}

	req, w = newAuthedRequest("GET", "/api/v1/me/preferences", "", "alice")
	h.Get(w, req)
	if got := decodePrefsData(t, w.Body)["locale"]; got != "tr" {
		t.Errorf("GET locale = %v, want tr", got)
	}
}

func TestPreferences_Get_ScopedPerUser(t *testing.T) {
	repo := newMockUserPrefRepo()
	h := NewPreferencesHandler(repo)

	req, w := newAuthedRequest("PUT", "/api/v1/me/preferences", `{"locale":"tr"}`, "alice")
	h.Update(w, req)

	// Bob has no stored preference — must not see Alice's.
	req, w = newAuthedRequest("GET", "/api/v1/me/preferences", "", "bob")
	h.Get(w, req)
	if got := decodePrefsData(t, w.Body)["locale"]; got != "" {
		t.Errorf("bob locale = %v, want empty (per-user scoping)", got)
	}
}

func TestPreferences_Update_InvalidLocale(t *testing.T) {
	h := NewPreferencesHandler(newMockUserPrefRepo())
	for _, bad := range []string{
		"", "e", "en_US", "toolonglocalesegment", "tr; DROP", "12",
		// Many short subtags: each passes the per-subtag regex but the whole
		// tag is over the length cap — must still be rejected.
		"aa-aa-aa-aa-aa-aa-aa-aa-aa-aa-aa-aa-aa-aa",
	} {
		body, _ := json.Marshal(map[string]string{"locale": bad})
		req, w := newAuthedRequest("PUT", "/api/v1/me/preferences", string(body), "alice")
		h.Update(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("locale %q: status = %d, want 400", bad, w.Code)
		}
	}
}

func TestPreferences_Update_AcceptsBcp47(t *testing.T) {
	h := NewPreferencesHandler(newMockUserPrefRepo())
	for _, good := range []string{"en", "tr", "pt-BR", "zh-Hans"} {
		body, _ := json.Marshal(map[string]string{"locale": good})
		req, w := newAuthedRequest("PUT", "/api/v1/me/preferences", string(body), "alice")
		h.Update(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("locale %q: status = %d, want 200 (body=%s)", good, w.Code, w.Body.String())
		}
	}
}

func TestPreferences_Update_MalformedBody(t *testing.T) {
	h := NewPreferencesHandler(newMockUserPrefRepo())
	req, w := newAuthedRequest("PUT", "/api/v1/me/preferences", `{not json`, "alice")
	h.Update(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPreferences_Unauthenticated(t *testing.T) {
	h := NewPreferencesHandler(newMockUserPrefRepo())

	// No principal in context on either verb → 401.
	req, w := newAuthedRequest("GET", "/api/v1/me/preferences", "", "")
	h.Get(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET status = %d, want 401", w.Code)
	}

	req, w = newAuthedRequest("PUT", "/api/v1/me/preferences", `{"locale":"tr"}`, "")
	h.Update(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PUT status = %d, want 401", w.Code)
	}
}

func TestPreferences_RepoErrorsSurface(t *testing.T) {
	repo := newMockUserPrefRepo()
	repo.getErr = errors.New("mongo down")
	h := NewPreferencesHandler(repo)
	req, w := newAuthedRequest("GET", "/api/v1/me/preferences", "", "alice")
	h.Get(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("GET status = %d, want 500", w.Code)
	}

	repo2 := newMockUserPrefRepo()
	repo2.setErr = errors.New("mongo down")
	h2 := NewPreferencesHandler(repo2)
	req, w = newAuthedRequest("PUT", "/api/v1/me/preferences", `{"locale":"tr"}`, "alice")
	h2.Update(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("PUT status = %d, want 500", w.Code)
	}
}
