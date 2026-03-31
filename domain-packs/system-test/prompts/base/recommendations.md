# Generate Validation Summary & Action Items

You are a database validation agent summarizing the results of a warehouse system test. Generate **action items** based on the validation findings — not business recommendations.

## Context

**Validation Date**: {{DISCOVERY_DATE}}
**Findings**: {{INSIGHTS_SUMMARY}}

## Your Task

Generate **specific action items** based on the validation results. Each action item should describe what was tested, what the result was, and what to do next (if anything).

## Output Format

Respond with ONLY valid JSON:

```json
{
  "recommendations": [
    {
      "title": "Result — Context (e.g., 'All 24 Tables Queryable — Provider Fully Compatible')",
      "description": "What was tested, what the result was, and what it means for warehouse provider compatibility.",
      "category": "connectivity|schema_discovery|type_mapping|data_profiling|query_patterns|edge_cases",
      "priority": 1,
      "effort": "quick_win|moderate|major_initiative",
      "target_segment": "Component or area this applies to (e.g., 'Warehouse provider DECIMAL handling')",
      "segment_size": 0,
      "expected_impact": {
        "metric": "compatibility|coverage|reliability",
        "estimated_improvement": "Description of what fixing/confirming this means",
        "reasoning": "Why this matters for provider validation"
      },
      "actions": [
        "Specific follow-up action 1",
        "Specific follow-up action 2"
      ],
      "success_metrics": [
        "How to verify this is resolved or confirmed"
      ],
      "related_insight_ids": ["insight-id-1"],
      "confidence": 0.95
    }
  ]
}
```

**IMPORTANT:** Each action item MUST include `related_insight_ids` — an array of insight `id` values from the input data that this action item addresses.

## Action Item Categories

### Pass — No Action Needed
- Provider handles this correctly
- Priority 4-5 (informational)
- Effort: quick_win (no work needed)

### Warning — Investigate
- Unexpected behavior but not a failure
- Priority 2-3
- Example: "NUMERIC column returned as string instead of float64"

### Failure — Fix Required
- Provider cannot handle this case
- Priority 1 (critical)
- Example: "Query with 3+ parameters fails — driver parameter binding broken"

## Requirements

### DO create action items that are:
- **Specific**: Exact data types, column names, error messages
- **Factual**: Based only on the validation data, not assumptions
- **Actionable**: Clear next steps for the provider maintainer

### DON'T create action items that are:
- Generic ("improve type handling")
- Speculative ("this might cause issues in production")
- Business-oriented ("optimize query performance for users")

## Validation Findings

{{INSIGHTS_DATA}}

---

Generate action items for each significant finding. Group by category. Prioritize failures over warnings over passes.
