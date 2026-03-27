// Package secrets provides a pluggable interface for managing sensitive
// credentials (LLM API keys, warehouse credentials, etc.).
//
// Secrets are scoped per-project and namespaced to avoid conflicts with
// other secrets in the same cloud account.
//
// Providers:
//   - mongodb: AES-256-GCM encrypted MongoDB collection (default, local dev)
//   - gcp: Google Cloud Secret Manager
//   - aws: AWS Secrets Manager
//   - azure: Azure Key Vault
//
// No delete via API — manual deletion only (cloud console, CLI, or direct DB).
package secrets

import (
	"context"
	"time"
)

// Provider manages per-project secrets.
// All operations are scoped to a namespace + project ID.
// No Delete method — secrets are removed manually via cloud console or CLI.
type Provider interface {
	// Get retrieves a secret value for a project.
	// Returns ErrNotFound if the secret doesn't exist.
	Get(ctx context.Context, projectID, key string) (string, error)

	// Set creates or updates a secret value for a project.
	Set(ctx context.Context, projectID, key, value string) error

	// List returns all secret keys for a project (masked values, never full values).
	List(ctx context.Context, projectID string) ([]SecretEntry, error)
}

// SecretEntry represents a secret in a list response.
// Value is always masked — never returned in full via List.
type SecretEntry struct {
	Key       string    `json:"key"`
	Masked    string    `json:"masked"`              // e.g., "sk-ant-***...DwAA"
	UpdatedAt time.Time `json:"updated_at"`
	Warning   string    `json:"warning,omitempty"`    // e.g., permission denied
}

// MaskValue masks a secret value for display.
// Shows first 6 and last 4 characters with *** in between.
func MaskValue(value string) string {
	if len(value) <= 10 {
		return "***"
	}
	return value[:6] + "***" + value[len(value)-4:]
}

// Config holds secret provider configuration.
type Config struct {
	Provider      string // mongodb | gcp | aws | azure
	Namespace     string // prefix for all secrets (default: "decisionbox")
	EncryptionKey string // for mongodb provider (base64-encoded 32-byte key)
	GCPProjectID  string // for gcp provider
	AWSRegion     string // for aws provider
	AzureVaultURL string // for azure provider (e.g., https://my-vault.vault.azure.net/)
}
