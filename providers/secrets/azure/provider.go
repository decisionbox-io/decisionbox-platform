// Package azure provides a secrets.Provider backed by Azure Key Vault.
// Secrets are namespaced to avoid conflicts with other secrets in the vault.
//
// Configuration:
//
//	SECRET_PROVIDER=azure
//	SECRET_AZURE_VAULT_URL=https://my-vault.vault.azure.net/
//	SECRET_NAMESPACE=decisionbox  (default)
//
// Authentication: Azure DefaultAzureCredential (env vars, managed identity, Azure CLI).
// Secret naming: {namespace}-{projectID}-{key} (e.g., decisionbox-proj123-llm-api-key)
package azure

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

// validSecretName matches Azure Key Vault's naming constraint: alphanumerics and hyphens, 1-127 chars.
// Reference: https://learn.microsoft.com/en-us/azure/key-vault/general/about-keys-secrets-certificates
var validSecretName = regexp.MustCompile(`^[a-zA-Z0-9-]{1,127}$`)

func init() {
	secrets.Register("azure", func(cfg secrets.Config) (secrets.Provider, error) {
		if cfg.AzureVaultURL == "" {
			return nil, fmt.Errorf("azure secret provider requires SECRET_AZURE_VAULT_URL")
		}
		return NewAzureProvider(cfg.AzureVaultURL, cfg.Namespace)
	}, secrets.ProviderMeta{
		Name:        "Azure Key Vault",
		Description: "Production secrets via Azure Key Vault with DefaultAzureCredential",
	})
}

// AzureProvider implements secrets.Provider using Azure Key Vault.
type AzureProvider struct {
	client    kvClient
	namespace string
}

// NewAzureProvider creates an Azure Key Vault provider.
func NewAzureProvider(vaultURL, namespace string) (*AzureProvider, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("azure secrets: failed to create credentials: %w", err)
	}
	client, err := azsecrets.NewClient(vaultURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("azure secrets: failed to create client: %w", err)
	}
	if namespace == "" {
		namespace = "decisionbox"
	}
	return &AzureProvider{client: client, namespace: namespace}, nil
}

// NewAzureProviderWithClient creates a provider with a custom client (for testing).
func NewAzureProviderWithClient(client kvClient, namespace string) *AzureProvider {
	if namespace == "" {
		namespace = "decisionbox"
	}
	return &AzureProvider{client: client, namespace: namespace}
}

func (p *AzureProvider) secretName(projectID, key string) string {
	return fmt.Sprintf("%s-%s-%s", p.namespace, projectID, key)
}

// validateSecretName checks that the composed secret name meets Azure Key Vault's
// naming constraints: alphanumerics and hyphens only, 1-127 characters.
// Underscores, dots, spaces, and other characters are not allowed.
func validateSecretName(name string) error {
	if !validSecretName.MatchString(name) {
		return fmt.Errorf("azure secrets: invalid secret name %q — must be 1-127 alphanumeric or hyphen characters (no underscores, dots, or spaces)", name)
	}
	return nil
}

func (p *AzureProvider) Get(ctx context.Context, projectID, key string) (string, error) {
	name := p.secretName(projectID, key)
	if err := validateSecretName(name); err != nil {
		return "", err
	}

	resp, err := p.client.GetSecret(ctx, name, "", nil)
	if err != nil {
		if isNotFound(err) {
			return "", secrets.ErrNotFound
		}
		return "", fmt.Errorf("azure secrets get: %w", err)
	}

	if resp.Value == nil {
		return "", secrets.ErrNotFound
	}
	return *resp.Value, nil
}

