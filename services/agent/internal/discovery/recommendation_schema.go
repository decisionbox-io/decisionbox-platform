package discovery

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// recommendationResponseFormatName is the schema identifier sent to providers
// that name their structured-output schema (OpenAI json_schema, the forced
// tool name on the Anthropic wire).
const recommendationResponseFormatName = "recommendations"

// recommendationResponseFormat returns the structured-output request for the
// recommendation phase, or nil when the schema is empty. Attached to the LLM
// call on providers that support structured output (platform#340) so the model
// is decode-constrained into the recommendation envelope — in particular an
// `expected_impact` object rather than the prose string that silently zeroed
// whole batches (issue #342), and the `{"recommendations": [...]}` envelope
// rather than a bare top-level array.
//
// Strict is left false on purpose: the shape is closed, but OpenAI strict mode
// requires every property to be `required` and forbids the open-ended objects
// platform#340 went out of its way to preserve. A permissive typed schema plus
// the tolerant parser (parseRecommendations) is enough — the schema pins the
// shape where it can, the parser is the net everywhere else.
func recommendationResponseFormat() *gollm.ResponseFormat {
	return &gollm.ResponseFormat{
		Name:   recommendationResponseFormatName,
		Schema: recommendationResponseSchema(),
		Strict: false,
	}
}

// recommendationResponseSchema is the curated JSON Schema (draft 2020-12) for
// the recommendation envelope. It describes only the input-contract subset the
// LLM is meant to produce — deliberately excluding server-assigned/internal
// fields (id, created_at, validation, description_md) so the generation
// contract cannot drift into asking the model for them. The property names are
// kept in lockstep with models.Recommendation / models.Impact json tags by
// TestRecommendationSchema_MatchesStructTags.
func recommendationResponseSchema() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	strItems := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}

	recProps := map[string]interface{}{
		"category":       str("Recommendation category (e.g. churn, engagement, monetization)"),
		"title":          str("Specific, action-oriented recommendation title"),
		"description":    str("Explanation of the recommendation, citing the supporting numbers"),
		"priority":       map[string]interface{}{"type": "integer", "description": "1 (highest priority) to 5 (lowest)"},
		"target_segment": str("The user segment this recommendation targets"),
		"segment_size":   map[string]interface{}{"type": "integer", "description": "Number of users in the target segment"},
		"expected_impact": map[string]interface{}{
			"type":        "object",
			"description": "Structured expected impact. MUST be an object, never a bare string.",
			"properties": map[string]interface{}{
				"metric":                str("Which metric is expected to improve"),
				"estimated_improvement": str(`Expected improvement, e.g. "+15-20%" or "+$4,975/month"`),
				"reasoning":             str("Why this improvement is expected"),
			},
		},
		"actions": map[string]interface{}{
			"type":        "array",
			"description": "Concrete implementation steps",
			"items":       strItems("A single implementation step"),
		},
		"related_insight_ids": map[string]interface{}{
			"type":        "array",
			"description": "UUIDs of the insights this recommendation addresses, copied verbatim from the input insights",
			"items":       strItems("An insight UUID from the input"),
		},
		"confidence": map[string]interface{}{"type": "number", "description": "Confidence from 0.0 to 1.0"},
	}

	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"recommendations": map[string]interface{}{
				"type":        "array",
				"description": "The generated recommendations",
				"items": map[string]interface{}{
					"type":       "object",
					"properties": recProps,
				},
			},
		},
		"required": []interface{}{"recommendations"},
	}
}
