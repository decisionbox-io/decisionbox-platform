# GKE node service account — only created with new cluster
resource "google_service_account" "gke_nodes" {
  count        = var.create_cluster ? 1 : 0
  account_id   = "${var.cluster_name}-nodes"
  display_name = "GKE Node Service Account for ${var.cluster_name}"
  project      = var.project_id
}

resource "google_project_iam_member" "node_log_writer" {
  count   = var.create_cluster ? 1 : 0
  project = var.project_id
  role    = "roles/logging.logWriter"
  member  = "serviceAccount:${google_service_account.gke_nodes[0].email}"
}

resource "google_project_iam_member" "node_metric_writer" {
  count   = var.create_cluster ? 1 : 0
  project = var.project_id
  role    = "roles/monitoring.metricWriter"
  member  = "serviceAccount:${google_service_account.gke_nodes[0].email}"
}

resource "google_project_iam_member" "node_monitoring_viewer" {
  count   = var.create_cluster ? 1 : 0
  project = var.project_id
  role    = "roles/monitoring.viewer"
  member  = "serviceAccount:${google_service_account.gke_nodes[0].email}"
}

# Workload Identity SA — always created
resource "google_service_account" "workload_identity" {
  account_id   = "${var.cluster_name}-api"
  display_name = "Workload Identity SA for DecisionBox API"
  project      = var.project_id
}

# Workload Identity pool (<project>.svc.id.goog) is created by the GKE cluster.
# This binding must wait for the cluster to exist.
resource "google_service_account_iam_member" "workload_identity_binding" {
  service_account_id = google_service_account.workload_identity.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[${var.k8s_namespace}/${var.k8s_service_account}]"

  depends_on = [
    google_container_cluster.primary,
    data.google_container_cluster.existing,
  ]
}

# Agent Workload Identity SA — separate from API, read-only secret access
resource "google_service_account" "agent_workload_identity" {
  account_id   = "${var.cluster_name}-agent"
  display_name = "Workload Identity SA for DecisionBox Agent (read-only)"
  project      = var.project_id
}

resource "google_service_account_iam_member" "agent_workload_identity_binding" {
  service_account_id = google_service_account.agent_workload_identity.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "serviceAccount:${var.project_id}.svc.id.goog[${var.k8s_namespace}/${var.k8s_agent_service_account}]"

  depends_on = [
    google_container_cluster.primary,
    data.google_container_cluster.existing,
  ]
}
