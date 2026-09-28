package discovery

import (
	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// reflectionResponseFormatName is the schema identifier sent to providers that
// name their structured-output schema.
const reflectionResponseFormatName = "discovery_reflection"

// reflectionResponseFormat returns the structured-output request for the
// reflection phase. ChatWithFormat self-gates on SupportsStructuredOutput, so
// this is a safe no-op on providers without it (the tolerant parser is the
// always-on net). Strict is false for the same reason as the questions schema:
// OpenAI strict mode requires every property required and forbids open objects.
func reflectionResponseFormat(mode agentplugin.EvolutionMode, demandPriorRejudgement bool) *gollm.ResponseFormat {
	return &gollm.ResponseFormat{
		Name:   reflectionResponseFormatName,
		Schema: reflectionResponseSchema(mode, demandPriorRejudgement),
		Strict: false,
	}
}

// reflectionResponseSchema is the curated JSON Schema (draft 2020-12) for the
// reflection envelope. It describes only the judgment content the model
// produces — coverage, prior-finding status re-judgement, durable learnings,
// next-tasks, and domain-pack deltas — never the server-assigned ids/timestamps.
// Property names track parsedReflection's json tags via
// TestReflectionSchema_MatchesStructTags.
//
// What is REQUIRED varies with the run, because on a provider with structured
// output the schema is the contract the model is decoded against, and a small
// model answers the shortest thing that satisfies it: everything optional comes
// back empty, run after run (#434). So each judgment output the run can
// actually produce is required, and only those:
//
//   - learnings — always. Every run touches this warehouse and learns
//     something durable about it.
//   - prior_status_updates — only when the ledger carried findings INTO this
//     run and the prompt lists them. There is nothing to re-judge on an early
//     run, and requiring a verdict with no finding to attach it to invites an
//     invented id.
//   - next_tasks — only when evolution is on. Off means the ledger records but
//     does not self-direct, so an empty queue is the correct answer there.
//
// domain_pack_deltas stays optional in every mode: a pack change is warranted
// only by a signal that keeps recurring across runs, so most runs genuinely
// have none and demanding one every time would churn the pack.
func reflectionResponseSchema(mode agentplugin.EvolutionMode, demandPriorRejudgement bool) map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	strArray := func(desc string) map[string]interface{} {
		return map[string]interface{}{
			"type":        "array",
			"description": desc,
			"items":       map[string]interface{}{"type": "string"},
		}
	}

	props := map[string]interface{}{
		"coverage_summary": str("One short paragraph: which tables/areas are now well covered and what remains unexplored (the frontier)."),
		"covered_tables":   strArray("Fully-qualified tables (dataset.table) this run actually queried/covered. Copy names from the warehouse catalog verbatim."),
		// Declared unconditionally, unlike the prompt's cube section, which
		// renders only for a run that has one. The schema is a shape for a
		// response, not instruction: an optional array a table-only run is
		// never asked to fill costs it nothing, and its description says so.
		// Keeping the PROMPT byte-identical for those runs is what matters,
		// since that is what teaches — and a stray item arriving anyway is
		// checked against the run's catalog before it is stored.
		"covered_catalog_items": strArray("Cube metrics/dimensions this run actually queried. Copy names from the cube catalog verbatim. Empty when this project has no cube-shaped datasource."),
		"covered_areas":         strArray("Analysis-area ids that produced findings this run."),
		"convergence_note":      str("One line: is the investigation still finding much that is new, or converging?"),
		"prior_status_updates": map[string]interface{}{
			"type":        "array",
			"description": "Status re-judgements for PRIOR findings (by id). Only when this run gives grounded evidence — do NOT mark a finding resolved merely because it did not reappear.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"finding_id": str("The prior finding id, copied verbatim from the PRIOR FINDINGS list"),
					"status":     map[string]interface{}{"type": "string", "enum": []string{"confirmed", "monitoring", "changed", "resolved", "refuted"}},
					"reason":     str("One line: the evidence for this status"),
				},
				"required": []interface{}{"finding_id", "status"},
			},
		},
		"task_status_updates": map[string]interface{}{
			"type":        "array",
			"description": "Close OPEN tasks (by id) this run resolved: 'done' when a finding from THIS run answers the task, 'dropped' when it is a proven dead-end / no longer relevant. Only with grounded evidence — leave a partially-explored or untouched task alone. Empty if you closed none.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task_id": str("The open task id, copied verbatim from the OPEN TASKS list"),
					"status":  map[string]interface{}{"type": "string", "enum": []string{"done", "dropped"}},
				},
				"required": []interface{}{"task_id", "status"},
			},
		},
		"learnings": map[string]interface{}{
			"type":        "array",
			"description": "Durable, reusable learnings about this warehouse/domain (opaque codes decoded, a table's grain, a join that works). Not findings — operating knowledge for future runs.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"category":  str("Short tag: schema / domain / warehouse / data-quality"),
					"note":      str("The durable learning, one or two sentences"),
					"relevance": map[string]interface{}{"type": "number", "description": "0..1 importance"},
				},
				"required": []interface{}{"note"},
			},
		},
		"next_tasks": map[string]interface{}{
			"type":        "array",
			"description": "Self-directed investigation threads for the NEXT run (couldn't verify X → check; A⋈B looked anomalous → investigate; table Z untouched → explore). Empty when evolution is off.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":       str("A short, plain-language title a business user would understand (max ~8 words; no table/column names or SQL jargon)"),
					"text":        str("The detailed, technical description of the task as an actionable next step (may reference tables, columns, metrics, the specific hypothesis)"),
					"kind":        map[string]interface{}{"type": "string", "enum": []string{"next_task", "hypothesis"}},
					"target_type": map[string]interface{}{"type": "string", "enum": []string{"insight", "recommendation", "table", "area"}},
					"target_id":   str("Optional: the id/name this task is about"),
					"supersedes":  str("Optional: the id of an OPEN task (from the OPEN TASKS list) this thread continues — set it when this follow-up grew out of a task you are marking done"),
				},
				"required": []interface{}{"title", "text"},
			},
		},
		"domain_pack_deltas": map[string]interface{}{
			"type":        "array",
			"description": "Proposed analysis-area changes grounded in recurring findings (strengthen a fraud area, add a churn area). Empty when evolution is off.",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action":    map[string]interface{}{"type": "string", "enum": []string{"add_area", "edit_area", "disable_area", "enable_area"}},
					"area_id":   str("The analysis-area id (existing for edit/disable/enable; a new slug for add)"),
					"area_name": str("Human-readable area name (for add/edit)"),
					"prompt":    str("The area's analysis prompt (for add/edit)"),
					"keywords":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Area keywords (for add/edit)"},
					"rationale": str("Why this change — grounded in the findings above (required)"),
				},
				"required": []interface{}{"action", "area_id", "rationale"},
			},
		},
	}

	// Required grows with what this run can honestly answer; see the doc
	// comment. minItems pairs with each addition so that "present" cannot be
	// satisfied by an empty array.
	required := []interface{}{"coverage_summary", "learnings"}
	requireNonEmpty(props, "learnings",
		"Give at least one: every run learns something durable about this warehouse.")
	if demandPriorRejudgement {
		required = append(required, "prior_status_updates")
		requireNonEmpty(props, "prior_status_updates",
			"This run carries prior findings: re-judge at least one — a prior finding this run saw again is grounded evidence for confirmed.")
	}
	if mode != agentplugin.EvolutionModeOff {
		required = append(required, "next_tasks")
		requireNonEmpty(props, "next_tasks",
			"Evolution is ON for this run: propose at least one, grounded in this run's findings and coverage.")
	}

	return map[string]interface{}{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

// requireNonEmpty marks an array property as needing at least one item and
// appends demand to its description. Both halves are load-bearing: a required
// array with no minimum is satisfied by [] — the answer this change exists to
// stop — and a description that still reads as optional argues against the
// constraint sitting next to it.
func requireNonEmpty(props map[string]interface{}, name, demand string) {
	prop, ok := props[name].(map[string]interface{})
	if !ok {
		return
	}
	prop["minItems"] = 1
	if desc, ok := prop["description"].(string); ok {
		prop["description"] = desc + " " + demand
	}
}
