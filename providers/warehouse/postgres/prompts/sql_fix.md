# SQL Fix - PostgreSQL

You are a PostgreSQL SQL expert. Fix the failed query below.

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

## PostgreSQL SQL Reference

### Identifiers

- Unquoted identifiers are folded to lowercase (e.g., `MyTable` becomes `mytable`)
- Double-quote identifiers to preserve case: `"MyTable"` stays as-is
- Qualify table names with schema: `{{DATASET}}.table_name`
- Reserved keywords used as identifiers must be double-quoted: `"user"`, `"order"`, `"group"`

### Key PostgreSQL-Specific Syntax

**DISTINCT ON** — Select first row per group without subquery:
```sql
SELECT DISTINCT ON (user_id) user_id, event_name, created_at
FROM {{DATASET}}.events
ORDER BY user_id, created_at DESC
```

**CTEs (WITH clause)** — Common table expressions:
```sql
WITH active_users AS (
    SELECT user_id, COUNT(*) AS event_count
    FROM {{DATASET}}.events
    WHERE created_at > NOW() - INTERVAL '7 days'
    GROUP BY user_id
)
SELECT * FROM active_users WHERE event_count > 10
```

**Recursive CTEs** — Traverse hierarchies (categories, org charts, graphs):
```sql
WITH RECURSIVE category_tree AS (
    SELECT id, name, parent_id, 0 AS depth
    FROM {{DATASET}}.categories WHERE parent_id IS NULL
    UNION ALL
    SELECT c.id, c.name, c.parent_id, ct.depth + 1
    FROM {{DATASET}}.categories c
    JOIN category_tree ct ON c.parent_id = ct.id
)
SELECT * FROM category_tree ORDER BY depth, name
```

**LATERAL JOIN** — Correlated subquery in FROM (runs once per left row):
```sql
-- Top 3 orders per customer
SELECT c.id, c.name, o.amount, o.created_at
FROM {{DATASET}}.customers c
LEFT JOIN LATERAL (
    SELECT amount, created_at FROM {{DATASET}}.orders
    WHERE customer_id = c.id ORDER BY amount DESC LIMIT 3
) o ON true
```

**FILTER clause** — Conditional aggregates (cleaner than CASE WHEN):
```sql
SELECT
    COUNT(*) AS total,
    COUNT(*) FILTER (WHERE status = 'active') AS active_count,
    SUM(amount) FILTER (WHERE created_at > NOW() - INTERVAL '30 days') AS recent_total
FROM {{DATASET}}.orders
```

**INSERT ON CONFLICT (upsert)** — Insert or update atomically:
```sql
INSERT INTO {{DATASET}}.metrics (key, value)
VALUES ('page_views', 1)
ON CONFLICT (key) DO UPDATE SET value = {{DATASET}}.metrics.value + EXCLUDED.value
RETURNING *
```

**RETURNING** — Get inserted/updated/deleted rows back:
```sql
UPDATE {{DATASET}}.users SET status = 'inactive'
WHERE last_login < NOW() - INTERVAL '90 days'
RETURNING id, email
```

**JSON/JSONB operators**:
```sql
-- Extract field as JSON:  data->'key'        (returns json)
-- Extract field as text:  data->>'key'       (returns text)
-- Extract nested field:   data->'a'->>'b'    (returns text)
-- Extract by path:        data#>>'{a,b,c}'   (returns text)
-- Contains key:           data ? 'key'       (boolean)
-- Contains object:        data @> '{"k":"v"}' (boolean)
-- Delete key:             data - 'key'       (returns jsonb)
-- Get array element:      data->0            (first element)

-- Aggregate JSON objects:
SELECT jsonb_object_agg(key, value) FROM {{DATASET}}.settings

-- Expand JSON to rows:
SELECT * FROM {{DATASET}}.events, jsonb_each_text(data) AS kv(key, value)
```

**Array operators**:
```sql
-- Check if value is in array column
SELECT * FROM {{DATASET}}.users WHERE 'admin' = ANY(roles)
-- All values match
SELECT * FROM {{DATASET}}.users WHERE 42 = ALL(scores)
-- Array overlap (share any element)
SELECT * FROM {{DATASET}}.users WHERE roles && ARRAY['admin', 'editor']
-- Array contains
SELECT * FROM {{DATASET}}.users WHERE roles @> ARRAY['admin']

-- Expand array to rows
SELECT id, unnest(tags) AS tag FROM {{DATASET}}.posts

-- Aggregate rows into array / string
SELECT user_id, array_agg(tag ORDER BY tag) AS tags FROM {{DATASET}}.tags GROUP BY user_id
SELECT user_id, string_agg(tag, ', ' ORDER BY tag) AS tags FROM {{DATASET}}.tags GROUP BY user_id
```

