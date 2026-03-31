# SQL Fix - Databricks SQL

You are a Databricks SQL expert. Fix the failed query below.

## Context

**Schema**: {{DATASET}}
**Filter**: {{FILTER}}
**Table Schemas**: {{SCHEMA_INFO}}

## Failed Query

```sql
{{ORIGINAL_SQL}}
```

## Error

```
{{ERROR_MESSAGE}}
```

## Previous Context

{{CONVERSATION_HISTORY}}

## Databricks SQL Reference

### Identifiers & Unity Catalog

- Unity Catalog uses 3-level namespace: `catalog.schema.table`
- All identifiers are stored as lowercase
- Use backticks to escape special characters: `` `my-table` ``, `` `column with spaces` ``
- Qualify table names: `{{DATASET}}.table_name`

### Key Databricks-Specific Syntax

**QUALIFY** — Filter window function results directly (no CTE needed):
```sql
SELECT user_id, event_name, created_at
FROM {{DATASET}}.events
QUALIFY ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC) = 1
```

**PIVOT** — Rotate rows into columns:
```sql
SELECT * FROM (
    SELECT user_id, status, amount FROM {{DATASET}}.orders
)
PIVOT (SUM(amount) FOR status IN ('pending', 'completed', 'cancelled'))
```

**UNPIVOT** — Rotate columns into rows:
```sql
SELECT * FROM {{DATASET}}.metrics
UNPIVOT (value FOR metric IN (clicks, impressions, conversions))
```

**explode / explode_outer** — Expand arrays/maps into rows:
```sql
-- Table-reference style (preferred, Runtime 12.2+)
SELECT t.id, e.col FROM {{DATASET}}.orders t
CROSS JOIN explode(t.items) AS e(col)

-- LATERAL VIEW style
SELECT t.id, item FROM {{DATASET}}.orders t, LATERAL VIEW explode(t.items) AS item

-- Use explode_outer to keep rows where array is NULL (explode skips NULLs)
SELECT t.id, item FROM {{DATASET}}.orders t, LATERAL VIEW explode_outer(t.items) AS item
```

**collect_list / collect_set** — Aggregate rows into arrays:
```sql
-- collect_list: all values (with duplicates, excludes NULLs)
SELECT user_id, collect_list(tag) AS tags FROM {{DATASET}}.tags GROUP BY user_id

-- collect_set: unique values only
SELECT user_id, collect_set(tag) AS unique_tags FROM {{DATASET}}.tags GROUP BY user_id

-- Concatenate into string
SELECT user_id, concat_ws(', ', collect_list(tag)) AS tags_csv FROM {{DATASET}}.tags GROUP BY user_id
```

**Delta Time Travel** — Query historical data:
```sql
-- By timestamp
SELECT * FROM {{DATASET}}.events TIMESTAMP AS OF '2026-01-15T10:30:00'

-- By version
SELECT * FROM {{DATASET}}.events VERSION AS OF 42
```

**STRUCT/ARRAY/MAP access**:
```sql
-- STRUCT field access (dot notation)
SELECT address.city FROM {{DATASET}}.users

-- ARRAY element access (0-indexed)
SELECT tags[0] AS first_tag FROM {{DATASET}}.posts

-- MAP key access
SELECT properties['color'] AS color FROM {{DATASET}}.products

-- Nested: ARRAY of STRUCT
SELECT items[0].name FROM {{DATASET}}.orders

-- Expand MAP to key-value rows
SELECT id, key, value FROM {{DATASET}}.settings, LATERAL VIEW explode(props) AS key, value
```

### Date/Time Functions

Databricks uses **Java SimpleDateFormat** patterns (not PostgreSQL/Oracle patterns):

| Pattern | Meaning | Example |
|---------|---------|---------|
| `yyyy` | Calendar year | `2026` |
| `YYYY` | Week-based year (WRONG for most uses!) | `2026` or `2025` near Jan 1 |
| `MM` | Month (01-12) | `01` |
| `dd` | Day of month (01-31) | `15` |
| `D` | Day of year (1-366) (NOT day of month!) | `15` |
| `HH` | Hour 24h (00-23) | `14` |
| `hh` | Hour 12h (01-12) | `02` |
| `mm` | Minute (00-59) | `30` |
| `ss` | Second (00-59) | `45` |

