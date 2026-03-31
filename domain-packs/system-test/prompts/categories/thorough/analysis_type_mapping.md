# Data Type Mapping Analysis (Thorough)

You are a database validation agent performing exhaustive data type mapping analysis. Report on every data type found in the warehouse and whether the provider maps it correctly.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and produce **structured validation findings** for data type mapping. This is a thorough run — go beyond basic checks:

1. **Every SQL type**: INTEGER, BIGINT, SMALLINT, TINYINT, FLOAT, DOUBLE, DECIMAL/NUMERIC (with varying precision/scale), VARCHAR, CHAR, TEXT, BOOLEAN, DATE, TIME, TIMESTAMP, TIMESTAMP_TZ, BINARY, JSON/JSONB, ARRAY, STRUCT/RECORD, etc.
2. **Precision tests**: DECIMAL(10,2) vs DECIMAL(38,18) — is precision preserved?
3. **Integer boundaries**: Small ints, large ints, int32 max, int64 max
4. **Casting behavior**: Explicit CAST operations — do they work and return correct types?
5. **Complex types**: JSON, ARRAY, STRUCT — do they return as strings?
6. **Type NULL behavior**: NULL for each type — all should return nil regardless of type

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding (e.g., 'DECIMAL Precision: DECIMAL(38,18) Preserves Full Precision')",
      "description": "Detailed description of type mapping behavior with specific values tested.",
      "severity": "info|low|medium|high|critical",
      "affected_count": 5,
      "risk_score": 0.0,
      "confidence": 0.90,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "type_mapping",
        "data_type": "DECIMAL",
        "columns_tested": 5,
        "columns_correct": 5,
        "columns_incorrect": 0
      },
      "indicators": [
        "DECIMAL(10,2): 29.99 returned as float64 29.99",
        "DECIMAL(38,18): 1.123456789012345678 returned as float64 (precision truncated to float64 limit)",
        "CAST(123 AS DECIMAL(5,2)): returned 123.00 as float64"
      ],
      "target_segment": "Warehouse provider type mapping",
      "source_steps": [10, 11, 12]
    }
  ]
}
```

## Severity Mapping

- **info**: Type maps correctly with expected Go type
- **low**: Correct but with known precision limits (e.g., float64 precision for DECIMAL(38,18))
- **medium**: Inconsistent mapping across columns of the same type
- **high**: Incorrect mapping causing data loss (e.g., int64 for DECIMAL, truncating fractions)
- **critical**: Type causes crash, panic, or query failure

## Important Rules

1. **One finding per data type family** (e.g., all DECIMAL variants together, all integer types together)
2. **Include exact tested values** — input and output
3. **Note any types NOT found in this warehouse** — "ARRAY type not present in schema, not tested"
4. **Compare actual vs expected**: "Expected float64, got string"

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
