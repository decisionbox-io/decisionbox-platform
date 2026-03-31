## Standard Validation Context

This is a **standard validation** run. You have a moderate query budget (~30-50 queries). In addition to connectivity and schema discovery, focus on:

1. **Data type coverage**: For every distinct data type found in the schema, query at least one column of that type and verify the returned value makes sense
2. **NULL handling**: Find columns with NULL values and verify NULLs are returned correctly (not as empty strings or zeros)
3. **Row counts**: Get exact row counts for every table
4. **Column statistics**: For numeric columns, check MIN/MAX/AVG. For string columns, check MIN/MAX length. For date columns, check date range.
5. **Data profiling**: Calculate NULL percentages and distinct value counts for key columns

### Standard Validation Queries

**Data Type Inventory**:
```sql
SELECT data_type, COUNT(*) AS column_count
FROM INFORMATION_SCHEMA.COLUMNS
WHERE table_schema = '{{DATASET}}'
GROUP BY data_type
ORDER BY column_count DESC
```

**NULL Rate Check**:
```sql
SELECT
  COUNT(*) AS total_rows,
  COUNT(column_name) AS non_null_count,
  COUNT(*) - COUNT(column_name) AS null_count,
  ROUND((COUNT(*) - COUNT(column_name)) * 100.0 / NULLIF(COUNT(*), 0), 2) AS null_percentage
FROM `{{DATASET}}.example_table`
{{FILTER}}
```

**Numeric Range Check**:
```sql
SELECT
  MIN(numeric_column) AS min_val,
  MAX(numeric_column) AS max_val,
  AVG(numeric_column) AS avg_val,
  COUNT(DISTINCT numeric_column) AS distinct_count
FROM `{{DATASET}}.example_table`
{{FILTER}}
```

**Date Range Check**:
```sql
SELECT
  MIN(date_column) AS earliest,
  MAX(date_column) AS latest,
  COUNT(DISTINCT date_column) AS distinct_dates
FROM `{{DATASET}}.example_table`
{{FILTER}}
```

**Distinct Value Cardinality**:
```sql
SELECT
  COUNT(DISTINCT string_column) AS distinct_values,
  COUNT(*) AS total_rows
FROM `{{DATASET}}.example_table`
{{FILTER}}
```