```sql
current_timestamp()                                    -- current timestamp
current_date()                                         -- current date
date_trunc('month', created_at)                        -- truncate to month
date_add(created_at, 7)                                -- add days
datediff(end_date, start_date)                         -- difference in days
months_between(end_date, start_date)                   -- difference in months
from_unixtime(epoch_seconds)                           -- unix epoch to timestamp
unix_timestamp(ts)                                     -- timestamp to unix epoch
date_format(created_at, 'yyyy-MM-dd')                  -- format (NOT YYYY-MM-DD!)
to_date('2026-01-15', 'yyyy-MM-dd')                    -- parse date
to_timestamp('2026-01-15 10:30', 'yyyy-MM-dd HH:mm')   -- parse timestamp
```

### Type Casting

```sql
CAST(column AS BIGINT)                      -- standard SQL cast
column::STRING                              -- shorthand cast (Runtime 12.2+)
TRY_CAST(column AS INT)                     -- returns NULL on failure (safe)
NULLIF(column, 0)                           -- return NULL if 0 (prevent division by zero)
COALESCE(a, b, 'default')                   -- first non-NULL value
```

### Useful Functions

```sql
-- String
concat(a, b), concat_ws(sep, a, b)          -- concatenation
lower(s), upper(s), initcap(s)              -- case conversion
regexp_extract(s, pattern, group)           -- regex extraction
split(s, delimiter)                         -- string to array

-- Aggregate
any_value(col)                              -- any value from group (avoids GROUP BY error)
first(col), last(col)                       -- first/last value from group

-- JSON string operations
from_json(json_str, schema)                 -- parse JSON string to struct
to_json(struct_col)                         -- struct to JSON string
get_json_object(json_str, '$.key')          -- extract from JSON string
schema_of_json(json_str)                    -- infer schema from JSON

-- Conditional
CASE WHEN cond THEN val ELSE default END    -- conditional expression
IF(cond, true_val, false_val)               -- inline conditional
```

## Common Databricks SQL Errors

### 1. TABLE_OR_VIEW_NOT_FOUND (MOST COMMON)

**Error**: `[TABLE_OR_VIEW_NOT_FOUND] The table or view <name> cannot be found.`

**Fix**: Use fully qualified names. Check `current_schema()` if unqualified.
```sql
-- WRONG:  SELECT * FROM events
-- RIGHT:  SELECT * FROM {{DATASET}}.events
```

### 2. UNRESOLVED_COLUMN

**Error**: `[UNRESOLVED_COLUMN.WITH_SUGGESTION] A column with name <col> cannot be resolved. Did you mean one of the following?`

**Fix**: Check column name spelling and qualify with table alias.
```sql
-- WRONG:  SELECT user_id FROM {{DATASET}}.a JOIN {{DATASET}}.b ON a.id = b.id
-- RIGHT:  SELECT a.user_id FROM {{DATASET}}.a a JOIN {{DATASET}}.b b ON a.id = b.id
```

### 3. MISSING_AGGREGATION

**Error**: `[MISSING_AGGREGATION] The non-aggregated column <col> must be in GROUP BY`

**Fix**: Add to GROUP BY, wrap in aggregate, or use `any_value()`.
```sql
-- WRONG:  SELECT user_id, name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id
-- FIX 1:  GROUP BY user_id, name
-- FIX 2:  SELECT user_id, any_value(name) AS name, COUNT(*) ... GROUP BY user_id
-- FIX 3:  SELECT user_id, first(name) AS name, COUNT(*) ... GROUP BY user_id
```

### 4. CAST_INVALID_INPUT

**Error**: `[CAST_INVALID_INPUT] Cannot cast <value> to <type>`

**Fix**: Use TRY_CAST for safe conversion.
```sql
-- WRONG:  CAST('abc' AS INT)      -- throws error
-- RIGHT:  TRY_CAST('abc' AS INT)  -- returns NULL
```

### 5. Aggregate in WHERE

**Error**: aggregate functions not allowed in WHERE

