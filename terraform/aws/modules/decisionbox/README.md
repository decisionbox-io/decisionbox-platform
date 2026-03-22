# DecisionBox — AWS Terraform Module

Provisions a production-ready EKS cluster for DecisionBox on AWS.

## What It Creates

| Resource | Description |
|----------|-------------|
| **VPC** | Dedicated network with public/private subnets across 3 AZs |
| **NAT Gateway** | Outbound internet for private subnets (single or per-AZ) |
| **Internet Gateway** | Public subnet internet access |
| **EKS cluster** | Private API + public endpoint, KMS-encrypted secrets, control plane logging |
| **Managed node group** | Auto-scaling with configurable instance type and disk |
| **KMS keys** | Encryption for EKS secrets and CloudWatch logs |
| **IAM roles** | Cluster role, node role, OIDC provider, IRSA roles for API + Agent, LB controller |
| **VPC flow logs** | Traffic logging to CloudWatch (optional) |
| **Secrets Manager IAM** | Namespace-scoped access for the API (optional) |
| **Redshift IAM** | Read access for data warehouse queries (optional) |

## Usage

```hcl
module "decisionbox" {
  source = "../modules/decisionbox"

  region       = "us-east-1"
  cluster_name = "decisionbox-prod"

  # Networking
  create_vpc = true
  vpc_cidr   = "10.0.0.0/16"

  # EKS
  instance_type      = "t3.large"
  min_node_count     = 1
  max_node_count     = 3
  desired_node_count = 2

  # IRSA
  k8s_namespace              = "decisionbox"
  k8s_service_account        = "decisionbox-api"
  k8s_agent_service_account  = "decisionbox-agent"

  # Optional
  enable_aws_secrets  = true
  secret_namespace    = "decisionbox"
  enable_bedrock_iam  = false
  enable_redshift_iam = false

  tags = {
    project     = "decisionbox"
    environment = "prod"
    managed_by  = "terraform"
  }
}
```

## Variables

All variables are defined in `variables.tf`. Key inputs:

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `region` | string | `us-east-1` | AWS region |
| `cluster_name` | string | `decisionbox-prod` | EKS cluster name |
| `create_vpc` | bool | `true` | Create VPC (false to use existing) |
| `create_cluster` | bool | `true` | Create EKS cluster (false for IAM-only) |
| `instance_type` | string | `t3.large` | EC2 instance type for nodes |
| `min_node_count` | number | `1` | Minimum nodes |
| `max_node_count` | number | `3` | Maximum nodes |
| `k8s_agent_service_account` | string | `decisionbox-agent` | Agent K8s service account |
| `enable_aws_secrets` | bool | `false` | Grant Secrets Manager access |
| `enable_bedrock_iam` | bool | `false` | Grant Bedrock InvokeModel access (Agent) |
| `enable_redshift_iam` | bool | `false` | Grant Redshift read access (Agent) |

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

## Documentation

See [Terraform AWS Deployment Guide](../../../docs/deployment/terraform-aws.md) for step-by-step instructions.
