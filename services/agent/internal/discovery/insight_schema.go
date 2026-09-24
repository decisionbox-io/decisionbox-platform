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
		"quantifier_claims": map[string]interface{}{
			"type": "array",
			"description": "One entry per statement whose truth depends on rows besides those it names " +
				"(only / every / largest / second largest / top N / improved each year / has N values)",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"claim":        str("The statement, verbatim as written in name, description or indicators"),
					"kind":         str(`One of "only", "all", "rank", "monotonic", "cardinality"`),
					"step":         map[string]interface{}{"type": "integer", "description": "Exploration step whose rows settle the claim"},
					"column":       str("Column the claim ranks by, or whose direction it asserts"),
					"filter":       str("Conjunction of `column <op> literal` terms joined by AND"),
					"scope":        str("Which rows the claim is about, same grammar as filter; applied before top_n"),
					"top_n":        map[string]interface{}{"type": "integer", "description": "Narrow the scope to the top N rows before applying the predicate"},
					"top_n_column": str("Column the top-N scope is ranked by"),
					"subject":      str("Terms selecting the single row a rank claim is about, same grammar as filter"),
					"rank":         map[string]interface{}{"type": "integer", "description": "1-based rank from the order end"},
					"count":        map[string]interface{}{"type": "integer", "description": "Asserted number of rows"},
					"order":        str(`"desc" (default) or "asc"`),
					"trend":        str(`"increasing" or "decreasing"`),
				},
			},
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
