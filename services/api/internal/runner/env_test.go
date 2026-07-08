package runner

import "testing"

// TestForwardedEnv_InferenceCredentials pins that the managed-inference
// gateway credential env vars are forwarded to spawned agents when set —
// the managed mode has no per-project AI secret, so the agent's
// resolveCredential env fallback must find these inside the agent
// container. Absent vars must not be forwarded (per-project-secret model
// stays clean for self-hosted).
func TestForwardedEnv_InferenceCredentials(t *testing.T) {
	t.Setenv("LLM_API_KEY", "dbx-live-analysis")
	t.Setenv("EMBEDDING_API_KEY", "dbx-live-embed")
	// BLURB_LLM_API_KEY deliberately left unset — it must NOT appear.

	got := collectForwardedEnv(agentForwardedEnvKeys)
	found := map[string]string{}
	for _, kv := range got {
		found[kv.Key] = kv.Value
	}

	if found["LLM_API_KEY"] != "dbx-live-analysis" {
		t.Errorf("LLM_API_KEY not forwarded: %q", found["LLM_API_KEY"])
	}
	if found["EMBEDDING_API_KEY"] != "dbx-live-embed" {
		t.Errorf("EMBEDDING_API_KEY not forwarded: %q", found["EMBEDDING_API_KEY"])
	}
	if _, present := found["BLURB_LLM_API_KEY"]; present {
		t.Error("BLURB_LLM_API_KEY should not be forwarded when unset")
	}
}
