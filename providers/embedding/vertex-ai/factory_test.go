package vertexai

import (
	"strings"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// Factory-level coverage: AuthMethods registration + routing into
// libs/gcpcreds via the public goembedding.NewProvider path. ADC and
// sa_key behaviour itself is covered exhaustively in libs/gcpcreds
// tests; this suite only asserts the vertex-ai embedding factory wires
// through and surfaces the gcpcreds error with the vertex-ai embedding:
// prefix.

func TestAuthMethods_Registered(t *testing.T) {
	meta, _ := goembedding.GetProviderMeta("vertex-ai")
	if len(meta.AuthMethods) != 2 {
		t.Fatalf("expected 2 auth methods, got %d", len(meta.AuthMethods))
	}
	want := map[string]bool{"adc": false, "sa_key": false}
	for _, m := range meta.AuthMethods {
		if _, ok := want[m.ID]; ok {
			want[m.ID] = true
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("missing auth method %q", id)
		}
	}
}

func TestAuthMethods_SAKeyDeclaresCredentialField(t *testing.T) {
	meta, _ := goembedding.GetProviderMeta("vertex-ai")
	for _, m := range meta.AuthMethods {
		if m.ID != "sa_key" {
			continue
		}
		if len(m.Fields) != 1 {
			t.Fatalf("sa_key should have 1 field, got %d", len(m.Fields))
		}
		if m.Fields[0].Key != "credentials_json" {
			t.Errorf("sa_key field key = %q, want credentials_json", m.Fields[0].Key)
		}
		if m.Fields[0].Type != "credential" {
			t.Errorf("sa_key field type = %q, want credential", m.Fields[0].Type)
		}
		return
	}
	t.Fatal("sa_key auth method not found")
}

func TestAuthMethods_ADCHasNoFields(t *testing.T) {
	meta, _ := goembedding.GetProviderMeta("vertex-ai")
	for _, m := range meta.AuthMethods {
		if m.ID != "adc" {
			continue
		}
		if len(m.Fields) != 0 {
			t.Errorf("adc should have 0 fields, got %d", len(m.Fields))
		}
		return
	}
	t.Fatal("adc auth method not found")
}

func TestFactory_MissingProjectID(t *testing.T) {
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"location":    "us-central1",
		"model":       "text-embedding-005",
		"auth_method": "adc",
	})
	if err == nil {
		t.Fatal("expected error for missing project_id")
	}
	if !strings.Contains(err.Error(), "project_id is required") {
		t.Errorf("error = %v, want substring 'project_id is required'", err)
	}
}

func TestFactory_DefaultLocationWhenOmitted(t *testing.T) {
	// Don't actually hit ADC — point it at a missing file so it errors
	// fast with a recognisable message. The factory still runs the
	// location-defaulting branch before delegating to newGCPAuth.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/path.json")
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"project_id":  "test-project",
		"model":       "text-embedding-005",
		"auth_method": "adc",
	})
	if err == nil {
		t.Fatal("expected error from ADC pointing at missing file")
	}
	if !strings.Contains(err.Error(), "vertex-ai embedding:") {
		t.Errorf("error missing 'vertex-ai embedding:' prefix: %v", err)
	}
}

func TestFactory_UnsupportedModel(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/path.json")
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"project_id":  "test-project",
		"location":    "us-central1",
		"model":       "made-up-model",
		"auth_method": "adc",
	})
	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("error = %v, want substring 'unsupported model'", err)
	}
}

func TestFactory_SAKeyMalformedJSON(t *testing.T) {
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"project_id":       "test-project",
		"location":         "us-central1",
		"model":            "text-embedding-005",
		"auth_method":      "sa_key",
		"credentials_json": "{not-valid-json",
	})
	if err == nil {
		t.Fatal("expected error for malformed SA key JSON")
	}
	if !strings.Contains(err.Error(), "vertex-ai embedding:") {
		t.Errorf("error missing 'vertex-ai embedding:' prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid service-account JSON") {
		t.Errorf("error = %v, want substring 'invalid service-account JSON'", err)
	}
}

func TestFactory_UnsupportedAuthMethodWrapsGCPCredsError(t *testing.T) {
	_, err := goembedding.NewProvider("vertex-ai", goembedding.ProviderConfig{
		"project_id":  "test-project",
		"location":    "us-central1",
		"model":       "text-embedding-005",
		"auth_method": "totally-bogus",
	})
	if err == nil {
		t.Fatal("expected error for unsupported auth method")
	}
	if !strings.Contains(err.Error(), "vertex-ai embedding:") {
		t.Errorf("error missing 'vertex-ai embedding:' prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error missing gcpcreds message: %v", err)
	}
}
