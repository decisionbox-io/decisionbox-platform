# Terraform — GCP

> **Version**: 0.1.0

Provision a production-ready GKE cluster for DecisionBox using the included Terraform module.

## What It Creates

| Resource | Description |
|----------|-------------|
| **VPC** | Dedicated network with subnets for nodes, pods, and services |
| **Cloud NAT** | Outbound internet access for private nodes |
| **Firewall rules** | Internal traffic + GCP health check ranges |
| **GKE cluster** | Private nodes, Dataplane V2, auto-upgrade, shielded nodes |
| **Node pool** | Auto-scaling with configurable machine type and disk |
| **Service accounts** | Node SA (logging/monitoring) + Workload Identity SA (API) |
| **IAM bindings** | Workload Identity, Secret Manager access, BigQuery access (optional) |

## Prerequisites

- [Terraform 1.5+](https://developer.hashicorp.com/terraform/install)
- [gcloud CLI](https://cloud.google.com/sdk/docs/install) authenticated with a project
- GCP project with billing enabled
- Sufficient IAM permissions (Project Owner or Editor)

## Quick Start with Setup Wizard

The included [setup wizard](setup-wizard.md) handles Terraform state, cluster provisioning, and Helm deployment in one flow:

```bash
cd terraform
./setup.sh          # Full interactive setup
./setup.sh --dry-run  # Generate config files only
./setup.sh --resume   # Resume from Helm deploy
```

The wizard prompts for:
1. Cloud provider (GCP)
2. Secret namespace prefix
3. Secret provider (GCP Secret Manager or MongoDB)
4. GCP project ID, region, cluster name
5. Terraform state bucket (auto-creates if needed)
6. Machine type and node scaling
7. BigQuery IAM (optional)
8. `SECRET_ENCRYPTION_KEY` (auto-generates or user-provided)

After provisioning, it automatically:
- Configures `kubectl` credentials
- Creates the Kubernetes namespace and secrets
- Deploys API and Dashboard via Helm
- Waits for ingress and verifies health checks
- Displays the dashboard URL

## Manual Deployment

### Step 1: Create a Terraform State Bucket

```bash
PROJECT_ID=$(gcloud config get-value project)
gsutil mb -p $PROJECT_ID gs://$PROJECT_ID-terraform-state
gsutil versioning set on gs://$PROJECT_ID-terraform-state
```

### Step 2: Configure Variables

Create `terraform/gcp/prod/terraform.tfvars`:

```hcl
project_id   = "my-gcp-project"
region       = "us-central1"
cluster_name = "decisionbox-prod"

# Networking
create_vpc  = true
subnet_cidr = "10.0.0.0/20"
pods_cidr   = "10.4.0.0/14"
services_cidr = "10.8.0.0/20"

# Node pool
machine_type   = "e2-standard-2"
min_node_count = 1
max_node_count = 2
disk_size_gb   = 50

# Workload Identity
k8s_namespace       = "decisionbox"
k8s_service_account = "decisionbox-api"

# Optional: GCP Secret Manager
enable_gcp_secrets = true
secret_namespace   = "decisionbox"

# Optional: BigQuery read access
enable_bigquery_iam = true

# Optional: Vertex AI access (for Claude via Vertex or Gemini)
enable_vertex_ai_iam = true
```

### Step 3: Initialize and Apply

```bash
cd terraform/gcp/prod

terraform init \
  -backend-config="bucket=$PROJECT_ID-terraform-state" \
  -backend-config="prefix=prod"

terraform plan -out=tfplan
terraform apply tfplan
```

### Step 4: Configure kubectl

```bash
gcloud container clusters get-credentials decisionbox-prod \
  --region us-central1 \
  --project $PROJECT_ID
```

### Step 5: Deploy with Helm

Follow the [Kubernetes Deployment](kubernetes.md) guide to deploy the API and Dashboard.

When using GCP Secret Manager with Workload Identity, annotate the service account:

```yaml
# values-prod.yaml
serviceAccountAnnotations:
  iam.gke.io/gcp-service-account: "decisionbox-prod-api@my-gcp-project.iam.gserviceaccount.com"
```

## Module Architecture

```
terraform/gcp/
├── prod/
│   ├── versions.tf      # Provider versions (Google 5.0-7.0)
│   ├── variables.tf     # Environment-level variables
│   ├── main.tf          # Module instantiation
│   └── outputs.tf       # Cluster outputs
└── modules/decisionbox/
    ├── apis.tf           # GCP API enablement
    ├── networking.tf     # VPC, subnets, NAT, firewalls
    ├── gke.tf            # GKE cluster + node pool
    ├── iam.tf            # Service accounts + Workload Identity
    ├── secrets.tf        # Secret Manager IAM (conditional)
    ├── bigquery.tf       # BigQuery IAM (conditional)
    ├── variables.tf      # 40+ input variables
    └── outputs.tf        # Cluster outputs
```

## Variables Reference

All variables are defined in `terraform/gcp/modules/decisionbox/variables.tf`.

### Required

| Variable | Type | Description |
|----------|------|-------------|
| `project_id` | string | GCP project ID |

### Cluster

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `region` | string | `us-central1` | GCP region |
| `cluster_name` | string | `decisionbox-prod` | GKE cluster name |
| `create_cluster` | bool | `true` | Create GKE cluster (false to use existing) |
| `deletion_protection` | bool | `true` | Prevent accidental cluster deletion |
| `release_channel` | string | `REGULAR` | GKE release channel |
| `datapath_provider` | string | `ADVANCED_DATAPATH` | Dataplane V2 for network policy |
| `enable_network_policy` | bool | `true` | Enable network policy enforcement |
| `network_policy_provider` | string | `CALICO` | Network policy provider (used when not ADVANCED_DATAPATH) |
| `enable_binary_authorization` | bool | `false` | Binary Authorization for container images |
| `logging_components` | list(string) | `["SYSTEM_COMPONENTS", "WORKLOADS"]` | GKE logging components |
| `monitoring_components` | list(string) | `["SYSTEM_COMPONENTS"]` | GKE monitoring components |

### Networking

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `create_vpc` | bool | `true` | Create VPC (false to use existing) |
| `existing_vpc_id` | string | `""` | Existing VPC self-link (when `create_vpc=false`) |
| `existing_subnet_id` | string | `""` | Existing subnet self-link |
| `subnet_cidr` | string | `10.0.0.0/20` | Node subnet CIDR |
| `pods_cidr` | string | `10.4.0.0/14` | Pod IP range |
| `pods_range_name` | string | `pods` | Secondary range name for pods |
| `services_cidr` | string | `10.8.0.0/20` | Service IP range |
| `services_range_name` | string | `services` | Secondary range name for services |
| `master_cidr` | string | `172.16.0.0/28` | Control plane CIDR |
| `enable_private_nodes` | bool | `true` | Nodes have no public IPs |
| `enable_private_endpoint` | bool | `false` | Restrict master to private network |
| `master_authorized_networks` | list(object) | `[{cidr_block="0.0.0.0/0", display_name="all"}]` | CIDRs allowed to reach the master API |
| `enable_flow_logs` | bool | `true` | VPC flow logs |
| `flow_log_interval` | string | `INTERVAL_10_MIN` | Flow log aggregation interval |
| `flow_log_sampling` | number | `0.5` | Flow log sampling rate (0.0-1.0) |
| `flow_log_metadata` | string | `INCLUDE_ALL_METADATA` | Flow log metadata inclusion |

### NAT

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `nat_ip_allocate_option` | string | `AUTO_ONLY` | NAT IP allocation |
| `nat_source_subnetwork_ip_ranges` | string | `ALL_SUBNETWORKS_ALL_IP_RANGES` | NAT source ranges |
| `enable_nat_logging` | bool | `true` | Cloud NAT logging |
| `nat_log_filter` | string | `ERRORS_ONLY` | NAT log filter |

### Firewall

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `internal_tcp_ports` | list(string) | `["0-65535"]` | Internal TCP ports allowed |
| `internal_udp_ports` | list(string) | `["0-65535"]` | Internal UDP ports allowed |
| `health_check_ports` | list(string) | `["80","443","3000","8080","10256"]` | Health check ports |
| `health_check_source_ranges` | list(string) | `["35.191.0.0/16","130.211.0.0/22"]` | GCP health check IP ranges |

### Node Pool

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `machine_type` | string | `e2-standard-2` | GCE machine type |
| `disk_size_gb` | number | `50` | Boot disk size (GB) |
| `disk_type` | string | `pd-standard` | Boot disk type |
| `image_type` | string | `COS_CONTAINERD` | Node image |
| `min_node_count` | number | `1` | Minimum nodes per zone |
| `max_node_count` | number | `2` | Maximum nodes per zone |
| `enable_secure_boot` | bool | `true` | Shielded VM secure boot |
| `enable_integrity_monitoring` | bool | `true` | Shielded VM integrity monitoring |
| `enable_auto_repair` | bool | `true` | Auto-repair unhealthy nodes |
| `enable_auto_upgrade` | bool | `true` | Auto-upgrade node versions |
| `disable_legacy_metadata_endpoints` | string | `"true"` | Disable legacy metadata API |

### IAM & Secrets

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `k8s_namespace` | string | `decisionbox` | Kubernetes namespace for Workload Identity |
| `k8s_service_account` | string | `decisionbox-api` | K8s service account name (API) |
| `k8s_agent_service_account` | string | `decisionbox-agent` | K8s service account name (Agent, read-only) |
| `enable_gcp_secrets` | bool | `false` | Create Secret Manager IAM bindings |
| `secret_namespace` | string | `decisionbox` | Secret name prefix for IAM conditions |
| `enable_bigquery_iam` | bool | `false` | Grant BigQuery read access to the agent SA |
| `enable_vertex_ai_iam` | bool | `false` | Grant Vertex AI access to the agent SA (Claude via Vertex, Gemini) |

### Labels

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `labels` | map(string) | `{}` | Resource labels applied to all resources |

## Outputs

| Output | Sensitive | Description |
|--------|-----------|-------------|
| `cluster_name` | No | GKE cluster name |
| `cluster_endpoint` | Yes | Kubernetes API endpoint |
| `cluster_ca_certificate` | Yes | CA certificate for kubectl |
| `vpc_name` | No | VPC network name |
| `workload_identity_sa_email` | No | GCP service account for API Workload Identity |
| `agent_workload_identity_sa_email` | No | GCP service account for Agent Workload Identity (read-only) |
| `gcp_secrets_iam_enabled` | No | Whether Secret Manager IAM was configured |
| `bigquery_iam_enabled` | No | Whether BigQuery IAM was configured |
| `vertex_ai_iam_enabled` | No | Whether Vertex AI IAM was configured |

## Workload Identity

The module creates a GCP service account and binds it to a Kubernetes service account via [Workload Identity](https://cloud.google.com/kubernetes-engine/docs/how-to/workload-identity). This allows the API pod to authenticate to GCP services (Secret Manager, BigQuery) without storing credentials.

```
K8s ServiceAccount: decisionbox/decisionbox-api
    ↕ Workload Identity binding
GCP ServiceAccount: decisionbox-prod-api@project.iam.gserviceaccount.com
    ↓ IAM roles
GCP Secret Manager (namespace-scoped)
BigQuery (data viewer + job user)
```

The Helm chart must annotate the K8s service account:

```yaml
serviceAccountAnnotations:
  iam.gke.io/gcp-service-account: "decisionbox-prod-api@my-project.iam.gserviceaccount.com"
```

## Secret Manager Scoping

When `enable_gcp_secrets=true`, the module creates IAM bindings with conditions that restrict the API to secrets prefixed with the configured namespace:

- **Allowed**: `decisionbox-project123-llm-api-key`
- **Blocked**: `other-app-database-password`

This ensures multi-tenant isolation when multiple applications share a GCP project.

## Using an Existing VPC

To deploy into an existing network:

```hcl
create_vpc         = false
existing_vpc_id    = "projects/my-project/global/networks/my-vpc"
existing_subnet_id = "projects/my-project/regions/us-central1/subnetworks/my-subnet"
```

The subnet must have secondary IP ranges named `pods` and `services`.

## Destroying Resources

Use the setup wizard's `--destroy` flag for a clean teardown:

```bash
cd terraform
./setup.sh --destroy
```

This uninstalls Helm releases, deletes the namespace, disables deletion protection, and runs `terraform destroy`.

Or manually:

```bash
# Remove Helm releases first
helm uninstall decisionbox-dashboard -n decisionbox
helm uninstall decisionbox-api -n decisionbox

# Disable deletion protection
terraform apply -var="deletion_protection=false"

# Destroy infrastructure
terraform destroy
```

## Next Steps

- [Kubernetes Deployment](kubernetes.md) — Deploy with Helm after Terraform
- [Helm Values Reference](../reference/helm-values.md) — All chart configuration options
- [Production Considerations](production.md) — Scaling, monitoring, backups
