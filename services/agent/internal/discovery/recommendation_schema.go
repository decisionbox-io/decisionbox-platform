package discovery

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// recommendationResponseFormatName is the schema identifier sent to providers
// that name their structured-output schema (OpenAI json_schema, the forced
// tool name on the Anthropic wire).
const recommendationResponseFormatName = "recommendations"

// recommendationReferenceRule is figureReferenceRule's counterpart for this phase, carried
// on every field that holds prose.
//
// Worded differently because the phase is different. An insight reads warehouse rows and
// declares arithmetic over them; a recommendation is handed the insights and nothing else,
// so every measured number it reports is already a checked figure in its input and naming
// one is the whole instruction.
//
// This schema is sent on every recommendation call, not only on a retry
// (generateRecommendations calls ChatWithFormat from attempt 0), so unlike the insight side
// this copy of the rule reaches the model today. Pinned by
// TestEveryProseFieldCarriesTheReferenceRule.
const recommendationReferenceRule = "Every measured number here names a figure an insight " +
	"already declared: add it to `figures` and write {{key}} in the text, never the number itself."

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
	recProps := recommendationItemProperties{
		Category:    boundedString("Recommendation category (e.g. churn, engagement, monetization)", maxSchemaLabelRunes),
		Title:       nonEmptyString("Specific, action-oriented recommendation title. One line, not a paragraph. "+recommendationReferenceRule, maxSchemaTitleRunes),
		Description: nonEmptyString("Explanation of the recommendation. "+recommendationReferenceRule, maxSchemaDescriptionRunes),
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
		Priority: map[string]interface{}{
			"type":        "integer",
			"description": "1 (highest priority) to 5 (lowest)",
			"minimum":     1,
			"maximum":     5,
		},
		TargetSegment: boundedString("The user segment this recommendation targets", maxSchemaSegmentRunes),
		SegmentSize:   map[string]interface{}{"type": "integer", "description": "Number of users in the target segment"},
		ExpectedImpact: map[string]interface{}{
			"type":        "object",
			"description": "Structured expected impact. MUST be an object, never a bare string.",
			"properties": expectedImpactProperties{
				Metric: nonEmptyString("Which metric is expected to improve. A metric is a name, not a number.", maxSchemaLabelRunes),
				// The mixed form, because this field legitimately carries both. The
				// contract's carve-out puts a projection here on purpose -- an estimate
				// is not a measurement -- so a rule against digits would contradict it.
				// What the old wording did teach was `"+$4,975/month"`: a typed amount
				// with a hardcoded currency, in the description of a field nothing
				// checks, on a warehouse that may report in any currency. That is the
				// same default that left every currency figure unmarked until
				// insightCurrencyFieldDescription was rewritten.
				EstimatedImprovement: nonEmptyString(`Expected improvement. What you project is yours and stays prose; the measured number it is projected from is a reference, e.g. "5-8% of the {{high_freq_buyers}} buyers"`, maxSchemaLabelRunes),
				Reasoning:            boundedString("Why this improvement is expected. "+recommendationReferenceRule, maxSchemaSentenceRunes),
			},
			// The two this object is worthless without. Requiring the object
			// and nothing inside it is the same hole one level down: run 16
			// persisted {"metric":"","estimated_improvement":""}, which ranks
			// no better than the absent field. `reasoning` stays optional --
			// it explains the impact rather than stating it.
			"required": []interface{}{"metric", "estimated_improvement"},
		},
		Actions: map[string]interface{}{
			"type":        "array",
			"description": "Concrete implementation steps",
			"items":       nonEmptyString("A single implementation step. "+recommendationReferenceRule, maxSchemaListItemRunes),
			"minItems":    1,
		},
		RelatedInsightIDs: map[string]interface{}{
			"type":        "array",
			"description": "UUIDs of the insights this recommendation addresses, copied verbatim from the input insights",
			"items":       nonEmptyString("An insight UUID from the input", maxSchemaIdentifierRunes),
			"minItems":    1,
		},
		Confidence: map[string]interface{}{"type": "number", "description": "Confidence from 0.0 to 1.0"},
		// Described here for the reason the insight schema describes its own
		// figures array: a contract the prompt asks for and the schema does not
		// mention is one a decode-constrained provider will not emit, and the
		// failure is silent -- the prose keeps its references and nothing
		// resolves them.
		//
		// No `value` property, deliberately. The platform takes the number from
		// the figure the refs name, so a value here is one the model would be
		// inventing where the whole point is that it does not have to.
		Figures: map[string]interface{}{
			"type":        "array",
			"description": "Numbers this recommendation reports, each naming a figure an insight already declared. The prose carries {{id}} references and the platform renders the checked value into it.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": recFigureItemProperties{
					ID:       nonEmptyString("Key the prose references, named for what it holds -- `band_total`, `win_back_revenue`. Unique within this recommendation.", maxSchemaIdentifierRunes),
					Kind:     nonEmptyString("`ref` to restate one insight figure, `sum` to total several", maxSchemaLabelRunes),
					Unit:     boundedString("count, currency, percent, multiple or plain — notation only, never a word like days", maxSchemaLabelRunes),
					Scale:    boundedString("thousands, millions or billions to abbreviate; omit to write in full", maxSchemaLabelRunes),
					Decimals: map[string]interface{}{"type": "integer", "description": "Decimal places to print. This is the precision being claimed."},
					Approx:   map[string]interface{}{"type": "boolean", "description": "Print a leading tilde to mark the number as rounded"},
					Refs: map[string]interface{}{
						"type":        "array",
						"description": "The insight figures this number comes from: one for `ref`, several for `sum`",
						"items": map[string]interface{}{
							"type": "object",
							"properties": recFigureRefProperties{
								Insight: nonEmptyString("The insight's id, copied verbatim from the input", maxSchemaIdentifierRunes),
								Figure:  nonEmptyString("The figure's key inside that insight's own figures array, copied verbatim", maxSchemaIdentifierRunes),
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
				// At least one (#437). A small model decoding against a schema
				// returns the shortest answer the schema accepts, and with an
				// unbounded minimum that is `{"recommendations": []}` -- observed
				// at eleven output tokens on a call that otherwise succeeded.
				//
				// Safe because this schema is only ever sent when at least one
				// insight is eligible: the phase above skips to
				// skipped_no_eligible_insights when none are, so "nothing worth
				// recommending" is answered before the grammar is involved and a
				// minimum here cannot force an invention. Whenever the model is
				// called there is something to ground a recommendation in.
				//
				// With the per-item required list below, the cheapest valid answer
				// is now one fully-formed recommendation rather than an empty array
				// or a title with nothing after it.
				"minItems": 1,
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
						// `figures` joins the list for the ordering reason measured on the
						// insight side in run 22: a grammar emits every required property
						// before any optional one, so an optional `figures` is moved behind
						// the required prose however early the struct declares it, and a model
						// that has written its prose can no longer reach it. The array may be
						// empty -- a recommendation that reports no number names no figure --
						// so this buys the position without forcing a declaration.
						"related_insight_ids", "figures", "description", "actions",
						"expected_impact", "title", "priority",
					},
				},
			},
		},
		"required": []interface{}{"recommendations"},
	}
}