func (p *AzureProvider) Set(ctx context.Context, projectID, key, value string) error {
	name := p.secretName(projectID, key)
	if err := validateSecretName(name); err != nil {
		return err
	}

	tags := map[string]*string{
		"managed-by": strPtr("decisionbox"),
		"namespace":  strPtr(p.namespace),
		"project-id": strPtr(projectID),
	}

	_, err := p.client.SetSecret(ctx, name, azsecrets.SetSecretParameters{
		Value: &value,
		Tags:  tags,
	}, nil)
	if err != nil {
		// Azure Key Vault has mandatory soft-delete. If a secret was previously
		// deleted externally (via portal/CLI), its name is occupied in the
		// soft-deleted state and SetSecret returns 409 Conflict. The secret must
		// be purged via the Azure portal or CLI before it can be recreated.
		if isConflict(err) {
			return fmt.Errorf("azure secrets set: secret %q is in a soft-deleted state — purge it via Azure portal or CLI (az keyvault secret purge) before recreating", name)
		}
		return fmt.Errorf("azure secrets set: %w", err)
	}

	return nil
}

func (p *AzureProvider) List(ctx context.Context, projectID string) ([]secrets.SecretEntry, error) {
	prefix := fmt.Sprintf("%s-%s-", p.namespace, projectID)
	entries := make([]secrets.SecretEntry, 0)

	// Azure Key Vault does not support server-side prefix filtering on secret names.
	// We list all secrets in the vault and filter client-side by namespace+projectID prefix.
	// For each matching secret, we call GetSecret to obtain the value for masking (N+1 pattern).
	// This is acceptable for typical DecisionBox usage (few secrets per project) but could be
	// slow for vaults with thousands of secrets across many projects.
	pager := p.client.NewListSecretPropertiesPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("azure secrets list: %w", err)
		}

		for _, prop := range page.Value {
			if prop == nil || prop.ID == nil {
				continue
			}

			// Extract secret name from the full ID URL
			// Format: https://{vault}.vault.azure.net/secrets/{name}/{version}
			name := extractSecretName(prop.ID)
			if !strings.HasPrefix(name, prefix) {
				continue
			}

			// Include all secrets regardless of enabled/disabled state.
			// Disabled secrets are still relevant for listing (admin visibility).

			key := strings.TrimPrefix(name, prefix)

			// Get masked value
			masked := "***"
			warning := ""
			val, err := p.client.GetSecret(ctx, name, "", nil)
			if err == nil && val.Value != nil {
				masked = secrets.MaskValue(*val.Value)
			} else if err != nil {
				warning = fmt.Sprintf("Failed to read value: %s", err.Error())
			}

			var updatedAt time.Time
			if prop.Attributes != nil {
				if prop.Attributes.Updated != nil {
					updatedAt = *prop.Attributes.Updated
				} else if prop.Attributes.Created != nil {
					updatedAt = *prop.Attributes.Created
				}
			}

			entries = append(entries, secrets.SecretEntry{
				Key:       key,
				Masked:    masked,
				UpdatedAt: updatedAt,
				Warning:   warning,
			})
		}
	}

	return entries, nil
}

// extractSecretName extracts the secret name from a Key Vault secret ID URL.
// ID format: https://{vault}.vault.azure.net/secrets/{name}[/{version}]
func extractSecretName(id *azsecrets.ID) string {
	if id == nil {
		return ""
	}
	s := string(*id)
	// Find "/secrets/" and extract the name after it
	const marker = "/secrets/"
	idx := strings.LastIndex(s, marker)
	if idx == -1 {
		return ""
	}
	rest := s[idx+len(marker):]
	// Remove version suffix if present
	if slashIdx := strings.Index(rest, "/"); slashIdx != -1 {
		rest = rest[:slashIdx]
	}
	return rest
}

// isNotFound checks if an Azure error indicates a 404 Not Found.
func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == http.StatusNotFound
	}
	return false
}

// isConflict checks if an Azure error indicates a 409 Conflict.
// This occurs when setting a secret whose name is occupied by a soft-deleted secret.
func isConflict(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == http.StatusConflict
	}
	return false
}

func strPtr(s string) *string {
	return &s
}
