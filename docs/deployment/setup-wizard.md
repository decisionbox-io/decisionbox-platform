# Setup Wizard

> **Version**: 0.3.0

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
| `--destroy` | Tear down everything — Helm releases, K8s namespace, Terraform resources |
| `--project NAME` | Project name (default: `decisionbox`) |
| `--env ENV` | Environment: `prod`, `staging`, `dev`, or custom (default: `prod`) |
| `--base DIR` | Base directory for deployment files (default: script's directory) |
| `--provider CLOUD` | Cloud provider: `gcp`, `aws`, or `azure` (for `--resume`/`--destroy`) |
| `--include FILE` | Source a plugin script that registers additional steps |

## Multi-Deployment Support

Each deployment gets its own isolated directory with independent `.terraform/`, `terraform.tfvars`, and `values-secrets.yaml`:

```
{base}/{project}/{cloud}/{env}/
├── main.tf              # Copied from template, module source rewritten
├── variables.tf
├── outputs.tf
├── versions.tf
├── terraform.tfvars     # Generated per deployment
├── values-secrets.yaml  # Helm values (per deployment)
└── .terraform/          # Isolated provider cache + state link
```

**Examples:**

```bash
# Default: terraform/decisionbox/gcp/prod/
./setup.sh

# Named project + staging environment
./setup.sh --project acme-corp --env staging

# External directory (outside the repo)
./setup.sh --project acme-corp --base ~/deployments

# Resume or destroy a specific deployment
./setup.sh --resume --project acme-corp --env staging --provider gcp
./setup.sh --destroy --project acme-corp --env prod --provider aws
```

## 10-Step Flow

### Step 1: Prerequisites

Verifies all required tools are installed with version info:
- `terraform` (1.5+)
- `gcloud` (for GCP), `aws` (for AWS), or `az` (for Azure)
- `kubectl`
- `helm` (3.7+)
- `openssl`

If any tool is missing, the wizard shows an install link and exits.

### Step 2: Deployment Identity

Configure the deployment's project name, environment, and base directory:
- **Project name** (default: `decisionbox`) — lowercase alphanumeric + hyphens
- **Environment** (default: `prod`) — e.g., `prod`, `staging`, `dev`
- **Base directory** (default: script's directory) — where deployment files are stored

These determine the deployment directory: `{base}/{project}/{cloud}/{env}/`.

### Step 3: Cloud Provider

Select your cloud provider:
- **GCP** — Google Cloud Platform
- **AWS** — Amazon Web Services
- **Azure** — Microsoft Azure

### Step 4: Secrets Configuration

Configure the secret namespace prefix used to scope secrets. Format: `{namespace}-{projectID}-{key}`.

Choose between:
- **Cloud Secret Manager** (GCP Secret Manager, AWS Secrets Manager, or Azure Key Vault) — recommended for production
- **MongoDB encrypted secrets** — uses AES-256 encryption with `SECRET_ENCRYPTION_KEY`

### Step 5: Cloud Provider Settings

**GCP:**
- Project ID (validated against GCP naming rules)
- Region (default: `us-central1`)
- GKE cluster name (default: `{project}-{env}`)
- Kubernetes namespace (default: `decisionbox`)
- Node pool: machine type, min/max nodes per zone (numeric validation, min <= max check)
- BigQuery IAM (optional — for data warehouse access)
- Vertex AI IAM (optional — for Claude via Vertex or Gemini)
- IP restriction (optional — restricts HTTP/HTTPS to specified CIDR blocks via Cloud Armor)

**AWS:**
- Region (default: `us-east-1`)
- EKS cluster name (default: `{project}-{env}`)
- Kubernetes namespace (default: `decisionbox`)
- Node group: instance type, min/max/desired nodes (numeric validation)
- Bedrock IAM (optional — for LLM access)
- Redshift IAM (optional — for data warehouse access)
- IP restriction (optional — restricts HTTP/HTTPS to specified CIDR blocks via security group)

**Azure:**
- Subscription ID
- Location / region (default: `eastus`)
- AKS cluster name (default: `{project}-{env}`)
- Kubernetes namespace (default: `decisionbox`)
- Node pool: VM size, min/max nodes (numeric validation)
- Key Vault (optional — for secret storage)
- IP restriction (optional — restricts HTTP/HTTPS to specified CIDR blocks via NSG rules)

### Step 6: Authentication

**GCP** — choose how Terraform authenticates:

- **User credentials (ADC):** If ADC already exists and is a user credential, offers to reuse. Otherwise prompts for interactive login with `--no-browser` mode.
- **Service account key file:** Provide a JSON key file path. Sets `GOOGLE_APPLICATION_CREDENTIALS`.
- Verifies 4 GCP permissions: GKE, Storage, IAM, Compute.

**AWS** — choose how Terraform authenticates:

- **AWS CLI profile:** Use an existing profile (default or named).
- **Environment variables:** Set `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`.
- Verifies identity via `aws sts get-caller-identity`.

**Azure** — choose how Terraform authenticates:

- **Azure CLI (`az login`):** If already logged in to the correct subscription, offers to reuse. Otherwise opens browser for interactive login.
- **Service principal:** Provide tenant ID, client ID, and client secret. Sets `ARM_*` environment variables.
- Verifies identity via `az account show`.

### Step 7: Terraform State

**GCP:** Configure a GCS bucket for remote state:
- Bucket name (default: `{project-id}-terraform-state`)
- State prefix (default: `{project}/{env}`)
- Auto-creates the bucket with versioning if it doesn't exist

**AWS:** Configure an S3 bucket for remote state with native locking:
- S3 bucket name (default: `{account-id}-terraform-state`)
- State key (default: `{project}/{env}/terraform.tfstate`)
- Auto-creates the bucket with versioning if it doesn't exist
- Uses S3-native locking (`use_lockfile=true`, Terraform 1.10+)

**Azure:** Configure an Azure Storage Account for remote state:
- Resource group for state storage (default: `terraform-state-rg`)
- Storage account name (must be globally unique, 3-24 lowercase chars)
- Container name (default: `tfstate`)
- State key (default: `prod.terraform.tfstate`)
- Auto-creates the resource group, storage account, and container if they don't exist

### Step 8: Review

Displays all collected configuration for review before proceeding, including project name, environment, deployment directory, and IP allowlist (if configured). Type `back` to change any value.

### Step 9: Generate Config Files

Creates the deployment directory (if new) by copying template files from `terraform/{cloud}/prod/` and rewriting the module source path. Then generates:

**`{base}/{project}/{cloud}/{env}/terraform.tfvars`** — Terraform variables:
```hcl
project_id   = "my-project"
region       = "us-central1"
cluster_name = "acme-corp-prod"
machine_type = "e2-standard-2"
min_node_count = 1
max_node_count = 2
k8s_namespace = "decisionbox"
enable_gcp_secrets  = true
secret_namespace    = "decisionbox"
allowed_ip_ranges   = ["203.0.113.0/24"]
```

**`{base}/{project}/{cloud}/{env}/values-secrets.yaml`** — Helm values with secret provider config.

### Step 10: Terraform & Deploy

1. **Terraform init** — initializes with remote backend (spinner + elapsed time)
2. **Terraform plan** — shows changes, prompts for approval
3. **Terraform apply** — provisions infrastructure (shows elapsed time)
4. **kubectl credentials** — configures cluster access
5. **Helm deploy** — deploys API + Dashboard with dependency build
6. **Ingress wait** — waits for IP assignment, health checks, and HTTP 200
7. **Completion** — shows dashboard URL and total elapsed time

## Navigation

Type `back` at any prompt to return to the previous step. The `(back)` hint is shown on every prompt. Steps 2-8 support back navigation. Steps 9-10 are sequential.

## Resume Mode

If the Helm deployment fails (e.g., missing chart dependencies, image pull errors), use `--resume` to retry without re-running Terraform:

```bash
./setup.sh --resume
./setup.sh --resume --project acme-corp --env staging --provider gcp
```

Resume mode:
1. Prompts for project/env (or uses `--project`/`--env`/`--provider` flags)
2. Reads config from existing `terraform.tfvars` (auto-detects GCP, AWS, or Azure)
3. Validates the cluster is reachable
4. Checks if Helm releases already exist (asks before re-deploying)
5. Automatically adds Bitnami Helm repo if missing
6. Runs `helm dependency build` before deploying
7. On failure, suggests `./setup.sh --resume` again

## Dry Run

Generate config files without applying any changes:

```bash
./setup.sh --dry-run
```

Shows the manual commands to apply afterwards.

## Destroy

Tear down all infrastructure:

```bash
./setup.sh --destroy
./setup.sh --destroy --project acme-corp --env prod --provider aws
```

Destroy mode:
1. Prompts for project/env (or uses `--project`/`--env`/`--provider` flags)
2. Reads config from existing `terraform.tfvars` (auto-detects GCP, AWS, or Azure)
3. Requires typing `destroy` to confirm (safety check)
4. Uninstalls Helm releases (dashboard, API)
5. Deletes the Kubernetes namespace
6. Disables deletion protection (GCP only — GKE requires this before destroy)
7. Runs `terraform destroy` to remove all cloud resources
8. Leaves the state bucket intact (contains state history)

## Terminal Features

- **Animated spinner** with elapsed time for all long operations
- **Color output** (auto-disabled for non-TTY / piped output)
- **Graceful cancel** — Ctrl+C cleans up tfplan and stops spinners
- **Input validation** — numeric checks, boolean checks, choice validation, CIDR format validation (bare IPs auto-append `/32`)
- **Permission verification** — checks GCP IAM, AWS identity, or Azure subscription before proceeding
- **ADC type detection** — warns if GCP Application Default Credentials use a service account instead of user credentials

## Generated Files

| File | Gitignored | Purpose |
|------|-----------|---------|
| `{base}/{project}/{cloud}/{env}/terraform.tfvars` | Yes (`*.tfvars`) | Terraform input variables |
| `{base}/{project}/{cloud}/{env}/values-secrets.yaml` | Yes | Helm values with secret provider config |

Both files are gitignored to prevent committing environment-specific values.

## Next Steps

- [Terraform GCP](terraform-gcp.md) — GKE module variables and details
- [Terraform AWS](terraform-aws.md) — EKS module variables and details
- [Terraform Azure](terraform-azure.md) — AKS module variables and details
- [Kubernetes (Helm)](kubernetes.md) — Manual Helm deployment guide
- [Production Considerations](production.md) — Scaling, monitoring, backups
