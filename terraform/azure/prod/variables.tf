variable "subscription_id" {
  description = "Azure subscription ID"
  type        = string
}

variable "location" {
  description = "Azure region"
  type        = string
  default     = "eastus"
}

variable "cluster_name" {
  description = "AKS cluster name"
  type        = string
  default     = "decisionbox-prod"
}

variable "resource_group_name" {
  description = "Resource group name"
  type        = string
  default     = "decisionbox-prod-rg"
}

# Networking
variable "create_vnet" {
  description = "Create a new VNet. Set to false to use an existing VNet."
  type        = bool
  default     = true
}

variable "existing_vnet_id" {
  description = "Resource ID of an existing VNet to use. Required when create_vnet is false."
  type        = string
  default     = ""
}

variable "existing_subnet_id" {
  description = "Resource ID of an existing subnet to use. Required when create_vnet is false."
  type        = string
  default     = ""
}

variable "vnet_cidr" {
  description = "CIDR range for the VNet"
  type        = string
  default     = "10.0.0.0/16"
}

variable "node_subnet_cidr" {
  description = "CIDR range for the AKS node subnet"
  type        = string
  default     = "10.0.0.0/20"
}

variable "enable_nat_gateway" {
  description = "Create a NAT Gateway for outbound internet access"
  type        = bool
  default     = true
}

# AKS
variable "create_cluster" {
  description = "Create a new AKS cluster. Set to false to use an existing cluster."
  type        = bool
  default     = true
}

variable "kubernetes_version" {
  description = "Kubernetes version for AKS. Null uses latest stable."
  type        = string
  default     = null
}

variable "sku_tier" {
  description = "AKS SKU tier (Free or Standard)"
  type        = string
  default     = "Free"
}

variable "vm_size" {
  description = "VM size for AKS nodes"
  type        = string
  default     = "Standard_D2s_v5"
}

variable "os_disk_size_gb" {
  description = "OS disk size in GB for AKS nodes"
  type        = number
  default     = 50
}

variable "min_node_count" {
  description = "Minimum number of nodes"
  type        = number
  default     = 3
}

variable "max_node_count" {
  description = "Maximum number of nodes"
  type        = number
  default     = 3
}

variable "api_server_authorized_ranges" {
  description = "CIDR blocks authorized to access the AKS API server"
  type        = list(string)
  default     = []
}

variable "allowed_ip_ranges" {
  description = "CIDR blocks allowed to access HTTP/HTTPS services. Empty list allows all traffic."
  type        = list(string)
  default     = []
}

# Workload Identity
variable "k8s_namespace" {
  description = "Kubernetes namespace for Workload Identity binding"
  type        = string
  default     = "decisionbox"
}

variable "k8s_service_account" {
  description = "Kubernetes service account name for API Workload Identity binding"
  type        = string
  default     = "decisionbox-api"
}

variable "k8s_agent_service_account" {
  description = "Kubernetes service account name for Agent Workload Identity binding (read-only access)"
  type        = string
  default     = "decisionbox-agent"
}

# Secrets
variable "secret_namespace" {
  description = "Namespace prefix for secrets stored in Key Vault or MongoDB to avoid naming conflicts across deployments"
  type        = string
  default     = "decisionbox"
}

# Observability
variable "enable_oms_agent" {
  description = "Enable OMS agent for Azure Monitor Container Insights"
  type        = bool
  default     = true
}

# Optional
variable "enable_key_vault" {
  description = "Create an Azure Key Vault for secret storage"
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
