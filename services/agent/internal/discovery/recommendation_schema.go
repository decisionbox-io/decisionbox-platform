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
	recProps := map[string]interface{}{
		"category":    boundedString("Recommendation category (e.g. churn, engagement, monetization)", maxSchemaLabelRunes),
		"title":       nonEmptyString("Specific, action-oriented recommendation title. One line, not a paragraph.", maxSchemaTitleRunes),
		"description": nonEmptyString("Explanation of the recommendation, citing the supporting numbers", maxSchemaDescriptionRunes),
		// Bounded as well as typed. An integer field cannot run away, but it
		// can arrive outside the scale the reader renders: the packs that get
		// this right say "emit as an integer, NOT a string", and two of the
		// three generated packs measured said `critical | high | medium | low`
		// instead -- which is why coercePriority exists. A model that cannot
		// write the word writes a number, and these bounds keep it on the
		// scale the field is documented as.
		//
		// These two survive grammar translation, unlike a string bound above
		// grammarRepetitionCeiling: json-schema-to-grammar.cpp routes an
		// integer carrying either keyword through build_min_max_int rather
		// than the unconstrained integer rule, so the range holds on an
		// Ollama-backed runner as well as on a json_schema provider.
		"priority": map[string]interface{}{
			"type":        "integer",
			"description": "1 (highest priority) to 5 (lowest)",
			"minimum":     1,
			"maximum":     5,
		},
		"target_segment": boundedString("The user segment this recommendation targets", maxSchemaSegmentRunes),
		"segment_size":   map[string]interface{}{"type": "integer", "description": "Number of users in the target segment"},
		"expected_impact": map[string]interface{}{
			"type":        "object",
			"description": "Structured expected impact. MUST be an object, never a bare string.",
			"properties": map[string]interface{}{
				"metric":                nonEmptyString("Which metric is expected to improve", maxSchemaLabelRunes),
				"estimated_improvement": nonEmptyString(`Expected improvement, e.g. "+15-20%" or "+$4,975/month"`, maxSchemaLabelRunes),
				"reasoning":             boundedString("Why this improvement is expected", maxSchemaSentenceRunes),
			},
			// The two this object is worthless without. Requiring the object
			// and nothing inside it is the same hole one level down: run 16
			// persisted {"metric":"","estimated_improvement":""}, which ranks
			// no better than the absent field. `reasoning` stays optional --
			// it explains the impact rather than stating it.
			"required": []interface{}{"metric", "estimated_improvement"},
		},
		"actions": map[string]interface{}{
			"type":        "array",
			"description": "Concrete implementation steps",
			"items":       nonEmptyString("A single implementation step", maxSchemaListItemRunes),
			"minItems":    1,
		},
		"related_insight_ids": map[string]interface{}{
			"type":        "array",
			"description": "UUIDs of the insights this recommendation addresses, copied verbatim from the input insights",
			"items":       nonEmptyString("An insight UUID from the input", maxSchemaIdentifierRunes),
			"minItems":    1,
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
					"id":       nonEmptyString(`What the prose references, "f1". Unique within this recommendation.`, maxSchemaIdentifierRunes),
					"kind":     nonEmptyString("`ref` to restate one insight figure, `sum` to total several", maxSchemaLabelRunes),
					"unit":     boundedString("count, currency, percent, multiple or plain — notation only, never a word like days", maxSchemaLabelRunes),
					"scale":    boundedString("thousands, millions or billions to abbreviate; omit to write in full", maxSchemaLabelRunes),
					"decimals": map[string]interface{}{"type": "integer", "description": "Decimal places to print. This is the precision being claimed."},
					"approx":   map[string]interface{}{"type": "boolean", "description": "Print a leading tilde to mark the number as rounded"},
					"refs": map[string]interface{}{
						"type":        "array",
						"description": "The insight figures this number comes from: one for `ref`, several for `sum`",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"insight": nonEmptyString("The insight's id, copied verbatim from the input", maxSchemaIdentifierRunes),
								"figure":  nonEmptyString(`The figure's id inside that insight's figures array, "f2"`, maxSchemaIdentifierRunes),
							},
							"required": []interface{}{"insight", "figure"},
						},
						// A declared figure with no refs names nothing, and the
						// renderer refuses it -- so the grammar should too.
						"minItems": 1,
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
					// Everything a recommendation is meaningless without.
					//
					// #347 put related_insight_ids here first, for a reason that
					// turned out to be the general one: a schema-honouring provider
					// (Ollama grammar, OpenAI/LiteLLM json_schema) emits what
					// `required` names, and a small model omits much of the rest
					// however clearly the prompt asks for it. That stayed a
					// single-field fix because the citation was the only field
					// observed missing.
					//
					// Then the string bounds stopped the runaway, the phase started
					// completing, and what it completed with was three
					// recommendations that were a title and nothing else -- no
					// description, no actions, priority unset, expected_impact an
					// empty object. The grammar permitted all of it. The one field
					// correct in all three was the one field named here, which is
					// the whole argument for this list: the pack prompt asks for
					// these fields in prose, the discipline rules reference them by
					// name, and a 27B model honoured neither.
					//
					// Advisory (Strict stays false); byte-identical for a model that
					// already fills these in, and a no-op on providers without
					// structured output. Held in lockstep with the reasons in
					// TestRecommendationItemRequiresTheFieldsARecommendationNeeds.
					"required": []interface{}{
						"title", "description", "priority",
						"expected_impact", "actions", "related_insight_ids",
					},
				},
			},
		},
		"required": []interface{}{"recommendations"},
	}
}
