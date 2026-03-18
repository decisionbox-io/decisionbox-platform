# Setup Wizard

> **Version**: 0.1.0

The setup wizard (`terraform/setup.sh`) is an interactive script that provisions cloud infrastructure and deploys DecisionBox in one flow. It handles Terraform configuration, cloud authentication, Kubernetes setup, and Helm deployment.

## Quick Start

```bash
cd terraform
./setup.sh
```

## Flags

| Flag | Description |
|------|-------------|
| `--help` | Show usage guide and exit |
| `--dry-run` | Generate config files (tfvars + Helm values) without applying |
| `--resume` | Resume from Helm deploy — skips Terraform, reloads config from existing `terraform.tfvars` |

## 9-Step Flow

### Step 1: Prerequisites

Verifies all required tools are installed with version info:
- `terraform` (1.5+)
- `gcloud` (for GCP)
- `kubectl`
- `helm` (3.7+)
- `openssl`

If any tool is missing, the wizard shows an install link and exits.

### Step 2: Cloud Provider

Select your cloud provider:
- **GCP** — Google Cloud Platform (available)
- **AWS** — Amazon Web Services (coming soon, visible but disabled)

### Step 3: Secrets Configuration

Configure the secret namespace prefix used to scope secrets. Format: `{namespace}-{projectID}-{key}`.

Choose between:
- **Cloud Secret Manager** (GCP Secret Manager) — recommended for production
- **MongoDB encrypted secrets** — uses AES-256 encryption with `SECRET_ENCRYPTION_KEY`

### Step 4: Cloud Provider Settings

For GCP:
- Project ID (validated against GCP naming rules)
- Region (default: `us-central1`)
- GKE cluster name (default: `decisionbox-prod`)
- Kubernetes namespace (default: `decisionbox`)
- Node pool: machine type, min/max nodes per zone (numeric validation, min <= max check)
- BigQuery IAM (optional)

### Step 5: Authentication

Choose how Terraform authenticates with GCP:

**User credentials (Application Default Credentials):**
- If ADC already exists and is a user credential, offers to reuse
- If ADC is missing or is a service account, prompts for interactive login
- Uses `--no-browser` mode — paste the auth code directly in the terminal

**Service account key file:**
- Provide a path to a JSON key file
- File existence validated before proceeding
- Sets `GOOGLE_APPLICATION_CREDENTIALS` for the session

After authentication, the wizard verifies 4 key GCP permissions:
- `container.clusters.list` (GKE)
- `storage.buckets.list` (Terraform state)
- `iam.serviceAccounts.list` (Workload Identity)
- `compute.networks.list` (VPC)

If permissions are missing, shows specific error messages with `gcloud` grant commands.

### Step 6: Terraform State

Configure the GCS bucket for Terraform remote state:
- Bucket name (default: `{project-id}-terraform-state`)
- State prefix / environment name (default: `prod`)
- Auto-creates the bucket with versioning if it doesn't exist

### Step 7: Review

Displays all collected configuration for review before proceeding. Type `back` to change any value.

### Step 8: Generate Config Files

Generates two files:

**`terraform/gcp/prod/terraform.tfvars`** — Terraform variables:
```hcl
project_id   = "my-project"
region       = "us-central1"
cluster_name = "decisionbox-prod"
machine_type = "e2-standard-2"
min_node_count = 1
max_node_count = 2
k8s_namespace = "decisionbox"
enable_gcp_secrets  = true
secret_namespace    = "decisionbox"
enable_bigquery_iam = false
```

**`helm-charts/decisionbox-api/values-secrets.yaml`** — Helm values:

When cloud secrets are enabled:
```yaml
serviceAccountAnnotations:
  iam.gke.io/gcp-service-account: "cluster-api@project.iam.gserviceaccount.com"
env:
  SECRET_PROVIDER: "gcp"
  SECRET_GCP_PROJECT_ID: "my-project"
```

When using MongoDB secrets:
```yaml
env:
  SECRET_PROVIDER: "mongodb"
```

### Step 9: Terraform & Deploy

1. **Terraform init** — initializes with remote backend (spinner + elapsed time)
2. **Terraform plan** — shows changes, prompts for approval
3. **Terraform apply** — provisions infrastructure (shows elapsed time)
4. **kubectl credentials** — configures cluster access
5. **Helm deploy** — deploys API + Dashboard with dependency build
6. **Ingress wait** — waits for IP assignment, health checks, and HTTP 200
7. **Completion** — shows dashboard URL and total elapsed time

## Navigation

Type `back` at any prompt to return to the previous step. The `(back)` hint is shown on every prompt. Steps 2-7 support back navigation. Steps 8-9 are sequential.

## Resume Mode

If the Helm deployment fails (e.g., missing chart dependencies, image pull errors), use `--resume` to retry without re-running Terraform:

```bash
./setup.sh --resume
```

Resume mode:
1. Reads config from existing `terraform/gcp/prod/terraform.tfvars`
2. Validates the cluster is reachable
3. Checks if Helm releases already exist (asks before re-deploying)
4. Automatically adds Bitnami Helm repo if missing
5. Runs `helm dependency build` before deploying
6. On failure, suggests `./setup.sh --resume` again

## Dry Run

Generate config files without applying any changes:

```bash
./setup.sh --dry-run
```

Shows the manual commands to apply afterwards.

## Terminal Features

- **Animated spinner** with elapsed time for all long operations
- **Color output** (auto-disabled for non-TTY / piped output)
- **Graceful cancel** — Ctrl+C cleans up tfplan and stops spinners
- **Input validation** — numeric checks, boolean checks, choice validation
- **Permission verification** — checks GCP IAM before proceeding
- **ADC type detection** — warns if Application Default Credentials use a service account instead of user credentials

## Generated Files

| File | Gitignored | Purpose |
|------|-----------|---------|
| `terraform/gcp/prod/terraform.tfvars` | Yes | Terraform input variables |
| `helm-charts/decisionbox-api/values-secrets.yaml` | Yes | Helm values with secret provider config |

Both files are gitignored to prevent committing environment-specific values.

## Next Steps

- [Terraform GCP](terraform-gcp.md) — Variables reference and module details
- [Kubernetes (Helm)](kubernetes.md) — Manual Helm deployment guide
- [Production Considerations](production.md) — Scaling, monitoring, backups
