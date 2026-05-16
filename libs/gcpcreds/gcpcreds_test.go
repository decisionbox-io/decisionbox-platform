package gcpcreds

import (
	"context"
	"strings"
	"testing"
)

// validSAJSON is a syntactically valid (but inert) service-account key.
// Public key only — never authenticates against any GCP project. Used to
// drive google.CredentialsFromJSON without touching the network.
const validSAJSON = `{
  "type": "service_account",
  "project_id": "decisionbox-test",
  "private_key_id": "deadbeef",
  "private_key": "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQDExampleKeyDataForTestingOnly\n-----END PRIVATE KEY-----\n",
  "client_email": "test@decisionbox-test.iam.gserviceaccount.com",
  "client_id": "1234567890",
  "auth_uri": "https://accounts.google.com/o/oauth2/auth",
  "token_uri": "https://oauth2.googleapis.com/token",
  "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
  "client_x509_cert_url": "https://www.googleapis.com/robot/v1/metadata/x509/test%40decisionbox-test.iam.gserviceaccount.com"
}`

func TestTokenSource_SAKey_ValidJSON(t *testing.T) {
	src, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: validSAJSON,
	})
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if src == nil {
		t.Fatal("TokenSource returned nil source")
	}
}

func TestTokenSource_SAKey_MalformedJSON(t *testing.T) {
	_, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: "{not-json",
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid service-account JSON") {
		t.Errorf("error = %v, want substring 'invalid service-account JSON'", err)
	}
}

func TestTokenSource_SAKey_EmptyJSONFallsThroughToADC(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/path/that/should/not/exist.json")
	_, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: "",
	})
	if err == nil {
		t.Fatal("expected error from ADC when env points to missing file")
	}
	if !strings.Contains(err.Error(), "failed to find default GCP credentials") {
		t.Errorf("error = %v, want ADC fallback error", err)
	}
}

func TestTokenSource_ADC(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/path/that/should/not/exist.json")
	_, err := TokenSource(context.Background(), Config{
		Method: MethodADC,
	})
	if err == nil {
		t.Fatal("expected error from ADC when env points to missing file")
	}
	if !strings.Contains(err.Error(), "failed to find default GCP credentials") {
		t.Errorf("error = %v, want ADC fallback error", err)
	}
}

func TestTokenSource_EmptyMethodDefaultsToADC(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent/path/that/should/not/exist.json")
	_, err := TokenSource(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected error from ADC when env points to missing file")
	}
	if !strings.Contains(err.Error(), "failed to find default GCP credentials") {
		t.Errorf("error = %v, want ADC fallback error", err)
	}
}

func TestTokenSource_UnsupportedMethod(t *testing.T) {
	_, err := TokenSource(context.Background(), Config{
		Method: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error = %v, want substring 'unsupported auth method'", err)
	}
}

func TestTokenSource_CustomScopes(t *testing.T) {
	// CredentialsFromJSON accepts arbitrary scopes; verify our custom
	// Scopes slice is honoured by checking the no-error path.
	src, err := TokenSource(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: validSAJSON,
		Scopes:          []string{"https://www.googleapis.com/auth/bigquery"},
	})
	if err != nil {
		t.Fatalf("TokenSource: %v", err)
	}
	if src == nil {
		t.Fatal("TokenSource returned nil source")
	}
}

func TestClientOptions_SAKey_ValidJSON(t *testing.T) {
	opts, err := ClientOptions(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: validSAJSON,
	})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if len(opts) != 1 {
		t.Errorf("ClientOptions len = %d, want 1", len(opts))
	}
}

func TestClientOptions_SAKey_MalformedJSON(t *testing.T) {
	_, err := ClientOptions(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: "{not-json",
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid service-account JSON") {
		t.Errorf("error = %v, want substring 'invalid service-account JSON'", err)
	}
}

func TestClientOptions_SAKey_EmptyJSON(t *testing.T) {
	// Empty project JSON → nil slice (SDK uses its own ADC).
	opts, err := ClientOptions(context.Background(), Config{
		Method:          MethodSAKey,
		CredentialsJSON: "",
	})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if opts != nil {
		t.Errorf("ClientOptions = %v, want nil", opts)
	}
}

func TestClientOptions_ADC(t *testing.T) {
	opts, err := ClientOptions(context.Background(), Config{
		Method: MethodADC,
	})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if opts != nil {
		t.Errorf("ClientOptions = %v, want nil", opts)
	}
}

func TestClientOptions_EmptyMethodDefaultsToADC(t *testing.T) {
	opts, err := ClientOptions(context.Background(), Config{})
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	if opts != nil {
		t.Errorf("ClientOptions = %v, want nil", opts)
	}
}

func TestClientOptions_UnsupportedMethod(t *testing.T) {
	_, err := ClientOptions(context.Background(), Config{
		Method: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error = %v, want substring 'unsupported auth method'", err)
	}
}

func TestConstants(t *testing.T) {
	if MethodADC != "adc" {
		t.Errorf("MethodADC = %q, want adc", MethodADC)
	}
	if MethodSAKey != "sa_key" {
		t.Errorf("MethodSAKey = %q, want sa_key", MethodSAKey)
	}
	if FieldCredentials != "credentials_json" {
		t.Errorf("FieldCredentials = %q, want credentials_json", FieldCredentials)
	}
	if DefaultScope != "https://www.googleapis.com/auth/cloud-platform" {
		t.Errorf("DefaultScope = %q, want cloud-platform URL", DefaultScope)
	}
}
