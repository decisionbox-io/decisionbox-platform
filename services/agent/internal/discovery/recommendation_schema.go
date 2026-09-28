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
		// Described here for the reason the insight schema describes its own
		// figures array: a contract the prompt asks for and the schema does not
		// mention is one a decode-constrained provider will not emit, and the
		// failure is silent -- the prose keeps its references and nothing
		// resolves them.
		//
		// No `value` property, deliberately. The platform takes the number from
		// the figure the refs name, so a value here is one the model would be
		// inventing where the whole point is that it does not have to.
		"figures": map[string]interface{}{
			"type":        "array",
			"description": "Numbers this recommendation reports, each naming a figure an insight already declared. The prose carries {{id}} references and the platform renders the checked value into it.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":       str(`What the prose references, "f1". Unique within this recommendation.`),
					"kind":     str("`ref` to restate one insight figure, `sum` to total several"),
					"unit":     str("count, currency, percent, multiple or plain — notation only, never a word like days"),
					"scale":    str("thousands, millions or billions to abbreviate; omit to write in full"),
					"decimals": map[string]interface{}{"type": "integer", "description": "Decimal places to print. This is the precision being claimed."},
					"approx":   map[string]interface{}{"type": "boolean", "description": "Print a leading tilde to mark the number as rounded"},
					"refs": map[string]interface{}{
						"type":        "array",
						"description": "The insight figures this number comes from: one for `ref`, several for `sum`",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"insight": str("The insight's id, copied verbatim from the input"),
								"figure":  str(`The figure's id inside that insight's figures array, "f2"`),
							},
							"required": []interface{}{"insight", "figure"},
						},
					},
				},
				"required": []interface{}{"id", "kind", "refs"},
			},
		},
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
					// Prevention layer (#347): mark related_insight_ids required on
					// each item so schema-honouring providers (Ollama grammar,
					// OpenAI/LiteLLM json_schema) are nudged to emit the citation a
					// small model otherwise omits — the field the server-side
					// citation recovery would otherwise have to salvage/backfill.
					// Advisory (Strict stays false); byte-identical for models that
					// already cite, and a no-op on providers without structured output.
					"required": []interface{}{"related_insight_ids"},
				},
			},
		},
		"required": []interface{}{"recommendations"},
	}
}
