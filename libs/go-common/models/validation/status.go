// Package validation owns the wire types for the LLM-native insight
// validation feature. Lives under libs/go-common so both
// services/agent and services/api can import the same shared types,
// avoiding cross-module-internal coupling.
package validation

import "strings"

// Status is the validation taxonomy emitted by verifier + refuter and
// combined by Combine() into the document's final verdict state.
// Seven terminal statuses — see the constants below.
type Status string

const (
	// StatusConfirmed — row evidence DIRECTLY EQUAL to the claim's
	// value (rare; cardinality only). Verifier-only.
	StatusConfirmed Status = "confirmed"

	// StatusSupported — row evidence consistent with the claim;
	// the default positive verdict.
	StatusSupported Status = "supported"

	// StatusRejected — row evidence CONTRADICTS the headline. Must
	// carry the contradicting row in evidence.
	StatusRejected Status = "rejected"

	// StatusPartial — verifier covered some claims but not all, OR
	// verifier supported/confirmed but refuter incomplete, OR a
	// coverage check failed.
	StatusPartial Status = "partial"

	// StatusUnverifiable — verifier could not produce evidence for
	// any claim (binary columns, beyond snapshot, no tool succeeded).
	StatusUnverifiable Status = "unverifiable"

	// StatusValidationDisabled — aiClient or schemaProvider was nil
	// at validation entry; the agent never ran.
	StatusValidationDisabled Status = "validation_disabled"

	// StatusSkippedBudgetCap — run-level cap reached; this document
	// never entered the agent.
	StatusSkippedBudgetCap Status = "skipped_budget_cap"
)

// IsTerminalPositive returns true for the verifier-or-refuter
// statuses that signal a meaningful positive verdict (the document's
// claims hold up). Used for downstream eligibility filters (e.g. only
// supported+confirmed insights flow to the recommender).
func (s Status) IsTerminalPositive() bool {
	switch s {
	case StatusConfirmed, StatusSupported:
		return true
	}
	return false
}

// IsKnown returns true when s is one of the seven defined statuses.
// Used for **doc-level** Overall validation (Combine accepts every
// status). Per-claim statuses are stricter — use IsValidPerClaim
// instead.
func (s Status) IsKnown() bool {
	switch s {
	case StatusConfirmed, StatusSupported, StatusRejected,
		StatusPartial, StatusUnverifiable,
		StatusValidationDisabled, StatusSkippedBudgetCap:
		return true
	}
	return false
}

// IsValidPerClaim returns true when s is one of the five values
// allowed in `ClaimVerdict.Status`. The two doc-level "agent never
// ran" statuses (`validation_disabled`, `skipped_budget_cap`) are
// NOT valid per-claim — a claim with no per-claim verdict still has
// to honour the evidence-required rule, which would silently pass
// if the claim's status were `validation_disabled`. The finaliser
// uses this to reject the doc-level statuses on per-claim entries.
func (s Status) IsValidPerClaim() bool {
	switch s {
	case StatusConfirmed, StatusSupported, StatusRejected,
		StatusPartial, StatusUnverifiable:
		return true
	}
	return false
}

// DefaultRecommendationVerdicts is the eligibility set used for
// recommendation generation when a project has not configured its own
// (the per-project recommendation_verdicts setting). It reproduces the
// historical hardcoded filter (IsTerminalPositive): only confirmed +
// supported insights flow to the recommender, so unset / legacy
// projects are byte-identical to today.
func DefaultRecommendationVerdicts() []Status {
	return []Status{StatusConfirmed, StatusSupported}
}

// ParseStatuses sanitises a free-form (case-insensitive) list of verdict
// names into the five user-selectable per-claim statuses — confirmed,
// supported, partial, unverifiable, rejected — dropping unknowns,
// duplicates, and the two internal "agent never ran" states
// (validation_disabled, skipped_budget_cap, which are handled fail-open
// by the eligibility filter, not selectable by operators). Used to clean
// the per-project recommendation-eligibility setting coming from the API
// / dashboard before it reaches the discovery filter.
func ParseStatuses(in []string) []Status {
	out := make([]Status, 0, len(in))
	seen := make(map[Status]struct{}, len(in))
	for _, raw := range in {
		s := Status(strings.ToLower(strings.TrimSpace(raw)))
		if !s.IsValidPerClaim() {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// DocKind discriminates insight verdicts from recommendation verdicts.
// Plan §"High-level shape and orchestrator ordering" — both kinds run
// through the same verifier package, but the dashboard renders them
// in different tabs and the validation log entries carry doc_kind.
type DocKind string

const (
	DocInsight        DocKind = "insight"
	DocRecommendation DocKind = "recommendation"
)

// AgentMode discriminates the verifier (defender frame) from the
// refuter (skeptic frame). Both run the same loop with different
// system prompts and budget knobs.
type AgentMode string

const (
	ModeVerifier AgentMode = "verifier"
	ModeRefuter  AgentMode = "refuter"
)
