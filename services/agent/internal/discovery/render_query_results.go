package discovery

import (
	"encoding/json"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// RenderCompactedSteps serializes the picked exploration steps for an
// analysis prompt's {{QUERY_RESULTS}} placeholder. Compact-by-default:
//
//   - step number, action, query, query_purpose, thinking, row_count,
//     error — kept verbatim because the LLM cites these in its
//     response (see analysis prompts' source_steps requirement) and
//     because the legacy JSON-marshal-the-whole-struct path included
//     the same fields, so prompt authors may already rely on them
//   - query_result is REPLACED by a CompactResult digest. If the
//     step already carries a CompactResult (computed at exploration
//     time), it is reused as-is. Otherwise the digest is rebuilt
//     here so legacy steps without the field still render compactly.
//
// The shape is deterministic — same input twice produces byte-
// identical JSON. Tests assert against golden output for this reason.
func RenderCompactedSteps(steps []models.ExplorationStep) string {
	view := buildCompactedView(steps)
	out, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		// json.MarshalIndent on a slice of plain structs cannot fail
		// for any input we accept; the err branch exists for safety
		// but is unreachable. Returning "[]" keeps the prompt valid
		// JSON if it ever did fail.
		return "[]"
	}
	return string(out)
}

// EstimateCompactedRenderedSize returns the byte size that
// RenderCompactedSteps would produce for the given steps. The picker
// uses this as its budget estimator without paying the cost of a
// second marshal — the marshal happens inline because Go's
// json.MarshalIndent allocates regardless. Caller can rely on
//
//	len(RenderCompactedSteps(s)) == EstimateCompactedRenderedSize(s)
//
// holding for any input.
func EstimateCompactedRenderedSize(steps []models.ExplorationStep) int {
	return len(RenderCompactedSteps(steps))
}

// compactedStep is the JSON shape we ship into the prompt. Every
// field maps 1:1 onto an analysis-prompt placeholder the domain
// packs already document.
type compactedStep struct {
	Step         int                    `json:"step"`
	Action       string                 `json:"action,omitempty"`
	Query        string                 `json:"query,omitempty"`
	QueryPurpose string                 `json:"query_purpose,omitempty"`
	Thinking     string                 `json:"thinking,omitempty"`
	RowCount     int                    `json:"row_count"`
	Error        string                 `json:"error,omitempty"`
	Compact      gomodels.CompactResult `json:"query_result"`
}

func buildCompactedView(steps []models.ExplorationStep) []compactedStep {
	out := make([]compactedStep, 0, len(steps))
	for _, s := range steps {
		var compact gomodels.CompactResult
		if s.CompactResult != nil {
			compact = *s.CompactResult
		} else {
			// Fallback: build the digest on-the-fly. Keeps legacy
			// rows / restored runs renderable without a one-shot
			// migration.
			compact = gomodels.BuildCompactResult(s.QueryResult)
		}
		out = append(out, compactedStep{
			Step:         s.Step,
			Action:       s.Action,
			Query:        s.Query,
			QueryPurpose: s.QueryPurpose,
			Thinking:     s.Thinking,
			RowCount:     s.RowCount,
			Error:        s.Error,
			Compact:      compact,
		})
	}
	return out
}
