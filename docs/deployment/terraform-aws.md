# Terraform — AWS

> **Version**: 0.3.0

Provision a production-ready EKS cluster for DecisionBox using the included Terraform module.

## What It Creates

| Resource | Description |
|----------|-------------|
| **VPC** | Dedicated network with public/private subnets across 3 AZs |
| **NAT Gateway** | Outbound internet for private subnets (single or per-AZ) |
| **EKS cluster** | Private API + public endpoint, KMS-encrypted secrets, control plane logging |
| **Managed node group** | Auto-scaling with configurable instance type and disk |
| **KMS keys** | Encryption for EKS secrets and CloudWatch logs |
| **IAM roles** | Cluster role, node role, OIDC provider, IRSA roles for API + Agent, LB controller |
| **VPC flow logs** | Traffic logging to CloudWatch (optional) |

## Prerequisites

- [Terraform 1.5+](https://developer.hashicorp.com/terraform/install)
- [AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) authenticated with an account
- AWS account with billing enabled
- Sufficient IAM permissions (AdministratorAccess or equivalent)

## Quick Start with Setup Wizard

The included [setup wizard](setup-wizard.md) handles Terraform state, cluster provisioning, and Helm deployment in one flow:

```bash
cd terraform
./setup.sh          # Full interactive setup (select AWS at step 2)
./setup.sh --dry-run  # Generate config files only
./setup.sh --resume   # Resume from Helm deploy
```

## Manual Deployment

### Step 1: Create a Terraform State Bucket

```bash
REGION=us-east-1
ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
BUCKET="$ACCOUNT_ID-terraform-state"

aws s3api create-bucket --bucket $BUCKET --region $REGION
aws s3api put-bucket-versioning --bucket $BUCKET --versioning-configuration Status=Enabled
aws s3api put-public-access-block --bucket $BUCKET \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
```

State locking uses S3-native locking (`use_lockfile=true`, Terraform 1.10+) — no DynamoDB table needed.

### Step 2: Configure Variables

Copy the example file and fill in your values:

```bash
cd terraform/aws/prod
cp terraform.tfvars.example terraform.tfvars
```

Example `terraform.tfvars`:

```hcl
region       = "us-east-1"
cluster_name = "decisionbox-prod"

# Networking
create_vpc = true
vpc_cidr   = "10.0.0.0/16"

# EKS node group
instance_type      = "t3.large"
min_node_count     = 1
max_node_count     = 3
desired_node_count = 2
disk_size_gb       = 50

# IRSA
k8s_namespace              = "decisionbox"
k8s_service_account        = "decisionbox-api"
k8s_agent_service_account  = "decisionbox-agent"

# Optional: AWS Secrets Manager
enable_aws_secrets = true
secret_namespace   = "decisionbox"

# Optional: Bedrock (LLM)
enable_bedrock_iam = false

# Optional: Redshift read access
enable_redshift_iam = false

# Optional: Restrict HTTP/HTTPS to specific IPs (empty = unrestricted)
# allowed_ip_ranges = ["203.0.113.0/24", "198.51.100.0/24"]

tags = {
  project     = "decisionbox"
  environment = "prod"
  managed_by  = "terraform"
}
```

### Step 3: Initialize and Apply

```bash
cd terraform/aws/prod

terraform init \
  -backend-config="bucket=$BUCKET" \
  -backend-config="key=prod/terraform.tfstate" \
  -backend-config="region=$REGION" \
  -backend-config="use_lockfile=true"

terraform plan -out=tfplan
terraform apply tfplan
```

### Step 4: Configure kubectl

```bash
aws eks update-kubeconfig \
  --name decisionbox-prod \
  --region us-east-1
```

### Step 5: Deploy with Helm

Follow the [Kubernetes Deployment](kubernetes.md) guide to deploy the API and Dashboard.

When using AWS Secrets Manager with IRSA, annotate the service account:

```yaml
# values-prod.yaml
serviceAccountAnnotations:
  eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/decisionbox-prod-api"
```

Get the IRSA role ARNs from Terraform output:

```bash
terraform output irsa_role_arn
terraform output irsa_agent_role_arn
```

## Module Architecture

```
terraform/aws/
├── prod/
│   ├── versions.tf      # Provider versions (AWS 5.0-7.0), S3 backend
│   ├── variables.tf     # Environment-level variables
│   ├── main.tf          # Module instantiation
│   ├── outputs.tf       # Cluster outputs
│   └── terraform.tfvars.example  # Copy and edit for your environment
└── modules/decisionbox/
    ├── vpc.tf            # VPC, subnets, NAT, IGW, route tables, flow logs
    ├── eks.tf            # EKS cluster, KMS, node group, security group
    ├── iam.tf            # IAM roles, OIDC provider, IRSA
    ├── secrets.tf        # Secrets Manager IAM (conditional)
    ├── redshift.tf       # Redshift IAM (conditional)
    ├── variables.tf      # Input variables
    ├── outputs.tf        # Module outputs
    └── versions.tf       # Required providers
```

## Variables Reference

All variables are defined in `terraform/aws/modules/decisionbox/variables.tf`.

### Required

No variables are strictly required — all have defaults.
Set `region` and `cluster_name` to match your environment.

### Cluster

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `region` | string | `us-east-1` | AWS region |
| `cluster_name` | string | `decisionbox-prod` | EKS cluster name |
| `create_cluster` | bool | `true` | Create EKS cluster (false for IAM-only) |
| `kubernetes_version` | string | `1.31` | Kubernetes version |
| `endpoint_private_access` | bool | `true` | Private API server endpoint |
| `endpoint_public_access` | bool | `true` | Public API server endpoint |
| `public_access_cidrs` | list(string) | `["0.0.0.0/0"]` | CIDRs allowed to access public endpoint |
| `enabled_cluster_log_types` | list(string) | `["api","audit","authenticator","controllerManager","scheduler"]` | Control plane log types |
| `log_retention_days` | number | `90` | CloudWatch log retention |

### Networking

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `create_vpc` | bool | `true` | Create VPC (false to use existing) |
| `existing_vpc_id` | string | `""` | Existing VPC ID (when `create_vpc=false`) |
| `existing_private_subnet_ids` | list(string) | `[]` | Existing private subnet IDs |
| `existing_public_subnet_ids` | list(string) | `[]` | Existing public subnet IDs |
| `vpc_cidr` | string | `10.0.0.0/16` | VPC CIDR block |
| `availability_zones` | list(string) | `[]` | AZs (defaults to first 3 in region) |
| `private_subnet_cidrs` | list(string) | `["10.0.0.0/19","10.0.32.0/19","10.0.64.0/19"]` | Private subnet CIDRs |
| `public_subnet_cidrs` | list(string) | `["10.0.96.0/22","10.0.100.0/22","10.0.104.0/22"]` | Public subnet CIDRs |
| `single_nat_gateway` | bool | `true` | Single NAT (cost-effective) vs one per AZ (HA) |
| `enable_flow_logs` | bool | `true` | VPC flow logs to CloudWatch |
| `flow_log_retention_days` | number | `30` | Flow log retention |

### Node Group

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `instance_type` | string | `t3.large` | EC2 instance type |
| `disk_size_gb` | number | `50` | EBS volume size (GB) |
| `ami_type` | string | `AL2023_x86_64_STANDARD` | AMI type |
| `min_node_count` | number | `1` | Minimum nodes |
| `max_node_count` | number | `3` | Maximum nodes |
| `desired_node_count` | number | `2` | Desired nodes |

### IAM & Secrets

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `k8s_namespace` | string | `decisionbox` | Kubernetes namespace for IRSA |
| `k8s_service_account` | string | `decisionbox-api` | K8s service account name (API) |
| `k8s_agent_service_account` | string | `decisionbox-agent` | K8s service account name (Agent) |
| `enable_aws_secrets` | bool | `false` | Grant Secrets Manager access |
| `secret_namespace` | string | `decisionbox` | Secret name prefix for IAM scoping |
| `enable_bedrock_iam` | bool | `false` | Grant Bedrock InvokeModel access (Agent) |
| `enable_redshift_iam` | bool | `false` | Grant Redshift Data API read access (Agent) |
| `allowed_ip_ranges` | list(string) | `[]` | CIDR blocks allowed for HTTP/HTTPS. Empty = unrestricted. Creates a security group for ALB attachment. |

### Tags

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `tags` | map(string) | `{}` | Tags applied to all resources |

## Outputs

| Output | Sensitive | Description |
|--------|-----------|-------------|
| `cluster_name` | No | EKS cluster name |
| `cluster_endpoint` | Yes | Kubernetes API endpoint |
| `cluster_ca_certificate` | Yes | CA certificate (base64) |
| `vpc_id` | No | VPC ID |
| `private_subnet_ids` | No | Private subnet IDs |
| `public_subnet_ids` | No | Public subnet IDs |
| `irsa_role_arn` | No | IRSA role ARN for DecisionBox API |
| `irsa_agent_role_arn` | No | IRSA role ARN for DecisionBox Agent |
| `oidc_provider_arn` | No | OIDC provider ARN |
| `lb_controller_role_arn` | No | IAM role ARN for AWS Load Balancer Controller |
| `aws_secrets_iam_enabled` | No | Whether Secrets Manager IAM was configured |
| `redshift_iam_enabled` | No | Whether Redshift IAM was configured |

## IRSA (IAM Roles for Service Accounts)

The module creates an OIDC provider and two IAM roles bound to Kubernetes service accounts via [IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html).
This allows pods to authenticate to AWS services without storing credentials.

```
K8s ServiceAccount: decisionbox/decisionbox-api
    ↕ IRSA binding (OIDC)
IAM Role: decisionbox-prod-api
    ↓ IAM policies
AWS Secrets Manager (read/write, namespace-scoped)

K8s ServiceAccount: decisionbox/decisionbox-agent
    ↕ IRSA binding (OIDC)
IAM Role: decisionbox-prod-agent
    ↓ IAM policies
AWS Secrets Manager (read-only, namespace-scoped)
Amazon Bedrock (InvokeModel)
Amazon Redshift Data API (read-only)
```

The Helm chart must annotate both K8s service accounts:

```yaml
# API service account
serviceAccountAnnotations:
  eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/decisionbox-prod-api"

# Agent service account
agentServiceAccount:
  annotations:
    eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/decisionbox-prod-agent"
```

## Secrets Manager Scoping

When `enable_aws_secrets=true`, the module creates IAM policies scoped to the configured namespace prefix.
The API gets read/write access; the Agent gets read-only access.

- **Allowed**: `decisionbox/project123/llm-api-key`
- **Blocked**: `other-app/database-password`

This ensures multi-tenant isolation when multiple applications share an AWS account.

## Using an Existing VPC

To deploy into an existing network:

```hcl
create_vpc                  = false
existing_vpc_id             = "vpc-0123456789abcdef0"
existing_private_subnet_ids = ["subnet-aaa", "subnet-bbb", "subnet-ccc"]
existing_public_subnet_ids  = ["subnet-ddd", "subnet-eee", "subnet-fff"]
```

The subnets must have the required Kubernetes tags:
- Private subnets: `kubernetes.io/role/internal-elb = 1`
- Public subnets: `kubernetes.io/role/elb = 1`
- Both: `kubernetes.io/cluster/<cluster-name> = shared`

## Cost Considerations

Default configuration costs (approximate, us-east-1):

| Resource | Estimate |
|----------|----------|
| EKS control plane | ~$73/month |
| 2x t3.large nodes | ~$60/month |
| NAT Gateway (single) | ~$32/month + data |
| CloudWatch logs | ~$5/month |
| **Total** | **~$170/month** |

To reduce costs:
- Use `t3.small` for dev/testing
- Set `min_node_count=1`, `desired_node_count=1`
- Keep `single_nat_gateway=true` (default)
- Reduce `log_retention_days`

## Destroying Resources

```bash
cd terraform/aws/prod

# Review what will be destroyed
terraform plan -destroy

# Destroy infrastructure
terraform destroy
```

## Next Steps

- [Kubernetes Deployment](kubernetes.md) — Deploy with Helm after Terraform
- [Terraform GCP](terraform-gcp.md) — GKE cluster provisioning on GCP
- [Terraform Azure](terraform-azure.md) — AKS cluster provisioning on Azure
- [Helm Values Reference](../reference/helm-values.md) — All chart configuration options
- [Production Considerations](production.md) — Scaling, monitoring, backups
