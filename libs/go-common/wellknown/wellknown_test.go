package wellknown

import (
	"encoding/json"
	"sync"
	"testing"
)

func TestGet_NothingRegistered(t *testing.T) {
	ResetForTest()

	cfg, ok := Get()
	if ok {
		t.Fatalf("Get() ok = true with nothing registered, want false")
	}
	// The zero Config must be inert: a caller that ignores ok and reads the
	// value anyway has to see an empty auth type, never a half-built OIDC
	// block it might advertise.
	if cfg.Auth.Type != "" {
		t.Errorf("zero Config Auth.Type = %q, want empty", cfg.Auth.Type)
	}
	if cfg.Auth.OIDC != nil {
		t.Errorf("zero Config Auth.OIDC = %+v, want nil", cfg.Auth.OIDC)
	}
	if cfg.Branding != nil {
		t.Errorf("zero Config Branding = %s, want nil", cfg.Branding)
	}
}

func TestRegister_RoundTrips(t *testing.T) {
	ResetForTest()

	want := Config{
		Auth: Auth{
			Type: AuthTypeOIDC,
			OIDC: &OIDC{
				Issuer:         "https://id.example/realms/decisionbox",
				MobileClientID: "decisionbox-mobile",
				Scopes:         []string{"openid", "profile", "email", "roles"},
				Audience:       "decisionbox-api",
			},
		},
		Branding: json.RawMessage(`{"name":"Acme"}`),
	}
	Register(want)

	got, ok := Get()
	if !ok {
		t.Fatalf("Get() ok = false after Register, want true")
	}
	if got.Auth.Type != AuthTypeOIDC {
		t.Errorf("Auth.Type = %q, want %q", got.Auth.Type, AuthTypeOIDC)
	}
	if got.Auth.OIDC == nil {
		t.Fatalf("Auth.OIDC = nil, want the registered block")
	}
	if got.Auth.OIDC.Issuer != want.Auth.OIDC.Issuer {
		t.Errorf("Issuer = %q, want %q", got.Auth.OIDC.Issuer, want.Auth.OIDC.Issuer)
	}
	if got.Auth.OIDC.MobileClientID != want.Auth.OIDC.MobileClientID {
		t.Errorf("MobileClientID = %q, want %q", got.Auth.OIDC.MobileClientID, want.Auth.OIDC.MobileClientID)
	}
	if got.Auth.OIDC.Audience != want.Auth.OIDC.Audience {
		t.Errorf("Audience = %q, want %q", got.Auth.OIDC.Audience, want.Auth.OIDC.Audience)
	}
	if len(got.Auth.OIDC.Scopes) != 4 {
		t.Errorf("Scopes = %v, want 4 entries", got.Auth.OIDC.Scopes)
	}
	if string(got.Branding) != `{"name":"Acme"}` {
		t.Errorf("Branding = %s, want the registered blob verbatim", got.Branding)
	}
}

func TestRegister_OIDCWithEmptyMobileClientIsStillOIDC(t *testing.T) {
	ResetForTest()

	// The invariant is about the MODE, not about the mobile client: a
	// deployment that enables auth but never provisioned a mobile client
	// must still advertise "oidc", or a client would attempt anonymous
	// access against an API that enforces auth.
	Register(Config{Auth: Auth{
		Type: AuthTypeOIDC,
		OIDC: &OIDC{Issuer: "https://id.example", MobileClientID: ""},
	}})

	got, ok := Get()
	if !ok || got.Auth.Type != AuthTypeOIDC {
		t.Fatalf("Auth.Type = %q (ok=%v), want %q", got.Auth.Type, ok, AuthTypeOIDC)
	}
	if got.Auth.OIDC == nil || got.Auth.OIDC.MobileClientID != "" {
		t.Errorf("OIDC = %+v, want a non-nil block with an empty mobile client id", got.Auth.OIDC)
	}
}

func TestRegister_LastWriterWins(t *testing.T) {
	ResetForTest()

	Register(Config{Auth: Auth{Type: AuthTypeNone}})
	Register(Config{Auth: Auth{
		Type: AuthTypeOIDC,
		OIDC: &OIDC{Issuer: "https://second.example"},
	}})

	got, _ := Get()
	if got.Auth.Type != AuthTypeOIDC {
		t.Errorf("Auth.Type = %q, want the second registration (%q)", got.Auth.Type, AuthTypeOIDC)
	}
	if got.Auth.OIDC == nil || got.Auth.OIDC.Issuer != "https://second.example" {
		t.Errorf("OIDC = %+v, want the second registration's issuer", got.Auth.OIDC)
	}
}

func TestRegister_StoresACopy(t *testing.T) {
	ResetForTest()

	cfg := Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "https://id.example"}}}
	Register(cfg)
	// Mutating the caller's struct after registering must not reach the
	// registry: Register takes a copy precisely so a build that reuses its
	// config value cannot retroactively change what the endpoint serves.
	cfg.Auth.Type = AuthTypeNone

	if got, _ := Get(); got.Auth.Type != AuthTypeOIDC {
		t.Errorf("Auth.Type = %q after mutating the caller's copy, want %q", got.Auth.Type, AuthTypeOIDC)
	}
}

func TestGet_ReturnsACopy(t *testing.T) {
	ResetForTest()

	Register(Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "https://id.example"}}})

	first, _ := Get()
	first.Auth.Type = AuthTypeNone

	if second, _ := Get(); second.Auth.Type != AuthTypeOIDC {
		t.Errorf("Auth.Type = %q after a caller mutated an earlier Get, want %q", second.Auth.Type, AuthTypeOIDC)
	}
}

