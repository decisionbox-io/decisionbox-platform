package handler

import (
	"net/http"
	"regexp"

	"github.com/decisionbox-io/decisionbox/services/api/database"
)

// localePattern validates a locale tag by shape (BCP-47-ish), not against a
// fixed list. The set of supported UI languages is defined by the dashboard
// message catalog, so the API must not hardcode it — that keeps adding a
// language a drop-in on the frontend with no server change.
var localePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{2,8})*$`)

// maxLocaleLen caps the whole tag. The per-subtag regex alone would still admit
// arbitrarily long chains of short subtags (aa-aa-aa-…); a total bound keeps a
// malformed value from being persisted and refetched on every dashboard load.
// RFC 5646 language tags don't exceed this in practice.
const maxLocaleLen = 35

// PreferencesHandler owns the /api/v1/me/preferences routes — per-user
// dashboard preferences keyed by the authenticated principal.
type PreferencesHandler struct {
	repo database.UserPreferenceRepo
}

func NewPreferencesHandler(repo database.UserPreferenceRepo) *PreferencesHandler {
	return &PreferencesHandler{repo: repo}
}

type preferencesResponse struct {
	Locale string `json:"locale"`
}

// Get — GET /api/v1/me/preferences
// Returns the caller's stored preferences. An empty locale means "unset": the
// dashboard then falls back to Accept-Language, then the default locale.
func (h *PreferencesHandler) Get(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	pref, err := h.repo.Get(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load preferences: "+err.Error())
		return
	}
	resp := preferencesResponse{}
	if pref != nil {
		resp.Locale = pref.Locale
	}
	writeJSON(w, http.StatusOK, resp)
}

// Update — PUT /api/v1/me/preferences
// Persists the caller's UI locale. Body: {"locale":"tr"}.
func (h *PreferencesHandler) Update(w http.ResponseWriter, r *http.Request) {
	uid, ok := userID(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var body struct {
		Locale string `json:"locale"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Locale) > maxLocaleLen || !localePattern.MatchString(body.Locale) {
		writeError(w, http.StatusBadRequest, "invalid locale")
		return
	}
	if err := h.repo.SetLocale(r.Context(), uid, body.Locale); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save preferences: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preferencesResponse{Locale: body.Locale})
}
