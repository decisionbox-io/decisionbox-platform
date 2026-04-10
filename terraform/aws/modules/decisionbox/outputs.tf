output "cluster_name" {
  description = "EKS cluster name"
  value       = local.cluster_name
}

output "cluster_endpoint" {
  description = "EKS cluster endpoint"
  value       = local.cluster_endpoint
  sensitive   = true
}

output "cluster_ca_certificate" {
  description = "EKS cluster CA certificate (base64)"
  value       = local.cluster_ca
  sensitive   = true
}

output "vpc_id" {
  description = "VPC ID"
  value       = local.vpc_id
}

output "private_subnet_ids" {
  description = "Private subnet IDs"
  value       = local.private_subnet_ids
}

output "public_subnet_ids" {
  description = "Public subnet IDs"
  value       = local.public_subnet_ids
}

output "irsa_role_arn" {
  description = "IRSA role ARN for the DecisionBox API service account"
  value       = aws_iam_role.irsa_api.arn
}

output "irsa_agent_role_arn" {
  description = "IRSA role ARN for the DecisionBox Agent service account"
  value       = aws_iam_role.irsa_agent.arn
}

output "oidc_provider_arn" {
  description = "OIDC provider ARN for IRSA"
  value       = aws_iam_openid_connect_provider.eks.arn
}

output "lb_controller_role_arn" {
  description = "IAM role ARN for AWS Load Balancer Controller"
  value       = var.create_cluster ? aws_iam_role.lb_controller[0].arn : ""
}

output "aws_secrets_iam_enabled" {
  description = "Whether AWS Secrets Manager IAM was granted"
  value       = var.enable_aws_secrets
}

output "redshift_iam_enabled" {
  description = "Whether Redshift IAM was enabled"
  value       = var.enable_redshift_iam
}
