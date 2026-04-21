// Package policy is the OSS hook point for plan-gated features and usage
// limits. It is neutral — no cloud-specific code lives here. Self-hosted
// deployments use NoopChecker (allow everything). DecisionBox Cloud
// registers a CloudChecker via init() in the cloud tenant image that
// calls the control plane at runtime.
//
// Handlers call policy.GetChecker() at every chokepoint:
//
//	ck := policy.GetChecker()
//	res, err := ck.CheckCreateProject(ctx, deploymentID, intent)
//	if err != nil { /* translate PolicyError → HTTP 402/403 */ }
//	defer ck.Release(ctx, res.ID) // released on error; superseded by caller on success
//
// The interface is future-ready for enforced LLM token caps — the
// ObserveLLMTokens call today is observe-only, but upgrading to an
// enforced reserve/confirm pair later requires only a cap field on
// PlanLimits and flipping the endpoint. The wire shape is identical.
package policy

import "context"

// Checker is the interface every community chokepoint calls. All methods
// are safe to call concurrently. A typed *PolicyError is returned on
// denial; any other error is infrastructural (the caller should surface
// 500 or retry per its own policy).
//
// The deploymentID parameter is the deployment whose policy applies.
// Community handlers generally do not know a deployment ID (there is no
// deployment concept in OSS) and should pass "". The cloud plugin
// substitutes its configured DEPLOYMENT_ID env var in that case.
// Control-plane-side library callers (e.g., portal memberships.Add)
// pass an explicit deployment ID because they already resolve one.
type Checker interface {
	// CheckCreateProject reserves a slot against projects_per_deployment.
	// Returns the reservation on success; the caller must Release it if
	// the repo insert fails.
	CheckCreateProject(ctx context.Context, deploymentID string, intent ProjectIntent) (*Reservation, error)

	// CheckStartDiscoveryRun reserves a slot for a new discovery run.
	// Internally enforces BOTH concurrent_runs_per_project AND
	// discovery_runs_per_period in a single atomic check — both must
	// pass or nothing increments.
	CheckStartDiscoveryRun(ctx context.Context, deploymentID, projectID, runID string) (*Reservation, error)

	// ConfirmDiscoveryRunEnded reports run completion so the concurrent
	// counter decrements. The period counter stays consumed either way.
	ConfirmDiscoveryRunEnded(ctx context.Context, reservationID string, outcome RunOutcome) error

	// CheckAddDataSource reserves a slot against data_sources_per_deployment.
	CheckAddDataSource(ctx context.Context, deploymentID string) (*Reservation, error)

	// CheckLLMProviderAllowed rejects projects that try to configure a
	// provider the plan does not permit. Feature-flag style (no reservation).
	CheckLLMProviderAllowed(ctx context.Context, deploymentID, providerID string) error

	// CheckRegisterUser records a newly-seen principal against the
	// deployment's users_total cap. Called from three paths: portal
	// invite, cloud-auth handoff, customer-IdP first-seen JWT.
	CheckRegisterUser(ctx context.Context, deploymentID string, user UserIdentity) error

	// FeatureEnabled gates handler entry for plan-flagged features
	// (audit, governance, run scheduling, API access, model training).
	FeatureEnabled(ctx context.Context, deploymentID, flag string) (bool, error)

	// Release rolls back a reservation that was not consumed. Idempotent.
	Release(ctx context.Context, reservationID string) error

	// ObserveLLMTokens emits an observability event for a single LLM
	// call. Non-blocking, fire-and-forget. Never returns an error that
	// affects the caller — errors are logged by the plugin. In v1 this
	// is observe-only; a future fair-use cap will make it enforced.
	ObserveLLMTokens(ctx context.Context, deploymentID string, event LLMUsageEvent)

	// SyncCounters reconciles the tenant's persistent counters (current
	// project count, current data-source count) with the control-plane
	// deployment_usage document. Called periodically by a background
	// goroutine on the community API side so drift introduced outside
	// the reserve path (e.g., a manual Mongo import, a bug, a crash
	// that lost a reservation) eventually converges. Non-blocking for
	// the caller: errors are logged by the plugin, never propagated.
	SyncCounters(ctx context.Context, deploymentID string, counts CounterSnapshot)
}

// CounterSnapshot is the ground-truth count the tenant reports to the
// control plane during periodic reconciliation. Extended over time as
// more persistent counters are added.
type CounterSnapshot struct {
	ProjectsCurrent    int
	DataSourcesCurrent int
}

// Feature flag names used across the platform. These are the ONLY
// valid wire strings for FeatureEnabled, /internal/.../features/{flag},
// and any matching control-plane lookup. The control-plane handler
// must accept every constant here and nothing else — any drift is
// caught by AllFeatures below, which both sides import.
const (
	FeatureAudit          = "audit_enabled"
	FeatureGovernance     = "governance_enabled"
	FeatureCustomDomain   = "custom_domain_enabled"
	FeatureSSOCustomerIdP = "sso_customer_idp_enabled"
	FeatureModelTraining  = "model_training_enabled"
	FeatureRunScheduling  = "run_scheduling_enabled"
	FeatureAPIAccess      = "api_access_enabled"
	FeatureBYOKEmbedding  = "byok_embedding_enabled"
)

// AllFeatures is the canonical, ordered list of wire flag names.
// The control-plane handler asserts every entry maps to a plan-flag
// field so a rename here without a matching handler update fails at
// compile time on the control-plane side.
var AllFeatures = []string{
	FeatureAudit,
	FeatureGovernance,
	FeatureCustomDomain,
	FeatureSSOCustomerIdP,
	FeatureModelTraining,
	FeatureRunScheduling,
	FeatureAPIAccess,
	FeatureBYOKEmbedding,
}

// Reservation kinds used in the /internal/deployments/{id}/usage/reserve/{kind}
// URL path. The control plane dispatches on this string to the right
// counter field and cap lookup.
const (
	KindProjectCreate     = "project.create"
	KindDiscoveryRunStart = "discovery-run.start"
	KindDataSourceAdd     = "data-source.add"
	KindUserRegister      = "user.register"
)
