package discovery

import (
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// insightResponseFormatName is the schema identifier sent to providers that
// name their structured-output schema (OpenAI json_schema, the forced tool name
// on the Anthropic wire).
const insightResponseFormatName = "insights"

// insightCurrencyFieldDescription describes the figure `currency` field.
//
// Named rather than written inline because the wording is the fix: both this and
// figureContract used to say to omit it for dollars, so an unmarked figure meant dollars
// by instruction, and most analysis calls in a run over a warehouse that does not report
// in dollars left every currency figure unmarked. Pinned by
// TestNeitherPromptTellsTheModelToOmitTheCurrency.
// figureReferenceRule is the one sentence every prose-bearing insight field carries, so the
// rule travels with the field and not only in the prompt.
//
// It exists because the two wires disagreed. figureContract tells the model every number it
// reads lives in `figures`; this schema described the same `description` field as prose
// "citing the supporting numbers", which is an invitation to type one. A field description
// is attached to the thing being written and is enforced as a grammar, so where the two
// disagree this is the one with the shorter path to the model.
//
// Deliberately one sentence. These descriptions are sent on every call that carries the
// schema, so the full argument stays in the contract and only the rule travels here.
//
// Known limit while this lands: the insight schema reaches the model on a corrective retry
// only -- 1 of 189 recorded analysis steps -- so on the analysis phase this is consistency
// work until the schema is attached to the first call. The recommendation schema is sent on
// every call, so its copy of the rule is live today. Pinned by
// TestEveryProseFieldCarriesTheReferenceRule.
const figureReferenceRule = "Every number you read from the rows belongs in `figures`: " +
	"declare it there and write {{key}} here, never the number itself."

// repairProseOnlyRule replaces figureReferenceRule in the repair schema, where a reference
// is not renderable -- see insightRepairResponseFormat.
const repairProseOnlyRule = "The numbers in this text are already rendered: rewrite the " +
	"words around them and do not add a {{key}} reference, which nothing will render."

const insightCurrencyFieldDescription = `Currency this amount is in: the symbol it ` +
	`prints or its ISO code, "₺" or "TRY". Always set it on a currency figure -- the ` +
	`warehouse's own currency is in the profile above, and a row in another currency is ` +
	`the case this field exists for`

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
	// Bounds, from the 562 insights this pipeline has written. A bound exists to stop a
	// runaway and must never be able to bind on a real answer, so each one clears its
	// observed maximum several times over: name 126, target_segment 136, an indicator 103,
	// a claim 142, the longest row/scope selector 261, every label and id under 20.
	//
	// Measured when the schema stopped being retry-only. While it was attached on a
	// corrective retry alone, describing an unbounded string here was a recorded risk
	// nobody had to take; sending it on every analysis call is what made these live.
	label := func(desc string) map[string]interface{} { return boundedString(desc, maxSchemaLabelRunes) }
	selector := func(desc string) map[string]interface{} { return boundedString(desc, maxSchemaSegmentRunes) }

	// The one string this schema still leaves unbounded, and the measurement that decides
	// it. Insight descriptions run long: p50 815, p95 1,146, p99 1,464, maximum 1,643. The
	// largest bound that does anything on an Ollama-backed runner is
	// grammarRepetitionCeiling (2,000) -- above it a maxLength is silently clamped back to
	// unbounded -- which leaves 1.22x the observed maximum and 1.37x p99. That is a bound
	// that can clip a legitimate description, and there is no higher one that protects
	// anything, so no bound is the honest answer rather than a comfortable-looking number.
	//
	// Named instead of reusing a generic helper so the exception is visible at the call
	// site, and pinned by TestTheInsightSchemaBoundsEveryStringItCan, which fails if a
	// second unbounded string appears.
	//
	// minLength is set even though no maximum is: `required` makes a key present and says
	// nothing about its value, so a grammar satisfies a required string with "". That is
	// the trap nonEmptyString exists for, and this field is required below.
	unboundedProse := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc, "minLength": 1}
	}

	insightProps := insightItemProperties{
		Name:        nonEmptyString("Short, specific name for the discovered pattern or finding. "+figureReferenceRule, maxSchemaTitleRunes),
		Description: unboundedProse("Explanation of the finding. " + figureReferenceRule),
		// nonEmptyString rather than label because this field is required below, and a
		// grammar satisfies a required string with "".
		Severity:      nonEmptyString(`One of "critical", "high", "medium", "low"`, maxSchemaLabelRunes),
		AffectedCount: map[string]interface{}{"type": "integer", "description": "Number of users/entities affected"},
		RiskScore:     map[string]interface{}{"type": "number", "description": "Risk score from 0.0 to 1.0"},
		Confidence:    map[string]interface{}{"type": "number", "description": "Confidence from 0.0 to 1.0"},
		TargetSegment: boundedString("The user segment this insight applies to", maxSchemaSegmentRunes),
		Metrics: map[string]interface{}{
			"type":        "object",
			"description": "Free-form domain-specific metrics for this insight",
		},
		Indicators: map[string]interface{}{
			"type":        "array",
			"description": "Signals/behaviours that characterize this pattern",
			"items":       boundedString("A single indicator. "+figureReferenceRule, maxSchemaListItemRunes),
		},
		Figures: map[string]interface{}{
			"type": "array",
			"description": "Every number this insight states, as data. The prose references them " +
				"as {{id}} and the platform renders the text.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": figureItemProperties{
					ID: nonEmptyString("Key the prose references; unique within this insight. Name it for what it "+
						"holds -- `churn_rate`, `total_revenue` -- not by position", maxSchemaIdentifierRunes),
					Value: map[string]interface{}{"type": "number", "description": "The number in the units of the step's own column: 8476238553, or 24.66 for a percentage"},
					// No "days". The closed set in models.Figure deliberately has no unit
					// for a word, because a unit here is notation and a word is prose --
					// the first live run rendered "{{f1}} days" with a days unit and
					// shipped "180.1 days days". This list advertised it for two commits
					// after the code dropped it, which is the schema inviting exactly the
					// mistake the contract forbids in prose; the schema is the stronger
					// signal, so it was the one being obeyed. Pinned by
					// TestInsightSchema_FigureEnumsMatchTheClosedSets.
					Unit:     label(`One of "count", "currency", "percent", "multiple", "plain" -- notation, never a word like days`),
					Currency: label(insightCurrencyFieldDescription),
					Scale:    label(`Abbreviation for a large number: "thousands", "millions", "billions"; omit to write it in full`),
					Decimals: map[string]interface{}{"type": "integer", "description": "Decimal places to print; this is the precision being claimed"},
					Approx:   map[string]interface{}{"type": "boolean", "description": "Print a tilde to mark the number as rounded"},
					Step:     map[string]interface{}{"type": "integer", "description": "Exploration step whose rows produced the figure"},
					// "excess" is listed beside "ratio" with the difference spelled out,
					// because the two read the same two cells and only the sentence tells
					// them apart. A run offering ratio alone shipped a vouched figure
					// behind "109.6% more" where the excess is 9.6%.
					Kind:   nonEmptyString(`One of "cell", "sum", "count", "ratio", "excess", "diff". "ratio" is a/b and "excess" is a/b-1, so a sentence saying one thing is N% MORE than another is an excess`, maxSchemaLabelRunes),
					Column: label("Column the arithmetic runs over"),
					Row:    selector("Terms selecting the single row a cell, a ratio or excess numerator, or a diff's left operand comes from"),
					Other:  selector("Terms selecting a diff's right operand, or the denominator row of a ratio or an excess"),
					Scope:  selector("Which rows a sum, a count, or a ratio or excess denominator covers"),
				},
				// []interface{} like every other required list in this schema. Both
				// marshal identically, but the mixed form made this list unreadable to
				// the shared test helper and so silently unpinned.
				"required": []interface{}{"id", "value", "step", "kind"},
			},
		},
		QuantifierClaims: map[string]interface{}{
			"type": "array",
			"description": "One entry per statement whose truth depends on rows besides those it names " +
				"(only / every / largest / second largest / top N / improved each year / has N values)",
			"items": map[string]interface{}{
				"type": "object",
				"properties": claimItemProperties{
					Claim:      nonEmptyString("The statement, verbatim as written in name, description or indicators", maxSchemaListItemRunes),
					Kind:       nonEmptyString(`One of "only", "all", "rank", "monotonic", "cardinality"`, maxSchemaLabelRunes),
					Step:       map[string]interface{}{"type": "integer", "description": "Exploration step whose rows settle the claim"},
					Column:     label("Column the claim ranks by, or whose direction it asserts"),
					Filter:     selector("Conjunction of `column <op> literal` terms joined by AND"),
					Scope:      selector("Which rows the claim is about, same grammar as filter; applied before top_n"),
					TopN:       map[string]interface{}{"type": "integer", "description": "Narrow the scope to the top N rows before applying the predicate"},
					TopNColumn: label("Column the top-N scope is ranked by"),
					Subject:    selector("Terms selecting the single row a rank claim is about, same grammar as filter"),
					Rank:       map[string]interface{}{"type": "integer", "description": "1-based rank from the order end"},
					Count: map[string]interface{}{"type": "integer", "minimum": 1,
						"description": "Asserted number of rows. Required for a cardinality claim, and must be positive: " +
							"an omitted count cannot be told from an asserted zero, so a cardinality claim without one is not checked"},
					Order: label(`"desc" (default) or "asc"`),
					// An enum rather than a description, because a trend the evaluator
					// cannot read is not checked at all. "decrease" or "down" used to be
					// silently treated as increasing, which refuted correct series.
					OrderBy: label("Required for a monotonic claim: the column that puts the rows in " +
						"sequence — the year, the decile, the band. The platform sorts by it ascending before " +
						"checking the trend, so the order your query returned does not matter. It must be a " +
						"numeric column, and it must not be the same column as `column`"),
					Trend: map[string]interface{}{"type": "string",
						"enum":        []interface{}{"increasing", "decreasing"},
						"description": "Required for a monotonic claim. Exactly \"increasing\" or \"decreasing\" — no other word is read"},
				},
				// The same defect as the insight object's, one level down, and found by
				// the same review: this object required nothing, so on a llama.cpp-backed
				// runner its optional properties were reachable only in Go's alphabetical
				// serialisation order. Every quantifierContract example opens with claim,
				// kind, step -- after which the grammar forbade returning to `column`,
				// `count`, `filter`, `rank` or `scope`, so a model following the examples
				// could not finish a checkable declaration. EvaluateQuantifierClaims then
				// marks the incomplete claim undecidable, which does not reach repair.
				//
				// Measured over the 439 claims this pipeline has written: claim, kind and
				// step are present in 439. Nothing else is required, because the rest are
				// kind-specific -- `column` 62%, `subject` 51%, `rank` 50%, `filter` 37%,
				// `count` 26%, `trend` and `order_by` under 2% -- and requiring one would
				// force a model to invent it for the kinds that do not use it. The three
				// required here are also the three figures.items already requires
				// alongside `id`, which is the same reasoning applied earlier.
				"required": []interface{}{"claim", "kind", "step"},
			},
		},
		SourceSteps: map[string]interface{}{
			"type":        "array",
			"description": "Exploration step numbers this insight is based on",
			"items":       map[string]interface{}{"type": "integer", "description": "An exploration step number"},
			// Required below, so minItems for the same reason name carries minLength: a
			// grammar satisfies a required array with [], and an insight citing no step is
			// a finding with no evidence behind it.
			"minItems": 1,
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
					// The fields an insight is worthless without.
					//
					// This object required nothing at all, which was survivable while the
					// schema was attached on a corrective retry only and became a defect
					// the moment it went on every analysis call: a schema that obliges no
					// field is presented as a contract while permitting `{"name": "..."}`,
					// and the tolerant parser accepts that, so nothing retries and the
					// area silently yields a headline with no finding under it. It is
					// sharpest on a llama.cpp-backed runner, where a JSON schema becomes a
					// grammar: optional properties are only reachable in the schema's own
					// order, Go serialises these map keys alphabetically, and a model
					// following the packs' name-first examples would have had no legal way
					// back to `description` or `figures` afterwards.
					//
					// Chosen from measurement, not taste: across the 562 insights this
					// pipeline has written, name, description, severity and source_steps
					// are present in 562. `figures` is in 88% and `quantifier_claims` in
					// 61% -- a finding that states no number declares no figure, and a
					// claim no quantifier covers needs no entry -- so requiring either
					// would force a model to invent one. The recommendation schema has
					// required six fields since platform#340, which is why that phase has
					// run on this wire without this failure.
					//
					// `figures` and `indicators` are also here for a second reason, and
					// it is the one run 22 measured. A grammar emits every required
					// property before any optional one, whatever order the schema
					// declares -- so an optional `figures` is moved behind the required
					// prose however early it is declared, and a model that has written
					// its prose can no longer reach it. On the small model that cost half
					// its figures. Being required is what keeps them in front.
					//
					// Both may be empty. An insight that states no number declares no
					// figure and `figures: []` says so honestly; the alternative is
					// forcing a model to invent one, which is worse than the ordering bug.
					// quantifier_claims stays optional because it quotes prose that does
					// not exist yet when the evidence is written.
					//
					// Pinned by TestTheInsightSchemaRequiresWhatAnInsightCannotLack and
					// TestSchemaPropertyOrderIsDeclaredNotAlphabetical.
					"required": []interface{}{
						"source_steps", "figures", "description", "indicators", "name", "severity",
					},
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
//
// Deleting the key is no longer enough on its own. Every prose field carries
// figureReferenceRule -- "declare it there and write {{key}} here" -- which in this schema
// would name an array that is not here, asking the repair for exactly the placeholder the
// paragraph above exists to prevent. So this schema is built from its own properties
// struct, whose prose fields carry repairProseOnlyRule instead.
//
// Built rather than copied-and-edited. It used to clone the analysis schema and delete a
// map key, which an ordered struct cannot do -- and the clone was never the honest
// description anyway, because three of its fields have to ask for something different
// here. Declaring the shape says that out loud. Pinned by
// TestTheRepairSchemaAsksForNoReference.
func insightRepairResponseFormat() *gollm.ResponseFormat {
	analysis := objectProperties(insightItemSchemaFrom(insightResponseSchema()))
	keep := func(field string) map[string]interface{} {
		node, _ := analysis[field].(map[string]interface{})
		return node
	}
	props := insightRepairItemProperties{
		SourceSteps:      keep("source_steps"),
		Description:      proseWithoutReference(keep("description")),
		Indicators:       indicatorsWithoutReference(keep("indicators")),
		Name:             proseWithoutReference(keep("name")),
		QuantifierClaims: keep("quantifier_claims"),
		Severity:         keep("severity"),
		TargetSegment:    keep("target_segment"),
		AffectedCount:    keep("affected_count"),
		RiskScore:        keep("risk_score"),
		Confidence:       keep("confidence"),
		Metrics:          keep("metrics"),
	}
	return &gollm.ResponseFormat{
		Name: insightResponseFormatName,
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"insights": map[string]interface{}{
					"type":        "array",
					"description": "The discovered insights",
					"items": map[string]interface{}{
						"type":       "object",
						"properties": props,
						// `figures` is gone, so nothing has to precede the prose and the
						// required set is the measured one minus the two that were there
						// only to hold the evidence in front of it.
						"required": []interface{}{"source_steps", "description", "name", "severity"},
					},
				},
			},
			"required": []interface{}{"insights"},
		},
		Strict: false,
	}
}

