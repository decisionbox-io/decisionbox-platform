variable "region" {
  description = "AWS region"
  type        = string
  default     = "us-east-1"
}

variable "cluster_name" {
  description = "EKS cluster name"
  type        = string
  default     = "decisionbox-prod"
}

# Networking - VPC
variable "create_vpc" {
  description = "Create a new VPC. Set to false to use an existing VPC."
  type        = bool
  default     = true
}

variable "existing_vpc_id" {
  description = "ID of an existing VPC to use. Required when create_vpc is false."
  type        = string
  default     = ""
}

variable "existing_private_subnet_ids" {
  description = "List of existing private subnet IDs. Required when create_vpc is false."
  type        = list(string)
  default     = []
}

variable "existing_public_subnet_ids" {
  description = "List of existing public subnet IDs (for load balancers). Required when create_vpc is false."
  type        = list(string)
  default     = []
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

variable "availability_zones" {
  description = "List of availability zones. Defaults to first 3 AZs in the region."
  type        = list(string)
  default     = []
}

variable "private_subnet_cidrs" {
  description = "CIDR blocks for private subnets (one per AZ, for EKS nodes)"
  type        = list(string)
  default     = ["10.0.0.0/19", "10.0.32.0/19", "10.0.64.0/19"]
}

variable "public_subnet_cidrs" {
  description = "CIDR blocks for public subnets (one per AZ, for NAT/LB)"
  type        = list(string)
  default     = ["10.0.96.0/22", "10.0.100.0/22", "10.0.104.0/22"]
}

variable "single_nat_gateway" {
  description = "Use a single NAT Gateway (cost-effective). Set to false for one per AZ (HA)."
  type        = bool
  default     = true
}

variable "enable_flow_logs" {
  description = "Enable VPC flow logs to CloudWatch"
  type        = bool
  default     = true
}

variable "flow_log_retention_days" {
  description = "CloudWatch log group retention for VPC flow logs"
  type        = number
  default     = 30
}

# EKS - cluster
variable "create_cluster" {
  description = "Create a new EKS cluster. Set to false to use an existing cluster (only IAM will be created)."
  type        = bool
  default     = true
}

variable "kubernetes_version" {
  description = "Kubernetes version for EKS"
  type        = string
  default     = "1.31"
}

variable "endpoint_private_access" {
  description = "Enable private API server endpoint"
  type        = bool
  default     = true
}

variable "endpoint_public_access" {
  description = "Enable public API server endpoint"
  type        = bool
  default     = true
}

variable "public_access_cidrs" {
  description = "CIDR blocks allowed to access the public API server endpoint"
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "log_retention_days" {
  description = "CloudWatch log group retention in days for EKS control plane logs"
  type        = number
  default     = 90
}

variable "enabled_cluster_log_types" {
  description = "EKS control plane log types to enable"
  type        = list(string)
  default     = ["api", "audit", "authenticator", "controllerManager", "scheduler"]
}

# EKS - node group
variable "instance_type" {
  description = "EC2 instance type for EKS nodes"
  type        = string
  default     = "t3.large"
}

variable "disk_size_gb" {
  description = "EBS volume size in GB for EKS nodes"
  type        = number
  default     = 50
}

variable "min_node_count" {
  description = "Minimum number of nodes in the node group"
  type        = number
  default     = 1
}

variable "max_node_count" {
  description = "Maximum number of nodes in the node group"
  type        = number
  default     = 3
}

variable "desired_node_count" {
  description = "Desired number of nodes in the node group"
  type        = number
  default     = 2
}

variable "ami_type" {
  description = "AMI type for EKS nodes"
  type        = string
  default     = "AL2023_x86_64_STANDARD"
}

# IRSA (IAM Roles for Service Accounts)
variable "k8s_namespace" {
  description = "Kubernetes namespace for IRSA binding"
  type        = string
  default     = "decisionbox"
}

variable "k8s_service_account" {
  description = "Kubernetes service account name for IRSA binding"
  type        = string
  default     = "decisionbox-api"
}

variable "k8s_agent_service_account" {
  description = "Kubernetes service account name for the agent IRSA binding"
  type        = string
  default     = "decisionbox-agent"
}

# Optional: AWS Secrets Manager
variable "enable_aws_secrets" {
  description = "Grant the IRSA role permission to manage secrets in AWS Secrets Manager, scoped to the secret_namespace prefix."
  type        = bool
  default     = false
}

variable "secret_namespace" {
  description = "Namespace prefix for AWS Secrets Manager secrets (e.g., decisionbox). The API creates secrets named {namespace}/{projectID}/{key}."
  type        = string
  default     = "decisionbox"
}

# Optional: Bedrock IAM
variable "enable_bedrock_iam" {
  description = "Grant Bedrock InvokeModel access to the agent IRSA role"
  type        = bool
  default     = false
}

# Optional: Redshift IAM
variable "enable_redshift_iam" {
  description = "Grant Redshift read access to the IRSA role"
  type        = bool
  default     = false
}

# Tags
variable "tags" {
  description = "Tags to apply to all resources"
  type        = map(string)
  default     = {}
}
