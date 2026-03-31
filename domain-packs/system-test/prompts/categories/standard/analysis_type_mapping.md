# Data Type Mapping Analysis

You are a database validation agent analyzing data type mapping results. Report whether each data type in the warehouse is correctly handled by the provider.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and produce **structured validation findings** for data type mapping. For each data type found in the warehouse, verify:

1. **Type returned**: What Go type does the provider return for this SQL type?
2. **Value accuracy**: Do returned values match expected values (e.g., DECIMAL 3.14 returns as float64 3.14, not string "3.14")?
3. **Precision**: For DECIMAL/NUMERIC, is precision preserved?
4. **Timestamps**: Are timestamps returned in a consistent format (RFC3339)?
5. **Booleans**: Are booleans returned as true/false?
6. **NULLs per type**: Does each type handle NULL correctly (returns nil, not zero/empty)?

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding (e.g., 'DECIMAL Columns: All 12 Columns Return float64 Correctly')",
      "description": "Which data types were tested, how many columns of each type, and whether the mapping is correct.",
      "severity": "info|low|medium|high|critical",
      "affected_count": 12,
      "risk_score": 0.0,
      "confidence": 0.90,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "type_mapping",
        "data_type": "DECIMAL",
        "columns_tested": 12,
        "columns_correct": 12,
        "columns_incorrect": 0
      },
      "indicators": [
        "DECIMAL(10,2) column 'price' returned 29.99 as float64",
        "DECIMAL(18,6) column 'rate' returned 0.001234 as float64",
        "NULL DECIMAL returned as nil (not 0)"
      ],
      "target_segment": "Warehouse provider type mapping",
      "source_steps": [10, 11, 12]
    }
  ]
}
```

## Severity Mapping

- **info**: All columns of this type map correctly
- **low**: Correct but non-standard (e.g., BOOLEAN returned as 0/1 int instead of true/false)
- **medium**: Inconsistent mapping (e.g., DECIMAL sometimes returns float64, sometimes string)
- **high**: Incorrect mapping (e.g., DECIMAL returns int64, losing fractional part)
- **critical**: Type causes query failure or data corruption

## Important Rules

1. **Group findings by data type** — one finding per SQL type tested
2. **Include specific column names and values** in indicators
3. **If a type was not found in the schema**, do not report on it
4. **Note any types the warehouse supports that were NOT tested**

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
