package agentplugin

import (
	"context"
	"fmt"
	"sync"
)

// DiscoveryPolicy is the per-project governance the compounding-discovery
// reflection phase + read path honor (enterprise#261). It reaches the agent
// through this registry rather than DiscoveryOptions because it lives in an
// enterprise collection (with an org/deployment default) the platform agent
// must not read directly — the enterprise evolution-settings plugin registers a
// provider from init(), exactly as the sources plugin registers a
// ContextProvider.
type DiscoveryPolicy struct {
	// EvolutionMode governs the risky, self-directing parts of the ledger
	// (domain-pack deltas + self-emitted next-tasks). Low-risk ledger writes
	// (coverage, finding capture) are unconditional.
	EvolutionMode EvolutionMode
	// FrontierPolicy biases how the next run is steered across the coverage
	// map: tile untouched territory vs. chase the highest-value seam.
	FrontierPolicy FrontierPolicy
}

// EvolutionMode values.
type EvolutionMode string

const (
	// EvolutionModeOff — the ledger still records coverage/findings, but no
	// domain-pack changes and no self-directed next-tasks are generated.
	EvolutionModeOff EvolutionMode = "off"
	// EvolutionModeSuggestOnly — deltas + next-tasks are proposed and shown,
	// nothing auto-applies.
	EvolutionModeSuggestOnly EvolutionMode = "suggest_only"
	// EvolutionModeAdminApproval — proposed deltas queue for an admin to
	// approve before they affect the next run.
	EvolutionModeAdminApproval EvolutionMode = "admin_approval"
	// EvolutionModeAuto — approved automatically with a full audit trail.
	EvolutionModeAuto EvolutionMode = "auto"
)

// FrontierPolicy values.
type FrontierPolicy string

const (
	FrontierBreadthFirst FrontierPolicy = "breadth_first"
	FrontierDepthFirst   FrontierPolicy = "depth_first"
	FrontierBalanced     FrontierPolicy = "balanced"
)

// ValidEvolutionMode reports whether m is a known mode.
func ValidEvolutionMode(m EvolutionMode) bool {
	switch m {
	case EvolutionModeOff, EvolutionModeSuggestOnly, EvolutionModeAdminApproval, EvolutionModeAuto:
		return true
	}
	return false
}

// ValidFrontierPolicy reports whether p is a known policy.
func ValidFrontierPolicy(p FrontierPolicy) bool {
	switch p {
	case FrontierBreadthFirst, FrontierDepthFirst, FrontierBalanced:
		return true
	}
	return false
}

// DefaultDiscoveryPolicy is the community default used when no provider is
// registered (community / self-hosted): the compounding feature is enterprise,
// so evolution is off and the frontier policy is neutral.
func DefaultDiscoveryPolicy() DiscoveryPolicy {
	return DiscoveryPolicy{EvolutionMode: EvolutionModeOff, FrontierPolicy: FrontierBalanced}
}

// DiscoveryPolicyProvider resolves the effective policy for a project. The
// enterprise evolution-settings plugin implements it over its Mongo collection
// (per-project doc → org default → env → built-in default).
type DiscoveryPolicyProvider interface {
	// Policy returns the effective policy for projectID. Must be safe for
	// concurrent use and honor ctx cancellation.
	Policy(ctx context.Context, projectID string) (DiscoveryPolicy, error)
	// Name is a stable identifier for telemetry.
	Name() string
}

var (
	policyMu       sync.RWMutex
	policyProvider DiscoveryPolicyProvider
)

// RegisterDiscoveryPolicyProvider installs p as the policy provider. There is a
// single provider (unlike the multi-provider ContextProvider registry); the last
// registrant wins. Registering nil, or a provider with an empty Name(), panics —
// those are programmer errors surfaced at init().
func RegisterDiscoveryPolicyProvider(p DiscoveryPolicyProvider) {
	if p == nil {
		panic("agentplugin: RegisterDiscoveryPolicyProvider called with nil provider")
	}
	if p.Name() == "" {
		panic("agentplugin: DiscoveryPolicyProvider.Name() returned empty string")
	}
	policyMu.Lock()
	policyProvider = p
	policyMu.Unlock()
}

// ResolveDiscoveryPolicy returns the effective policy for projectID. It is
// best-effort and never fails discovery: with no provider registered it returns
// the community default and a nil error; if the provider errors or panics it
// returns the default and the error so the caller can log it and continue.
func ResolveDiscoveryPolicy(ctx context.Context, projectID string) (policy DiscoveryPolicy, err error) {
	policyMu.RLock()
	p := policyProvider
	policyMu.RUnlock()
	if p == nil {
		return DefaultDiscoveryPolicy(), nil
	}
	defer func() {
		if r := recover(); r != nil {
			policy = DefaultDiscoveryPolicy()
			err = fmt.Errorf("agentplugin: discovery policy provider %q panicked: %v", safePolicyName(p), r)
		}
	}()
	pol, perr := p.Policy(ctx, projectID)
	if perr != nil {
		return DefaultDiscoveryPolicy(), perr
	}
	// Normalize unknown/empty values to the default so downstream code can
	// trust the returned policy without re-validating.
	if !ValidEvolutionMode(pol.EvolutionMode) {
		pol.EvolutionMode = DefaultDiscoveryPolicy().EvolutionMode
	}
	if !ValidFrontierPolicy(pol.FrontierPolicy) {
		pol.FrontierPolicy = DefaultDiscoveryPolicy().FrontierPolicy
	}
	return pol, nil
}

func safePolicyName(p DiscoveryPolicyProvider) (name string) {
	defer func() {
		if recover() != nil {
			name = "<unknown:Name() panicked>"
		}
	}()
	return p.Name()
}

// resetPolicyForTest clears the registered provider. Test-only.
func resetPolicyForTest() {
	policyMu.Lock()
	policyProvider = nil
	policyMu.Unlock()
}
