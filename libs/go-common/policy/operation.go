package policy

import "context"

// Effort is the discovery-run intensity a caller picks instead of a raw step
// count. Each level maps to a max_steps value the agent runs with. The
// mapping is neutral platform vocabulary; how (or whether) an operation is
// metered is a deployment-plan concern handled by the OperationCharger below.
const (
	EffortLower  = "lower"
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortHigher = "higher"
)

// DefaultEffort is used when a caller does not specify one.
const DefaultEffort = EffortMedium

// effortSteps maps each effort level to the discovery agent's max_steps.
var effortSteps = map[string]int{
	EffortLower:  20,
	EffortLow:    40,
	EffortMedium: 60,
	EffortHigh:   80,
	EffortHigher: 100,
}

// ValidEffort reports whether e is a recognised effort level.
func ValidEffort(e string) bool {
	_, ok := effortSteps[e]
	return ok
}

// StepsForEffort returns the max_steps for an effort level and whether it was
// recognised. Callers use it to translate a customer-facing effort into the
// agent's step budget without exposing raw step counts.
func StepsForEffort(e string) (int, bool) {
	s, ok := effortSteps[e]
	return s, ok
}

// Operation names — the priced product operations a plan may meter. Neutral
// identifiers shared with the deployment plan; no plan/pricing detail lives
// in the community codebase.
const (
	OpDiscoveryRun         = "discovery_run"
	OpDomainPackGeneration = "domain_pack_generation"
	OpExecutiveSummary     = "executive_summary"
	OpAskMessage           = "ask_message"
	OpSourcesIndexing      = "sources_indexing"
)

// Operation describes a single priced product operation to meter at its
// start. Reference is the caller's idempotency key and the handle a later
// refund uses. Effort is set only for OpDiscoveryRun.
type Operation struct {
	Name      string
	Effort    string
	Reference string
}

// OperationCharge is the receipt of a metered operation. On a self-hosted
// deployment nothing is metered and Charged is zero.
type OperationCharge struct {
	Charged int64
	Balance int64
}

// OperationCharger is an OPTIONAL extension a Checker may implement to meter
// priced product operations (DecisionBox Cloud does; self-hosted does not).
// It is deliberately NOT part of the Checker interface, so the community
// NoopChecker and every existing mock stay unchanged — callers reach it only
// through the ChargeIfMetered / RefundIfMetered helpers below, which no-op
// when the active checker doesn't implement it.
type OperationCharger interface {
	// ChargeOperation meters op at its start. A typed *PolicyError signals
	// the operation is blocked (e.g. balance exhausted); the caller should
	// surface it (HTTP 402) and not proceed. Any other error is
	// infrastructural.
	ChargeOperation(ctx context.Context, deploymentID string, op Operation) (*OperationCharge, error)

	// RefundOperation reverses a prior ChargeOperation by its reference,
	// after a defined SYSTEM failure. Idempotent; user/LLM errors are not
	// refundable and that decision lives at the call site.
	RefundOperation(ctx context.Context, deploymentID, reference string) error
}

// ChargeIfMetered meters op against the active checker when it supports
// metering, otherwise it is a no-op returning a zero charge. Product-boundary
// callers use this so the community path stays free and the cloud path
// debits, without either knowing about the other.
func ChargeIfMetered(ctx context.Context, deploymentID string, op Operation) (*OperationCharge, error) {
	if ch, ok := GetChecker().(OperationCharger); ok {
		return ch.ChargeOperation(ctx, deploymentID, op)
	}
	return &OperationCharge{}, nil
}

// RefundIfMetered reverses a prior metered charge when the active checker
// supports metering; a no-op otherwise. Best-effort: refund failures are the
// plugin's to log, never the caller's to handle.
func RefundIfMetered(ctx context.Context, deploymentID, reference string) {
	if ch, ok := GetChecker().(OperationCharger); ok {
		_ = ch.RefundOperation(ctx, deploymentID, reference)
	}
}