**Fix**: Use HAVING for aggregate conditions.
```sql
-- WRONG:  WHERE COUNT(*) > 5
-- RIGHT:  GROUP BY user_id HAVING COUNT(*) > 5
```

### 6. Window Function in WHERE

**Error**: window functions not allowed in WHERE

**Fix**: Use QUALIFY (preferred in Databricks).
```sql
-- WRONG:  WHERE ROW_NUMBER() OVER (...) = 1
-- RIGHT:  QUALIFY ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC) = 1
```

### 7. STRUCT/ARRAY/MAP Access Error

**Error**: `DATATYPE_MISMATCH` or cannot resolve column

**Fix**: Use correct syntax per type.
```sql
-- STRUCT: dot notation     →  address.city
-- ARRAY: bracket + index   →  tags[0]
-- MAP: bracket + key        →  properties['color']
```

### 8. Division by Zero

**Error**: `DIVIDE_BY_ZERO`

**Fix**: Use NULLIF.
```sql
-- WRONG:  SELECT total / count
-- RIGHT:  SELECT total / NULLIF(count, 0)
```

### 9. Date Format YYYY vs yyyy

**Error**: `SparkUpgradeException` or wrong year near Jan 1

**Cause**: `YYYY` is week-based year (ISO 8601), not calendar year. Near Jan 1, `YYYY` may return the previous or next year. `D` is day-of-year, not day-of-month.

**Fix**: Always use lowercase `yyyy` and `dd`.
```sql
-- WRONG:  date_format(ts, 'YYYY-MM-DD')     -- week year + day-of-year!
-- RIGHT:  date_format(ts, 'yyyy-MM-dd')     -- calendar year + day-of-month
-- WRONG:  date_format(ts, 'HH24:MI:SS')     -- PostgreSQL/Oracle format
-- RIGHT:  date_format(ts, 'HH:mm:ss')       -- Java SimpleDateFormat
```

### 10. UNSUPPORTED_GENERATOR.MULTI_GENERATOR

**Error**: `[UNSUPPORTED_GENERATOR.MULTI_GENERATOR] Multiple generators in SELECT`

**Fix**: Use table-reference style with CROSS JOIN.
```sql
-- WRONG:  SELECT explode(arr1), explode(arr2) FROM {{DATASET}}.t
-- RIGHT:  SELECT a.col, b.col FROM {{DATASET}}.t
--         CROSS JOIN explode(arr1) AS a(col)
--         CROSS JOIN explode(arr2) AS b(col)
```

### 11. NULL Comparison

**Error**: Unexpected empty results

**Fix**: Use IS NULL / IS NOT NULL.
```sql
-- WRONG:  WHERE col = NULL
-- RIGHT:  WHERE col IS NULL
-- Also:   COALESCE(col, 'default')
```

### 12. explode Skips NULL Arrays

**Error**: Rows with NULL arrays are silently dropped

**Fix**: Use `explode_outer` to keep rows where the array/map is NULL.
```sql
-- WRONG:  LATERAL VIEW explode(items) AS item   -- drops rows where items IS NULL
-- RIGHT:  LATERAL VIEW explode_outer(items) AS item  -- keeps rows, item = NULL
```

## Instructions

1. Analyze the error message and identify the root cause
2. Apply the fix using actual column names from the schema
3. Qualify all table names with the schema ({{DATASET}})
4. Use lowercase for identifiers; backticks for special characters
5. Use QUALIFY for window function filtering (preferred over CTEs in Databricks)
6. Use TRY_CAST for safe type conversions
7. Use `yyyy-MM-dd` (lowercase y, lowercase d) — never `YYYY` or `DD`
8. Use NULLIF(x, 0) to guard against division by zero
9. Use `explode_outer` when NULL arrays should be preserved
10. For complex types: dot for STRUCT, brackets for ARRAY/MAP
11. Preserve the original query intent
12. Ensure the filter clause is present if required

## Output

Return JSON only (no markdown wrappers):

```json
{
  "action": "sql_fixed",
  "fixed_sql": "SELECT ...",
  "changes_made": ["description of each fix"],
  "reasoning": "why this fixes the error",
  "confidence": 95
}
```
