module "decisionbox" {
  source = "../modules/decisionbox"

  subscription_id     = var.subscription_id
  location            = var.location
  cluster_name        = var.cluster_name
  resource_group_name = var.resource_group_name

  # Networking
  create_vnet        = var.create_vnet
  existing_vnet_id   = var.existing_vnet_id
  existing_subnet_id = var.existing_subnet_id
  vnet_cidr          = var.vnet_cidr
  node_subnet_cidr   = var.node_subnet_cidr
  enable_nat_gateway = var.enable_nat_gateway

  # AKS
  create_cluster               = var.create_cluster
  kubernetes_version           = var.kubernetes_version
  sku_tier                     = var.sku_tier
  vm_size                      = var.vm_size
  os_disk_size_gb              = var.os_disk_size_gb
  min_node_count               = var.min_node_count
  max_node_count               = var.max_node_count
  api_server_authorized_ranges = var.api_server_authorized_ranges
  allowed_ip_ranges            = var.allowed_ip_ranges

  # Workload Identity
  k8s_namespace             = var.k8s_namespace
  k8s_service_account       = var.k8s_service_account
  k8s_agent_service_account = var.k8s_agent_service_account

  # Observability
  enable_oms_agent = var.enable_oms_agent

  # Optional
  enable_key_vault = var.enable_key_vault

  tags = var.tags
}
