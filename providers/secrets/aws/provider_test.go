package aws

import (
	"fmt"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

func TestAWSProvider_ImplementsInterface(t *testing.T) {
	var _ secrets.Provider = (*AWSProvider)(nil)
}

func TestSecretName(t *testing.T) {
	p := &AWSProvider{namespace: "decisionbox"}

	name := p.secretName("proj-123", "llm-api-key")
	if name != "decisionbox/proj-123/llm-api-key" {
		t.Errorf("name = %q", name)
	}
}

func TestSecretName_CustomNamespace(t *testing.T) {
	p := &AWSProvider{namespace: "myapp-prod"}

	name := p.secretName("proj-456", "warehouse-creds")
	if name != "myapp-prod/proj-456/warehouse-creds" {
		t.Errorf("name = %q", name)
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"ResourceNotFoundException: secret not found", true},
		{"Secrets Manager can't find the specified secret", true},
		{"AccessDeniedException: not allowed", false},
		{"", false},
	}
	for _, tt := range tests {
		err := fmt.Errorf("%s", tt.msg)
		if got := isNotFound(err, nil); got != tt.want {
			t.Errorf("isNotFound(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"ResourceExistsException: already exists", true},
		{"A resource with the ID you requested already exists", true},
		{"ResourceNotFoundException: not found", false},
	}
	for _, tt := range tests {
		err := fmt.Errorf("%s", tt.msg)
		if got := isAlreadyExists(err, nil); got != tt.want {
			t.Errorf("isAlreadyExists(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}
