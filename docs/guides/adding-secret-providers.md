# Adding Secret Providers

> **Version**: 0.1.0

Secret providers store per-project credentials (LLM API keys, warehouse service account keys). This guide shows how to add a new secret backend.

## Interface

```go
// libs/go-common/secrets/provider.go
type Provider interface {
    Get(ctx context.Context, projectID, key string) (string, error)
    Set(ctx context.Context, projectID, key, value string) error
    List(ctx context.Context, projectID string) ([]SecretEntry, error)
}
```

Three methods. No Delete — secrets are removed manually via cloud console or CLI.

| Method | Purpose |
|--------|---------|
| `Get` | Retrieve a secret value. Return `secrets.ErrNotFound` if it doesn't exist. |
| `Set` | Create or update a secret (upsert). |
| `List` | List all secret keys for a project. Return masked values only. |

**SecretEntry** (returned by List):

```go
type SecretEntry struct {
    Key       string    // Secret key (e.g., "llm-api-key")
    Masked    string    // Masked value (e.g., "sk-ant***DwAA")
    UpdatedAt time.Time // Last update
    Warning   string    // Optional warning (e.g., permission denied)
}
```

Use `secrets.MaskValue(value)` to mask values (shows first 6 + last 4 characters).

## Secret Naming

Secrets are scoped by namespace + project ID + key:

```
{namespace}/{projectID}/{key}
```

Example: `decisionbox/507f1f77bcf86cd799439011/llm-api-key`

The namespace (configurable via `SECRET_NAMESPACE`, default: `decisionbox`) prevents conflicts when multiple DecisionBox instances share the same secret backend.

## Implementation

```go
// providers/secrets/vault/provider.go
package vault

import (
    "context"
    "fmt"

    "github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
)

func init() {
    secrets.Register("vault", func(cfg secrets.Config) (secrets.Provider, error) {
        addr := cfg.Extra["vault_addr"]
        if addr == "" {
            return nil, fmt.Errorf("vault: VAULT_ADDR is required")
        }
        token := cfg.Extra["vault_token"]
        if token == "" {
            return nil, fmt.Errorf("vault: VAULT_TOKEN is required")
        }

        return &VaultProvider{
            addr:      addr,
            token:     token,
            namespace: cfg.Namespace,
        }, nil
    }, secrets.ProviderMeta{
        Name:        "HashiCorp Vault",
        Description: "Store secrets in HashiCorp Vault",
    })
}

type VaultProvider struct {
    addr      string
    token     string
    namespace string
}

func (p *VaultProvider) secretPath(projectID, key string) string {
    return fmt.Sprintf("%s/%s/%s", p.namespace, projectID, key)
}

func (p *VaultProvider) Get(ctx context.Context, projectID, key string) (string, error) {
    // Read from Vault at secretPath(projectID, key)
    // Return secrets.ErrNotFound if not found
    return "", secrets.ErrNotFound
}

func (p *VaultProvider) Set(ctx context.Context, projectID, key, value string) error {
    // Write to Vault at secretPath(projectID, key)
    return nil
}

func (p *VaultProvider) List(ctx context.Context, projectID string) ([]secrets.SecretEntry, error) {
    // List secrets under namespace/projectID/
    // For each: read value and mask it with secrets.MaskValue()
    entries := make([]secrets.SecretEntry, 0)
    return entries, nil
}
```

### Config Structure

The `secrets.Config` struct:

```go
type Config struct {
    Provider     string            // "vault" (from SECRET_PROVIDER env var)
    Namespace    string            // From SECRET_NAMESPACE env var
    EncryptionKey string           // From SECRET_ENCRYPTION_KEY env var (MongoDB only)
    GCPProjectID string            // From SECRET_GCP_PROJECT_ID env var
    AWSRegion    string            // From SECRET_AWS_REGION env var
    Extra        map[string]string // Additional provider-specific config
}
```

For custom env vars (like `VAULT_ADDR`), read them from `os.Getenv()` in your factory function or use `cfg.Extra`.

## Registration and Testing

1. Import in `services/agent/main.go` and `services/api/main.go`
2. Add `replace` directives in both go.mod files
3. Write tests:
   - Interface compliance (`var _ secrets.Provider = (*VaultProvider)(nil)`)
   - Secret naming format
   - Factory validation (missing required config)
   - Integration tests with the actual secret backend (skip without credentials)

## Checklist

- [ ] All 3 methods implemented (Get, Set, List)
- [ ] `secrets.ErrNotFound` returned when secret doesn't exist
- [ ] Values masked with `secrets.MaskValue()` in List
- [ ] Secrets scoped by namespace + projectID + key
- [ ] Warning field populated on permission errors
- [ ] Registered via `secrets.Register()` in `init()`
- [ ] Imported in agent + API
- [ ] Unit tests + integration tests

## Next Steps

- [Providers Concept](../concepts/providers.md) — Plugin architecture overview
- [Configuring Secrets](configuring-secrets.md) — How users configure secret providers
