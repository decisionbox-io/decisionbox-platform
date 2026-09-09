package runner

// This file holds the agent environment-variable wiring shared by the
// container-spawning runners (Kubernetes Jobs and Docker containers). A
// spawned agent runs in its own container — unlike the subprocess runner,
// which inherits the API process environment via os.Environ() — so the
// runner has to forward a curated set of variables explicitly.
//
// The set is deliberately small. The agent loads per-project providers
// (warehouse, LLM, embedding) and their secrets from MongoDB, so it does
// NOT need those credentials in its environment. What it does need is the
// connection to Mongo itself, the secret-provider config used to decrypt
// the Mongo-stored secrets, the Qdrant endpoint, the in-agent discovery
// cap, and the validation-pipeline knobs.

// defaultMongoURI / defaultMongoDB mirror the subprocess runner's
// fallbacks so a spawned agent points at the same Mongo as the API when
// the variables are unset.
const (
	defaultMongoURI = "mongodb://localhost:27017"
	defaultMongoDB  = "decisionbox"
)

// envKV is a forwarded environment variable. Each runner maps it onto its
// own representation (corev1.EnvVar for Kubernetes, "KEY=value" strings
// for Docker).
type envKV struct {
	Key   string
	Value string
}

// agentForwardedEnvKeys is the canonical set of API-process environment
// variables forwarded to every spawned agent (Kubernetes Job container or
// Docker container) when set. Order is preserved for deterministic specs.
//
// Keep this list in sync with what the agent reads at startup
// (services/agent/internal/config + the verifier's LoadConfigFromEnv).
var agentForwardedEnvKeys = []string{
	"SECRET_PROVIDER",
	"SECRET_NAMESPACE",
	"SECRET_ENCRYPTION_KEY",
	"SECRET_GCP_PROJECT_ID",
	"QDRANT_URL",
	"QDRANT_API_KEY",
	// Inference-credential env fallback. resolveCredential (agentserver /
	// index_schema) reads these INSIDE the agent when no per-project
	// secret exists — the managed-inference gateway mode relies on exactly
	// that fallback (nothing AI-related is stored per project), and a
	// self-hosted operator may set them as a global default too. Forwarded
	// only when set, so the per-project-secret model is unaffected when
	// they're absent.
	"LLM_API_KEY",
	"EMBEDDING_API_KEY",
	"BLURB_LLM_API_KEY",
	// DISCOVERY_MAX_DURATION caps the outer agent ctx — it lives on the
	// agent side (not the API), so it has to be forwarded for container
	// runs. Subprocess runs already inherit it from the API process env.
	"DISCOVERY_MAX_DURATION",
	// SOURCES_ENABLED gates the enterprise sources agent plugin. Without
	// forwarding, the plugin still loads but hands back the community NoOp
	// retriever, so discovery runs with no knowledge-source context at all
	// while the API reports Knowledge Sources as enabled — a silent
	// downgrade rather than a visible failure.
	"SOURCES_ENABLED",
	// VALIDATION_* knobs for the LLM-native verifier+refuter pipeline.
	// All consumed by the agent via verifier.LoadConfigFromEnv during
	// both full-discovery validation and manual --mode=validate-doc runs.
	// Without forwarding here, operators setting these on the API
	// deployment silently get defaults inside agent containers.
	"VALIDATION_REFUTER_ENABLED",
	"VALIDATION_MAX_INSIGHTS_PER_RUN",
	"VALIDATION_MAX_RECOMMENDATIONS_PER_RUN",
	"VALIDATION_VERIFIER_MAX_ROUNDS",
	"VALIDATION_VERIFIER_TOKEN_CAP",
	"VALIDATION_VERIFIER_MAX_OUTPUT",
	"VALIDATION_REFUTER_MAX_ROUNDS",
	"VALIDATION_REFUTER_TOKEN_CAP",
	"VALIDATION_REFUTER_MAX_OUTPUT",
	"VALIDATION_BUNDLE_SAMPLE_ROWS",
	"VALIDATION_MAX_READ_STEP_ROWS",
	"VALIDATION_NUMERIC_TOLERANCE",
	"VALIDATION_MIN_SAMPLE_SIZE",
	"VALIDATION_BUNDLE_CELL_CHAR_CAP",
	"VALIDATION_REC_STEPS_TOKEN_BUDGET",
	"VALIDATION_ESTIMATE_TOKEN_RATIO",
	// DISCOVERY_QUESTIONS_* knobs for the post-run clarifying-questions hop.
	// Consumed by the agent (discovery.runPhaseQuestions). Without forwarding
	// here, the enabled flag set on the API deployment never reaches a
	// container-spawned agent and the feature stays silently off.
	"DISCOVERY_QUESTIONS_ENABLED",
	"DISCOVERY_QUESTIONS_MAX",
	"DISCOVERY_QUESTIONS_MAX_OUTPUT",
	"DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT",
	"DISCOVERY_QUESTIONS_PARSE_MAX_RETRIES",
	"DISCOVERY_QUESTIONS_TIMEOUT",
	// DISCOVERY_REFLECTION_* + DISCOVERY_LEDGER_* knobs for the post-run
	// reflection / Discovery Ledger hop (compounding discovery, enterprise#261).
	// Consumed by the agent (discovery.RunPhaseReflection). Without forwarding
	// here, the enabled flag set on the API deployment never reaches a
	// container-spawned agent and the ledger stays silently off.
	"DISCOVERY_REFLECTION_ENABLED",
	"DISCOVERY_REFLECTION_TIMEOUT",
	"DISCOVERY_REFLECTION_MAX_OUTPUT",
	"DISCOVERY_REFLECTION_PARSE_MAX_RETRIES",
	"DISCOVERY_LEDGER_MAX_FINDINGS",
	"DISCOVERY_LEDGER_DEDUP_MINSCORE",
	"DISCOVERY_LEDGER_TREND_DELTA",
	// Cloud policy-checker identity. On cloud tenants the agent runs the
	// cloud-enterprise agent image, whose policy.Checker is
	// decisionbox-cloud-tenant/policy-plugin (registered only when
	// POLICY_PROVIDER=cloud) and forwards entitlement checks to the control
	// plane. Its config loader requires all three of CONTROL_PLANE_URL /
	// DEPLOYMENT_ID / CONTROL_PLANE_INTERNAL_TOKEN. Without forwarding, the
	// agent falls back to the allow-all Noop checker and plan caps go
	// unenforced agent-side. The api pod already holds all four (POLICY_PROVIDER
	// + CONTROL_PLANE_URL as literals, the latter two via envFrom the
	// per-release cloud-auth / cloud-policy secrets), so this forwards them the
	// same way the inference-credential secrets above are. Absent on self-hosted
	// (POLICY_PROVIDER unset) ⇒ nothing forwarded, behavior unchanged.
	// See decisionbox-cloud-enterprise-tenant#39.
	"POLICY_PROVIDER",
	"CONTROL_PLANE_URL",
	"DEPLOYMENT_ID",
	"CONTROL_PLANE_INTERNAL_TOKEN",
	// GOVERNANCE_ENABLED gates the enterprise governance plugin's init() in the
	// agent process (governance/register.go): without it the warehouse provider
	// is never wrapped, so agent queries stay ungoverned regardless of the
	// policy checker's FeatureGovernance entitlement. Forwarding the flag set on
	// the api deployment is required for governance to apply to agent queries;
	// the checker forwarding above is necessary but not sufficient on its own.
	"GOVERNANCE_ENABLED",
}

