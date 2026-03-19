variable "project_id" {
  description = "GCP project ID"
  type        = string
}

variable "region" {
  description = "GCP region"
  type        = string
  default     = "us-central1"
}

variable "cluster_name" {
  description = "GKE cluster name"
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
  description = "Self-link of an existing VPC to use. Required when create_vpc is false."
  type        = string
  default     = ""
}

variable "existing_subnet_id" {
  description = "Self-link of an existing subnet to use. Required when create_vpc is false. Must have secondary ranges for pods and services."
  type        = string
  default     = ""
}

variable "subnet_cidr" {
  description = "CIDR range for the GKE subnet"
  type        = string
  default     = "10.0.0.0/20"
}

variable "pods_cidr" {
  description = "CIDR range for GKE pods"
  type        = string
  default     = "10.4.0.0/14"
}

variable "services_cidr" {
  description = "CIDR range for GKE services"
  type        = string
  default     = "10.8.0.0/20"
}

variable "create_cluster" {
  description = "Create a new GKE cluster. Set to false to use an existing cluster."
  type        = bool
  default     = true
}

variable "machine_type" {
  description = "Machine type for GKE nodes"
  type        = string
  default     = "e2-standard-2"
}

variable "disk_size_gb" {
  description = "Boot disk size in GB for GKE nodes"
  type        = number
  default     = 50
}

variable "min_node_count" {
  description = "Minimum number of nodes per zone"
  type        = number
  default     = 1
}

variable "max_node_count" {
  description = "Maximum number of nodes per zone"
  type        = number
  default     = 2
}

variable "master_cidr" {
  description = "CIDR block for the GKE master"
  type        = string
  default     = "172.16.0.0/28"
}

variable "k8s_namespace" {
  description = "Kubernetes namespace for Workload Identity binding"
  type        = string
  default     = "decisionbox"
}

variable "k8s_service_account" {
  description = "Kubernetes service account name for Workload Identity binding"
  type        = string
  default     = "decisionbox-api"
}

variable "enable_gcp_secrets" {
  description = "Grant the Workload Identity SA permission to manage secrets in GCP Secret Manager."
  type        = bool
  default     = false
}

variable "secret_namespace" {
  description = "Namespace prefix for GCP Secret Manager secrets (e.g., decisionbox)."
  type        = string
  default     = "decisionbox"
}

variable "enable_bigquery_iam" {
  description = "Grant BigQuery read access to the agent SA"
  type        = bool
  default     = false
}

variable "enable_vertex_ai_iam" {
  description = "Grant Vertex AI access to the agent SA for LLM calls"
  type        = bool
  default     = false
}

variable "labels" {
  description = "Labels to apply to all resources"
  type        = map(string)
  default = {
    project     = "decisionbox"
    environment = "prod"
    managed_by  = "terraform"
  }
}