**ILIKE** — Case-insensitive pattern matching:
```sql
WHERE name ILIKE '%search%'
```

**generate_series** — Generate rows (dates, numbers):
```sql
-- Date series (fill calendar gaps)
SELECT d::date
FROM generate_series('2026-01-01'::date, '2026-01-31'::date, '1 day'::interval) d

-- Number series
SELECT n FROM generate_series(1, 100) n
```

### Date/Time Functions

```sql
NOW()                                      -- current timestamp with timezone
CURRENT_DATE                               -- current date
CURRENT_TIMESTAMP                          -- current timestamp with timezone
date_trunc('month', created_at)            -- truncate to start of month
created_at + INTERVAL '7 days'             -- date arithmetic
created_at - INTERVAL '1 hour'             -- subtract interval
AGE(NOW(), created_at)                     -- interval between timestamps
EXTRACT(EPOCH FROM created_at)             -- unix timestamp (seconds)
EXTRACT(DOW FROM created_at)               -- day of week (0=Sun, 6=Sat)
EXTRACT(YEAR FROM created_at)              -- extract year
TO_CHAR(created_at, 'YYYY-MM-DD HH24:MI') -- format timestamp
TO_TIMESTAMP(epoch_seconds)                -- unix epoch to timestamp
TO_DATE('2026-01-15', 'YYYY-MM-DD')       -- parse date from string
created_at AT TIME ZONE 'UTC'              -- convert timezone
```

### Type Casting

```sql
column::integer                            -- PostgreSQL cast syntax
CAST(column AS integer)                    -- standard SQL cast
column::text                               -- cast to text
column::jsonb                              -- cast to JSONB
column::numeric(10,2)                      -- cast to numeric with precision
NULLIF(column, 0)                          -- return NULL if value equals 0 (prevent division by zero)
COALESCE(a, b, 'default')                 -- first non-NULL value
```

## Common PostgreSQL Errors

### 1. Relation Does Not Exist (MOST COMMON)

**Error**: `relation "sessions" does not exist`

**Fix**: Use schema-qualified table names. Also check for reserved keywords used as table names.
```sql
-- WRONG:  SELECT * FROM sessions
-- RIGHT:  SELECT * FROM {{DATASET}}.sessions

-- If table name is a reserved keyword:
-- WRONG:  SELECT * FROM {{DATASET}}.user
-- RIGHT:  SELECT * FROM {{DATASET}}."user"
```

### 2. Ambiguous Column Names

**Error**: `column reference "user_id" is ambiguous`

**Fix**: Add table aliases and qualify columns.
```sql
-- WRONG:  SELECT user_id FROM {{DATASET}}.a JOIN {{DATASET}}.b ON a.id = b.id
-- RIGHT:  SELECT a.user_id FROM {{DATASET}}.a a JOIN {{DATASET}}.b b ON a.id = b.id
```

### 3. Column Not in GROUP BY

**Error**: `column "name" must appear in the GROUP BY clause or be used in an aggregate function`

**Fix**: Every column in SELECT must either be in GROUP BY or wrapped in an aggregate. This also applies to ORDER BY.
```sql
-- WRONG:  SELECT user_id, name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id
-- FIX 1:  Add to GROUP BY
SELECT user_id, name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id, name
-- FIX 2:  Wrap in aggregate
SELECT user_id, MAX(name) AS name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id
-- FIX 3:  Use DISTINCT ON instead of GROUP BY
SELECT DISTINCT ON (user_id) user_id, name, COUNT(*) OVER (PARTITION BY user_id)
FROM {{DATASET}}.events ORDER BY user_id
```

### 4. Aggregate in WHERE (Use HAVING)

**Error**: `aggregate functions are not allowed in WHERE`

**Fix**: Use HAVING to filter on aggregate results. WHERE filters rows before grouping; HAVING filters after.
```sql
-- WRONG:  SELECT user_id, COUNT(*) FROM {{DATASET}}.events
--         WHERE COUNT(*) > 5 GROUP BY user_id
-- RIGHT:  SELECT user_id, COUNT(*) FROM {{DATASET}}.events
--         GROUP BY user_id HAVING COUNT(*) > 5
```

### 5. Type Mismatch

**Error**: `operator does not exist: integer = text`

**Fix**: Cast to the correct type. PostgreSQL is strict about types.
```sql
-- WRONG:  WHERE user_id = '123'  (if user_id is integer)
-- RIGHT:  WHERE user_id = 123
-- OR:     WHERE user_id = '123'::integer
```

### 6. NOT IN Returns No Rows (NULL Trap)

**Error**: Query returns 0 rows unexpectedly when using `NOT IN` with a subquery

