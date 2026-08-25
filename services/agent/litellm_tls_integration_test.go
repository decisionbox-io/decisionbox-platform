//go:build integration

// Integration test for #338: a REAL LiteLLM proxy served over HTTPS behind
// a private (self-signed) CA, proving the litellm provider's custom-TLS
// path end to end — no CA fails, CA-uploaded succeeds, skip-verify
// succeeds, and live model listing works through the custom TLS.
//
// Self-contained: LiteLLM is configured with a mock_response model, so no
// upstream LLM, API key, or network egress is needed.
//
// Run: make test-litellm  (requires Docker)

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/litellm"
)

const (
	litellmImage    = "ghcr.io/berriai/litellm:main-stable"
	litellmMockText = "DecisionBox LiteLLM TLS reply"
	litellmModel    = "mock-model"
)

// genPrivateCA creates a self-signed CA plus a leaf certificate valid for
// localhost / 127.0.0.1, returning the CA in PEM (what an operator pastes
// into tls_ca_cert) and the leaf cert+key in PEM (what the proxy serves).
func genPrivateCA(t *testing.T) (caPEM, certPEM, keyPEM []byte) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "DecisionBox Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}

	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})
	return caPEM, certPEM, keyPEM
}

// startLiteLLMTLS boots a LiteLLM proxy over HTTPS with the given leaf
// cert, returns its base URL (https://localhost:<port>) and a cleanup.
func startLiteLLMTLS(t *testing.T, certPEM, keyPEM []byte) (string, func()) {
	t.Helper()
	ctx := context.Background()

	dir := t.TempDir()
	config := fmt.Sprintf(`model_list:
  - model_name: %s
    litellm_params:
      model: openai/gpt-3.5-turbo
      api_key: sk-fake
      mock_response: "%s"
`, litellmModel, litellmMockText)
	writeFile(t, filepath.Join(dir, "config.yaml"), []byte(config))
	writeFile(t, filepath.Join(dir, "server.crt"), certPEM)
	writeFile(t, filepath.Join(dir, "server.key"), keyPEM)

	req := testcontainers.ContainerRequest{
		Image:        litellmImage,
		ExposedPorts: []string{"4000/tcp"},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: filepath.Join(dir, "config.yaml"), ContainerFilePath: "/app/config.yaml", FileMode: 0o644},
			{HostFilePath: filepath.Join(dir, "server.crt"), ContainerFilePath: "/certs/server.crt", FileMode: 0o644},
			{HostFilePath: filepath.Join(dir, "server.key"), ContainerFilePath: "/certs/server.key", FileMode: 0o644},
		},
		Cmd: []string{
			"--config", "/app/config.yaml",
			"--port", "4000",
			"--ssl_keyfile_path", "/certs/server.key",
			"--ssl_certfile_path", "/certs/server.crt",
		},
		WaitingFor: wait.ForListeningPort("4000/tcp").WithStartupTimeout(180 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start LiteLLM container: %v", err)
	}
	port, err := container.MappedPort(ctx, "4000")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("mapped port: %v", err)
	}
	// The leaf cert is valid for localhost; testcontainers publishes the
	// mapped port on the loopback interface.
	base := fmt.Sprintf("https://localhost:%s", port.Port())
	return base, func() { _ = container.Terminate(ctx) }
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func litellmProvider(t *testing.T, base string, extra map[string]string) gollm.Provider {
	t.Helper()
	cfg := gollm.ProviderConfig{"base_url": base, "model": litellmModel}
	for k, v := range extra {
		cfg[k] = v
	}
	prov, err := gollm.NewProvider("litellm", cfg)
	if err != nil {
		t.Fatalf("NewProvider(litellm): %v", err)
	}
	return prov
}

func TestLiteLLMTLS_NoCA_FailsVerification(t *testing.T) {
	_, certPEM, keyPEM := genPrivateCA(t)
	base, cleanup := startLiteLLMTLS(t, certPEM, keyPEM)
	defer cleanup()

	prov := litellmProvider(t, base, nil)
	_, err := prov.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if err == nil {
		t.Fatal("expected an x509 error against the private-CA proxy without a CA")
	}
	var unknownAuth x509.UnknownAuthorityError
	if !errors.As(err, &unknownAuth) && !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("want a certificate-verification error, got %v", err)
	}
}

func TestLiteLLMTLS_WithCA_Succeeds(t *testing.T) {
	caPEM, certPEM, keyPEM := genPrivateCA(t)
	base, cleanup := startLiteLLMTLS(t, certPEM, keyPEM)
	defer cleanup()

	prov := litellmProvider(t, base, map[string]string{gollm.TLSCACertKey: string(caPEM)})

	// Live model listing through the custom TLS.
	if lister, ok := prov.(gollm.ModelLister); ok {
		models, err := lister.ListModels(context.Background())
		if err != nil {
			t.Fatalf("ListModels over CA TLS: %v", err)
		}
		var found bool
		for _, m := range models {
			if m.ID == litellmModel {
				found = true
			}
		}
		if !found {
			t.Errorf("live model list %v missing %q", models, litellmModel)
		}
	} else {
		t.Fatal("litellm provider should implement ModelLister")
	}

	// Real chat over the private-CA endpoint returns the mock reply.
	resp, err := prov.Chat(context.Background(), gollm.ChatRequest{Messages: []gollm.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat over CA TLS: %v", err)
	}
	if !strings.Contains(resp.Content, litellmMockText) {
		t.Errorf("content = %q, want it to contain %q", resp.Content, litellmMockText)
	}
}

func TestLiteLLMTLS_SkipVerify_Succeeds(t *testing.T) {
	_, certPEM, keyPEM := genPrivateCA(t)
	base, cleanup := startLiteLLMTLS(t, certPEM, keyPEM)
	defer cleanup()

	prov := litellmProvider(t, base, map[string]string{gollm.TLSSkipVerifyKey: "true"})
	if err := prov.Validate(context.Background()); err != nil {
		t.Fatalf("Validate with skip-verify over private-CA TLS: %v", err)
	}
}
