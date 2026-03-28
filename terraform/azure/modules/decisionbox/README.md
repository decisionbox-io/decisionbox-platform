# DecisionBox Azure Module

Terraform module that provisions Azure infrastructure for running DecisionBox on AKS.

## Resources Created

- Resource Group
- VNet + Subnet with NSG
- NAT Gateway for outbound internet
- AKS cluster with Workload Identity
- Managed Identities (API + Agent) with federated credentials
- Azure Key Vault with RBAC (optional)
- Log Analytics workspace for Container Insights (optional)

## Usage

```hcl
module "decisionbox" {
  source = "../modules/decisionbox"

  subscription_id     = "your-subscription-id"
  location            = "eastus"
  cluster_name        = "decisionbox-prod"
  resource_group_name = "decisionbox-prod-rg"

  vm_size        = "Standard_D2s_v5"
  min_node_count = 1
  max_node_count = 3

  k8s_namespace    = "decisionbox"
  enable_key_vault = true

  tags = {
    project     = "decisionbox"
    environment = "prod"
    managed_by  = "terraform"
  }
}
```

## Requirements

| Name | Version |
|------|---------|
| terraform | >= 1.5 |
| azurerm | >= 3.80, < 5.0 |
| azuread | >= 2.47, < 4.0 |
