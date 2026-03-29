data "azurerm_kubernetes_cluster" "existing" {
  count               = var.create_cluster ? 0 : 1
  name                = var.cluster_name
  resource_group_name = local.resource_group_name
}

# Log Analytics workspace for Container Insights (created if not provided)
resource "azurerm_log_analytics_workspace" "aks" {
  count               = var.create_cluster && var.enable_oms_agent && var.log_analytics_workspace_id == "" ? 1 : 0
  name                = "${var.cluster_name}-logs"
  resource_group_name = local.resource_group_name
  location            = var.location
  sku                 = "PerGB2018"
  retention_in_days   = 30
  tags                = local.all_tags
}

resource "azurerm_kubernetes_cluster" "main" {
  count               = var.create_cluster ? 1 : 0
  name                = var.cluster_name
  resource_group_name = local.resource_group_name
  location            = var.location
  dns_prefix          = var.cluster_name
  kubernetes_version  = var.kubernetes_version
  sku_tier            = var.sku_tier

  private_cluster_enabled = var.private_cluster_enabled

  dynamic "api_server_access_profile" {
    for_each = length(var.api_server_authorized_ranges) > 0 ? [1] : []
    content {
      authorized_ip_ranges = var.api_server_authorized_ranges
    }
  }

  default_node_pool {
    name                 = "default"
    vm_size              = var.vm_size
    os_disk_size_gb      = var.os_disk_size_gb
    os_disk_type         = var.os_disk_type
    vnet_subnet_id       = local.subnet_id
    node_count           = var.enable_auto_scaling ? null : var.node_count
    min_count            = var.enable_auto_scaling ? var.min_node_count : null
    max_count            = var.enable_auto_scaling ? var.max_node_count : null
    max_pods             = var.max_pods_per_node
    type                 = "VirtualMachineScaleSets"
    auto_scaling_enabled = var.enable_auto_scaling
    tags                 = local.all_tags

    upgrade_settings {
      max_surge = "10%"
    }
  }

  identity {
    type = "SystemAssigned"
  }

  # Workload Identity + OIDC issuer (required for federated credentials)
  oidc_issuer_enabled       = true
  workload_identity_enabled = true

  network_profile {
    network_plugin = var.network_plugin
    network_policy = var.network_policy
    outbound_type  = var.enable_nat_gateway && var.create_vnet ? "userAssignedNATGateway" : "loadBalancer"
    service_cidr   = "10.96.0.0/16"
    dns_service_ip = "10.96.0.10"
  }

  dynamic "oms_agent" {
    for_each = var.enable_oms_agent ? [1] : []
    content {
      log_analytics_workspace_id = local.log_analytics_workspace_id
    }
  }

  azure_policy_enabled = var.enable_azure_policy

  dynamic "web_app_routing" {
    for_each = var.enable_web_app_routing ? [1] : []
    content {
      dns_zone_ids = []
    }
  }

  tags = local.all_tags

  depends_on = [
    azurerm_subnet_nat_gateway_association.nodes,
  ]
}

# Grant AKS managed identity Network Contributor on the subnet (required for Azure CNI)
resource "azurerm_role_assignment" "aks_network" {
  count                = var.create_cluster && var.create_vnet ? 1 : 0
  scope                = azurerm_subnet.nodes[0].id
  role_definition_name = "Network Contributor"
  principal_id         = azurerm_kubernetes_cluster.main[0].identity[0].principal_id
}

locals {
  log_analytics_workspace_id = var.log_analytics_workspace_id != "" ? var.log_analytics_workspace_id : (
    var.create_cluster && var.enable_oms_agent ? azurerm_log_analytics_workspace.aks[0].id : ""
  )

  cluster_name = var.create_cluster ? azurerm_kubernetes_cluster.main[0].name : data.azurerm_kubernetes_cluster.existing[0].name
  cluster_fqdn = var.create_cluster ? azurerm_kubernetes_cluster.main[0].fqdn : data.azurerm_kubernetes_cluster.existing[0].fqdn

  cluster_kube_config = var.create_cluster ? azurerm_kubernetes_cluster.main[0].kube_config[0] : data.azurerm_kubernetes_cluster.existing[0].kube_config[0]

  oidc_issuer_url = var.create_cluster ? azurerm_kubernetes_cluster.main[0].oidc_issuer_url : data.azurerm_kubernetes_cluster.existing[0].oidc_issuer_url
}
