package gcpcreds

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The service-account flow's defining property is that it holds no refresh
// token: every access token is minted afresh by signing a JWT with the stored
// private key. That only works economically if the resulting token is CACHED
// until it expires — otherwise every API call costs a token round-trip, which
// on a quota-limited source is a silent tax that shows up as throttling rather
// than as an error.
//
// Nothing pinned that. These tests do, hermetically: the fake service-account
// key points token_uri at a local server, so the mint count is observable
// without touching Google.

// tokenServer is a stand-in for Google's OAuth token endpoint that counts mints
// and issues tokens with a caller-chosen lifetime.
type tokenServer struct {
	*httptest.Server
	mints     atomic.Int32
	expiresIn int
}

func newTokenServer(t *testing.T, expiresIn int) *tokenServer {
	t.Helper()
	ts := &tokenServer{expiresIn: expiresIn}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := ts.mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":%d}`,
			fmt.Sprintf("token-%d", n), ts.expiresIn)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// saKeyJSON builds a syntactically real service-account key whose token_uri
// points at the test server. The RSA key is generated per call so nothing
// resembling a credential is committed.
func saKeyJSON(t *testing.T, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: mustPKCS8(t, key),
	})
	b, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "test-project",
		"private_key":  string(pemBytes),
		"client_email": "probe@test-project.iam.gserviceaccount.com",
		"token_uri":    tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(b)
}

func mustPKCS8(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	b, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return b
}

// TestTokenSource_SAKey_CachesUntilExpiry pins the caching half of the
// service-account model: repeated Token() calls on one source reuse a live
// token instead of re-signing a JWT and re-minting each time.
func TestTokenSource_SAKey_CachesUntilExpiry(t *testing.T) {
	srv := newTokenServer(t, 3600)

	src, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: saKeyJSON(t, srv.URL),
		Scopes:          []string{"https://www.googleapis.com/auth/analytics.readonly"},
	})
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}

	first, err := src.Token()
	if err != nil {
		t.Fatalf("first Token(): %v", err)
	}
	for i := 0; i < 5; i++ {
		tok, err := src.Token()
		if err != nil {
			t.Fatalf("Token() call %d: %v", i+2, err)
		}
		if tok.AccessToken != first.AccessToken {
			t.Fatalf("call %d returned a different token (%q vs %q) — the source is not caching",
				i+2, tok.AccessToken, first.AccessToken)
		}
	}

	if got := srv.mints.Load(); got != 1 {
		t.Errorf("token endpoint hit %d times for 6 Token() calls, want 1 — each call is minting a new token", got)
	}
}

// TestTokenSource_SAKey_ReMintsAfterExpiry pins the other half: the source must
// not serve a stale token. With no refresh token to fall back on, re-minting
// from the key IS the refresh path.
func TestTokenSource_SAKey_ReMintsAfterExpiry(t *testing.T) {
	// oauth2 treats a token as expired slightly before its stated expiry, so a
	// short lifetime here is already past that threshold when issued.
	srv := newTokenServer(t, 1)

	src, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: saKeyJSON(t, srv.URL),
	})
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}

	first, err := src.Token()
	if err != nil {
		t.Fatalf("first Token(): %v", err)
	}
	second, err := src.Token()
	if err != nil {
		t.Fatalf("second Token(): %v", err)
	}

	if first.AccessToken == second.AccessToken {
		t.Error("an expired token was served again; the source must re-mint from the key")
	}
	if got := srv.mints.Load(); got != 2 {
		t.Errorf("token endpoint hit %d times, want 2 (one per expired token)", got)
	}
}

// TestTokenSource_SAKey_HoldsNoRefreshToken pins the property the phase-1
// decision rests on: nothing to store, rotate, or race across pods. A minted
// token carries an expiry and no refresh token.
func TestTokenSource_SAKey_HoldsNoRefreshToken(t *testing.T) {
	srv := newTokenServer(t, 3600)

	src, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: saKeyJSON(t, srv.URL),
	})
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}

	tok, err := src.Token()
	if err != nil {
		t.Fatalf("Token(): %v", err)
	}
	if tok.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty — the service-account flow stores no refresh token", tok.RefreshToken)
	}
	if tok.Expiry.IsZero() {
		t.Error("token has no expiry; caching cannot be bounded without one")
	}
	if !tok.Expiry.After(time.Now()) {
		t.Error("a freshly minted token is already expired")
	}
}
