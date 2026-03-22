package secrets

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMaskValue(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sk-ant-api03-abc123def456", "sk-ant***f456"},
		{"short", "***"},
		{"exactly10!", "***"},
		{"12345678901", "123456***8901"},
		{"", "***"},
	}
	for _, tt := range tests {
		got := MaskValue(tt.input)
		if got != tt.want {
			t.Errorf("MaskValue(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg := LoadConfig()
	if cfg.Provider != "mongodb" {
		t.Errorf("default provider = %q, want mongodb", cfg.Provider)
	}
	if cfg.Namespace != "decisionbox" {
		t.Errorf("default namespace = %q, want decisionbox", cfg.Namespace)
	}
}

func TestErrNotFound(t *testing.T) {
	if ErrNotFound == nil {
		t.Error("ErrNotFound should not be nil")
	}
	if ErrNotFound.Error() != "secret not found" {
		t.Errorf("ErrNotFound = %q", ErrNotFound.Error())
	}
}

func TestRegisterAndList(t *testing.T) {
	// Register a test provider
	Register("test-secrets", func(cfg Config) (Provider, error) {
		return nil, nil
	}, ProviderMeta{Name: "Test Provider", Description: "for testing"})

	providers := RegisteredProviders()
	found := false
	for _, p := range providers {
		if p == "test-secrets" {
			found = true
		}
	}
	if !found {
		t.Error("test-secrets not found in registered providers")
	}

	metas := RegisteredProvidersMeta()
	found = false
	for _, m := range metas {
		if m.ID == "test-secrets" {
			found = true
			if m.Name != "Test Provider" {
				t.Errorf("name = %q", m.Name)
			}
		}
	}
	if !found {
		t.Error("test-secrets meta not found")
	}
}

func TestNewProvider_Unknown(t *testing.T) {
	_, err := NewProvider(Config{Provider: "nonexistent"})
	if err == nil {
		t.Error("expected error for unknown provider")
	}
}

// mockProvider implements Provider for testing.
type mockProvider struct{}

func (m *mockProvider) Get(_ context.Context, _, _ string) (string, error) { return "", nil }
func (m *mockProvider) Set(_ context.Context, _, _, _ string) error        { return nil }
func (m *mockProvider) List(_ context.Context, _ string) ([]SecretEntry, error) {
	return nil, nil
}

func TestNewProvider_Success(t *testing.T) {
	const name = "test-success-provider"
	Register(name, func(cfg Config) (Provider, error) {
		return &mockProvider{}, nil
	}, ProviderMeta{Name: "Test Success", Description: "for testing"})

	provider, err := NewProvider(Config{Provider: name})
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	if provider == nil {
		t.Error("NewProvider() returned nil")
	}
}

func TestNewProvider_DefaultNamespace(t *testing.T) {
	const name = "test-ns-provider"
	var receivedCfg Config
	Register(name, func(cfg Config) (Provider, error) {
		receivedCfg = cfg
		return &mockProvider{}, nil
	}, ProviderMeta{Name: "Test NS", Description: "for testing namespace"})

	_, err := NewProvider(Config{Provider: name, Namespace: ""})
	if err != nil {
		t.Fatalf("NewProvider() error: %v", err)
	}
	if receivedCfg.Namespace != "decisionbox" {
		t.Errorf("default namespace = %q, want %q", receivedCfg.Namespace, "decisionbox")
	}
}

func TestLoadConfig_CustomEnvVars(t *testing.T) {
	os.Setenv("SECRET_PROVIDER", "gcp")
	os.Setenv("SECRET_NAMESPACE", "custom-ns")
	os.Setenv("SECRET_ENCRYPTION_KEY", "test-key-123")
	os.Setenv("SECRET_GCP_PROJECT_ID", "my-gcp-project")
	os.Setenv("SECRET_AWS_REGION", "eu-west-1")
	defer func() {
		os.Unsetenv("SECRET_PROVIDER")
		os.Unsetenv("SECRET_NAMESPACE")
		os.Unsetenv("SECRET_ENCRYPTION_KEY")
		os.Unsetenv("SECRET_GCP_PROJECT_ID")
		os.Unsetenv("SECRET_AWS_REGION")
	}()

	cfg := LoadConfig()

	if cfg.Provider != "gcp" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "gcp")
	}
	if cfg.Namespace != "custom-ns" {
		t.Errorf("Namespace = %q, want %q", cfg.Namespace, "custom-ns")
	}
	if cfg.EncryptionKey != "test-key-123" {
		t.Errorf("EncryptionKey = %q, want %q", cfg.EncryptionKey, "test-key-123")
	}
	if cfg.GCPProjectID != "my-gcp-project" {
		t.Errorf("GCPProjectID = %q, want %q", cfg.GCPProjectID, "my-gcp-project")
	}
	if cfg.AWSRegion != "eu-west-1" {
		t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, "eu-west-1")
	}
}

func TestLoadConfig_AWSRegionDefault(t *testing.T) {
	os.Unsetenv("SECRET_AWS_REGION")
	cfg := LoadConfig()
	if cfg.AWSRegion != "us-east-1" {
		t.Errorf("default AWSRegion = %q, want %q", cfg.AWSRegion, "us-east-1")
	}
}

func TestMaskValue_BoundaryValues(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"exactly 11 chars", "12345678901", "123456***8901"},
		{"10 chars boundary", "1234567890", "***"},
		{"long value", "sk-ant-api03-very-long-key-value", "sk-ant***alue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaskValue(tt.input)
			if got != tt.want {
				t.Errorf("MaskValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSecretEntry_Fields(t *testing.T) {
	now := time.Now()
	entry := SecretEntry{
		Key:       "LLM_API_KEY",
		Masked:    "sk-ant***DwAA",
		UpdatedAt: now,
		Warning:   "",
	}
	if entry.Key != "LLM_API_KEY" {
		t.Errorf("Key = %q", entry.Key)
	}
	if entry.Masked != "sk-ant***DwAA" {
		t.Errorf("Masked = %q", entry.Masked)
	}
	if !entry.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt mismatch")
	}
}
