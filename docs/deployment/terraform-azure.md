# Terraform — Azure

> **Version**: 0.2.0

Provision a production-ready AKS cluster for DecisionBox using the included Terraform module.

## What It Creates

| Resource | Description |
|----------|-------------|
| **Resource Group** | Dedicated RG for all DecisionBox resources |
| **VNet + Subnet** | Dedicated network with node subnet |
| **NAT Gateway** | Outbound internet access for private nodes |
| **NSG** | Network Security Group with SSH deny-by-default |
| **AKS cluster** | Workload Identity, OIDC issuer, Azure CNI, auto-scaling |
| **Node pool** | Auto-scaling with configurable VM size and disk |
| **Managed Identities** | API identity (secrets read/write) + Agent identity (read-only) |
| **Key Vault** | Secret storage with RBAC authorization (optional) |
| **Log Analytics** | Container Insights workspace (optional) |

## Prerequisites

- [Terraform 1.5+](https://developer.hashicorp.com/terraform/install)
- [Azure CLI](https://learn.microsoft.com/en-us/cli/azure/install-azure-cli) authenticated (`az login`)
- Azure subscription with sufficient quota
- Sufficient permissions (Owner or Contributor + User Access Administrator)

## Quick Start with Setup Wizard

The included [setup wizard](setup-wizard.md) handles Terraform state, cluster provisioning, and Helm deployment in one flow:

```bash
cd terraform
./setup.sh          # Full interactive setup
./setup.sh --dry-run  # Generate config files only
./setup.sh --resume   # Resume from Helm deploy
```

The wizard prompts for:
1. Cloud provider (Azure)
2. Secret namespace prefix
3. Secret provider (Azure Key Vault or MongoDB)
4. Azure subscription ID, region, cluster name
5. Terraform state (Azure Storage Account, auto-creates if needed)
6. VM size and node scaling
7. Key Vault (optional)
8. `SECRET_ENCRYPTION_KEY` (auto-generates or user-provided)

After provisioning, it automatically:
- Configures `kubectl` credentials via `az aks get-credentials`
- Creates the Kubernetes namespace and secrets
- Deploys API and Dashboard via Helm

## Manual Setup

### 1. Create State Backend

```bash
# Create resource group for Terraform state
az group create --name terraform-state-rg --location eastus

# Create storage account (name must be globally unique)
az storage account create \
  --name decisionboxstate \
  --resource-group terraform-state-rg \
  --sku Standard_LRS \
  --encryption-services blob

# Create blob container
az storage container create \
  --name terraform \
  --account-name decisionboxstate
```

### 2. Configure Variables

```bash
cd terraform/azure/prod
cp terraform.tfvars terraform.tfvars.local
```

Edit `terraform.tfvars.local`:

```hcl
subscription_id     = "your-subscription-id"
location            = "eastus"
cluster_name        = "decisionbox-prod"
resource_group_name = "decisionbox-prod-rg"

# AKS node pool
vm_size        = "Standard_D2s_v5"
min_node_count = 3
max_node_count = 3

# Workload Identity
k8s_namespace = "decisionbox"

# Optional features
enable_key_vault = true

# Optional: Restrict HTTP/HTTPS to specific IPs (empty = unrestricted)
# allowed_ip_ranges = ["203.0.113.0/24", "198.51.100.0/24"]
```

### 3. Initialize and Plan

```bash
terraform init \
  -backend-config="resource_group_name=terraform-state-rg" \
  -backend-config="storage_account_name=decisionboxstate" \
  -backend-config="container_name=terraform" \
  -backend-config="key=prod/terraform.tfstate"

terraform plan -var-file=terraform.tfvars.local
```

### 4. Apply (requires human approval)

```bash
terraform apply -var-file=terraform.tfvars.local
```

### 5. Configure kubectl

```bash
az aks get-credentials \
  --resource-group decisionbox-prod-rg \
  --name decisionbox-prod
```

### 6. Deploy DecisionBox via Helm

See [Kubernetes deployment guide](kubernetes.md) for Helm chart installation.

The Terraform outputs provide the values needed for Helm:

```bash
# Get managed identity client IDs for Workload Identity annotations
terraform output api_identity_client_id
terraform output agent_identity_client_id

# Get Key Vault URI (if enabled)
terraform output key_vault_uri
```

## Module Reference

### Variables

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `subscription_id` | string | — | Azure subscription ID (required) |
| `location` | string | `eastus` | Azure region |
| `cluster_name` | string | `decisionbox-prod` | AKS cluster name |
| `resource_group_name` | string | `decisionbox-prod-rg` | Resource group name |
| `create_vnet` | bool | `true` | Create a new VNet |
| `vnet_cidr` | string | `10.0.0.0/16` | VNet CIDR range |
| `node_subnet_cidr` | string | `10.0.0.0/20` | Node subnet CIDR |
| `enable_nat_gateway` | bool | `true` | Create NAT Gateway |
| `create_cluster` | bool | `true` | Create a new AKS cluster |
| `kubernetes_version` | string | `null` | K8s version (null = latest stable) |
| `sku_tier` | string | `Free` | AKS SKU (Free or Standard) |
| `vm_size` | string | `Standard_D2s_v5` | Node VM size |
| `os_disk_size_gb` | number | `50` | OS disk size |
| `min_node_count` | number | `3` | Min nodes (auto-scaling) |
| `max_node_count` | number | `3` | Max nodes (auto-scaling) |
| `private_cluster_enabled` | bool | `false` | Private API server |
| `api_server_authorized_ranges` | list(string) | `[]` | API server allow-list |
| `allowed_ip_ranges` | list(string) | `[]` | CIDR blocks allowed for HTTP/HTTPS. Empty = unrestricted (NSG allows Internet). |
| `k8s_namespace` | string | `decisionbox` | K8s namespace |
| `k8s_service_account` | string | `decisionbox-api` | API K8s SA name |
| `k8s_agent_service_account` | string | `decisionbox-agent` | Agent K8s SA name (read-only) |
| `enable_key_vault` | bool | `false` | Create Key Vault |
| `enable_oms_agent` | bool | `true` | Enable Container Insights |
| `tags` | map(string) | `{}` | Resource tags |

### Outputs

| Output | Description |
|--------|-------------|
| `cluster_name` | AKS cluster name |
| `cluster_fqdn` | AKS cluster FQDN |
| `resource_group_name` | Resource group name |
| `api_identity_client_id` | API managed identity client ID |
| `agent_identity_client_id` | Agent managed identity client ID |
| `key_vault_uri` | Key Vault URI (empty if not enabled) |
| `key_vault_enabled` | Whether Key Vault was created |

## Architecture

```
Azure Subscription
├── Resource Group (decisionbox-prod-rg)
│   ├── VNet (decisionbox-prod-vnet)
│   │   └── Subnet (decisionbox-prod-nodes)
│   │       └── NSG (SSH deny-by-default)
│   ├── NAT Gateway + Public IP
│   ├── AKS Cluster (decisionbox-prod)
│   │   ├── Default Node Pool (Standard_D2s_v5, 3 nodes)
│   │   ├── Workload Identity (OIDC issuer)
│   │   └── Container Insights → Log Analytics
│   ├── Key Vault (decisionbox-prod-kv)
│   │   ├── API identity → Secrets Officer
│   │   └── Agent identity → Secrets User (read-only)
│   └── Managed Identities
│       ├── decisionbox-prod-api (federated to K8s SA)
│       └── decisionbox-prod-agent (federated to K8s SA)
│
└── Storage Account (Terraform state)
    └── terraform/prod/terraform.tfstate
```

## Using an Existing VNet

Set `create_vnet = false` and provide the existing resource IDs:

```hcl
create_vnet        = false
existing_vnet_id   = "/subscriptions/.../resourceGroups/.../providers/Microsoft.Network/virtualNetworks/my-vnet"
existing_subnet_id = "/subscriptions/.../resourceGroups/.../providers/Microsoft.Network/virtualNetworks/my-vnet/subnets/aks-subnet"
```

The existing subnet must have sufficient address space for AKS nodes.

## Using an Existing Cluster

Set `create_cluster = false` to skip cluster creation. Terraform will only create managed identities and Key Vault:

```hcl
create_cluster = false
cluster_name   = "my-existing-aks"
```

The existing cluster must have:
- OIDC issuer enabled (`--enable-oidc-issuer`)
- Workload Identity enabled (`--enable-workload-identity`)

## Security Defaults

- **Private nodes**: Nodes have no public IPs (outbound via NAT Gateway)
- **SSH denied**: NSG denies SSH by default (configurable via `nsg_allowed_ssh_cidrs`)
- **RBAC**: Key Vault uses Azure RBAC (not access policies)
- **Least privilege**: Agent identity gets read-only Key Vault access
- **Workload Identity**: No long-lived credentials — pods authenticate via federated tokens
- **Soft delete**: Key Vault soft delete with configurable retention
- **Purge protection**: Disabled by default for development flexibility

## Cost Optimization

| Resource | Default | Monthly Estimate |
|----------|---------|-----------------|
| AKS control plane (Free tier) | Free | $0 |
| 3x Standard_D2s_v5 nodes | 2 vCPU, 8 GB each | ~$210 |
| NAT Gateway | Standard | ~$32 |
| Public IP (NAT) | Standard/Static | ~$4 |
| Key Vault | Standard | ~$0.03/10k ops |
| Log Analytics | 30-day retention | ~$2.76/GB |
| **Total** | | **~$250/mo** |

Scale down for development:
```hcl
vm_size        = "Standard_B2s"       # Burstable, ~$30/mo each
min_node_count = 1
max_node_count = 1
enable_oms_agent = false              # Skip Log Analytics
sku_tier       = "Free"               # No uptime SLA
```

## Next Steps

- [Kubernetes Deployment](kubernetes.md) — Deploy with Helm after Terraform
- [Terraform GCP](terraform-gcp.md) — GKE cluster provisioning on GCP
- [Terraform AWS](terraform-aws.md) — EKS cluster provisioning on AWS
- [Helm Values Reference](../reference/helm-values.md) — All chart configuration options
- [Production Considerations](production.md) — Scaling, monitoring, backups
