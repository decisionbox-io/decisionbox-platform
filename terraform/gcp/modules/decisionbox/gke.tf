data "google_container_cluster" "existing" {
  count    = var.create_cluster ? 0 : 1
  name     = var.cluster_name
  location = var.region
  project  = var.project_id
}

resource "google_container_cluster" "primary" {
  count    = var.create_cluster ? 1 : 0
  name     = var.cluster_name
  project  = var.project_id
  location = var.region

  depends_on = [google_project_service.apis["container.googleapis.com"]]

  deletion_protection      = var.deletion_protection
  remove_default_node_pool = true
  initial_node_count       = 1

  network    = local.vpc_id
  subnetwork = local.subnet_id

  ip_allocation_policy {
    cluster_secondary_range_name  = var.pods_range_name
    services_secondary_range_name = var.services_range_name
  }

  private_cluster_config {
    enable_private_nodes    = var.enable_private_nodes
    enable_private_endpoint = var.enable_private_endpoint
    master_ipv4_cidr_block  = var.master_cidr
  }

  master_auth {
    client_certificate_config {
      issue_client_certificate = false
    }
  }

  master_authorized_networks_config {
    dynamic "cidr_blocks" {
      for_each = var.master_authorized_networks
      content {
        cidr_block   = cidr_blocks.value.cidr_block
        display_name = cidr_blocks.value.display_name
      }
    }
  }

  workload_identity_config {
    workload_pool = "${var.project_id}.svc.id.goog"
  }

  # Network policy is built-in with ADVANCED_DATAPATH (Dataplane V2).
  # Only enable Calico network policy when NOT using Dataplane V2.
  dynamic "network_policy" {
    for_each = var.datapath_provider != "ADVANCED_DATAPATH" ? [1] : []
    content {
      enabled  = var.enable_network_policy
      provider = var.enable_network_policy ? var.network_policy_provider : "PROVIDER_UNSPECIFIED"
    }
  }

  addons_config {
    network_policy_config {
      disabled = var.datapath_provider == "ADVANCED_DATAPATH" ? true : !var.enable_network_policy
    }
  }

  binary_authorization {
    evaluation_mode = var.enable_binary_authorization ? "PROJECT_SINGLETON_POLICY_ENFORCE" : "DISABLED"
  }

  datapath_provider = var.datapath_provider

  release_channel {
    channel = var.release_channel
  }

  logging_config {
    enable_components = var.logging_components
  }

  monitoring_config {
    enable_components = var.monitoring_components
  }

  resource_labels = var.labels
}

resource "google_container_node_pool" "primary" {
  count    = var.create_cluster ? 1 : 0
  name     = "${var.cluster_name}-pool"
  project  = var.project_id
  location = var.region
  cluster  = google_container_cluster.primary[0].name

  autoscaling {
    min_node_count = var.min_node_count
    max_node_count = var.max_node_count
  }

  node_config {
    machine_type    = var.machine_type
    disk_size_gb    = var.disk_size_gb
    disk_type       = var.disk_type
    image_type      = var.image_type
    service_account = google_service_account.gke_nodes[0].email

    oauth_scopes = [
      "https://www.googleapis.com/auth/cloud-platform",
    ]

    metadata = {
      disable-legacy-endpoints = var.disable_legacy_metadata_endpoints
    }

    workload_metadata_config {
      mode = "GKE_METADATA"
    }

    shielded_instance_config {
      enable_secure_boot          = var.enable_secure_boot
      enable_integrity_monitoring = var.enable_integrity_monitoring
    }

    labels = var.labels
  }

  management {
    auto_repair  = var.enable_auto_repair
    auto_upgrade = var.enable_auto_upgrade
  }
}

locals {
  cluster_name           = var.create_cluster ? google_container_cluster.primary[0].name : data.google_container_cluster.existing[0].name
  cluster_endpoint       = var.create_cluster ? google_container_cluster.primary[0].endpoint : data.google_container_cluster.existing[0].endpoint
  cluster_ca_certificate = var.create_cluster ? google_container_cluster.primary[0].master_auth[0].cluster_ca_certificate : data.google_container_cluster.existing[0].master_auth[0].cluster_ca_certificate
}
