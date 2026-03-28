# Configuring Secret Providers

> **Version**: 0.1.0

Secrets store per-project credentials like LLM API keys and warehouse service account keys. DecisionBox supports four secret backends.

## Provider Comparison

| Provider | Best For | Encryption | Dependencies |
|----------|----------|------------|-------------|
| **MongoDB** (default) | Local dev, single-server | AES-256-GCM | None (uses existing MongoDB) |
| **GCP Secret Manager** | GCP production | Google-managed | GCP project + IAM |
| **AWS Secrets Manager** | AWS production | AWS-managed | AWS account + IAM |
| **Azure Key Vault** | Azure production | Azure-managed | Azure subscription + RBAC |

## MongoDB Provider (Default)

Uses an encrypted MongoDB collection. No external service needed — it reuses your existing MongoDB.

### Setup

```bash
# Generate encryption key
export SECRET_ENCRYPTION_KEY=$(openssl rand -base64 32)

# Set env vars (docker-compose or shell)
SECRET_PROVIDER=mongodb
SECRET_NAMESPACE=decisionbox
SECRET_ENCRYPTION_KEY=your-base64-key
```

### How It Works

- Secrets stored in the `secrets` MongoDB collection
- Each secret encrypted with **AES-256-GCM** using the encryption key
- Random nonce per encryption (same plaintext → different ciphertext)
- Unique index on `(namespace, project_id, key)` prevents duplicates

### Without Encryption Key

If `SECRET_ENCRYPTION_KEY` is not set, secrets are stored in **plaintext** with a warning in the logs. This is acceptable for local development but not for production.

### Secret Format

MongoDB stores:
```json
{
  "namespace": "decisionbox",
  "project_id": "507f1f77bcf86cd799439011",
  "key": "llm-api-key",
  "value": "<encrypted-base64>",
  "nonce": "<base64-nonce>",
  "updated_at": "2026-03-14T10:00:00Z"
}
```

## GCP Secret Manager

Uses Google Cloud Secret Manager for production GCP deployments.

### Setup

```bash
SECRET_PROVIDER=gcp
SECRET_GCP_PROJECT_ID=my-gcp-project
SECRET_NAMESPACE=decisionbox
```

### Prerequisites

- GCP project with Secret Manager API enabled
- Service account or ADC with:
  - `secretmanager.secrets.create`
  - `secretmanager.secrets.list`
  - `secretmanager.versions.add`
  - `secretmanager.versions.access`

### How It Works

- Secrets stored as GCP Secret Manager secrets
- Naming: `{namespace}-{projectID}-{key}` (e.g., `decisionbox-507f1f-llm-api-key`)
- Labels: `managed-by=decisionbox`, `namespace=...`, `project-id=...`
- Listing filtered by labels (only shows DecisionBox-managed secrets)
- New values added as new secret versions

### Authentication

On GKE: Uses Workload Identity automatically.
Outside GCP: Uses Application Default Credentials (`gcloud auth application-default login`).

## AWS Secrets Manager

Uses AWS Secrets Manager for production AWS deployments.

### Setup

```bash
SECRET_PROVIDER=aws
SECRET_AWS_REGION=us-east-1
SECRET_NAMESPACE=decisionbox
```

### Prerequisites

- AWS account with Secrets Manager access
- IAM permissions:
  - `secretsmanager:CreateSecret`
  - `secretsmanager:GetSecretValue`
  - `secretsmanager:PutSecretValue`
  - `secretsmanager:ListSecrets`

### How It Works

- Secrets stored as AWS Secrets Manager secrets
- Naming: `{namespace}/{projectID}/{key}` (e.g., `decisionbox/507f1f/llm-api-key`)
- Tags: `managed-by=decisionbox`, `namespace=...`, `project-id=...`
- Listing filtered by name prefix and tags
- Updates use `PutSecretValue` (creates new version)

### Authentication

On EKS: Uses IAM role for service accounts (IRSA) automatically.
Outside AWS: Uses `~/.aws/credentials` or environment variables.

## Azure Key Vault

Uses Azure Key Vault for production Azure deployments.

### Setup

```bash
SECRET_PROVIDER=azure
SECRET_AZURE_VAULT_URL=https://my-vault.vault.azure.net/
SECRET_NAMESPACE=decisionbox
```

### Prerequisites

- Azure Key Vault with RBAC authorization enabled
- Managed identity or service principal with:
  - **Key Vault Secrets Officer** (for API — create, read, update, list, delete)
  - **Key Vault Secrets User** (for Agent — read-only: get, list)

### How It Works

- Secrets stored as Azure Key Vault secrets
- Naming: `{namespace}-{projectID}-{key}` (e.g., `decisionbox-507f1f-llm-api-key`)
- Tags: `managed-by=decisionbox`, `namespace=...`, `project-id=...`
- Listing filtered by tags (only shows DecisionBox-managed secrets)
- Updates create new secret versions automatically

### Authentication

On AKS: Uses Workload Identity (federated credentials) automatically.
Outside Azure: Uses Azure CLI credentials (`az login`) or service principal environment variables (`AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TENANT_ID`).

## Using Secrets

### Setting Secrets (Dashboard)

**During project creation:** If you select an LLM provider that requires an API key (Claude, OpenAI), the wizard asks for it in the AI step.
The key is saved as an encrypted secret immediately after the project is created.

**After project creation:** Go to project **Settings → AI Provider** tab to update the API key, or **Settings → Data Warehouse** tab to update warehouse credentials.

### Setting Secrets (API)

```bash
curl -X PUT http://localhost:8080/api/v1/projects/{id}/secrets/llm-api-key \
  -H "Content-Type: application/json" \
  -d '{"value": "sk-ant-api03-..."}'
```

### How the Agent Reads Secrets

1. Agent initializes the secret provider (same config as API)
2. Reads `llm-api-key` for the project → passes to LLM provider
3. Warehouse uses the agent's cloud credentials (ADC on GCP, IAM role on AWS, Workload Identity on Azure)

### No Delete Via API

Secrets cannot be deleted through the DecisionBox API. This is intentional — to prevent accidental deletion. To remove a secret:
- **MongoDB provider**: Delete from the `secrets` collection directly
- **GCP provider**: Delete via GCP Console or `gcloud secrets delete`
- **AWS provider**: Delete via AWS Console or `aws secretsmanager delete-secret`
- **Azure provider**: Delete via Azure Portal or `az keyvault secret delete`

## Namespace Isolation

The `SECRET_NAMESPACE` prevents conflicts when multiple DecisionBox instances share the same secret backend:

```
Instance 1: SECRET_NAMESPACE=decisionbox-prod
Instance 2: SECRET_NAMESPACE=decisionbox-staging
```

Each instance only sees and manages its own secrets.

## Next Steps

- [Configuration Reference](../reference/configuration.md) — All environment variables
- [Adding Secret Providers](adding-secret-providers.md) — Support a new secret backend
