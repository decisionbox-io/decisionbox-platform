terraform {
  required_version = ">= 1.5"

  # Backend configured via -backend-config flags during terraform init.
  # Run setup.sh or pass manually:
  #   terraform init \
  #     -backend-config="resource_group_name=<RG>" \
  #     -backend-config="storage_account_name=<ACCOUNT>" \
  #     -backend-config="container_name=terraform" \
  #     -backend-config="key=prod/terraform.tfstate"
  backend "azurerm" {}

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = ">= 3.80, < 5.0"
    }
  }
}

provider "azurerm" {
  subscription_id = var.subscription_id
  features {
    key_vault {
      purge_soft_delete_on_destroy = false
    }
  }
}
