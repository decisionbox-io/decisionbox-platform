output "cluster_name" {
  description = "GKE cluster name"
  value       = local.cluster_name
}

output "cluster_endpoint" {
  description = "GKE cluster endpoint"
  value       = local.cluster_endpoint
  sensitive   = true
}

output "cluster_ca_certificate" {
  description = "GKE cluster CA certificate"
  value       = local.cluster_ca_certificate
  sensitive   = true
}

output "vpc_name" {
  description = "VPC network name"
  value       = local.vpc_name
}

output "gke_node_sa_email" {
  description = "GKE node service account email (empty if using existing cluster)"
  value       = var.create_cluster ? google_service_account.gke_nodes[0].email : ""
}

output "workload_identity_sa_email" {
  description = "Workload Identity service account email"
  value       = google_service_account.workload_identity.email
}

output "gcp_secrets_iam_enabled" {
  description = "Whether GCP Secret Manager IAM was granted"
  value       = var.enable_gcp_secrets
}

output "bigquery_iam_enabled" {
  description = "Whether BigQuery IAM was enabled"
  value       = var.enable_bigquery_iam
}
