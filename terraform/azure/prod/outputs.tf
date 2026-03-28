output "cluster_name" {
  description = "AKS cluster name"
  value       = module.decisionbox.cluster_name
}

output "cluster_fqdn" {
  description = "AKS cluster FQDN"
  value       = module.decisionbox.cluster_fqdn
}

output "kube_config_host" {
  description = "AKS cluster API server URL"
  value       = module.decisionbox.kube_config_host
  sensitive   = true
}

output "resource_group_name" {
  description = "Resource group name"
  value       = module.decisionbox.resource_group_name
}

output "vnet_name" {
  description = "VNet name"
  value       = module.decisionbox.vnet_name
}

output "api_identity_client_id" {
  description = "Client ID of the API managed identity"
  value       = module.decisionbox.api_identity_client_id
}

output "agent_identity_client_id" {
  description = "Client ID of the Agent managed identity"
  value       = module.decisionbox.agent_identity_client_id
}

output "key_vault_uri" {
  description = "Azure Key Vault URI"
  value       = module.decisionbox.key_vault_uri
}

output "key_vault_enabled" {
  description = "Whether Azure Key Vault was created"
  value       = module.decisionbox.key_vault_enabled
}
