package discovery

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// reflectionResponseFormatName is the schema identifier sent to providers that
// name their structured-output schema.
const reflectionResponseFormatName = "discovery_reflection"

// reflectionResponseFormat returns the structured-output request for the
// reflection phase. ChatWithFormat self-gates on SupportsStructuredOutput, so
// this is a safe no-op on providers without it (the tolerant parser is the
// always-on net). Strict is false for the same reason as the questions schema:
// OpenAI strict mode requires every property required and forbids open objects.
func reflectionResponseFormat() *gollm.ResponseFormat {
	return &gollm.ResponseFormat{
		Name:   reflectionResponseFormatName,
		Schema: reflectionResponseSchema(),
		Strict: false,
	}
}

// reflectionResponseSchema is the curated JSON Schema (draft 2020-12) for the
// reflection envelope. It describes only the judgment content the model
// produces — coverage, prior-finding status re-judgement, durable learnings,
// next-tasks, and domain-pack deltas — never the server-assigned ids/timestamps.
// Property names track parsedReflection's json tags via
// TestReflectionSchema_MatchesStructTags.
func reflectionResponseSchema() map[string]interface{} {
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

	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"coverage_summary": str("One short paragraph: which tables/areas are now well covered and what remains unexplored (the frontier)."),
			"covered_tables":   strArray("Fully-qualified tables (dataset.table) this run actually queried/covered. Copy names from the catalog verbatim."),
			"covered_areas":    strArray("Analysis-area ids that produced findings this run."),
			"convergence_note": str("One line: is the investigation still finding much that is new, or converging?"),
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
		},
		"required": []interface{}{"coverage_summary"},
	}
}
