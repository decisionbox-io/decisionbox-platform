package validation

// StructuredVerdict is the agent's terminal output. The verifier
// (defender) emits one; the refuter (skeptic) emits another; Combine()
// merges them into a final Status persisted on InsightValidation.
//
// Wire shape is locked by the agent prompts — see plan §"Verdict
// schema" in services/agent/internal/validation/verifier/prompts/.
//
// All fields use snake_case bson/json tags to match the LLM's
// emitted shape and the legacy ValidationResult shape (so MongoDB
// decode round-trips cleanly without an explicit migration step).
type StructuredVerdict struct {
	DocID            string         `bson:"doc_id"            json:"doc_id"`
	DocKind          DocKind        `bson:"doc_kind"          json:"doc_kind"`
	Mode             AgentMode      `bson:"mode"              json:"mode"`
	ClaimsConsidered []string       `bson:"claims_considered" json:"claims_considered"`
	ClaimVerdicts    []ClaimVerdict `bson:"claim_verdicts"    json:"claim_verdicts"`
	Overall          Status         `bson:"overall"           json:"overall"`
	OverallReason    string         `bson:"overall_reason"    json:"overall_reason"`

	// Bookkeeping populated by the agent loop, not by the LLM.
	LookupsUsed    int   `bson:"lookups_used"    json:"lookups_used"`
	QueriesIssued  int   `bson:"queries_issued"  json:"queries_issued"`
	StepReadsUsed  int   `bson:"step_reads_used" json:"step_reads_used"`
	LLMTokensIn    int   `bson:"llm_tokens_in"   json:"llm_tokens_in"`
	LLMTokensOut   int   `bson:"llm_tokens_out"  json:"llm_tokens_out"`
	DurationMillis int64 `bson:"duration_millis" json:"duration_millis"`
}

// ClaimVerdict is the agent's per-claim assessment. Exactly one entry
// in StructuredVerdict.ClaimVerdicts has IsHeadline=true AND its
// ClaimText equals StructuredVerdict.ClaimsConsidered[0]. The
// finaliser enforces this positional rule.
type ClaimVerdict struct {
	ClaimText  string        `bson:"claim_text"  json:"claim_text"`
	ClaimKind  string        `bson:"claim_kind"  json:"claim_kind"`
	IsHeadline bool          `bson:"is_headline" json:"is_headline"`
	Status     Status        `bson:"status"      json:"status"`
	Reasoning  string        `bson:"reasoning"   json:"reasoning"`
	Evidence   ClaimEvidence `bson:"evidence"    json:"evidence"`
}

// ClaimEvidence carries one row of supporting (or contradicting) data
// for a claim. Every claim verdict whose Status is non-unverifiable
// MUST have Evidence.Kind != "none" AND a non-nil Evidence.Row — the
// finaliser downgrades to partial otherwise.
type ClaimEvidence struct {
	// Kind discriminates how the row was obtained:
	//   "step_row"        — read_step_rows from an exploration step's snapshot
	//   "warehouse_query" — query_warehouse executed during validation
	//   "none"            — only valid when claim Status == unverifiable
	Kind string `bson:"kind" json:"kind"`

	// StepID is set when Kind == "step_row".
	StepID int `bson:"step_id,omitempty" json:"step_id,omitempty"`

	// QuerySQL is set when Kind == "warehouse_query".
	QuerySQL string `bson:"query_sql,omitempty" json:"query_sql,omitempty"`

	// Row is the actual row the agent cites. Must be non-nil for
	// non-unverifiable claims.
	Row map[string]any `bson:"row,omitempty" json:"row,omitempty"`

	// AdditionalRows reports how many other rows in the same result
	// set the agent could have cited. Lets the dashboard show
	// "+N more" without forcing the agent to attach them all.
	AdditionalRows int `bson:"additional_rows,omitempty" json:"additional_rows,omitempty"`
}

// Combine merges a verifier verdict with an optional refuter verdict
// into the document's final combined Status. Mirror of plan §"Combine
// — full 7-status matrix".
//
//   - refuterDisabled distinguishes "refuter intentionally not run"
//     from "refuter should have run but returned nil". Disabled means
//     the refuter side carries no weight; nil-when-enabled is treated
//     as an evidence gap (incomplete).
//   - Returns (combinedStatus, refuterDisabledFlag). Both are persisted
//     on InsightValidation so the dashboard can render the right
//     legend (e.g. "verifier supported; refuter intentionally off").
func Combine(v, r *StructuredVerdict, refuterDisabled bool) (Status, bool) {
	if v == nil {
		return StatusUnverifiable, refuterDisabled
	}

	// Verifier trip-wires preserved verbatim — these short-circuit
	// every other rule because they signal "agent never ran".
	if v.Overall == StatusValidationDisabled {
		return StatusValidationDisabled, refuterDisabled
	}
	if v.Overall == StatusSkippedBudgetCap {
		return StatusSkippedBudgetCap, refuterDisabled
	}

	// Either side's rejected is authoritative — counter-evidence
	// stands regardless of the other side's verdict.
	if v.Overall == StatusRejected {
		return StatusRejected, refuterDisabled
	}
	if r != nil && r.Overall == StatusRejected {
		return StatusRejected, refuterDisabled
	}

	rState := refuterState(r, refuterDisabled)

	switch v.Overall {
	case StatusUnverifiable:
		return StatusUnverifiable, refuterDisabled
	case StatusPartial:
		return StatusPartial, refuterDisabled
	case StatusConfirmed:
		if rState == refuterOK {
			return StatusConfirmed, refuterDisabled
		}
		return StatusSupported, refuterDisabled
	case StatusSupported:
		if rState == refuterOK {
			return StatusSupported, refuterDisabled
		}
		return StatusPartial, refuterDisabled
	}
	// Unknown or empty verifier overall — conservative.
	return StatusUnverifiable, refuterDisabled
}

// refuterState collapses the refuter's branch of the truth-table into
// three cases:
//   - refuterOK         — refuter ran and produced a supportive verdict
//                         (or is intentionally disabled-by-config)
//   - refuterIncomplete — refuter ran but didn't reach a clean state
//                         (partial/unverifiable/disabled/skipped); OR
//                         refuter was expected to run but didn't (r==nil
//                         while refuterDisabled==false)
type refuterCondition int

const (
	refuterAbsent refuterCondition = iota
	refuterOK
	refuterIncomplete
)

func refuterState(r *StructuredVerdict, refuterDisabled bool) refuterCondition {
	if refuterDisabled {
		return refuterOK
	}
	if r == nil {
		return refuterIncomplete
	}
	switch r.Overall {
	case StatusConfirmed, StatusSupported:
		return refuterOK
	case StatusPartial, StatusUnverifiable,
		StatusValidationDisabled, StatusSkippedBudgetCap:
		return refuterIncomplete
	case StatusRejected:
		// Caller already short-circuited on rejected; treat as
		// incomplete defensively (Combine returns rejected before
		// reaching here).
		return refuterIncomplete
	}
	return refuterAbsent
}
