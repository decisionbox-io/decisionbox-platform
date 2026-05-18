package bedrock

import (
	"context"
	"strings"
	"testing"

	goembedding "github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
)

// Factory-level coverage: AuthMethods registration + routing into
// libs/awscreds via the public goembedding.NewProvider path. Auth-method
// behaviour itself is covered exhaustively in libs/awscreds tests; this
// suite only asserts the bedrock embedding factory wires through and
// surfaces the awscreds error with the bedrock embedding: prefix.

func TestAuthMethods_Registered(t *testing.T) {
	meta, _ := goembedding.GetProviderMeta("bedrock")
	if len(meta.AuthMethods) != 3 {
		t.Fatalf("expected 3 auth methods, got %d", len(meta.AuthMethods))
	}
	want := map[string]bool{"iam_role": false, "access_keys": false, "assume_role": false}
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

func TestAuthMethods_AccessKeysDeclaresCredentialField(t *testing.T) {
	meta, _ := goembedding.GetProviderMeta("bedrock")
	for _, m := range meta.AuthMethods {
		if m.ID != "access_keys" {
			continue
		}
		if len(m.Fields) != 1 {
			t.Fatalf("access_keys should have 1 field, got %d", len(m.Fields))
		}
		if m.Fields[0].Key != "credentials_json" {
			t.Errorf("access_keys field key = %q, want credentials_json", m.Fields[0].Key)
		}
		if m.Fields[0].Type != "credential" {
			t.Errorf("access_keys field type = %q, want credential", m.Fields[0].Type)
		}
		return
	}
	t.Fatal("access_keys auth method not found")
}

func TestAuthMethods_AssumeRoleDeclaresRoleARNAndExternalID(t *testing.T) {
	meta, _ := goembedding.GetProviderMeta("bedrock")
	for _, m := range meta.AuthMethods {
		if m.ID != "assume_role" {
			continue
		}
		keys := map[string]string{}
		for _, f := range m.Fields {
			keys[f.Key] = f.Type
		}
		if keys["role_arn"] != "string" {
			t.Errorf("assume_role missing role_arn (string) field, got %+v", keys)
		}
		if _, ok := keys["external_id"]; !ok {
			t.Errorf("assume_role missing external_id field, got %+v", keys)
		}
		return
	}
	t.Fatal("assume_role auth method not found")
}

func TestFactory_UnsupportedAuthMethodWrapsAWSCredsError(t *testing.T) {
	_, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":      "us-east-1",
		"model":       "amazon.titan-embed-text-v2:0",
		"auth_method": "totally-bogus",
	})
	if err == nil {
		t.Fatal("expected error for unsupported auth method")
	}
	if !strings.Contains(err.Error(), "bedrock embedding:") {
		t.Errorf("error missing 'bedrock embedding:' prefix: %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error missing awscreds message: %v", err)
	}
}

func TestFactory_AssumeRoleMissingARN(t *testing.T) {
	_, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":      "us-east-1",
		"model":       "amazon.titan-embed-text-v2:0",
		"auth_method": "assume_role",
	})
	if err == nil {
		t.Fatal("expected error for missing role_arn")
	}
	if !strings.Contains(err.Error(), "role_arn is required") {
		t.Errorf("error = %v, want substring 'role_arn is required'", err)
	}
}

func TestFactory_AccessKeysInvalidFormat(t *testing.T) {
	_, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":           "us-east-1",
		"model":            "amazon.titan-embed-text-v2:0",
		"auth_method":      "access_keys",
		"credentials_json": "no-colon-here",
	})
	if err == nil {
		t.Fatal("expected error for malformed access keys")
	}
	if !strings.Contains(err.Error(), "invalid access key format") {
		t.Errorf("error = %v, want substring 'invalid access key format'", err)
	}
}

func TestFactory_AccessKeysValid(t *testing.T) {
	p, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":           "us-east-1",
		"model":            "amazon.titan-embed-text-v2:0",
		"auth_method":      "access_keys",
		"credentials_json": "AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", //nolint:gosec // example placeholder
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.Dimensions() != 1024 {
		t.Errorf("Dimensions() = %d, want 1024", p.Dimensions())
	}
}

func TestFactory_IAMRoleHappyPath(t *testing.T) {
	p, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":      "us-east-1",
		"model":       "amazon.titan-embed-text-v2:0",
		"auth_method": "iam_role",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
}

func TestFactory_DefaultModelWhenOmitted(t *testing.T) {
	p, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":      "us-east-1",
		"auth_method": "iam_role",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
	// Default model is titan-embed-text-v2:0 (1024 dims).
	if p.Dimensions() != 1024 {
		t.Errorf("Dimensions() = %d, want 1024 (default model)", p.Dimensions())
	}
}

func TestFactory_DefaultRegionWhenOmitted(t *testing.T) {
	p, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"model":       "amazon.titan-embed-text-v2:0",
		"auth_method": "iam_role",
	})
	if err != nil {
		t.Fatalf("factory error: %v", err)
	}
	if p == nil {
		t.Fatal("nil provider")
	}
}

func TestFactory_UnsupportedModel(t *testing.T) {
	_, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":      "us-east-1",
		"model":       "made-up-model-name",
		"auth_method": "iam_role",
	})
	if err == nil {
		t.Fatal("expected error for unsupported model")
	}
	if !strings.Contains(err.Error(), "unsupported model") {
		t.Errorf("error = %v, want substring 'unsupported model'", err)
	}
}

// TestFactory_StashesAwsCfg_AccessKeys is the regression for the same
// PR #222 gap the LLM Bedrock provider had — the factory built an
// awsCfg from access_keys but didn't store it on the provider, so
// ListModels later called LoadDefaultConfig and threw the dashboard-
// supplied keys away. The fix stashes the factory's awsCfg on the
// provider so ListModels reuses it.
func TestFactory_StashesAwsCfg_AccessKeys(t *testing.T) {
	prov, err := goembedding.NewProvider("bedrock", goembedding.ProviderConfig{
		"region":           "us-east-1",
		"model":            "amazon.titan-embed-text-v2:0",
		"auth_method":      "access_keys",
		"credentials_json": "AKIA-fixture:secret-fixture",
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	bp, ok := prov.(*provider)
	if !ok {
		t.Fatalf("provider type = %T, want *provider", prov)
	}
	if bp.awsCfg.Credentials == nil {
		t.Fatal("awsCfg.Credentials is nil — factory dropped the credential provider")
	}
	creds, err := bp.awsCfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("retrieve from awsCfg: %v", err)
	}
	if creds.AccessKeyID != "AKIA-fixture" {
		t.Errorf("AccessKeyID = %q, want AKIA-fixture (factory did not stash the static credentials provider)", creds.AccessKeyID)
	}
	if creds.SecretAccessKey != "secret-fixture" {
		t.Errorf("SecretAccessKey = %q, want secret-fixture", creds.SecretAccessKey)
	}
}
