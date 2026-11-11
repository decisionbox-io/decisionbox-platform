# Edge Cases Analysis

You are a database validation agent analyzing edge case handling. Report whether the warehouse provider correctly handles boundary conditions and unusual inputs.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and produce **structured validation findings** for edge case handling. Check:

1. **NULL arithmetic**: NULL + 1, NULL * 0, NULL / NULL — should all return NULL
2. **NULL comparisons**: NULL = NULL, NULL != NULL, NULL IS NULL, NULL IS NOT NULL
3. **COALESCE**: COALESCE(NULL, NULL, 'value'), COALESCE with different types
4. **CASE expressions**: CASE WHEN NULL THEN ... , nested CASE, CASE with type mixing
5. **Empty strings**: '' vs NULL — are they distinct? Does LENGTH('') return 0?
6. **Large numbers**: Values near int32/int64 boundaries, very large DECIMAL values
7. **Timestamp edge cases**: Epoch (1970-01-01), far-future dates, timezone conversions
8. **String edge cases**: Very long strings, Unicode characters, special characters in identifiers
9. **Division by zero**: Does it error or return NULL/Infinity?
10. **LIMIT/OFFSET**: LIMIT 0, OFFSET beyond row count

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding (e.g., 'NULL Arithmetic: All NULL Operations Return NULL Correctly')",
      "description": "What edge cases were tested and how the provider handled them.",
      "affected_count": 5,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "edge_cases",
        "edge_case_category": "null_arithmetic",
        "cases_tested": 5,
        "cases_passed": 5,
        "cases_failed": 0
      },
      "indicators": [
        "NULL + 1 = NULL (correct)",
        "NULL * 0 = NULL (correct)",
        "NULL = NULL returned NULL/false (correct — NULL is not equal to NULL)",
        "NULL IS NULL returned true (correct)"
      ],
      "target_segment": "Warehouse provider edge case handling",
      "source_steps": [40, 41, 42]
    }
  ]
}
```

## What the platform measures this by

Severity is derived from the figures you declare, not from a word you choose.
The platform re-checks each figure's arithmetic against the rows and bands the
largest one that holds: a currency amount in this warehouse's own currency, or
a percentage. So a finding that states its magnitude as a declared figure is
ranked on that magnitude, and one that states no figure falls back to an area
baseline and is marked *not quantified*.

Declare the number that makes the finding matter. Do not describe how severe
it is.

## Important Rules

1. **Group by edge case category** (NULL handling, large numbers, strings, etc.)
2. **Include exact input and output** for each test
3. **Compare to SQL standard** — note deviations
4. **If an edge case was not tested**, do not report on it

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
