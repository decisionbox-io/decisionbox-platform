output "cluster_name" {
  description = "AKS cluster name"
  value       = local.cluster_name
}

output "cluster_fqdn" {
  description = "AKS cluster FQDN"
  value       = local.cluster_fqdn
}

output "kube_config_host" {
  description = "AKS cluster API server URL"
  value       = local.cluster_kube_config.host
  sensitive   = true
}

output "resource_group_name" {
  description = "Resource group name"
  value       = local.resource_group_name
}

output "vnet_name" {
  description = "VNet name"
  value       = var.create_vnet ? azurerm_virtual_network.main[0].name : ""
}

output "api_identity_client_id" {
  description = "Client ID of the API managed identity (for K8s service account annotation)"
  value       = azurerm_user_assigned_identity.api.client_id
}

output "agent_identity_client_id" {
  description = "Client ID of the Agent managed identity (for K8s service account annotation)"
  value       = azurerm_user_assigned_identity.agent.client_id
}

output "key_vault_uri" {
  description = "Azure Key Vault URI (empty if Key Vault is not enabled)"
  value       = var.enable_key_vault ? azurerm_key_vault.main[0].vault_uri : ""
}

output "key_vault_name" {
  description = "Azure Key Vault name (empty if Key Vault is not enabled)"
  value       = var.enable_key_vault ? azurerm_key_vault.main[0].name : ""
}

output "key_vault_enabled" {
  description = "Whether Azure Key Vault was created"
  value       = var.enable_key_vault
}

output "allowed_ip_ranges" {
  description = "Configured IP allowlist ranges"
  value       = var.allowed_ip_ranges
}

output "log_analytics_workspace_id" {
  description = "Log Analytics workspace ID (empty if not created)"
  value       = var.create_cluster && var.enable_oms_agent && var.log_analytics_workspace_id == "" ? azurerm_log_analytics_workspace.aks[0].id : var.log_analytics_workspace_id
}
