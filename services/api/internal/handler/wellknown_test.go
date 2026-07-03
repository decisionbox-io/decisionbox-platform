package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/wellknown"
)

func doWellKnown(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest("GET", "/.well-known/decisionbox", nil)
	w := httptest.NewRecorder()
	WellKnown(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &top); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, w.Body.String())
	}
	return top
}

func TestWellKnown_RawNotEnveloped(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)

	top := doWellKnown(t)
	// Raw JSON contract: the top-level object carries the discovery
	// fields directly, NOT a {"data": …} wrapper.
	if _, wrapped := top["data"]; wrapped {
		t.Fatal("response is enveloped in {data}, want raw discovery object")
	}
	for _, key := range []string{"api_version", "features", "auth"} {
		if _, ok := top[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
}

func TestWellKnown_DefaultsToNoAuth(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)

	top := doWellKnown(t)

	var apiVer string
	_ = json.Unmarshal(top["api_version"], &apiVer)
	if apiVer != "v1" {
		t.Errorf("api_version = %q, want v1", apiVer)
	}

	var feats []string
	_ = json.Unmarshal(top["features"], &feats)
	want := []string{"projects", "grounded_chat", "ask_sessions", "me", "ask_session_rename"}
	if len(feats) != len(want) {
		t.Fatalf("features = %v, want %v", feats, want)
	}
	for i := range want {
		if feats[i] != want[i] {
			t.Errorf("features[%d] = %q, want %q", i, feats[i], want[i])
		}
	}

	var auth struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(top["auth"], &auth)
	if auth.Type != "none" {
		t.Errorf("auth.type = %q, want none", auth.Type)
	}
	if _, ok := top["oidc"]; ok {
		t.Error("oidc block present with auth type none, want omitted")
	}
	if _, ok := top["branding"]; ok {
		t.Error("branding present when unregistered, want omitted")
	}
}

func TestWellKnown_OIDCFromRegistry(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)

	wellknown.Register(wellknown.Config{
		Auth: wellknown.Auth{
			Type: wellknown.AuthTypeOIDC,
			OIDC: &wellknown.OIDC{
				Issuer:         "https://tenant.example.com",
				MobileClientID: "mobile-xyz",
				Scopes:         []string{"openid", "profile", "email", "offline_access"},
				Audience:       "https://api.example.com",
			},
		},
		Branding: json.RawMessage(`{"app_name":"Acme"}`),
	})

	top := doWellKnown(t)

	var auth struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(top["auth"], &auth)
	if auth.Type != "oidc" {
		t.Fatalf("auth.type = %q, want oidc", auth.Type)
	}

	raw, ok := top["oidc"]
	if !ok {
		t.Fatal("oidc block missing with auth type oidc")
	}
	var oidc struct {
		Issuer         string   `json:"issuer"`
		MobileClientID string   `json:"mobile_client_id"`
		Scopes         []string `json:"scopes"`
		Audience       string   `json:"audience"`
	}
	if err := json.Unmarshal(raw, &oidc); err != nil {
		t.Fatalf("decode oidc: %v", err)
	}
	if oidc.Issuer != "https://tenant.example.com" {
		t.Errorf("issuer = %q", oidc.Issuer)
	}
	if oidc.MobileClientID != "mobile-xyz" {
		t.Errorf("mobile_client_id = %q", oidc.MobileClientID)
	}
	if len(oidc.Scopes) != 4 {
		t.Errorf("scopes = %v, want 4", oidc.Scopes)
	}
	if oidc.Audience != "https://api.example.com" {
		t.Errorf("audience = %q", oidc.Audience)
	}

	// branding passthrough surfaced verbatim.
	var branding struct {
		AppName string `json:"app_name"`
	}
	_ = json.Unmarshal(top["branding"], &branding)
	if branding.AppName != "Acme" {
		t.Errorf("branding.app_name = %q, want Acme", branding.AppName)
	}
}

// TestWellKnown_NoOIDCBlockWhenTypeNone ensures a registration that sets
// type "none" (e.g. a build that explicitly advertises no auth) does not
// leak an oidc block even if one were somehow attached.
func TestWellKnown_NoOIDCBlockWhenTypeNone(t *testing.T) {
	wellknown.ResetForTest()
	t.Cleanup(wellknown.ResetForTest)

	wellknown.Register(wellknown.Config{Auth: wellknown.Auth{Type: wellknown.AuthTypeNone}})

	top := doWellKnown(t)
	var auth struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(top["auth"], &auth)
	if auth.Type != "none" {
		t.Errorf("auth.type = %q, want none", auth.Type)
	}
	if _, ok := top["oidc"]; ok {
		t.Error("oidc present with type none")
	}
}
