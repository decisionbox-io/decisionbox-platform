package openai

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// tlsServer returns an HTTPS test server that answers GET /models with an
// empty OpenAI-shaped list (enough for Validate), plus its own cert in PEM.
func tlsServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	return srv, string(pem.EncodeToMemory(block))
}

// TestFactory_TLS_CATrusted proves the registered openai factory threads a
// per-project CA through HTTPClientFor so a private-CA HTTPS endpoint is
// trusted — the core of #338 for OpenAI-compatible gateways.
func TestFactory_TLS_CATrusted(t *testing.T) {
	srv, caPEM := tlsServer(t)
	defer srv.Close()

	prov, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": "sk-test",
		"model":            "gpt-4o",
		"base_url":         srv.URL,
		gollm.TLSCACertKey: caPEM,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := prov.Validate(context.Background()); err != nil {
		t.Fatalf("Validate with CA should succeed, got %v", err)
	}
}

// TestFactory_TLS_NoCAFails confirms the same endpoint fails without the CA
// (proving the gap the CA upload closes).
func TestFactory_TLS_NoCAFails(t *testing.T) {
	srv, _ := tlsServer(t)
	defer srv.Close()

	prov, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": "sk-test",
		"model":            "gpt-4o",
		"base_url":         srv.URL,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	err = prov.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate without CA should fail on the private cert")
	}
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if !errors.As(err, &unknownAuth) && !errors.As(err, &hostErr) {
		t.Fatalf("want an x509 verification error, got %v", err)
	}
}

// TestFactory_TLS_SkipVerify confirms the skip-verify escape hatch connects.
func TestFactory_TLS_SkipVerify(t *testing.T) {
	srv, _ := tlsServer(t)
	defer srv.Close()

	prov, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json":     "sk-test",
		"model":                "gpt-4o",
		"base_url":             srv.URL,
		gollm.TLSSkipVerifyKey: "true",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := prov.Validate(context.Background()); err != nil {
		t.Fatalf("Validate with skip-verify should succeed, got %v", err)
	}
}

// TestFactory_TLS_MalformedCA confirms a garbage CA fails at construction.
func TestFactory_TLS_MalformedCA(t *testing.T) {
	_, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": "sk-test",
		"model":            "gpt-4o",
		gollm.TLSCACertKey: "not a certificate",
	})
	if err == nil {
		t.Fatal("NewProvider should reject a malformed CA PEM")
	}
}
