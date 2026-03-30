resource "google_compute_network" "vpc" {
  count                   = var.create_vpc ? 1 : 0
  name                    = "${var.cluster_name}-vpc"
  project                 = var.project_id
  auto_create_subnetworks = false

  # GKE creates NEGs (network endpoint groups) for load balancers/ingress.
  # These are cleaned up asynchronously after the cluster is deleted.
  # The extended delete timeout gives GCP time to remove them before
  # Terraform tries to delete the VPC.
  timeouts {
    delete = "10m"
  }

  depends_on = [google_project_service.apis["compute.googleapis.com"]]
}

resource "google_compute_subnetwork" "gke_subnet" {
  count                    = var.create_vpc ? 1 : 0
  name                     = "${var.cluster_name}-subnet"
  project                  = var.project_id
  region                   = var.region
  network                  = google_compute_network.vpc[0].id
  ip_cidr_range            = var.subnet_cidr
  private_ip_google_access = true

  secondary_ip_range {
    range_name    = var.pods_range_name
    ip_cidr_range = var.pods_cidr
  }

  secondary_ip_range {
    range_name    = var.services_range_name
    ip_cidr_range = var.services_cidr
  }

  dynamic "log_config" {
    for_each = var.enable_flow_logs ? [1] : []
    content {
      aggregation_interval = var.flow_log_interval
      flow_sampling        = var.flow_log_sampling
      metadata             = var.flow_log_metadata
    }
  }

  # GKE NEGs reference subnets — allow time for async cleanup after cluster deletion
  timeouts {
    delete = "10m"
  }
}

resource "google_compute_router" "router" {
  count   = var.create_vpc ? 1 : 0
  name    = "${var.cluster_name}-router"
  project = var.project_id
  region  = var.region
  network = google_compute_network.vpc[0].id
}

resource "google_compute_router_nat" "nat" {
  count                              = var.create_vpc ? 1 : 0
  name                               = "${var.cluster_name}-nat"
  project                            = var.project_id
  router                             = google_compute_router.router[0].name
  region                             = var.region
  nat_ip_allocate_option             = var.nat_ip_allocate_option
  source_subnetwork_ip_ranges_to_nat = var.nat_source_subnetwork_ip_ranges

  log_config {
    enable = var.enable_nat_logging
    filter = var.nat_log_filter
  }
}

resource "google_compute_firewall" "allow_internal" {
  count   = var.create_vpc ? 1 : 0
  name    = "${var.cluster_name}-allow-internal"
  project = var.project_id
  network = google_compute_network.vpc[0].id

  allow {
    protocol = "tcp"
    ports    = var.internal_tcp_ports
  }
  allow {
    protocol = "udp"
    ports    = var.internal_udp_ports
  }
  allow {
    protocol = "icmp"
  }

  source_ranges = [var.subnet_cidr, var.pods_cidr, var.services_cidr]
}

resource "google_compute_firewall" "allow_health_checks" {
  count   = var.create_vpc ? 1 : 0
  name    = "${var.cluster_name}-allow-health-checks"
  project = var.project_id
  network = google_compute_network.vpc[0].id

  allow {
    protocol = "tcp"
    ports    = var.health_check_ports
  }

  # GCP health check ranges (fixed by Google)
  source_ranges = var.health_check_source_ranges
}

locals {
  vpc_id    = var.create_vpc ? google_compute_network.vpc[0].id : var.existing_vpc_id
  subnet_id = var.create_vpc ? google_compute_subnetwork.gke_subnet[0].id : var.existing_subnet_id
  vpc_name  = var.create_vpc ? google_compute_network.vpc[0].name : split("/", var.existing_vpc_id)[length(split("/", var.existing_vpc_id)) - 1]
}

# ─── Cloud Armor IP restriction ────────────────────────────────────────────
# When allowed_ip_ranges is populated, creates a security policy that
# only permits traffic from the specified CIDRs. Attach to backend
# services via a BackendConfig annotation:
#   cloud.google.com/backend-config: '{"default":"<policy-name>"}'

# Cloud Armor is project-scoped (not VPC-bound), so unlike AWS/Azure this is
# intentionally not gated on create_vpc.
resource "google_compute_security_policy" "ip_allowlist" {
  count   = length(var.allowed_ip_ranges) > 0 ? 1 : 0
  name    = "${var.cluster_name}-ip-allowlist"
  project = var.project_id

  # Default rule: deny all traffic not matching an allow rule
  rule {
    action   = "deny(403)"
    priority = "2147483647"
    match {
      versioned_expr = "SRC_IPS_V1"
      config {
        src_ip_ranges = ["*"]
      }
    }
    description = "Default deny"
  }

  # Allow traffic from the specified CIDR ranges
  rule {
    action   = "allow"
    priority = "1000"
    match {
      versioned_expr = "SRC_IPS_V1"
      config {
        src_ip_ranges = var.allowed_ip_ranges
      }
    }
    description = "Allowed IP ranges"
  }

  depends_on = [google_project_service.apis["compute.googleapis.com"]]
}
