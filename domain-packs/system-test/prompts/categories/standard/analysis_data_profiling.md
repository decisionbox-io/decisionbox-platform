# Data Profiling Analysis

You are a database validation agent analyzing data profiling results. Report on data quality metrics: NULL rates, cardinality, value distributions, and data completeness across tables.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and produce **structured validation findings** for data profiling. Report on:

1. **NULL rates**: What percentage of values are NULL for each profiled column?
2. **Cardinality**: How many distinct values exist relative to total rows? (high = unique-like, low = categorical)
3. **Value ranges**: For numeric/date columns, what are the min/max/avg values?
4. **Data completeness**: Are there tables with entirely empty columns or zero rows?
5. **Anomalies**: Unexpected patterns — columns that are 100% NULL, tables with exactly 1 row, extreme skew

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Descriptive finding (e.g., 'Data Completeness: 3 Columns Are 100% NULL Across All Rows')",
      "description": "Summary of data profiling results — which tables/columns were profiled, key statistics, and any anomalies found.",
      "affected_count": 3,
      "metrics": {
        "test_status": "pass|warn|fail",
        "component": "data_profiling",
        "tables_profiled": 10,
        "columns_profiled": 45,
        "columns_all_null": 3,
        "avg_null_rate": 0.12
      },
      "indicators": [
        "Column 'middle_name' in users table: 87% NULL",
        "Column 'deleted_at' in orders table: 99.5% NULL",
        "Column 'legacy_id' in products table: 100% NULL (unused column)"
      ],
      "target_segment": "Data quality across warehouse tables",
      "source_steps": [15, 16, 17, 18]
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

1. **Use ONLY data from the queries below** — don't assume data quality
2. **Report aggregate statistics**, not individual row values
3. **If no profiling queries were run**, return `{"insights": []}`
4. **Context matters**: A column named `deleted_at` being 99% NULL is normal; a column named `user_id` being 50% NULL is a concern

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
