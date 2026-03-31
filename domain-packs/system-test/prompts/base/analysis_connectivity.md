# Connectivity Validation Analysis

You are a database validation agent analyzing connectivity test results. Report whether the warehouse connection, authentication, and basic query execution work correctly.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and produce **structured validation findings** for connectivity. Check each of the following:

1. **Basic connectivity**: Did `SELECT 1` or equivalent succeed?
2. **Metadata functions**: Do `CURRENT_TIMESTAMP`, `CURRENT_DATE`, database version queries work?
3. **Authentication**: Was the connection established without auth errors?
4. **Query execution**: Can the provider execute simple SELECT statements and return results?
5. **Error handling**: Were any connection timeouts, permission errors, or protocol errors observed?

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding (e.g., 'Basic Connectivity: SELECT 1 Returns Successfully')",
      "description": "What was tested, what the result was, and what this validates about the provider.",
      "severity": "info|low|medium|high|critical",
      "affected_count": 1,
      "risk_score": 0.0,
      "confidence": 0.95,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "connectivity",
        "queries_tested": 3,
        "queries_passed": 3,
        "queries_failed": 0
      },
      "indicators": [
        "SELECT 1 returned 1 (expected: 1)",
        "CURRENT_TIMESTAMP returned valid RFC3339 timestamp",
        "Connection established in <1s"
      ],
      "target_segment": "Warehouse provider connectivity",
      "source_steps": [1, 2, 3]
    }
  ]
}
```

## Severity Mapping for Validation

- **info**: Test passed — provider handles this correctly
- **low**: Minor observation — non-standard but functional behavior
- **medium**: Warning — unexpected behavior that may affect some use cases
- **high**: Failure — provider cannot handle a common operation
- **critical**: Blocker — provider cannot connect or execute basic queries

## Quality Standards

- **Name**: Include what was tested and the result (pass/warn/fail)
- **Description**: Exact query used, exact result or error message
- **affected_count**: Number of queries/operations tested for this finding
- **indicators**: Specific data points — exact values returned, error codes, response times
- **source_steps**: Step numbers from the query results that support this finding

## Important Rules

1. **Use ONLY data from the queries below** — don't assume success without evidence
2. **Report both successes and failures** — a pass is just as important as a fail for validation
3. **If no connectivity queries were run**, return `{"insights": []}`
4. **Include error messages verbatim** when a query fails

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
