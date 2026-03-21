output "cluster_name" {
  description = "EKS cluster name"
  value       = module.decisionbox.cluster_name
}

output "cluster_endpoint" {
  description = "EKS cluster endpoint"
  value       = module.decisionbox.cluster_endpoint
  sensitive   = true
}

output "cluster_ca_certificate" {
  description = "EKS cluster CA certificate"
  value       = module.decisionbox.cluster_ca_certificate
  sensitive   = true
}

output "vpc_id" {
  description = "VPC ID"
  value       = module.decisionbox.vpc_id
}

output "irsa_role_arn" {
  description = "IRSA role ARN for DecisionBox API"
  value       = module.decisionbox.irsa_role_arn
}

output "irsa_agent_role_arn" {
  description = "IRSA role ARN for DecisionBox Agent"
  value       = module.decisionbox.irsa_agent_role_arn
}

output "lb_controller_role_arn" {
  description = "IAM role ARN for AWS Load Balancer Controller"
  value       = module.decisionbox.lb_controller_role_arn
}

output "aws_secrets_iam_enabled" {
  description = "Whether AWS Secrets Manager IAM was granted"
  value       = module.decisionbox.aws_secrets_iam_enabled
}

output "redshift_iam_enabled" {
  description = "Whether Redshift IAM was enabled"
  value       = module.decisionbox.redshift_iam_enabled
}
