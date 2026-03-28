data "azurerm_client_config" "current" {}

# Managed Identity for DecisionBox API (Workload Identity)
resource "azurerm_user_assigned_identity" "api" {
  name                = "${var.cluster_name}-api"
  resource_group_name = local.resource_group_name
  location            = var.location
  tags                = local.all_tags
}

# Federated credential: K8s service account → Azure managed identity (API)
resource "azurerm_federated_identity_credential" "api" {
  name                = "${var.cluster_name}-api"
  resource_group_name = local.resource_group_name
  parent_id           = azurerm_user_assigned_identity.api.id
  audience            = ["api://AzureADTokenExchange"]
  issuer              = local.oidc_issuer_url
  subject             = "system:serviceaccount:${var.k8s_namespace}:${var.k8s_service_account}"
}

# Managed Identity for DecisionBox Agent (Workload Identity, read-only)
resource "azurerm_user_assigned_identity" "agent" {
  name                = "${var.cluster_name}-agent"
  resource_group_name = local.resource_group_name
  location            = var.location
  tags                = local.all_tags
}

# Federated credential: K8s service account → Azure managed identity (Agent)
resource "azurerm_federated_identity_credential" "agent" {
  name                = "${var.cluster_name}-agent"
  resource_group_name = local.resource_group_name
  parent_id           = azurerm_user_assigned_identity.agent.id
  audience            = ["api://AzureADTokenExchange"]
  issuer              = local.oidc_issuer_url
  subject             = "system:serviceaccount:${var.k8s_namespace}:${var.k8s_agent_service_account}"
}
