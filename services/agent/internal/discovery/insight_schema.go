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
	insightProps := map[string]interface{}{
		"name":           boundedString("Short, specific name for the discovered pattern or finding", maxSchemaTitleRunes),
		"description":    boundedString("Explanation of the finding, citing the supporting numbers", maxSchemaDescriptionRunes),
		"severity":       boundedString(`One of "critical", "high", "medium", "low"`, maxSchemaLabelRunes),
		"affected_count": map[string]interface{}{"type": "integer", "description": "Number of users/entities affected"},
		"risk_score":     map[string]interface{}{"type": "number", "description": "Risk score from 0.0 to 1.0"},
		"confidence":     map[string]interface{}{"type": "number", "description": "Confidence from 0.0 to 1.0"},
		"target_segment": boundedString("The user segment this insight applies to", maxSchemaSegmentRunes),
		"metrics": map[string]interface{}{
			"type":        "object",
			"description": "Free-form domain-specific metrics for this insight",
		},
		"indicators": map[string]interface{}{
			"type":        "array",
			"description": "Signals/behaviours that characterize this pattern",
			"items":       boundedString("A single indicator", maxSchemaListItemRunes),
		},
		"figures": map[string]interface{}{
			"type": "array",
			"description": "Every number this insight states, as data. The prose references them " +
				"as {{id}} and the platform renders the text.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id":    boundedString(`Reference the prose uses, e.g. "f1"; unique within this insight`, maxSchemaIdentifierRunes),
					"value": map[string]interface{}{"type": "number", "description": "The number in the units of the step's own column: 8476238553, or 24.66 for a percentage"},
					// No "days". The closed set in models.Figure deliberately has no unit
					// for a word, because a unit here is notation and a word is prose --
					// the first live run rendered "{{f1}} days" with a days unit and
					// shipped "180.1 days days". This list advertised it for two commits
					// after the code dropped it, which is the schema inviting exactly the
					// mistake the contract forbids in prose; the schema is the stronger
					// signal, so it was the one being obeyed. Pinned by
					// TestInsightSchema_FigureEnumsMatchTheClosedSets.
					"unit":     boundedString(`One of "count", "currency", "percent", "multiple", "plain" -- notation, never a word like days`, maxSchemaLabelRunes),
					"currency": boundedString(`Symbol a currency figure prints; omit for dollars, set it when the data is not in dollars (e.g. "€")`, maxSchemaLabelRunes),
					"scale":    boundedString(`Abbreviation for a large number: "thousands", "millions", "billions"; omit to write it in full`, maxSchemaLabelRunes),
					"decimals": map[string]interface{}{"type": "integer", "description": "Decimal places to print; this is the precision being claimed"},
					"approx":   map[string]interface{}{"type": "boolean", "description": "Print a tilde to mark the number as rounded"},
					"step":     map[string]interface{}{"type": "integer", "description": "Exploration step whose rows produced the figure"},
					// "excess" is listed beside "ratio" with the difference spelled out,
					// because the two read the same two cells and only the sentence tells
					// them apart. A run offering ratio alone shipped a vouched figure
					// behind "109.6% more" where the excess is 9.6%.
					"kind":   boundedString(`One of "cell", "sum", "count", "ratio", "excess", "diff". "ratio" is a/b and "excess" is a/b-1, so a sentence saying one thing is N% MORE than another is an excess`, maxSchemaLabelRunes),
					"column": boundedString("Column the arithmetic runs over", maxSchemaIdentifierRunes),
					"row":    boundedString("Terms selecting the single row a cell, a ratio or excess numerator, or a diff's left operand comes from", maxSchemaExpressionRunes),
					"other":  boundedString("Terms selecting a diff's right operand, or the denominator row of a ratio or an excess", maxSchemaExpressionRunes),
					"scope":  boundedString("Which rows a sum, a count, or a ratio or excess denominator covers", maxSchemaExpressionRunes),
				},
				"required": []string{"id", "value", "step", "kind"},
			},
		},
		"quantifier_claims": map[string]interface{}{
			"type": "array",
			"description": "One entry per statement whose truth depends on rows besides those it names " +
				"(only / every / largest / second largest / top N / improved each year / has N values)",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"claim":        boundedString("The statement, verbatim as written in name, description or indicators", maxSchemaDescriptionRunes),
					"kind":         boundedString(`One of "only", "all", "rank", "monotonic", "cardinality"`, maxSchemaLabelRunes),
					"step":         map[string]interface{}{"type": "integer", "description": "Exploration step whose rows settle the claim"},
					"column":       boundedString("Column the claim ranks by, or whose direction it asserts", maxSchemaIdentifierRunes),
					"filter":       boundedString("Conjunction of `column <op> literal` terms joined by AND", maxSchemaExpressionRunes),
					"scope":        boundedString("Which rows the claim is about, same grammar as filter; applied before top_n", maxSchemaExpressionRunes),
					"top_n":        map[string]interface{}{"type": "integer", "description": "Narrow the scope to the top N rows before applying the predicate"},
					"top_n_column": boundedString("Column the top-N scope is ranked by", maxSchemaIdentifierRunes),
					"subject":      boundedString("Terms selecting the single row a rank claim is about, same grammar as filter", maxSchemaExpressionRunes),
					"rank":         map[string]interface{}{"type": "integer", "description": "1-based rank from the order end"},
					"count": map[string]interface{}{"type": "integer", "minimum": 1,
						"description": "Asserted number of rows. Required for a cardinality claim, and must be positive: " +
							"an omitted count cannot be told from an asserted zero, so a cardinality claim without one is not checked"},
					"order": boundedString(`"desc" (default) or "asc"`, maxSchemaLabelRunes),
					// An enum rather than a description, because a trend the evaluator
					// cannot read is not checked at all. "decrease" or "down" used to be
					// silently treated as increasing, which refuted correct series.
					"order_by": boundedString("Required for a monotonic claim: the column that puts the rows in "+
						"sequence — the year, the decile, the band. The platform sorts by it ascending before "+
						"checking the trend, so the order your query returned does not matter. It must be a "+
						"numeric column, and it must not be the same column as `column`", maxSchemaDescriptionRunes),
					"trend": map[string]interface{}{"type": "string",
						"enum":        []interface{}{"increasing", "decreasing"},
						"description": "Required for a monotonic claim. Exactly \"increasing\" or \"decreasing\" — no other word is read"},
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

// insightRepairResponseFormat is the analysis schema with `figures` removed, for the bounded
// repair pass.
//
// rewriteInsight reuses the analysis response format, so describing `figures` there quietly
// handed the repair prompt a facility the repair path cannot support: mergeRepairedInsight
// copies the rewritten prose and not its figures, and rendering has already finished by the
// time repair runs. A structured-output repair could therefore return "{{f1}}" with a
// matching declaration, pass the quantifier checks, and ship the placeholder to a reader.
//
// Removing the key is the fix rather than teaching repair to render, because by that point
// every number in the insight is already rendered text and a repair that wants to change one
// is changing prose. There is nothing for it to declare.
func insightRepairResponseFormat() *gollm.ResponseFormat {
	schema := insightResponseSchema()
	props, _ := schema["properties"].(map[string]interface{})
	items, _ := props["insights"].(map[string]interface{})["items"].(map[string]interface{})
	if itemProps, ok := items["properties"].(map[string]interface{}); ok {
		delete(itemProps, "figures")
	}
	return &gollm.ResponseFormat{
		Name:   insightResponseFormatName,
		Schema: schema,
		Strict: false,
	}
}
