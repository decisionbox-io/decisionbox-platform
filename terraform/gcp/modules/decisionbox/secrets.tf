# Secret Manager IAM — grants the API's Workload Identity SA permission to
# create, read, and list secrets scoped to the configured namespace prefix.
# The API itself creates and manages secrets at runtime (not Terraform).
#
# Uses two custom roles:
#   1. A "list" role bound WITHOUT a condition (list is a project-level operation
#      and IAM conditions on resource.name don't apply to list calls).
#   2. A "manage" role bound WITH a namespace condition (restricts create/read/
#      update to secrets prefixed with the configured namespace).

data "google_project" "current" {
  project_id = var.project_id
}

# Role 1: List secrets (project-level, no condition possible)
resource "google_project_iam_custom_role" "secret_list" {
  count       = var.enable_gcp_secrets ? 1 : 0
  project     = var.project_id
  role_id     = replace("${var.cluster_name}_secret_list", "-", "_")
  title       = "DecisionBox Secret List (${var.cluster_name})"
  description = "List secrets in the project"
  permissions = [
    "secretmanager.secrets.list",
  ]
}

resource "google_project_iam_member" "secret_list" {
  count   = var.enable_gcp_secrets ? 1 : 0
  project = var.project_id
  role    = google_project_iam_custom_role.secret_list[0].id
  member  = "serviceAccount:${google_service_account.workload_identity.email}"
}

# Role 2: Manage secrets (scoped to namespace prefix via IAM condition)
resource "google_project_iam_custom_role" "secret_manager" {
  count       = var.enable_gcp_secrets ? 1 : 0
  project     = var.project_id
  role_id     = replace("${var.cluster_name}_secret_manager", "-", "_")
  title       = "DecisionBox Secret Manager (${var.cluster_name})"
  description = "Create, read, and update secrets — no delete/disable"
  permissions = [
    "secretmanager.secrets.create",
    "secretmanager.secrets.get",
    "secretmanager.secrets.update",
    "secretmanager.versions.add",
    "secretmanager.versions.access",
    "secretmanager.versions.list",
  ]
}

resource "google_project_iam_member" "secret_manager" {
  count   = var.enable_gcp_secrets ? 1 : 0
  project = var.project_id
  role    = google_project_iam_custom_role.secret_manager[0].id
  member  = "serviceAccount:${google_service_account.workload_identity.email}"

  condition {
    title       = "Restrict to ${var.secret_namespace} namespace"
    description = "Only allow access to secrets prefixed with ${var.secret_namespace}-"
    expression  = "resource.name.startsWith(\"projects/${data.google_project.current.number}/secrets/${var.secret_namespace}-\")"
  }
}

# Agent: read-only secret access (get + access versions, no create/update)
resource "google_project_iam_custom_role" "agent_secret_reader" {
  count       = var.enable_gcp_secrets ? 1 : 0
  project     = var.project_id
  role_id     = replace("${var.cluster_name}_agent_secret_reader", "-", "_")
  title       = "DecisionBox Agent Secret Reader (${var.cluster_name})"
  description = "Read-only access to secrets — no create, update, or delete"
  permissions = [
    "secretmanager.secrets.get",
    "secretmanager.versions.access",
    "secretmanager.versions.list",
  ]
}

resource "google_project_iam_member" "agent_secret_list" {
  count   = var.enable_gcp_secrets ? 1 : 0
  project = var.project_id
  role    = google_project_iam_custom_role.secret_list[0].id
  member  = "serviceAccount:${google_service_account.agent_workload_identity.email}"
}

resource "google_project_iam_member" "agent_secret_reader" {
  count   = var.enable_gcp_secrets ? 1 : 0
  project = var.project_id
  role    = google_project_iam_custom_role.agent_secret_reader[0].id
  member  = "serviceAccount:${google_service_account.agent_workload_identity.email}"

  condition {
    title       = "Restrict agent to ${var.secret_namespace} namespace"
    description = "Only allow read access to secrets prefixed with ${var.secret_namespace}-"
    expression  = "resource.name.startsWith(\"projects/${data.google_project.current.number}/secrets/${var.secret_namespace}-\")"
  }
}
