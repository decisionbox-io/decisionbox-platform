module "decisionbox" {
  source = "../modules/decisionbox"

  project_id   = var.project_id
  region       = var.region
  cluster_name = var.cluster_name

  # Networking
  create_vpc         = var.create_vpc
  existing_vpc_id    = var.existing_vpc_id
  existing_subnet_id = var.existing_subnet_id
  subnet_cidr        = var.subnet_cidr
  pods_cidr          = var.pods_cidr
  services_cidr      = var.services_cidr

  # GKE
  create_cluster = var.create_cluster
  machine_type   = var.machine_type
  disk_size_gb   = var.disk_size_gb
  min_node_count = var.min_node_count
  max_node_count = var.max_node_count
  master_cidr    = var.master_cidr

  # Workload Identity
  k8s_namespace       = var.k8s_namespace
  k8s_service_account = var.k8s_service_account

  # Optional
  enable_gcp_secrets   = var.enable_gcp_secrets
  secret_namespace     = var.secret_namespace
  enable_bigquery_iam  = var.enable_bigquery_iam
  enable_vertex_ai_iam = var.enable_vertex_ai_iam

  labels = var.labels
}
