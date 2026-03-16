output "cluster_name" {
  description = "GKE cluster name"
  value       = module.decisionbox.cluster_name
}

output "cluster_endpoint" {
  description = "GKE cluster endpoint"
  value       = module.decisionbox.cluster_endpoint
  sensitive   = true
}

output "cluster_ca_certificate" {
  description = "GKE cluster CA certificate"
  value       = module.decisionbox.cluster_ca_certificate
  sensitive   = true
}

output "vpc_name" {
  description = "VPC network name"
  value       = module.decisionbox.vpc_name
}

output "gke_node_sa_email" {
  description = "GKE node service account email"
  value       = module.decisionbox.gke_node_sa_email
}

output "workload_identity_sa_email" {
  description = "Workload Identity service account email"
  value       = module.decisionbox.workload_identity_sa_email
}

output "gcp_secrets_iam_enabled" {
  description = "Whether GCP Secret Manager IAM was granted"
  value       = module.decisionbox.gcp_secrets_iam_enabled
}

output "bigquery_iam_enabled" {
  description = "Whether BigQuery IAM was enabled"
  value       = module.decisionbox.bigquery_iam_enabled
}
