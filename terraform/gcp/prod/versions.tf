terraform {
  required_version = ">= 1.5"

  # Backend configured via -backend-config flags during terraform init.
  # Run setup.sh or pass manually:
  #   terraform init -backend-config="bucket=<BUCKET>" -backend-config="prefix=<ENV>"
  backend "gcs" {}

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0, < 7.0"
    }
    google-beta = {
      source  = "hashicorp/google-beta"
      version = ">= 5.0, < 7.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}
