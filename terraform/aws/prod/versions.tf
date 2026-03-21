terraform {
  required_version = ">= 1.5"

  # Backend configured via -backend-config flags during terraform init.
  # Run setup.sh or pass manually:
  #   terraform init -backend-config="bucket=<BUCKET>" -backend-config="key=<ENV>/terraform.tfstate" -backend-config="region=<REGION>" -backend-config="use_lockfile=true"
  backend "s3" {}

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0, < 7.0"
    }
  }
}

provider "aws" {
  region = var.region
}
