package llm

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// caPEM renders the test server's own certificate as a PEM block — the
// exact material an operator would paste into the tls_ca_cert field.
func caPEM(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	if cert == nil {
		t.Fatal("test server has no certificate")
	}
	block := &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}
	return string(pem.EncodeToMemory(block))
}

func doGet(t *testing.T, client *http.Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func TestHTTPClientFor_NoTLSKeys_DefaultClient(t *testing.T) {
	client, err := HTTPClientFor(ProviderConfig{}, 7*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.Timeout != 7*time.Second {
		t.Errorf("timeout = %v, want 7s", client.Timeout)
	}
	// The default path must not install a custom transport — it should be
	// byte-for-byte the historical &http.Client{Timeout: timeout}.
	if client.Transport != nil {
		t.Errorf("default client should have nil Transport, got %T", client.Transport)
	}
}

func TestHTTPClientFor_NilConfig(t *testing.T) {
	client, err := HTTPClientFor(nil, time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil || client.Transport != nil {
		t.Errorf("nil cfg should yield a plain default client")
	}
}

func TestHTTPClientFor_PrivateCA_FailsWithoutTrust(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Default client does not trust the server's ad-hoc CA → x509 error.
	client, err := HTTPClientFor(ProviderConfig{}, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = doGet(t, client, srv.URL)
	if err == nil {
		t.Fatal("expected a certificate-verification error, got nil")
	}
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if !errors.As(err, &unknownAuth) && !errors.As(err, &hostErr) {
		t.Fatalf("expected an x509 verification error, got %v", err)
	}
}

func TestHTTPClientFor_PrivateCA_TrustedWhenUploaded(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := ProviderConfig{TLSCACertKey: caPEM(t, srv)}
	client, err := HTTPClientFor(cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.Transport == nil {
		t.Fatal("expected a custom transport when a CA is supplied")
	}
	if err := doGet(t, client, srv.URL); err != nil {
		t.Fatalf("request should succeed with the CA trusted, got %v", err)
	}
}

func TestHTTPClientFor_SkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := ProviderConfig{TLSSkipVerifyKey: "true"}
	client, err := HTTPClientFor(cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := doGet(t, client, srv.URL); err != nil {
		t.Fatalf("request should succeed with verification disabled, got %v", err)
	}
}

func TestHTTPClientFor_MalformedCA(t *testing.T) {
	cfg := ProviderConfig{TLSCACertKey: "-----BEGIN CERTIFICATE-----\nnot base64 at all\n-----END CERTIFICATE-----"}
	_, err := HTTPClientFor(cfg, time.Second)
	if err == nil {
		t.Fatal("expected an error for a malformed PEM, got nil")
	}
	if !strings.Contains(err.Error(), TLSCACertKey) {
		t.Errorf("error should name the offending key %q, got %v", TLSCACertKey, err)
	}
}

func TestHTTPClientFor_GarbageCA(t *testing.T) {
	// Non-PEM garbage: AppendCertsFromPEM returns false → error.
	cfg := ProviderConfig{TLSCACertKey: "definitely not a certificate"}
	if _, err := HTTPClientFor(cfg, time.Second); err == nil {
		t.Fatal("expected an error for non-PEM input, got nil")
	}
}

func TestHTTPClientFor_SkipVerifyBeatsMissingCA(t *testing.T) {
	// skip-verify alone (no CA) still connects to a private-CA server.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := ProviderConfig{TLSCACertKey: caPEM(t, srv), TLSSkipVerifyKey: "true"}
	client, err := HTTPClientFor(cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := doGet(t, client, srv.URL); err != nil {
		t.Fatalf("request should succeed, got %v", err)
	}
}

func TestHasCustomTLS(t *testing.T) {
	cases := []struct {
		name string
		cfg  ProviderConfig
		want bool
	}{
		{"nil", nil, false},
		{"empty", ProviderConfig{}, false},
		{"ca", ProviderConfig{TLSCACertKey: "x"}, true},
		{"skip true", ProviderConfig{TLSSkipVerifyKey: "true"}, true},
		{"skip false", ProviderConfig{TLSSkipVerifyKey: "false"}, false},
		{"skip other", ProviderConfig{TLSSkipVerifyKey: "1"}, false},
	}
	for _, c := range cases {
		if got := HasCustomTLS(c.cfg); got != c.want {
			t.Errorf("%s: HasCustomTLS = %v, want %v", c.name, got, c.want)
		}
	}
}
