package wellknown

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestGet_UnsetReturnsNotOK(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	cfg, ok := Get()
	if ok {
		t.Fatalf("Get() ok = true on empty registry, want false")
	}
	if cfg.Auth.Type != "" || cfg.Auth.OIDC != nil || cfg.Branding != nil {
		t.Fatalf("Get() on empty registry = %+v, want zero Config", cfg)
	}
}

func TestRegister_RoundTrips(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	want := Config{
		Auth: Auth{
			Type: AuthTypeOIDC,
			OIDC: &OIDC{
				Issuer:         "https://tenant.example.com",
				MobileClientID: "mobile-abc",
				Scopes:         []string{"openid", "profile", "email"},
				Audience:       "https://api.example.com",
			},
		},
		Branding: json.RawMessage(`{"app_name":"Acme Chat"}`),
	}
	Register(want)

	got, ok := Get()
	if !ok {
		t.Fatal("Get() ok = false after Register, want true")
	}
	if got.Auth.Type != AuthTypeOIDC {
		t.Errorf("Auth.Type = %q, want %q", got.Auth.Type, AuthTypeOIDC)
	}
	if got.Auth.OIDC == nil {
		t.Fatal("Auth.OIDC = nil, want non-nil")
	}
	if got.Auth.OIDC.Issuer != want.Auth.OIDC.Issuer {
		t.Errorf("Issuer = %q, want %q", got.Auth.OIDC.Issuer, want.Auth.OIDC.Issuer)
	}
	if got.Auth.OIDC.MobileClientID != want.Auth.OIDC.MobileClientID {
		t.Errorf("MobileClientID = %q, want %q", got.Auth.OIDC.MobileClientID, want.Auth.OIDC.MobileClientID)
	}
	if len(got.Auth.OIDC.Scopes) != 3 {
		t.Errorf("Scopes = %v, want 3 entries", got.Auth.OIDC.Scopes)
	}
	if string(got.Branding) != `{"app_name":"Acme Chat"}` {
		t.Errorf("Branding = %s, want the registered blob", got.Branding)
	}
}

func TestRegister_LastWriterWins(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Config{Auth: Auth{Type: AuthTypeNone}})
	Register(Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "https://second"}}})

	got, ok := Get()
	if !ok || got.Auth.Type != AuthTypeOIDC || got.Auth.OIDC == nil || got.Auth.OIDC.Issuer != "https://second" {
		t.Fatalf("Get() = %+v, want the second registration", got)
	}
}

func TestResetForTest_Clears(t *testing.T) {
	Register(Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "x"}}})
	ResetForTest()
	if _, ok := Get(); ok {
		t.Fatal("Get() ok = true after ResetForTest, want false")
	}
}

// TestConcurrentAccess exercises Register/Get under -race to prove the
// mutex guards the process-global correctly.
func TestConcurrentAccess(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); Register(Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "https://x"}}}) }()
		go func() { defer wg.Done(); _, _ = Get() }()
	}
	wg.Wait()
}
