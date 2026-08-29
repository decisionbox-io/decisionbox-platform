package discovery

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// insightResponseFormatName is the schema identifier sent to providers that
// name their structured-output schema (OpenAI json_schema, the forced tool name
// on the Anthropic wire).
const insightResponseFormatName = "insights"

// insightResponseFormat returns the structured-output request for the analysis
// phase's corrective RETRY only. The first analysis call stays a plain Chat so
// big models that already work are byte-identical; this schema is attached only
// when a small/open model's first response yielded zero parseable insights and
// the provider supports structured output (ChatWithFormat self-gates on
// SupportsStructuredOutput, so it is a safe no-op elsewhere and the tolerant
// per-item parser is the always-on net).
//
// Strict is left false on purpose (mirrors recommendationResponseFormat): the
// shape is closed, but OpenAI strict mode requires every property to be
// `required` and forbids the open-ended `metrics` object we deliberately keep
// free-form. A permissive typed schema plus the tolerant parser is enough — the
// schema pins the shape where it can, the parser is the net everywhere else.
func insightResponseFormat() *gollm.ResponseFormat {
	return &gollm.ResponseFormat{
		Name:   insightResponseFormatName,
		Schema: insightResponseSchema(),
		Strict: false,
	}
}

// insightResponseSchema is the curated JSON Schema (draft 2020-12) for the
// insight envelope. It describes only the input-contract subset the LLM is
// meant to produce — deliberately excluding server-assigned/internal fields
// (id, analysis_area, discovered_at, validation, description_md, sql_metadata)
// so the generation contract cannot drift into asking the model for them. The
// property names are kept in lockstep with models.Insight json tags by
// TestInsightSchema_MatchesStructTags.
func insightResponseSchema() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}

	insightProps := map[string]interface{}{
		"name":           str("Short, specific name for the discovered pattern or finding"),
		"description":    str("Explanation of the finding, citing the supporting numbers"),
		"severity":       str(`One of "critical", "high", "medium", "low"`),
		"affected_count": map[string]interface{}{"type": "integer", "description": "Number of users/entities affected"},
		"risk_score":     map[string]interface{}{"type": "number", "description": "Risk score from 0.0 to 1.0"},
		"confidence":     map[string]interface{}{"type": "number", "description": "Confidence from 0.0 to 1.0"},
		"target_segment": str("The user segment this insight applies to"),
		"metrics": map[string]interface{}{
			"type":        "object",
			"description": "Free-form domain-specific metrics for this insight",
		},
		"indicators": map[string]interface{}{
			"type":        "array",
			"description": "Signals/behaviours that characterize this pattern",
			"items":       str("A single indicator"),
		},
		"source_steps": map[string]interface{}{
			"type":        "array",
			"description": "Exploration step numbers this insight is based on",
			"items":       map[string]interface{}{"type": "integer", "description": "An exploration step number"},
		},
	}

	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"insights": map[string]interface{}{
				"type":        "array",
				"description": "The discovered insights",
				"items": map[string]interface{}{
					"type":       "object",
					"properties": insightProps,
				},
			},
		},
		"required": []interface{}{"insights"},
	}
}