// insightItemSchemaFrom returns the per-insight object out of an envelope.
func insightItemSchemaFrom(schema map[string]interface{}) map[string]interface{} {
	props := objectProperties(schema)
	arr, _ := props["insights"].(map[string]interface{})
	items, _ := arr["items"].(map[string]interface{})
	return items
}

// proseWithoutReference copies a prose field's schema with the reference rule swapped for
// the repair's own. Copied rather than edited in place, so the two schemas share no state.
func proseWithoutReference(node map[string]interface{}) map[string]interface{} {
	if node == nil {
		return nil
	}
	out := make(map[string]interface{}, len(node))
	for k, v := range node {
		out[k] = v
	}
	if desc, ok := out["description"].(string); ok {
		out["description"] = strings.Replace(desc, figureReferenceRule, repairProseOnlyRule, 1)
	}
	return out
}

// indicatorsWithoutReference is proseWithoutReference for the array whose items carry the
// rule rather than the array itself.
func indicatorsWithoutReference(node map[string]interface{}) map[string]interface{} {
	if node == nil {
		return nil
	}
	out := make(map[string]interface{}, len(node))
	for k, v := range node {
		out[k] = v
	}
	if items, ok := out["items"].(map[string]interface{}); ok {
		out["items"] = proseWithoutReference(items)
	}
	return out
}