func TestResetForTest_Clears(t *testing.T) {
	ResetForTest()

	Register(Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "https://id.example"}}})
	if _, ok := Get(); !ok {
		t.Fatalf("Get() ok = false after Register, want true")
	}

	ResetForTest()
	if _, ok := Get(); ok {
		t.Errorf("Get() ok = true after ResetForTest, want false")
	}
}

// TestConcurrentRegisterAndGet is the -race guard. Registration happens at
// init() in production, but the endpoint reads it on every request, and the
// read path must be safe against a build (or a test) that registers late.
func TestConcurrentRegisterAndGet(t *testing.T) {
	ResetForTest()

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			Register(Config{Auth: Auth{
				Type: AuthTypeOIDC,
				OIDC: &OIDC{Issuer: "https://id.example", Scopes: []string{"openid"}},
			}})
		}()
		go func() {
			defer wg.Done()
			if cfg, ok := Get(); ok && cfg.Auth.Type != AuthTypeOIDC {
				t.Errorf("Auth.Type = %q, want %q", cfg.Auth.Type, AuthTypeOIDC)
			}
		}()
	}
	wg.Wait()
}

// TestRegister_BrokeredRoundTrips covers the brokered descriptor: both blocks
// survive registration, because a client needs the issuer to sign in AND the
// broker to turn that identity into the token the API accepts.
func TestRegister_BrokeredRoundTrips(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)

	Register(Config{Auth: Auth{
		Type: AuthTypeBrokered,
		OIDC: &OIDC{Issuer: "https://id.example/", MobileClientID: "mobile"},
		Broker: &Broker{
			URL:          "https://broker.example",
			DeploymentID: "6a5e9999169725e95aa71f1d",
			Kind:         "example-broker",
		},
	}})

	got, ok := Get()
	if !ok {
		t.Fatalf("Get() ok = false after Register, want true")
	}
	if got.Auth.Type != AuthTypeBrokered {
		t.Errorf("Auth.Type = %q, want %q", got.Auth.Type, AuthTypeBrokered)
	}
	if got.Auth.Broker == nil {
		t.Fatalf("Auth.Broker = nil, wanted the registered block")
	}
	if got.Auth.Broker.URL != "https://broker.example" {
		t.Errorf("Broker.URL = %q", got.Auth.Broker.URL)
	}
	if got.Auth.Broker.DeploymentID != "6a5e9999169725e95aa71f1d" {
		t.Errorf("Broker.DeploymentID = %q", got.Auth.Broker.DeploymentID)
	}
	if got.Auth.Broker.Kind != "example-broker" {
		t.Errorf("Broker.Kind = %q", got.Auth.Broker.Kind)
	}
	// The sign-in block must survive too: a brokered deployment that
	// advertised no issuer would leave a client with nothing to exchange.
	if got.Auth.OIDC == nil || got.Auth.OIDC.Issuer != "https://id.example/" {
		t.Errorf("Auth.OIDC = %+v, want the registered sign-in block", got.Auth.OIDC)
	}
}

// TestGet_NothingRegistered_BrokerInert mirrors the OIDC case: a caller that
// ignores ok must not find a half-built broker block it might advertise.
func TestGet_NothingRegistered_BrokerInert(t *testing.T) {
	ResetForTest()

	cfg, _ := Get()
	if cfg.Auth.Broker != nil {
		t.Errorf("zero Config Auth.Broker = %+v, want nil", cfg.Auth.Broker)
	}
}

// TestRegisterFallback_OrderIndependent is the point of having a fallback at
// all: two auth plugins in one binary, init order undefined, and the client
// must be told the same thing either way.
//
// Without it, the plugin that is only right when alone has to either call
// Register and hope the other one runs later — a coin flip the client reads —
// or stay silent and let a build that enforces auth advertise "none".
func TestRegisterFallback_OrderIndependent(t *testing.T) {
	authoritative := Config{Auth: Auth{Type: AuthTypeOIDC, OIDC: &OIDC{Issuer: "https://idp.example/"}}}
	onlyIfAlone := Config{Auth: Auth{
		Type:   AuthTypeBrokered,
		Broker: &Broker{URL: "https://broker.example", Kind: "example-broker"},
	}}

	t.Run("fallback first", func(t *testing.T) {
		ResetForTest()
		t.Cleanup(ResetForTest)
		RegisterFallback(onlyIfAlone)
		Register(authoritative)

		got, ok := Get()
		if !ok || got.Auth.Type != AuthTypeOIDC {
			t.Errorf("Auth.Type = %q (ok=%v), want %q", got.Auth.Type, ok, AuthTypeOIDC)
		}
	})

	t.Run("authoritative first", func(t *testing.T) {
		ResetForTest()
		t.Cleanup(ResetForTest)
		Register(authoritative)
		RegisterFallback(onlyIfAlone)

		got, ok := Get()
		if !ok || got.Auth.Type != AuthTypeOIDC {
			t.Errorf("Auth.Type = %q (ok=%v), want %q — a fallback must never displace an authoritative config", got.Auth.Type, ok, AuthTypeOIDC)
		}
	})

	t.Run("fallback alone is served", func(t *testing.T) {
		ResetForTest()
		t.Cleanup(ResetForTest)
		RegisterFallback(onlyIfAlone)

		got, ok := Get()
		if !ok {
			t.Fatalf("Get() ok = false with only a fallback; the deployment would advertise \"none\" while enforcing a token")
		}
		if got.Auth.Type != AuthTypeBrokered {
			t.Errorf("Auth.Type = %q, want %q", got.Auth.Type, AuthTypeBrokered)
		}
	})

	t.Run("ResetForTest clears both", func(t *testing.T) {
		Register(authoritative)
		RegisterFallback(onlyIfAlone)
		ResetForTest()

		if _, ok := Get(); ok {
			t.Errorf("Get() ok = true after reset — a leaked fallback would bleed into the next test")
		}
	})
}
