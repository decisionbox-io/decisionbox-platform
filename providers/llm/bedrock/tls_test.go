package bedrock

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestFactory_TLS_MalformedCA proves the bedrock factory threads the
// per-project TLS config through gollm.HTTPClientFor: a garbage CA PEM is
// rejected at construction, before AWS credential resolution.
func TestFactory_TLS_MalformedCA(t *testing.T) {
	_, err := gollm.NewProvider(providerName, gollm.ProviderConfig{
		"region":           "us-east-1",
		"model":            "anthropic.claude-opus-4-7-v1:0",
		gollm.TLSCACertKey: "not a certificate",
	})
	if err == nil {
		t.Fatal("NewProvider should reject a malformed CA PEM")
	}
}
