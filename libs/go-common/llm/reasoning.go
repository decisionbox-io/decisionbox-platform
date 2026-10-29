package llm

import (
	"os"
	"strings"
)

// ReasoningEffortEnvVar overrides the deployment's default reasoning effort
// for every layer and every provider that wires effort (Rule 2 — no
// hard-coded operational value). Accepts the same values as
// ChatRequest.ReasoningEffort; an unset or unrecognised value leaves
// DefaultReasoningEffort in force.
//
//nolint:gosec // env var name, not a credential
const ReasoningEffortEnvVar = "LLM_REASONING_EFFORT"

// DefaultReasoningEffort is the effort requested when a caller expresses no
// preference.
//
// High, deliberately. On the current reasoning models effort is the only
// control left — thinking cannot be switched off, and the token budget is
// shared between reasoning and answer — so leaving it unset does not mean
// "no reasoning", it means "whatever this model's default happens to be".
// Those defaults differ between models in the same family and shift between
// releases, which makes the quality of every layer depend on an unstated
// provider default. Naming it keeps the choice ours and visible; operators
// who want to trade quality for tokens set ReasoningEffortEnvVar.
const DefaultReasoningEffort = ReasoningEffortHigh

// ResolveReasoningEffort returns the effort to send for a request.
//
// requested is ChatRequest.ReasoningEffort as the caller set it:
//   - a concrete level (low/medium/high) is honoured as-is;
//   - "on" means "reason, I don't mind how much" and takes the deployment
//     default;
//   - "" (no preference) also takes the deployment default;
//   - "off" is honoured as the lowest level we can express rather than as a
//     disable. Several current models reject an explicit disable outright, so
//     a request that tried to turn thinking off would fail the whole call;
//     the lowest effort is the documented substitute and is what the caller
//     actually wanted — as little reasoning as possible.
//
// Providers whose models do not take an effort setting must not call this —
// see anthropicwire.OutputConfig for the gate.
func ResolveReasoningEffort(requested string) string {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case ReasoningEffortLow:
		return ReasoningEffortLow
	case ReasoningEffortMedium:
		return ReasoningEffortMedium
	case ReasoningEffortHigh:
		return ReasoningEffortHigh
	case ReasoningEffortOff:
		return ReasoningEffortLow
	default:
		// ReasoningEffortDefault (""), ReasoningEffortOn, or a value no
		// provider documents: take the deployment default.
		return deploymentReasoningEffort()
	}
}

// deploymentReasoningEffort reads the operator override, falling back to
// DefaultReasoningEffort. An unrecognised override is ignored rather than
// passed through: an invalid effort is rejected by the provider, and a typo
// in one env var should not fail every LLM call in the deployment.
//
// "off" is accepted and means what it means on a request — the lowest level
// we can express. The variable documents the request's own values, so an
// operator who sets it to minimise reasoning must not get the highest effort
// instead, which is what treating "off" as unrecognised would do.
func deploymentReasoningEffort() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(ReasoningEffortEnvVar))) {
	case ReasoningEffortLow, ReasoningEffortOff:
		return ReasoningEffortLow
	case ReasoningEffortMedium:
		return ReasoningEffortMedium
	case ReasoningEffortHigh:
		return ReasoningEffortHigh
	default:
		return DefaultReasoningEffort
	}
}
