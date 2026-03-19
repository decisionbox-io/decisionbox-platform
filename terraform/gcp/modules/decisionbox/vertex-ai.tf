# Vertex AI IAM — granted to the agent SA for LLM access via Vertex AI.
# Required when using vertex-ai LLM provider (Claude via Vertex, Gemini).
# The API does not call Vertex AI directly — only the agent does.

resource "google_project_iam_member" "agent_vertex_ai_user" {
  count   = var.enable_vertex_ai_iam ? 1 : 0
  project = var.project_id
  role    = "roles/aiplatform.user"
  member  = "serviceAccount:${google_service_account.agent_workload_identity.email}"
}
