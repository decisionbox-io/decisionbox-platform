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

# Networking
variable "create_vpc" {
  description = "Create a new VPC. Set to false to use an existing VPC."
  type        = bool
  default     = true
}

variable "existing_vpc_id" {
  description = "ID of an existing VPC. Required when create_vpc is false."
  type        = string
  default     = ""
}

variable "existing_private_subnet_ids" {
  description = "List of existing private subnet IDs. Required when create_vpc is false."
  type        = list(string)
  default     = []
}

variable "existing_public_subnet_ids" {
  description = "List of existing public subnet IDs. Required when create_vpc is false."
  type        = list(string)
  default     = []
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

# EKS
variable "create_cluster" {
  description = "Create a new EKS cluster. Set to false to use existing."
  type        = bool
  default     = true
}

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
  description = "Minimum number of nodes"
  type        = number
  default     = 1
}

variable "max_node_count" {
  description = "Maximum number of nodes"
  type        = number
  default     = 3
}

variable "desired_node_count" {
  description = "Desired number of nodes"
  type        = number
  default     = 2
}

# IRSA
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

# Optional
variable "enable_aws_secrets" {
  description = "Grant Secrets Manager access to the IRSA role."
  type        = bool
  default     = false
}

variable "secret_namespace" {
  description = "Namespace prefix for Secrets Manager secrets."
  type        = string
  default     = "decisionbox"
}

variable "enable_bedrock_iam" {
  description = "Grant Bedrock InvokeModel access to the agent IRSA role."
  type        = bool
  default     = false
}

variable "enable_redshift_iam" {
  description = "Grant Redshift read access to the IRSA role."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Tags to apply to all resources"
  type        = map(string)
  default = {
    project     = "decisionbox"
    environment = "prod"
    managed_by  = "terraform"
  }
}
