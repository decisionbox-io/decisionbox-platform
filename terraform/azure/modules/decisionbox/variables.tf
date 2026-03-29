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
  description = "Name of the resource group. Created if create_resource_group is true."
  type        = string
  default     = "decisionbox-prod-rg"
}

# Resource Group
variable "create_resource_group" {
  description = "Create a new resource group. Set to false to use an existing one."
  type        = bool
  default     = true
}

# Networking - VNet
variable "create_vnet" {
  description = "Create a new VNet. Set to false to use an existing VNet."
  type        = bool
  default     = true
}

variable "existing_vnet_id" {
  description = "Resource ID of an existing VNet to use. Required when create_vnet is false."
  type        = string
  default     = ""

  validation {
    condition     = var.existing_vnet_id == "" || can(regex("^/subscriptions/", var.existing_vnet_id))
    error_message = "existing_vnet_id must be a full Azure resource ID."
  }
}

variable "existing_subnet_id" {
  description = "Resource ID of an existing subnet to use for AKS nodes. Required when create_vnet is false."
  type        = string
  default     = ""

  validation {
    condition     = var.existing_subnet_id == "" || can(regex("^/subscriptions/", var.existing_subnet_id))
    error_message = "existing_subnet_id must be a full Azure resource ID."
  }
}

# Networking
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

# Networking - NSG
variable "enable_nsg" {
  description = "Create a Network Security Group for the AKS subnet"
  type        = bool
  default     = true
}

variable "nsg_allowed_ssh_cidrs" {
  description = "CIDR blocks allowed to SSH to nodes (empty = no SSH access)"
  type        = list(string)
  default     = []
}

# IP Allowlisting
variable "allowed_ip_ranges" {
  description = "CIDR blocks allowed to access HTTP/HTTPS services. Empty list allows all traffic from the Internet (no restriction)."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for cidr in var.allowed_ip_ranges : can(cidrhost(cidr, 0))])
    error_message = "Each entry in allowed_ip_ranges must be a valid CIDR block (e.g., 203.0.113.0/24)."
  }
}

# Networking - NAT Gateway
variable "enable_nat_gateway" {
  description = "Create a NAT Gateway for outbound internet access from private nodes"
  type        = bool
  default     = true
}

# AKS - cluster
variable "create_cluster" {
  description = "Create a new AKS cluster. Set to false to use an existing cluster (only IAM will be created)."
  type        = bool
  default     = true
}

variable "kubernetes_version" {
  description = "Kubernetes version for the AKS cluster. Null uses the latest stable version."
  type        = string
  default     = null
}

variable "sku_tier" {
  description = "AKS SKU tier (Free or Standard). Standard includes uptime SLA."
  type        = string
  default     = "Free"

  validation {
    condition     = contains(["Free", "Standard"], var.sku_tier)
    error_message = "sku_tier must be Free or Standard."
  }
}

variable "private_cluster_enabled" {
  description = "Enable private cluster (API server not accessible from public internet)"
  type        = bool
  default     = false
}

# NOTE: The default allows all IPs for quick-start convenience.
# For production, restrict this to your office/VPN CIDR blocks.
variable "api_server_authorized_ranges" {
  description = "CIDR blocks authorized to access the AKS API server. Empty list allows all."
  type        = list(string)
  default     = []
}

variable "network_plugin" {
  description = "AKS network plugin (azure or kubenet)"
  type        = string
  default     = "azure"
}

variable "network_policy" {
  description = "AKS network policy (azure, calico, or null to disable)"
  type        = string
  default     = "azure"
}

variable "enable_azure_policy" {
  description = "Enable Azure Policy add-on for AKS"
  type        = bool
  default     = false
}

variable "enable_web_app_routing" {
  description = "Enable Web App Routing add-on for AKS ingress"
  type        = bool
  default     = true
}

variable "enable_oms_agent" {
  description = "Enable OMS agent for Azure Monitor Container Insights"
  type        = bool
  default     = true
}

variable "log_analytics_workspace_id" {
  description = "Resource ID of an existing Log Analytics workspace. Created automatically if empty and enable_oms_agent is true."
  type        = string
  default     = ""
}

# AKS - default node pool
variable "vm_size" {
  description = "VM size for AKS default node pool"
  type        = string
  default     = "Standard_D2s_v5"
}

variable "os_disk_size_gb" {
  description = "OS disk size in GB for AKS nodes"
  type        = number
  default     = 50
}

variable "os_disk_type" {
  description = "OS disk type for AKS nodes (Managed, Ephemeral)"
  type        = string
  default     = "Managed"
}

variable "min_node_count" {
  description = "Minimum number of nodes in the default pool"
  type        = number
  default     = 3
}

variable "max_node_count" {
  description = "Maximum number of nodes in the default pool"
  type        = number
  default     = 3
}

variable "node_count" {
  description = "Initial number of nodes in the default pool"
  type        = number
  default     = 3
}

variable "enable_auto_scaling" {
  description = "Enable auto-scaling for the default node pool"
  type        = bool
  default     = true
}

variable "max_pods_per_node" {
  description = "Maximum number of pods per node"
  type        = number
  default     = 50
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

# Optional: Azure Key Vault
variable "enable_key_vault" {
  description = "Create an Azure Key Vault and grant the API managed identity access to manage secrets."
  type        = bool
  default     = false
}

variable "key_vault_sku" {
  description = "SKU for Azure Key Vault (standard or premium)"
  type        = string
  default     = "standard"
}

variable "key_vault_soft_delete_retention_days" {
  description = "Number of days to retain soft-deleted Key Vault secrets"
  type        = number
  default     = 7
}

variable "key_vault_purge_protection" {
  description = "Enable purge protection on the Key Vault. Set to true for production to prevent permanent deletion during the retention period."
  type        = bool
  default     = false
}

# Tags
variable "tags" {
  description = "Tags to apply to all resources"
  type        = map(string)
  default     = {}
}
