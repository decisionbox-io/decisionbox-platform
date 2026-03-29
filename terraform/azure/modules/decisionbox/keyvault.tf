# Azure Key Vault for per-project secret storage.
# The API creates and manages secrets at runtime (not Terraform).
# Terraform only creates the vault and grants IAM access.

resource "azurerm_key_vault" "main" {
  count               = var.enable_key_vault ? 1 : 0
  name                = substr(replace("${var.cluster_name}-kv", "_", "-"), 0, 24)
  resource_group_name = local.resource_group_name
  location            = var.location
  tenant_id           = data.azurerm_client_config.current.tenant_id
  sku_name            = var.key_vault_sku

  # Soft delete is mandatory on new vaults; configure retention
  soft_delete_retention_days = var.key_vault_soft_delete_retention_days
  purge_protection_enabled   = var.key_vault_purge_protection

  # Use Azure RBAC for access control (not access policies)
  rbac_authorization_enabled = true

  tags = local.all_tags
}

# API managed identity: Key Vault Secrets Officer (create, read, update, list, delete)
resource "azurerm_role_assignment" "api_keyvault" {
  count                = var.enable_key_vault ? 1 : 0
  scope                = azurerm_key_vault.main[0].id
  role_definition_name = "Key Vault Secrets Officer"
  principal_id         = azurerm_user_assigned_identity.api.principal_id
}

# Agent managed identity: Key Vault Secrets User (read-only: get, list)
resource "azurerm_role_assignment" "agent_keyvault" {
  count                = var.enable_key_vault ? 1 : 0
  scope                = azurerm_key_vault.main[0].id
  role_definition_name = "Key Vault Secrets User"
  principal_id         = azurerm_user_assigned_identity.agent.principal_id
}

# Grant the Terraform operator access to manage the vault (needed for initial setup)
resource "azurerm_role_assignment" "operator_keyvault" {
  count                = var.enable_key_vault ? 1 : 0
  scope                = azurerm_key_vault.main[0].id
  role_definition_name = "Key Vault Administrator"
  principal_id         = data.azurerm_client_config.current.object_id
}