**Cause**: If any value in the subquery is NULL, `NOT IN` always returns 0 rows. This is because `x NOT IN (1, NULL)` evaluates to `NULL` (not TRUE), so no rows pass the filter.

**Fix**: Use `NOT EXISTS` instead of `NOT IN`.
```sql
-- WRONG (returns 0 rows if bar.x has any NULLs):
SELECT * FROM {{DATASET}}.foo WHERE col NOT IN (SELECT x FROM {{DATASET}}.bar)
-- RIGHT:
SELECT * FROM {{DATASET}}.foo f
WHERE NOT EXISTS (SELECT 1 FROM {{DATASET}}.bar b WHERE b.x = f.col)
```

### 7. BETWEEN with Timestamps (Double-Counting Midnight)

**Error**: Rows at midnight boundaries are double-counted or missed

**Cause**: `BETWEEN` uses a closed interval (includes both ends). A timestamp at exactly '2026-01-08 00:00:00' is included in both `BETWEEN '2026-01-01' AND '2026-01-08'` and `BETWEEN '2026-01-08' AND '2026-01-15'`.

**Fix**: Use half-open intervals with `>=` and `<`.
```sql
-- WRONG:  WHERE created_at BETWEEN '2026-01-01' AND '2026-01-08'
-- RIGHT:  WHERE created_at >= '2026-01-01' AND created_at < '2026-01-08'
```

### 8. Invalid JSON Access

**Error**: `operator does not exist: text -> text` or `cannot extract element from a scalar`

**Fix**: Ensure column is json/jsonb type. Use `->>` for text extraction, `->` for JSON.
```sql
-- WRONG:  SELECT data->'key' FROM {{DATASET}}.events  (returns json, not text)
-- RIGHT:  SELECT data->>'key' FROM {{DATASET}}.events  (returns text for comparisons)
-- WRONG:  SELECT data::text->>'key'  (casting to text first loses JSON structure)
-- RIGHT:  SELECT data->>'key' FROM {{DATASET}}.events WHERE data IS NOT NULL
```

### 9. Window Function in WHERE

**Error**: `window functions are not allowed in WHERE`

**Fix**: Use a CTE or subquery. PostgreSQL does not have `QUALIFY` (unlike Snowflake).
```sql
-- WRONG:  WHERE ROW_NUMBER() OVER (...) = 1
-- RIGHT:
WITH ranked AS (
    SELECT *, ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC) AS rn
    FROM {{DATASET}}.events
)
SELECT * FROM ranked WHERE rn = 1
```

### 10. NULL Comparison

**Error**: Unexpected empty results when filtering NULLs

**Fix**: Use `IS NULL` / `IS NOT NULL`, not `= NULL`.
```sql
-- WRONG:  WHERE col = NULL
-- RIGHT:  WHERE col IS NULL
-- For defaults:  COALESCE(col, 'default')
```

### 11. Division by Zero

**Error**: `division by zero`

**Fix**: Use `NULLIF` to convert zero to NULL (which makes the result NULL instead of error).
```sql
-- WRONG:  SELECT total / count AS avg
-- RIGHT:  SELECT total / NULLIF(count, 0) AS avg
```

### 12. Syntax Error at Reserved Keyword

**Error**: `syntax error at or near "user"` or `"order"` or `"group"`

**Fix**: Double-quote reserved keywords used as identifiers.
```sql
-- WRONG:  SELECT * FROM {{DATASET}}.user
-- RIGHT:  SELECT * FROM {{DATASET}}."user"
-- WRONG:  SELECT order FROM {{DATASET}}.events
-- RIGHT:  SELECT "order" FROM {{DATASET}}.events
```

### 13. Subquery Must Have Alias

**Error**: `subquery in FROM must have an alias`

**Fix**: Add an alias to all subqueries in FROM.
```sql
-- WRONG:  SELECT * FROM (SELECT id FROM {{DATASET}}.users)
-- RIGHT:  SELECT * FROM (SELECT id FROM {{DATASET}}.users) AS sub
```

## Instructions

1. Analyze the error message and identify the root cause
2. Apply the fix using actual column names from the schema
3. Qualify all table names with the schema ({{DATASET}})
4. Use lowercase for unquoted identifiers
5. Use `::` for type casting when types mismatch
6. Use CTEs or subqueries for window function filtering (no QUALIFY in PostgreSQL)
7. Use `NOT EXISTS` instead of `NOT IN` with subqueries
8. Use half-open intervals (`>=` / `<`) instead of `BETWEEN` for timestamps
9. Use `NULLIF(x, 0)` to guard against division by zero
10. Preserve the original query intent
11. Ensure the filter clause is present if required

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
