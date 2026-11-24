package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/wellknown"
)

// decodeDiscovery reads the response body as a generic map. It deliberately
// does NOT decode into discoveryResponse: the point of most of these tests
// is the shape on the wire (which keys are present, and that there is no
// envelope), which a typed decode would hide.
func decodeDiscovery(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("response is not a JSON object: %v\nbody: %s", err, body)
	}
	return got
}

func serveWellKnown(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	WellKnown(rr, httptest.NewRequest(http.MethodGet, "/.well-known/decisionbox", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	return rr
}

func TestWellKnown_NothingRegistered_ReportsExplicitNone(t *testing.T) {
	wellknown.ResetForTest()

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	auth, ok := got["auth"].(map[string]any)
	if !ok {
		t.Fatalf("auth = %v, want an object", got["auth"])
	}
	// Explicit, never absent: a client must not have to infer "no auth" from
	// a missing field, because a misconfigured OIDC deployment would then be
	// silently downgraded to anonymous access.
	if auth["type"] != wellknown.AuthTypeNone {
		t.Errorf("auth.type = %v, want %q", auth["type"], wellknown.AuthTypeNone)
	}
	if _, present := got["oidc"]; present {
		t.Errorf("oidc key is present on a no-auth deployment: %v", got["oidc"])
	}
	if _, present := got["branding"]; present {
		t.Errorf("branding key is present with nothing registered: %v", got["branding"])
	}
	if got["api_version"] != discoveryAPIVersion {
		t.Errorf("api_version = %v, want %q", got["api_version"], discoveryAPIVersion)
	}
}

// TestWellKnown_NotEnveloped is the contract test that matters most for the
// client: this is the ONE endpoint that is not wrapped in {"data": …},
// because a client reads it before it knows the platform's conventions.
func TestWellKnown_NotEnveloped(t *testing.T) {
	wellknown.ResetForTest()

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	if _, enveloped := got["data"]; enveloped {
		t.Fatalf("discovery response is wrapped in a data envelope: %v", got)
	}
	if _, present := got["api_version"]; !present {
		t.Errorf("api_version is not at the top level: %v", got)
	}
}

func TestWellKnown_OIDCRegistered(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{Auth: wellknown.Auth{
		Type: wellknown.AuthTypeOIDC,
		OIDC: &wellknown.OIDC{
			Issuer:         "https://id.example/realms/decisionbox",
			MobileClientID: "decisionbox-mobile",
			Scopes:         []string{"openid", "profile", "email", "roles"},
			Audience:       "decisionbox-api",
		},
	}})
	t.Cleanup(wellknown.ResetForTest)

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	auth := got["auth"].(map[string]any)
	if auth["type"] != wellknown.AuthTypeOIDC {
		t.Errorf("auth.type = %v, want %q", auth["type"], wellknown.AuthTypeOIDC)
	}
	oidc, ok := got["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("oidc = %v, want an object sibling of auth", got["oidc"])
	}
	// The OIDC details are a SIBLING of auth, not nested inside it — the
	// client reads them from the top level.
	if _, nested := auth["oidc"]; nested {
		t.Errorf("oidc is nested inside auth; it must be a top-level sibling")
	}
	if oidc["issuer"] != "https://id.example/realms/decisionbox" {
		t.Errorf("oidc.issuer = %v", oidc["issuer"])
	}
	if oidc["mobile_client_id"] != "decisionbox-mobile" {
		t.Errorf("oidc.mobile_client_id = %v", oidc["mobile_client_id"])
	}
	if oidc["audience"] != "decisionbox-api" {
		t.Errorf("oidc.audience = %v", oidc["audience"])
	}
	scopes, ok := oidc["scopes"].([]any)
	if !ok || len(scopes) != 4 {
		t.Errorf("oidc.scopes = %v, want 4 entries", oidc["scopes"])
	}
}

// TestWellKnown_OIDCWithEmptyMobileClientStillAdvertisesOIDC guards the
// registry invariant at the wire level: a deployment that enables auth but
// never provisioned a mobile client is still an OIDC deployment. Reporting
// "none" here would send a client down the anonymous path against an API
// that enforces auth.
func TestWellKnown_OIDCWithEmptyMobileClientStillAdvertisesOIDC(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{Auth: wellknown.Auth{
		Type: wellknown.AuthTypeOIDC,
		OIDC: &wellknown.OIDC{Issuer: "https://id.example", MobileClientID: ""},
	}})
	t.Cleanup(wellknown.ResetForTest)

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	if auth := got["auth"].(map[string]any); auth["type"] != wellknown.AuthTypeOIDC {
		t.Errorf("auth.type = %v, want %q", auth["type"], wellknown.AuthTypeOIDC)
	}
	oidc, ok := got["oidc"].(map[string]any)
	if !ok {
		t.Fatalf("oidc = %v, want the block even with an empty client id", got["oidc"])
	}
	// Present but empty, so a client can say "this deployment has no mobile
	// client configured" instead of guessing.
	if oidc["mobile_client_id"] != "" {
		t.Errorf("oidc.mobile_client_id = %v, want an empty string", oidc["mobile_client_id"])
	}
}

