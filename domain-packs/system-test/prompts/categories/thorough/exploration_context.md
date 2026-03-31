## Thorough Validation Context

This is a **thorough validation** run. You have a large query budget (~80-100 queries). In addition to connectivity, schema discovery, type mapping, and data profiling, you must also:

1. **Test complex SQL patterns**: JOINs between tables, CTEs (WITH clauses), subqueries, UNION/UNION ALL
2. **Test window functions**: ROW_NUMBER, RANK, DENSE_RANK, LAG, LEAD, SUM OVER, AVG OVER with PARTITION BY and ORDER BY
3. **Test aggregations**: GROUP BY with HAVING, COUNT DISTINCT, multiple aggregation functions in one query
4. **Test edge cases**: NULL in arithmetic (NULL + 1), NULL in comparisons (NULL = NULL), empty string vs NULL, COALESCE, CASE WHEN
5. **Test precision**: Large integers (> 2^31), high-precision decimals, very long strings
6. **Test date/time**: Date arithmetic, timestamp with timezone, date formatting functions, epoch conversion
7. **Test LIMIT/OFFSET**: Pagination patterns, ORDER BY with LIMIT
8. **Test aliasing**: Column aliases, table aliases, subquery aliases

### Thorough Validation Queries

**CTE (Common Table Expression)**:
```sql
WITH table_stats AS (
  SELECT
    COUNT(*) AS total_rows,
    COUNT(DISTINCT column_name) AS distinct_values
  FROM `{{DATASET}}.example_table`
  {{FILTER}}
)
SELECT * FROM table_stats
```

**Window Function**:
```sql
SELECT
  column_a,
  column_b,
  ROW_NUMBER() OVER (PARTITION BY column_a ORDER BY column_b) AS row_num,
  RANK() OVER (ORDER BY column_b DESC) AS rank_val
FROM `{{DATASET}}.example_table`
{{FILTER}}
LIMIT 20
```

**JOIN Between Tables**:
```sql
SELECT a.id, a.name, b.value
FROM `{{DATASET}}.table_a` a
JOIN `{{DATASET}}.table_b` b ON a.id = b.foreign_id
{{FILTER}}
LIMIT 10
```

**NULL Arithmetic**:
```sql
SELECT
  NULL + 1 AS null_plus_one,
  NULL = NULL AS null_eq_null,
  COALESCE(NULL, 'fallback') AS coalesce_test,
  CASE WHEN NULL IS NULL THEN 'yes' ELSE 'no' END AS case_null
```

**Subquery**:
```sql
SELECT *
FROM `{{DATASET}}.example_table`
WHERE column_name IN (
  SELECT DISTINCT column_name
  FROM `{{DATASET}}.another_table`
  {{FILTER}}
)
{{FILTER}}
LIMIT 10
```

**Large Number Precision**:
```sql
SELECT
  2147483647 AS max_int32,
  9223372036854775807 AS max_int64,
  3.141592653589793 AS pi_double
```

Explore systematically — don't skip a category just because earlier tests passed. Each SQL pattern tests a different provider code path.
