package ollama

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestFactory_TLS_MalformedCA proves the ollama factory threads the
// per-project TLS config through gollm.HTTPClientFor: a garbage CA PEM
// is rejected at construction rather than surfacing as an opaque
// handshake error later.
func TestFactory_TLS_MalformedCA(t *testing.T) {
	_, err := gollm.NewProvider("ollama", gollm.ProviderConfig{
		"host":             "https://ollama.internal:11434",
		"model":            "qwen2.5",
		gollm.TLSCACertKey: "not a certificate",
	})
	if err == nil {
		t.Fatal("NewProvider should reject a malformed CA PEM")
	}
}