// TestWellKnown_OIDCTypeWithNilBlock covers a build that violates the
// registry invariant halfway: type "oidc" with no OIDC details. The
// endpoint must still report the enforced mode honestly and simply omit
// the block — reporting "none" would be the dangerous failure.
func TestWellKnown_OIDCTypeWithNilBlock(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{Auth: wellknown.Auth{Type: wellknown.AuthTypeOIDC}})
	t.Cleanup(wellknown.ResetForTest)

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	if auth := got["auth"].(map[string]any); auth["type"] != wellknown.AuthTypeOIDC {
		t.Errorf("auth.type = %v, want %q — the enforced mode, not a downgrade", auth["type"], wellknown.AuthTypeOIDC)
	}
	if _, present := got["oidc"]; present {
		t.Errorf("oidc = %v, want the key omitted when no block was registered", got["oidc"])
	}
}

// TestWellKnown_RegisteredNoneIsReportedAsNone covers a build that
// explicitly registers the unauthenticated mode (rather than registering
// nothing at all).
func TestWellKnown_RegisteredNoneIsReportedAsNone(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{Auth: wellknown.Auth{
		Type: wellknown.AuthTypeNone,
		// An OIDC block left over from a reused config value must NOT leak
		// into a "none" document.
		OIDC: &wellknown.OIDC{Issuer: "https://stale.example"},
	}})
	t.Cleanup(wellknown.ResetForTest)

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	if auth := got["auth"].(map[string]any); auth["type"] != wellknown.AuthTypeNone {
		t.Errorf("auth.type = %v, want %q", auth["type"], wellknown.AuthTypeNone)
	}
	if _, present := got["oidc"]; present {
		t.Errorf("oidc = %v leaked into a none document", got["oidc"])
	}
}

// TestWellKnown_EmptyTypeFallsBackToNone covers a registration with a blank
// type (a build that filled Branding only). The endpoint must not serve an
// empty auth.type, which the client would reject as malformed.
func TestWellKnown_EmptyTypeFallsBackToNone(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{Branding: json.RawMessage(`{"name":"Acme"}`)})
	t.Cleanup(wellknown.ResetForTest)

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	if auth := got["auth"].(map[string]any); auth["type"] != wellknown.AuthTypeNone {
		t.Errorf("auth.type = %v, want %q", auth["type"], wellknown.AuthTypeNone)
	}
	if got["branding"] == nil {
		t.Errorf("branding was dropped: %v", got)
	}
}

func TestWellKnown_BrandingPassedThroughVerbatim(t *testing.T) {
	wellknown.ResetForTest()
	wellknown.Register(wellknown.Config{
		Auth:     wellknown.Auth{Type: wellknown.AuthTypeNone},
		Branding: json.RawMessage(`{"name":"Acme","logo_url":"https://acme.example/logo.png"}`),
	})
	t.Cleanup(wellknown.ResetForTest)

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	branding, ok := got["branding"].(map[string]any)
	if !ok {
		t.Fatalf("branding = %v, want the registered object", got["branding"])
	}
	if branding["name"] != "Acme" {
		t.Errorf("branding.name = %v, want Acme", branding["name"])
	}
	// Opaque pass-through: a key the platform knows nothing about survives.
	if branding["logo_url"] != "https://acme.example/logo.png" {
		t.Errorf("branding.logo_url = %v", branding["logo_url"])
	}
}

func TestWellKnown_AdvertisesTheFeaturesTheClientRequires(t *testing.T) {
	wellknown.ResetForTest()

	got := decodeDiscovery(t, serveWellKnown(t).Body.Bytes())

	raw, ok := got["features"].([]any)
	if !ok {
		t.Fatalf("features = %v, want an array", got["features"])
	}
	have := make(map[string]bool, len(raw))
	for _, f := range raw {
		s, isString := f.(string)
		if !isString {
			t.Fatalf("features contains a non-string entry: %v", f)
		}
		have[s] = true
	}
	// The four the client treats as required (its compat gate refuses a
	// deployment missing any of them), plus the capabilities Phase 3 adds.
	for _, want := range []string{
		FeatureProjects, FeatureGroundedChat, FeatureAskSessions, FeatureMe,
		FeatureAskSessionRename,
		FeatureInsights, FeatureRecommendations, FeatureLists, FeatureReads,
		FeatureSearch, FeatureCrossProjectSearch,
	} {
		if !have[want] {
			t.Errorf("features is missing %q: %v", want, raw)
		}
	}
	// Plan-gated and per-request capabilities must NOT be advertised here —
	// see the scope rule on discoveryFeatures.
	for _, unwanted := range []string{"executive-summary", "reports", "data_query", "async_ask"} {
		if have[unwanted] {
			t.Errorf("features advertises %q, which is not a deployment-level property", unwanted)
		}
	}
}

// TestWellKnown_NoStore keeps a shared proxy from handing one deployment's
// auth mode to a client pointed at another.
func TestWellKnown_NoStore(t *testing.T) {
	wellknown.ResetForTest()

	rr := serveWellKnown(t)
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}
