package policy

import (
	"fmt"
	"time"
)

// Reservation is the handle a tenant holds after a successful reserve call.
// The reservation must be either confirmed (work succeeded, increment is
// permanent) or released (work failed, counter rolls back). An abandoned
// reservation is eventually swept by the control plane after ExpiresAt.
type Reservation struct {
	ID             string    `json:"reservation_id"`
	DeploymentID   string    `json:"deployment_id"`
	Kind           string    `json:"kind"`
	Subject        string    `json:"subject,omitempty"`
	CounterCurrent int       `json:"counter_current"`
	CounterMax     int       `json:"counter_max"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// ProjectIntent is the policy-relevant subset of a project at create time.
// It is decoupled from the full project model so the policy hook does not
// depend on the community project schema.
//
// ProjectID may be empty: the community ProjectsHandler calls the Checker
// before the repo assigns a Mongo ObjectID, so plugins must not rely on
// ProjectID as a stable identifier at create time (it is provided when
// the client supplies one, and as a human-readable label otherwise).
// Idempotency should key on the Reservation id the Checker returns.
type ProjectIntent struct {
	ProjectID   string
	Name        string
	LLMProvider string
}

// RunOutcome reports how a discovery run ended, so the control plane can
// record it (success/failure metrics, reservation consumed or released).
type RunOutcome struct {
	Status     string // "success" | "failure" | "cancelled"
	StartedAt  time.Time
	EndedAt    time.Time
	Error      string
}

// UserIdentity is the identity presented at a tenant login that must be
// counted toward the deployment's users_total cap.
type UserIdentity struct {
	PrincipalSub string // Auth0 sub or customer-IdP sub
	Email        string
	Source       string // "portal" | "cloud-auth" | "customer-idp"
}

// LLMUsageEvent is a single LLM call's observability record. Emitted by the
// agent's LLM wrapper after every call (success or failure). In v1 the
// control plane records tokens for monitoring only — never enforces a cap.
type LLMUsageEvent struct {
	ProjectID    string    `json:"project_id"`
	RunID        string    `json:"run_id,omitempty"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	LatencyMs    int       `json:"latency_ms"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// PolicyError is the typed error returned by every Check* method when the
// action is denied. Handlers unwrap this with errors.As and translate the
// fields into the 402/403 JSON body.
type PolicyError struct {
	Kind     string // "limit" (counted cap reached) or "feature" (flag off / provider disallowed)
	Limit    string // e.g. "projects_per_deployment"
	Feature  string // e.g. "audit_enabled" or "llm_provider"
	Current  int    // current counter value (limit errors only)
	Max      int    // cap (limit errors only)
	PlanID   string // plan slug for the deployment at error time
	Allowed  []string // for provider-allow-list denials
	Message  string
}

func (e *PolicyError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	switch e.Kind {
	case "limit":
		return fmt.Sprintf("plan %s: %s limit reached (%d/%d)", e.PlanID, e.Limit, e.Current, e.Max)
	case "feature":
		if e.Feature == "llm_provider" && len(e.Allowed) > 0 {
			return fmt.Sprintf("plan %s: llm provider not allowed (allowed: %v)", e.PlanID, e.Allowed)
		}
		return fmt.Sprintf("plan %s: feature %s not enabled", e.PlanID, e.Feature)
	default:
		return fmt.Sprintf("plan %s: policy denied", e.PlanID)
	}
}

// IsLimit reports whether the error was a counted-cap denial (HTTP 402).
func (e *PolicyError) IsLimit() bool { return e.Kind == "limit" }

// IsFeature reports whether the error was a feature-flag denial (HTTP 403).
func (e *PolicyError) IsFeature() bool { return e.Kind == "feature" }
