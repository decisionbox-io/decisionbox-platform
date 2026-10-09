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

// TestForwardedEnv_LLMBehaviourKnobs pins that the LLM behaviour knobs are in
// the set every runner forwards, and in exactly one list, so the Docker runner
// (which forwards both lists) never emits them twice.
func TestForwardedEnv_LLMBehaviourKnobs(t *testing.T) {
	knobs := map[string]string{
		"LLM_TIMEOUT":            "1h",
		"LLM_MAX_RETRIES":        "5",
		"LLM_REQUEST_DELAY_MS":   "250",
		"LLM_RETRY_BASE_BACKOFF": "2s",
		"LLM_RETRY_MAX_ATTEMPTS": "4",
	}
	for k, v := range knobs {
		t.Setenv(k, v)
	}

	found := map[string]string{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys) {
		found[kv.Key] = kv.Value
	}
	for k, want := range knobs {
		if found[k] != want {
			t.Errorf("%s not forwarded to every runner: got %q, want %q", k, found[k], want)
		}
	}

	counts := map[string]int{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys, dockerAgentExtraEnvKeys) {
		counts[kv.Key]++
	}
	for k := range knobs {
		if counts[k] != 1 {
			t.Errorf("%s forwarded %d times by the Docker runner, want 1", k, counts[k])
		}
	}
}

// TestForwardedEnv_DiscoveryRunBudget pins that both halves of the discovery
// run budget reach agent containers.
//
// The agent is what applies DISCOVERY_MAX_DURATION and what creates the
// checkpoint TTL index, so neither value means anything if it stops at the
// API. And the two are constrained to each other — retention must exceed the
// run cap, or a long run's checkpoints expire while it is still running — so
// an operator raising one and not seeing the other take effect gets exactly
// the configuration the constraint exists to prevent.
func TestForwardedEnv_DiscoveryRunBudget(t *testing.T) {
	t.Setenv("DISCOVERY_MAX_DURATION", "168h")
	t.Setenv("DISCOVERY_CHECKPOINT_RETENTION", "192h")

	found := map[string]string{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys) {
		found[kv.Key] = kv.Value
	}

	if found["DISCOVERY_MAX_DURATION"] != "168h" {
		t.Errorf("DISCOVERY_MAX_DURATION not forwarded: %q", found["DISCOVERY_MAX_DURATION"])
	}
	if found["DISCOVERY_CHECKPOINT_RETENTION"] != "192h" {
		t.Errorf("DISCOVERY_CHECKPOINT_RETENTION not forwarded: %q — a resumable long run's checkpoints would expire mid-run", found["DISCOVERY_CHECKPOINT_RETENTION"])
	}
}

// TestForwardedEnv_DiscoveryRunBudgetAbsentWhenUnset keeps the
// forward-only-when-set contract: an unset knob must not be injected as an
// empty string, which the agent would have to parse and fall back from.
func TestForwardedEnv_DiscoveryRunBudgetAbsentWhenUnset(t *testing.T) {
	found := map[string]bool{}
	for _, kv := range collectForwardedEnv(agentForwardedEnvKeys) {
		found[kv.Key] = true
	}
	for _, k := range []string{"DISCOVERY_MAX_DURATION", "DISCOVERY_CHECKPOINT_RETENTION"} {
		if found[k] {
			t.Errorf("%s should not be forwarded when unset", k)
		}
	}
}
