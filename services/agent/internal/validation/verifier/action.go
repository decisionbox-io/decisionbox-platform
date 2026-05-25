package verifier

import (
	"encoding/json"
	"fmt"
	"strings"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// ActionKind enumerates the four tools the agent can call. Plan §"Tool
// set — verifier-private parser".
type ActionKind string

const (
	ActionLookupSchema   ActionKind = "lookup_schema"
	ActionQueryWarehouse ActionKind = "query_warehouse"
	ActionReadStepRows   ActionKind = "read_step_rows"
	ActionSubmitVerdict  ActionKind = "submit_verdict"
)

// Action is one parsed agent turn. Exactly one of LookupSchemaRefs /
// Sql / StepRowsReq / Verdict is non-zero, discriminated by Kind.
type Action struct {
	Kind             ActionKind                    `json:"-"`
	LookupSchemaRefs []string                      `json:"lookup_schema,omitempty"`
	Sql              string                        `json:"query_warehouse,omitempty"`
	StepRowsReq      *StepRowsRequest              `json:"read_step_rows,omitempty"`
	Verdict          *valmodels.StructuredVerdict  `json:"submit_verdict,omitempty"`
}

// StepRowsRequest is the body of read_step_rows. Limit<=0 is treated
// as the default by the executor (typically 50).
type StepRowsRequest struct {
	StepID int `json:"step_id"`
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// ParseAction extracts exactly one Action from the model's response.
// Contract (exactly one envelope per response):
//
//  1. Strip code fences, locate the outermost balanced JSON object.
//  2. The object MUST contain exactly one of the four envelope keys.
//     More than one is a parse error (NOT first-key-wins).
//  3. Extra unknown top-level keys (e.g. `{"thinking": "...", "query_warehouse": "..."}`)
//     are also a parse error when an envelope key is present.
//  4. Bare-verdict fallback: when no envelope key is found but the
//     top-level object has `claims_considered` + `claim_verdicts` +
//     `overall`, treat the whole object as the submit_verdict body.
//  5. If `allowed` is non-empty, the chosen action's kind MUST be in
//     `allowed` — otherwise the parser rejects it. The forced-final
//     round passes [ActionSubmitVerdict] so any tool call gets
//     rejected.
//
// Returns (Action{}, error) on any failure; the agent loop logs the
// error to recent_tool_errors and lets the model retry.
func ParseAction(response string, allowed []ActionKind) (Action, error) {
	body := extractJSON(response)
	if body == "" {
		return Action{}, fmt.Errorf("no JSON object found in response")
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		return Action{}, fmt.Errorf("invalid JSON: %w", err)
	}

	recognised := make([]ActionKind, 0, 4)
	unknown := make([]string, 0)
	for k := range top {
		switch ActionKind(k) {
		case ActionLookupSchema, ActionQueryWarehouse, ActionReadStepRows, ActionSubmitVerdict:
			recognised = append(recognised, ActionKind(k))
		default:
			if !isBareVerdictKey(k) {
				unknown = append(unknown, k)
			}
		}
	}

	if len(recognised) > 1 {
		names := make([]string, 0, len(recognised))
		for _, n := range recognised {
			names = append(names, string(n))
		}
		return Action{}, fmt.Errorf("multiple action keys in one response (%s); emit exactly one per turn", strings.Join(names, ", "))
	}

	if len(recognised) == 1 && len(unknown) > 0 {
		return Action{}, fmt.Errorf("envelope must contain only one of {lookup_schema, query_warehouse, read_step_rows, submit_verdict}; extra top-level keys present: %s", strings.Join(unknown, ", "))
	}

	if len(recognised) == 0 {
		if isBareVerdict(top) {
			return parseVerdictRaw(body, allowed)
		}
		return Action{}, fmt.Errorf("no recognised action key (expected one of lookup_schema, query_warehouse, read_step_rows, submit_verdict)")
	}

	kind := recognised[0]
	if len(allowed) > 0 && !allowedKind(kind, allowed) {
		return Action{}, fmt.Errorf("action %q not allowed in this round", kind)
	}

	switch kind {
	case ActionLookupSchema:
		var refs []string
		if err := json.Unmarshal(top["lookup_schema"], &refs); err != nil {
			return Action{}, fmt.Errorf("lookup_schema must be an array of table references: %w", err)
		}
		return Action{Kind: kind, LookupSchemaRefs: refs}, nil
	case ActionQueryWarehouse:
		var sql string
		if err := json.Unmarshal(top["query_warehouse"], &sql); err != nil {
			return Action{}, fmt.Errorf("query_warehouse must be a SQL string (e.g. {\"query_warehouse\": \"SELECT 1\"}); object form like {\"sql\": ...} is not accepted: %w", err)
		}
		return Action{Kind: kind, Sql: sql}, nil
	case ActionReadStepRows:
		var req StepRowsRequest
		if err := json.Unmarshal(top["read_step_rows"], &req); err != nil {
			return Action{}, fmt.Errorf("read_step_rows must be {step_id, offset, limit}: %w", err)
		}
		return Action{Kind: kind, StepRowsReq: &req}, nil
	case ActionSubmitVerdict:
		var v valmodels.StructuredVerdict
		if err := json.Unmarshal(top["submit_verdict"], &v); err != nil {
			return Action{}, fmt.Errorf("submit_verdict payload invalid: %w", err)
		}
		return Action{Kind: kind, Verdict: &v}, nil
	}
	return Action{}, fmt.Errorf("unhandled action kind %q", kind)
}

// isBareVerdictKey returns true for top-level keys that are part of a
// bare-verdict object (claims_considered + claim_verdicts + overall +
// metadata fields). These keys are tolerated only when there is no
// envelope key — the fallback path then unmarshals the whole object
// as a StructuredVerdict.
func isBareVerdictKey(k string) bool {
	switch k {
	case "claims_considered", "claim_verdicts", "overall", "overall_reason",
		"doc_id", "doc_kind", "mode",
		"lookups_used", "queries_issued", "step_reads_used",
		"llm_tokens_in", "llm_tokens_out", "duration_millis":
		return true
	}
	return false
}

func allowedKind(k ActionKind, allowed []ActionKind) bool {
	for _, a := range allowed {
		if a == k {
			return true
		}
	}
	return false
}

func isBareVerdict(top map[string]json.RawMessage) bool {
	_, c1 := top["claims_considered"]
	_, c2 := top["claim_verdicts"]
	_, c3 := top["overall"]
	return c1 && c2 && c3
}

func parseVerdictRaw(body string, allowed []ActionKind) (Action, error) {
	if len(allowed) > 0 && !allowedKind(ActionSubmitVerdict, allowed) {
		return Action{}, fmt.Errorf("bare verdict not allowed in this round")
	}
	var v valmodels.StructuredVerdict
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return Action{}, fmt.Errorf("bare-verdict payload invalid: %w", err)
	}
	return Action{Kind: ActionSubmitVerdict, Verdict: &v}, nil
}

// extractJSON pulls the outermost balanced {...} block out of the
// model's response (which often wraps JSON in prose or code fences).
// Handles strings with escapes correctly.
func extractJSON(s string) string {
	// Strip leading code fence ```json ... ```.
	if idx := strings.Index(s, "```"); idx >= 0 {
		rest := s[idx+3:]
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			rest = rest[nl+1:]
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			s = rest[:end]
		}
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if inStr {
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
