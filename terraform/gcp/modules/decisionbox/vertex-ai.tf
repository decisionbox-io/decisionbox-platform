# Vertex AI IAM — granted to BOTH the API and agent Workload Identity SAs
# when the deployment uses the vertex-ai LLM provider (Claude via Vertex,
# Gemini, etc.).
#
#   - Agent calls Vertex during discovery runs (SQL generation, analysis).
#   - API calls Vertex for /ask synthesis and other LLM-powered endpoints.

resource "google_project_iam_member" "api_vertex_ai_user" {
  count   = var.enable_vertex_ai_iam ? 1 : 0
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.workload_identity.email}"
}

resource "google_project_iam_member" "agent_vertex_ai_user" {
  count   = var.enable_vertex_ai_iam ? 1 : 0
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.agent_workload_identity.email}"
}
