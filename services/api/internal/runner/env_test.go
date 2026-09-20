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

// TestForwardedEnv_SourcesEnabled pins that SOURCES_ENABLED reaches agent
// containers. Without it the enterprise sources plugin still loads but returns
// the community NoOp retriever, so discovery runs with no knowledge-source
// context while the API reports Knowledge Sources as enabled.
func TestForwardedEnv_SourcesEnabled(t *testing.T) {
	t.Setenv("SOURCES_ENABLED", "true")

	found := map[string]string{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys) {
		found[kv.Key] = kv.Value
	}

	if found["SOURCES_ENABLED"] != "true" {
		t.Errorf("SOURCES_ENABLED not forwarded: %q", found["SOURCES_ENABLED"])
	}
}

// TestForwardedEnv_CloudPolicyIdentity pins that the cloud policy-checker
// identity + the governance init gate reach agent Jobs. On cloud tenants the
// agent runs the cloud-enterprise agent image, whose policy.Checker is the
// cloud policy-plugin (POLICY_PROVIDER=cloud) and needs CONTROL_PLANE_URL /
// DEPLOYMENT_ID / CONTROL_PLANE_INTERNAL_TOKEN, and whose governance plugin
// needs GOVERNANCE_ENABLED to wrap the warehouse provider. Without forwarding,
// every agent-side entitlement was denied by the onprem license checker and
// governance never engaged (decisionbox-cloud-enterprise-tenant#39).
func TestForwardedEnv_CloudPolicyIdentity(t *testing.T) {
	t.Setenv("POLICY_PROVIDER", "cloud")
	t.Setenv("CONTROL_PLANE_URL", "https://cloud.decisionbox.io")
	t.Setenv("DEPLOYMENT_ID", "deadbeefdeadbeefdeadbeef")
	t.Setenv("CONTROL_PLANE_INTERNAL_TOKEN", "internal.jwt.token")
	t.Setenv("GOVERNANCE_ENABLED", "true")

	found := map[string]string{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys) {
		found[kv.Key] = kv.Value
	}

	for k, want := range map[string]string{
		"POLICY_PROVIDER":              "cloud",
		"CONTROL_PLANE_URL":            "https://cloud.decisionbox.io",
		"DEPLOYMENT_ID":                "deadbeefdeadbeefdeadbeef",
		"CONTROL_PLANE_INTERNAL_TOKEN": "internal.jwt.token",
		"GOVERNANCE_ENABLED":           "true",
	} {
		if found[k] != want {
			t.Errorf("%s not forwarded: got %q, want %q", k, found[k], want)
		}
	}
}

// TestForwardedEnv_CloudPolicyAbsentOnSelfHosted pins that the cloud identity
// vars are NOT forwarded when unset — self-hosted deployments (no
// POLICY_PROVIDER) must see none of them so the agent uses the OSS Noop
// checker and its behavior is unchanged.
func TestForwardedEnv_CloudPolicyAbsentOnSelfHosted(t *testing.T) {
	// Explicitly clear so a polluted CI env can't mask a regression.
	for _, k := range []string{
		"POLICY_PROVIDER", "CONTROL_PLANE_URL", "DEPLOYMENT_ID",
		"CONTROL_PLANE_INTERNAL_TOKEN", "GOVERNANCE_ENABLED",
	} {
		t.Setenv(k, "")
	}

	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys) {
		switch kv.Key {
		case "POLICY_PROVIDER", "CONTROL_PLANE_URL", "DEPLOYMENT_ID",
			"CONTROL_PLANE_INTERNAL_TOKEN", "GOVERNANCE_ENABLED":
			t.Errorf("%s must not be forwarded when unset, got %q", kv.Key, kv.Value)
		}
	}
}