// dockerAgentExtraEnvKeys are forwarded only by the Docker runner. The
// Kubernetes runner relies on Workload Identity / IRSA and per-namespace
// service accounts for cloud access and on cluster defaults for logging,
// so it does not forward these. On a single host the agent container has
// no such ambient identity, so the Docker runner passes through whatever
// the API process holds.
var dockerAgentExtraEnvKeys = []string{
	// Cloud secret-provider config not covered by the shared set.
	"SECRET_AWS_REGION",
	"SECRET_AZURE_VAULT_URL",
	// AWS credential passthrough — the AWS SDK default credential chain
	// (used by awscreds for IAM-role / default auth) reads these from the
	// environment. On a single host there is no instance role to fall
	// back on, so forward the API process's credentials.
	"AWS_ACCESS_KEY_ID",
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"AWS_REGION",
	"AWS_DEFAULT_REGION",
	// Azure environment-credential passthrough — the Azure secret provider
	// uses DefaultAzureCredential, whose EnvironmentCredential reads these.
	// Without them a single-host agent with SECRET_PROVIDER=azure cannot
	// reach Key Vault (no managed identity to fall back on).
	"AZURE_TENANT_ID",
	"AZURE_CLIENT_ID",
	"AZURE_CLIENT_SECRET",
	// NOTE on GCP: GOOGLE_APPLICATION_CREDENTIALS is deliberately NOT
	// forwarded. It is a *file path*, and the runner cannot make that file
	// available inside the sibling agent container — it only knows the path
	// as seen in the API container, not the host path needed to bind-mount
	// it. Forwarding just the path would resolve to a non-existent file in
	// the agent and fail with file-not-found. For GCP in docker mode the
	// agent must obtain credentials another way (the host's GCE metadata
	// server / Workload Identity, or a credentials file baked into / mounted
	// onto a custom AGENT_IMAGE). See docs/reference/configuration.md.
	//
	// LLM behaviour knobs the compose agent service also sets, so a Docker
	// agent matches the API's tuning. (LLM API keys are NOT here — they
	// live per-project in the secret provider.)
	"LLM_TIMEOUT",
	"LLM_MAX_RETRIES",
	"LLM_REQUEST_DELAY_MS",
	"LLM_RETRY_BASE_BACKOFF",
	"LLM_RETRY_MAX_ATTEMPTS",
	// Logging parity with the API process.
	"ENV",
	"LOG_LEVEL",
	// Telemetry opt-out: the agent's telemetry gate reads these from its own
	// environment and defaults to enabled, so without forwarding them a
	// docker-mode agent would still emit telemetry despite a deployment
	// opting out at the API.
	"TELEMETRY_ENABLED",
	"DO_NOT_TRACK",
}

// agentBaseEnv returns the variables every spawned agent gets
// unconditionally: the Mongo connection (with the same fallbacks the
// subprocess runner uses) and writable HOME/TMPDIR so disk-touching SDKs
// (gosnowflake OCSP cache, AWS/GCP token caches) have a scratch path even
// when the container's home is not writable.
func agentBaseEnv() []envKV {
	return []envKV{
		{Key: "MONGODB_URI", Value: getEnv("MONGODB_URI", defaultMongoURI)},
		{Key: "MONGODB_DB", Value: getEnv("MONGODB_DB", defaultMongoDB)},
		{Key: "TMPDIR", Value: "/tmp"},
		{Key: "HOME", Value: "/tmp"},
	}
}

// collectForwardedEnv returns the entries from the given key groups that
// are set (non-empty) in the API process environment, preserving order.
func collectForwardedEnv(keyGroups ...[]string) []envKV {
	var out []envKV
	for _, keys := range keyGroups {
		for _, k := range keys {
			if v := getEnv(k, ""); v != "" {
				out = append(out, envKV{Key: k, Value: v})
			}
		}
	}
	return out
}
