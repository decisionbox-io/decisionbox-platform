package awscreds

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestLoad_AccessKeys_Valid(t *testing.T) {
	cfg, err := Load(context.Background(), Config{
		Method:      MethodAccessKeys,
		Region:      "us-east-1",
		Credentials: "AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", //nolint:gosec // example placeholder
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", cfg.Region)
	}
	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve credentials: %v", err)
	}
	if creds.AccessKeyID != "AKIAEXAMPLE" {
		t.Errorf("AccessKeyID = %q, want AKIAEXAMPLE", creds.AccessKeyID)
	}
	if creds.SecretAccessKey != "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" { //nolint:gosec // example placeholder
		t.Errorf("SecretAccessKey wrong")
	}
}

func TestLoad_AccessKeys_MissingColon(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Method:      MethodAccessKeys,
		Region:      "us-east-1",
		Credentials: "AKIAEXAMPLEnocolon",
	})
	if err == nil {
		t.Fatal("expected error for missing colon")
	}
	if !strings.Contains(err.Error(), "invalid access key format") {
		t.Errorf("error = %v, want substring 'invalid access key format'", err)
	}
}

func TestLoad_AccessKeys_EmptyKeyHalf(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Method:      MethodAccessKeys,
		Region:      "us-east-1",
		Credentials: ":secret",
	})
	if err == nil {
		t.Fatal("expected error for empty access key id")
	}
	if !strings.Contains(err.Error(), "invalid access key format") {
		t.Errorf("error = %v, want substring 'invalid access key format'", err)
	}
}

func TestLoad_AccessKeys_EmptySecretHalf(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Method:      MethodAccessKeys,
		Region:      "us-east-1",
		Credentials: "AKIA:",
	})
	if err == nil {
		t.Fatal("expected error for empty secret access key")
	}
	if !strings.Contains(err.Error(), "invalid access key format") {
		t.Errorf("error = %v, want substring 'invalid access key format'", err)
	}
}

func TestLoad_AccessKeys_EmptyCredentialsFallsThroughToDefault(t *testing.T) {
	// Empty Credentials with access_keys method must fall through to
	// LoadDefaultConfig (so env vars / IAM role can still resolve).
	// LoadDefaultConfig itself never errors when no credentials are
	// available — it returns a config whose Credentials.Retrieve fails
	// lazily. Verify the call returns without our format-error.
	_, err := Load(context.Background(), Config{
		Method:      MethodAccessKeys,
		Region:      "us-east-1",
		Credentials: "",
	})
	if err != nil && strings.Contains(err.Error(), "invalid access key format") {
		t.Errorf("empty Credentials should not raise format error: %v", err)
	}
}

func TestLoad_AssumeRole_Valid(t *testing.T) {
	cfg, err := Load(context.Background(), Config{
		Method:      MethodAssumeRole,
		Region:      "us-east-1",
		RoleARN:     "arn:aws:iam::123456789012:role/DecisionBoxTest",
		ExternalID:  "external-xyz",
		SessionName: "test-session",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("Region = %q, want us-east-1", cfg.Region)
	}
	if cfg.Credentials == nil {
		t.Fatal("Credentials not set")
	}
	// We can't trigger STS Retrieve without a network call; verifying the
	// provider is wired in via the CredentialsCache wrapper is enough.
	if _, ok := cfg.Credentials.(*aws.CredentialsCache); !ok {
		t.Errorf("Credentials type = %T, want *aws.CredentialsCache", cfg.Credentials)
	}
}

func TestLoad_AssumeRole_MissingRoleARN(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Method: MethodAssumeRole,
		Region: "us-east-1",
	})
	if err == nil {
		t.Fatal("expected error for missing role_arn")
	}
	if !strings.Contains(err.Error(), "role_arn is required") {
		t.Errorf("error = %v, want substring 'role_arn is required'", err)
	}
}

func TestLoad_AssumeRole_NoExternalID(t *testing.T) {
	cfg, err := Load(context.Background(), Config{
		Method:  MethodAssumeRole,
		Region:  "us-east-1",
		RoleARN: "arn:aws:iam::123456789012:role/DecisionBoxTest",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Credentials == nil {
		t.Fatal("Credentials not set")
	}
}

func TestLoad_AssumeRole_DefaultSessionName(t *testing.T) {
	cfg, err := Load(context.Background(), Config{
		Method:  MethodAssumeRole,
		Region:  "us-east-1",
		RoleARN: "arn:aws:iam::123456789012:role/DecisionBoxTest",
	})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	// Default session name "decisionbox-agent" is applied internally —
	// we cannot inspect it without retrieving credentials. Surface check
	// only: no panic, no error.
	if cfg.Credentials == nil {
		t.Fatal("Credentials not set")
	}
}

func TestLoad_IAMRole(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Method: MethodIAMRole,
		Region: "us-east-1",
	})
	if err != nil {
		t.Errorf("Load returned error: %v", err)
	}
}

func TestLoad_EmptyMethodDefaultsToIAMRole(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Region: "us-east-1",
	})
	if err != nil {
		t.Errorf("Load returned error: %v", err)
	}
}

func TestLoad_UnsupportedMethod(t *testing.T) {
	_, err := Load(context.Background(), Config{
		Method: "bogus",
		Region: "us-east-1",
	})
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if !strings.Contains(err.Error(), "unsupported auth method") {
		t.Errorf("error = %v, want substring 'unsupported auth method'", err)
	}
}

func TestLoad_EmptyRegion(t *testing.T) {
	// Empty region is allowed — SDK can resolve from env (AWS_REGION).
	// Verify no error path is added for missing region.
	_, err := Load(context.Background(), Config{
		Method:      MethodAccessKeys,
		Credentials: "AKIAEXAMPLE:secretEXAMPLE", //nolint:gosec // example placeholder
	})
	if err != nil {
		t.Errorf("Load returned error: %v", err)
	}
}

func TestConstants(t *testing.T) {
	// Hard-coded check that the exported constants haven't drifted —
	// every consumer wires AuthMethod.Fields[].Key to these values.
	if MethodIAMRole != "iam_role" {
		t.Errorf("MethodIAMRole = %q, want iam_role", MethodIAMRole)
	}
	if MethodAccessKeys != "access_keys" {
		t.Errorf("MethodAccessKeys = %q, want access_keys", MethodAccessKeys)
	}
	if MethodAssumeRole != "assume_role" {
		t.Errorf("MethodAssumeRole = %q, want assume_role", MethodAssumeRole)
	}
	if FieldCredentials != "credentials_json" {
		t.Errorf("FieldCredentials = %q, want credentials_json", FieldCredentials)
	}
	if FieldRoleARN != "role_arn" {
		t.Errorf("FieldRoleARN = %q, want role_arn", FieldRoleARN)
	}
	if FieldExternalID != "external_id" {
		t.Errorf("FieldExternalID = %q, want external_id", FieldExternalID)
	}
}