// TestOperatorForwardedEnv_ForwardsAGateThisRepoDoesNotKnow is the whole
// point: a plugin shipping outside this repo has gates whose names are not
// in agentForwardedEnvKeys, and a gate that never reaches the agent fails
// silently rather than loudly.
func TestOperatorForwardedEnv_ForwardsAGateThisRepoDoesNotKnow(t *testing.T) {
	t.Setenv("AGENT_FORWARD_ENV", "SOME_PLUGIN_ENABLED, ANOTHER_GATE")
	t.Setenv("SOME_PLUGIN_ENABLED", "true")
	t.Setenv("ANOTHER_GATE", "1")

	found := map[string]string{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys, operatorForwardedEnvKeys()) {
		found[kv.Key] = kv.Value
	}
	if found["SOME_PLUGIN_ENABLED"] != "true" {
		t.Errorf("named gate not forwarded: got %q", found["SOME_PLUGIN_ENABLED"])
	}
	// Surrounding whitespace is what a YAML list written by hand actually
	// looks like; refusing it would be a trap rather than a validation.
	if found["ANOTHER_GATE"] != "1" {
		t.Errorf("name with surrounding space not forwarded: got %q", found["ANOTHER_GATE"])
	}
}

// TestOperatorForwardedEnv_NamingAnUnsetVariableForwardsNothing pins that
// the list names variables rather than declaring them. An operator listing
// a gate they have not actually set must not hand the agent an empty value,
// which several gates would read as a deliberate "off".
func TestOperatorForwardedEnv_NamingAnUnsetVariableForwardsNothing(t *testing.T) {
	t.Setenv("AGENT_FORWARD_ENV", "NEVER_SET_GATE")

	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys, operatorForwardedEnvKeys()) {
		if kv.Key == "NEVER_SET_GATE" {
			t.Fatalf("unset variable forwarded as %q", kv.Value)
		}
	}
}

// TestOperatorForwardedEnv_CannotDuplicateOrOverrideACanonicalKey pins that
// the operator list cannot put the same key in a container spec twice.
// Kubernetes rejects a duplicate env name outright, so a deployment that
// listed a canonical key would fail to spawn an agent at all — and the
// operator's reason for listing it (wanting it forwarded) is already true.
func TestOperatorForwardedEnv_CannotDuplicateOrOverrideACanonicalKey(t *testing.T) {
	t.Setenv("AGENT_FORWARD_ENV", "SOURCES_ENABLED,DUPE_GATE,DUPE_GATE")
	t.Setenv("SOURCES_ENABLED", "true")
	t.Setenv("DUPE_GATE", "true")

	counts := map[string]int{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys, operatorForwardedEnvKeys()) {
		counts[kv.Key]++
	}
	if counts["SOURCES_ENABLED"] != 1 {
		t.Errorf("canonical key appears %d times, want exactly 1", counts["SOURCES_ENABLED"])
	}
	if counts["DUPE_GATE"] != 1 {
		t.Errorf("repeated name appears %d times, want exactly 1", counts["DUPE_GATE"])
	}
}

// TestOperatorForwardedEnv_AbsentOrEmptyChangesNothing pins that a
// deployment that never sets this is byte-identical to before — which is
// every existing deployment.
func TestOperatorForwardedEnv_AbsentOrEmptyChangesNothing(t *testing.T) {
	t.Setenv("SOURCES_ENABLED", "true")
	base := collectForwardedEnv(agentForwardedEnvKeys)

	for _, v := range []string{"", "  ", ",", " , "} {
		t.Setenv("AGENT_FORWARD_ENV", v)
		if keys := operatorForwardedEnvKeys(); len(keys) != 0 {
			t.Errorf("AGENT_FORWARD_ENV=%q yielded %v, want none", v, keys)
		}
		got := collectForwardedEnv(agentForwardedEnvKeys, operatorForwardedEnvKeys())
		if len(got) != len(base) {
			t.Errorf("AGENT_FORWARD_ENV=%q changed the forwarded set: %d vs %d", v, len(got), len(base))
		}
	}
}
