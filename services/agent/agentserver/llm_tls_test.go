package agentserver

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gosecrets "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/config"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"

	_ "github.com/decisionbox-io/decisionbox/providers/llm/litellm"
)

// notFoundSecrets is a Provider stub with no stored secrets, so
// resolveCredential falls through to the (unset) env var and yields an
// empty credential — matching an open LLM endpoint that needs no key.
type notFoundSecrets struct{}

func (notFoundSecrets) Get(context.Context, string, string) (string, error) {
	return "", gosecrets.ErrNotFound
}
func (notFoundSecrets) Set(context.Context, string, string, string) error { return nil }
func (notFoundSecrets) List(context.Context, string) ([]gosecrets.SecretEntry, error) {
	return nil, nil
}

// tlsProxy is an HTTPS test server standing in for an OpenAI-compatible
// endpoint (LiteLLM speaks this wire). It answers /v1/models and
// /v1/chat/completions and returns its own certificate as a PEM CA.
func tlsProxy(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"mock-model"}]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	return srv, string(pem.EncodeToMemory(block))
}

func projectWithLLMConfig(baseURL string, extra map[string]string) *models.Project {
	cfg := map[string]string{"base_url": baseURL}
	for k, v := range extra {
		cfg[k] = v
	}
	return &models.Project{
		LLM: models.LLMConfig{
			Provider: "litellm",
			Model:    "mock-model",
			Config:   cfg,
		},
	}
}

// TestInitLLMProvider_CATravelsViaProjectConfig is the crux of #338: the
// spawned agent builds its provider from project.LLM.Config, so a CA
// stored there must reach it. initLLMProvider merges LLM.Config into the
// provider cfg; with the CA present, a call against a private-CA HTTPS
// endpoint succeeds — the exact path the agent uses, no env forwarding.
func TestInitLLMProvider_CATravelsViaProjectConfig(t *testing.T) {
	srv, caPEM := tlsProxy(t)
	defer srv.Close()

	project := projectWithLLMConfig(srv.URL, map[string]string{gollm.TLSCACertKey: caPEM})

	prov, err := initLLMProvider(context.Background(), &config.Config{}, project, notFoundSecrets{}, "proj-1")
	if err != nil {
		t.Fatalf("initLLMProvider: %v", err)
	}
	if err := prov.Validate(context.Background()); err != nil {
		t.Fatalf("Validate over private-CA TLS should succeed with the CA in project config, got %v", err)
	}
	if _, err := prov.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat over private-CA TLS should succeed, got %v", err)
	}
}

// TestInitLLMProvider_NoCAFails proves the CA is what makes it work: the
// same endpoint without the CA in project config fails verification.
func TestInitLLMProvider_NoCAFails(t *testing.T) {
	srv, _ := tlsProxy(t)
	defer srv.Close()

	project := projectWithLLMConfig(srv.URL, nil)

	prov, err := initLLMProvider(context.Background(), &config.Config{}, project, notFoundSecrets{}, "proj-1")
	if err != nil {
		t.Fatalf("initLLMProvider: %v", err)
	}
	err = prov.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate should fail without the CA")
	}
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if !errors.As(err, &unknownAuth) && !errors.As(err, &hostErr) {
		t.Fatalf("want an x509 verification error, got %v", err)
	}
}

// TestInitLLMProvider_SkipVerify proves the skip-verify escape hatch also
// travels via project config.
func TestInitLLMProvider_SkipVerify(t *testing.T) {
	srv, _ := tlsProxy(t)
	defer srv.Close()

	project := projectWithLLMConfig(srv.URL, map[string]string{gollm.TLSSkipVerifyKey: "true"})

	prov, err := initLLMProvider(context.Background(), &config.Config{}, project, notFoundSecrets{}, "proj-1")
	if err != nil {
		t.Fatalf("initLLMProvider: %v", err)
	}
	if err := prov.Validate(context.Background()); err != nil {
		t.Fatalf("Validate with skip-verify should succeed, got %v", err)
	}
}
