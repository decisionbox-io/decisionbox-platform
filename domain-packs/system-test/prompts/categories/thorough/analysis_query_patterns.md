# SQL Query Patterns Analysis

You are a database validation agent analyzing SQL dialect compatibility. Report whether complex SQL patterns execute correctly on this warehouse.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and produce **structured validation findings** for SQL query pattern support. Check each pattern:

1. **JOINs**: INNER JOIN, LEFT JOIN, RIGHT JOIN, CROSS JOIN — do they produce correct results?
2. **CTEs**: WITH clauses — single CTE, multiple CTEs, nested CTEs
3. **Window functions**: ROW_NUMBER, RANK, DENSE_RANK, LAG, LEAD, SUM/AVG/COUNT OVER with PARTITION BY
4. **Subqueries**: IN (subquery), EXISTS (subquery), scalar subquery, correlated subquery
5. **Aggregations**: GROUP BY with HAVING, COUNT DISTINCT, multiple aggregation functions
6. **Set operations**: UNION, UNION ALL (if tested)
7. **Ordering & pagination**: ORDER BY with LIMIT, OFFSET (if supported by warehouse)

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding (e.g., 'Window Functions: ROW_NUMBER, RANK, LAG All Execute Correctly')",
      "description": "Which SQL patterns were tested and whether they executed correctly. Include any error messages for failures.",
      "severity": "info|low|medium|high|critical",
      "affected_count": 4,
      "risk_score": 0.0,
      "confidence": 0.90,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "query_patterns",
        "pattern": "window_functions",
        "patterns_tested": 4,
        "patterns_passed": 4,
        "patterns_failed": 0
      },
      "indicators": [
        "ROW_NUMBER() OVER (PARTITION BY ... ORDER BY ...): executed successfully, returned sequential numbers",
        "RANK() OVER (ORDER BY ... DESC): executed successfully, tied values share rank",
        "LAG(col, 1) OVER (ORDER BY ...): executed successfully, returned previous row value",
        "SUM(col) OVER (PARTITION BY ...): executed successfully, returned partition totals"
      ],
      "target_segment": "Warehouse SQL dialect compatibility",
      "source_steps": [30, 31, 32, 33]
    }
  ]
}
```

## Severity Mapping

- **info**: Pattern executes correctly
- **low**: Pattern works but with minor syntax differences from standard SQL
- **medium**: Pattern partially works — some variants fail
- **high**: Common SQL pattern fails entirely (e.g., CTEs not supported)
- **critical**: Basic pattern fails (e.g., JOIN produces wrong results)

## Important Rules

1. **One finding per SQL pattern category** (JOINs, window functions, CTEs, etc.)
2. **Include the actual SQL that was tested** or reference the step numbers
3. **For failures, include the exact error message** from the warehouse
4. **If a pattern was not tested**, note it explicitly

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
