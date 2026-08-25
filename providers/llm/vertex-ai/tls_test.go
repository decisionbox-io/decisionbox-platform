package vertexai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestFactory_TLS_MalformedCA proves the vertex-ai factory threads the
// per-project TLS config through gollm.HTTPClientFor: a garbage CA PEM is
// rejected at construction, before any credential resolution.
func TestFactory_TLS_MalformedCA(t *testing.T) {
	_, err := gollm.NewProvider(providerName, gollm.ProviderConfig{
		"project_id":       "my-gcp-project",
		gollm.TLSCACertKey: "not a certificate",
	})
	if err == nil {
		t.Fatal("NewProvider should reject a malformed CA PEM")
	}
}
