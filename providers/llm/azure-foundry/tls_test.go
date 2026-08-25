package azurefoundry

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestFactory_TLS_MalformedCA proves the azure-foundry factory threads the
// per-project TLS config through gollm.HTTPClientFor: a garbage CA PEM is
// rejected at construction.
func TestFactory_TLS_MalformedCA(t *testing.T) {
	_, err := gollm.NewProvider(providerName, gollm.ProviderConfig{
		"endpoint":         "https://my-resource.services.ai.azure.com",
		"credentials_json": "azure-key",
		"model":            "gpt-4o",
		gollm.TLSCACertKey: "not a certificate",
	})
	if err == nil {
		t.Fatal("NewProvider should reject a malformed CA PEM")
	}
}
