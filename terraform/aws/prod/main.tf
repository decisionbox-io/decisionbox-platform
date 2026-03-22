module "decisionbox" {
  source = "../modules/decisionbox"

  region       = var.region
  cluster_name = var.cluster_name

  # Networking
  create_vpc                  = var.create_vpc
  existing_vpc_id             = var.existing_vpc_id
  existing_private_subnet_ids = var.existing_private_subnet_ids
  existing_public_subnet_ids  = var.existing_public_subnet_ids
  vpc_cidr                    = var.vpc_cidr

  # EKS
  create_cluster     = var.create_cluster
  instance_type      = var.instance_type
  disk_size_gb       = var.disk_size_gb
  min_node_count     = var.min_node_count
  max_node_count     = var.max_node_count
  desired_node_count = var.desired_node_count

  # IRSA
  k8s_namespace             = var.k8s_namespace
  k8s_service_account       = var.k8s_service_account
  k8s_agent_service_account = var.k8s_agent_service_account

  # Optional
  enable_aws_secrets  = var.enable_aws_secrets
  secret_namespace    = var.secret_namespace
  enable_bedrock_iam  = var.enable_bedrock_iam
  enable_redshift_iam = var.enable_redshift_iam

  tags = var.tags
}
