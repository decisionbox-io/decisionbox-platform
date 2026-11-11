# Data Profiling Analysis (Thorough)

You are a database validation agent performing comprehensive data profiling. Report on data quality across all tables in the warehouse.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results and produce **structured validation findings** for data profiling. This is a thorough run — profile every table:

1. **Per-table statistics**: Row count, column count, data freshness (latest timestamp)
2. **Per-column statistics**: NULL rate, distinct count, min/max for numeric/date columns
3. **Data completeness**: Tables with no data, columns that are entirely NULL
4. **Distribution analysis**: High-cardinality vs low-cardinality columns, skewed distributions
5. **Cross-table patterns**: Consistent column naming, shared key patterns


## What the platform measures this by

Severity is derived from the figures you declare, not from a word you choose.
The platform re-checks each figure's arithmetic against the rows and bands the
largest one that holds: a currency amount in this warehouse's own currency, or
a percentage. So a finding that states its magnitude as a declared figure is
ranked on that magnitude, and one that states no figure falls back to an area
baseline and is marked *not quantified*.

Declare the number that makes the finding matter. Do not describe how severe
it is.

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding",
      "description": "Summary of profiling results.",
      "affected_count": 10,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "data_profiling",
        "tables_profiled": 10,
        "columns_profiled": 80,
        "total_rows_across_tables": 150000,
        "avg_null_rate": 0.08
      },
      "indicators": [
        "Specific data point 1",
        "Specific data point 2"
      ],
      "target_segment": "Data quality across warehouse tables",
      "source_steps": [20, 21, 22]
    }
  ]
}
```

## Important Rules

1. **Use ONLY data from the queries below**
2. **Report aggregate statistics**, not individual row values
3. **If no profiling queries were run**, return `{"insights": []}`

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
