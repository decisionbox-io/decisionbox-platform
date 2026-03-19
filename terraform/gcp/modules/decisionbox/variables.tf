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

# Networking - VPC
variable "create_vpc" {
  description = "Create a new VPC. Set to false to use an existing VPC."
  type        = bool
  default     = true
}

variable "existing_vpc_id" {
  description = "Self-link of an existing VPC to use. Required when create_vpc is false."
  type        = string
  default     = ""

  validation {
    condition     = var.existing_vpc_id == "" || can(regex("^projects/", var.existing_vpc_id))
    error_message = "existing_vpc_id must be a VPC self-link (projects/<project>/global/networks/<name>)."
  }
}

variable "existing_subnet_id" {
  description = "Self-link of an existing subnet to use. Required when create_vpc is false. Must have secondary ranges for pods and services."
  type        = string
  default     = ""

  validation {
    condition     = var.existing_subnet_id == "" || can(regex("^projects/", var.existing_subnet_id))
    error_message = "existing_subnet_id must be a subnet self-link (projects/<project>/regions/<region>/subnetworks/<name>)."
  }
}

# Networking
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

variable "pods_range_name" {
  description = "Name of the secondary IP range for pods"
  type        = string
  default     = "pods"
}

variable "services_range_name" {
  description = "Name of the secondary IP range for services"
  type        = string
  default     = "services"
}

# Networking - flow logs
variable "enable_flow_logs" {
  description = "Enable VPC flow logs on the GKE subnet"
  type        = bool
  default     = true
}

variable "flow_log_interval" {
  description = "VPC flow log aggregation interval"
  type        = string
  default     = "INTERVAL_10_MIN"
}

variable "flow_log_sampling" {
  description = "VPC flow log sampling rate (0.0 to 1.0)"
  type        = number
  default     = 0.5
}

variable "flow_log_metadata" {
  description = "VPC flow log metadata inclusion"
  type        = string
  default     = "INCLUDE_ALL_METADATA"
}

# Networking - NAT
variable "nat_ip_allocate_option" {
  description = "How external IPs are allocated for Cloud NAT"
  type        = string
  default     = "AUTO_ONLY"
}

variable "nat_source_subnetwork_ip_ranges" {
  description = "Which subnetwork IP ranges to NAT"
  type        = string
  default     = "ALL_SUBNETWORKS_ALL_IP_RANGES"
}

variable "enable_nat_logging" {
  description = "Enable Cloud NAT logging"
  type        = bool
  default     = true
}

variable "nat_log_filter" {
  description = "Cloud NAT log filter"
  type        = string
  default     = "ERRORS_ONLY"
}

# Firewall
# NOTE: These are convenience defaults for quick-start. For production
# deployments, narrow these to only the ports your services actually use.
variable "internal_tcp_ports" {
  description = "TCP ports allowed for internal traffic"
  type        = list(string)
  default     = ["0-65535"]
}

variable "internal_udp_ports" {
  description = "UDP ports allowed for internal traffic"
  type        = list(string)
  default     = ["0-65535"]
}

variable "health_check_ports" {
  description = "TCP ports allowed for GCP health checks"
  type        = list(string)
  default     = ["80", "443", "3000", "8080", "10256"]
}

variable "health_check_source_ranges" {
  description = "Source IP ranges for GCP health checks"
  type        = list(string)
  default     = ["35.191.0.0/16", "130.211.0.0/22"]
}

# GKE - cluster
variable "deletion_protection" {
  description = "Enable deletion protection on the GKE cluster. Set to false for dev/sandbox environments."
  type        = bool
  default     = true
}

variable "create_cluster" {
  description = "Create a new GKE cluster. Set to false to use an existing cluster (only IAM and secrets will be created)."
  type        = bool
  default     = true
}

variable "master_cidr" {
  description = "CIDR block for the GKE master"
  type        = string
  default     = "172.16.0.0/28"
}

variable "enable_private_nodes" {
  description = "Enable private nodes (no public IPs)"
  type        = bool
  default     = true
}

variable "enable_private_endpoint" {
  description = "Enable private endpoint (master not accessible from public internet)"
  type        = bool
  default     = false
}

# NOTE: The default allows all IPs for quick-start convenience.
# For production, restrict this to your office/VPN CIDR blocks.
variable "master_authorized_networks" {
  description = "List of CIDR blocks authorized to access the GKE master"
  type = list(object({
    cidr_block   = string
    display_name = string
  }))
  default = [{
    cidr_block   = "0.0.0.0/0"
    display_name = "all"
  }]
}

variable "enable_network_policy" {
  description = "Enable Kubernetes network policy on the cluster"
  type        = bool
  default     = true
}

variable "network_policy_provider" {
  description = "Network policy provider (CALICO)"
  type        = string
  default     = "CALICO"
}

variable "enable_binary_authorization" {
  description = "Enable Binary Authorization on the cluster"
  type        = bool
  default     = false
}

variable "datapath_provider" {
  description = "Datapath provider for the cluster (ADVANCED_DATAPATH for Dataplane V2)"
  type        = string
  default     = "ADVANCED_DATAPATH"
}

variable "release_channel" {
  description = "GKE release channel"
  type        = string
  default     = "REGULAR"
}

variable "logging_components" {
  description = "GKE logging components to enable"
  type        = list(string)
  default     = ["SYSTEM_COMPONENTS", "WORKLOADS"]
}

variable "monitoring_components" {
  description = "GKE monitoring components to enable"
  type        = list(string)
  default     = ["SYSTEM_COMPONENTS"]
}

# GKE - node pool
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

variable "disk_type" {
  description = "Boot disk type for GKE nodes"
  type        = string
  default     = "pd-standard"
}

variable "image_type" {
  description = "Node image type for GKE nodes"
  type        = string
  default     = "COS_CONTAINERD"
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

variable "disable_legacy_metadata_endpoints" {
  description = "Disable legacy metadata endpoints on nodes"
  type        = string
  default     = "true"
}

variable "enable_secure_boot" {
  description = "Enable Secure Boot for shielded GKE nodes"
  type        = bool
  default     = true
}

variable "enable_integrity_monitoring" {
  description = "Enable integrity monitoring for shielded GKE nodes"
  type        = bool
  default     = true
}

variable "enable_auto_repair" {
  description = "Enable auto-repair for node pool"
  type        = bool
  default     = true
}

variable "enable_auto_upgrade" {
  description = "Enable auto-upgrade for node pool"
  type        = bool
  default     = true
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

# Optional: GCP Secret Manager
variable "enable_gcp_secrets" {
  description = "Grant the Workload Identity SA permission to manage secrets in GCP Secret Manager, scoped to the secret_namespace prefix."
  type        = bool
  default     = false
}

variable "secret_namespace" {
  description = "Namespace prefix for GCP Secret Manager secrets (e.g., decisionbox). The API creates secrets named {namespace}-{projectID}-{key}."
  type        = string
  default     = "decisionbox"
}

# Optional: BigQuery IAM
variable "enable_bigquery_iam" {
  description = "Grant BigQuery read access to the Workload Identity SA"
  type        = bool
  default     = false
}

# Labels
variable "labels" {
  description = "Labels to apply to all resources"
  type        = map(string)
  default     = {}
}
