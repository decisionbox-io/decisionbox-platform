# BigQuery IAM — only granted to the agent SA (agent queries the warehouse).
# The API does not need BigQuery access.

resource "google_project_iam_member" "agent_bq_data_viewer" {
  count   = var.enable_bigquery_iam ? 1 : 0
  project = var.project_id
  role    = "roles/bigquery.dataViewer"
  member  = "serviceAccount:${google_service_account.agent_workload_identity.email}"
}

resource "google_project_iam_member" "agent_bq_job_user" {
  count   = var.enable_bigquery_iam ? 1 : 0
  project = var.project_id
  role    = "roles/bigquery.jobUser"
  member  = "serviceAccount:${google_service_account.agent_workload_identity.email}"
}
