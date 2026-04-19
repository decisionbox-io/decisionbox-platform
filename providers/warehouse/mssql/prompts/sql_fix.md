# SQL Fix - Microsoft SQL Server (T-SQL)

You are a Microsoft SQL Server T-SQL expert. Fix the failed query below.

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

## T-SQL Reference

### Identifiers

- SQL Server is case-insensitive for identifiers under the default collation (e.g. `SQL_Latin1_General_CP1_CI_AS`). Data comparisons follow the collation; `WHERE name = 'bob'` and `WHERE name = 'BOB'` match the same rows unless the column uses a `_CS_` (case-sensitive) collation.
- Qualify object names as `schema.table` (e.g. `{{DATASET}}.orders`). Fully qualified names look like `database.schema.table`.
- Escape reserved keywords or names containing special characters with square brackets: `[user]`, `[order]`, `[group]`, `[column with space]`. Double quotes also work only when `QUOTED_IDENTIFIER` is ON.
- Parameter placeholders are `@name` (or `@p1, @p2, ...`), not `?` or `$1`.

### Paging, TOP, and OFFSET/FETCH

```sql
-- Top N rows (no paging)
SELECT TOP 10 * FROM {{DATASET}}.orders ORDER BY created_at DESC

-- Paging with OFFSET/FETCH — ORDER BY is REQUIRED
SELECT * FROM {{DATASET}}.orders
ORDER BY created_at DESC
OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY

-- Arbitrary order when using OFFSET/FETCH without a natural sort:
SELECT * FROM {{DATASET}}.orders
ORDER BY (SELECT NULL)
OFFSET 0 ROWS FETCH NEXT 100 ROWS ONLY
```

**Critical rules**:
- `TOP` and `OFFSET/FETCH` **cannot** be combined in the same query.
- `OFFSET/FETCH` **requires** `ORDER BY`. Use `ORDER BY (SELECT NULL)` for arbitrary order.
- `FETCH NEXT n ROWS ONLY` cannot be used without `OFFSET`.

### CTEs (WITH clause)

```sql
WITH active_users AS (
    SELECT user_id, COUNT(*) AS event_count
    FROM {{DATASET}}.events
    WHERE created_at > DATEADD(day, -7, SYSUTCDATETIME())
    GROUP BY user_id
)
SELECT * FROM active_users WHERE event_count > 10
```

Recursive CTE:
```sql
WITH category_tree AS (
    SELECT id, name, parent_id, 0 AS depth
    FROM {{DATASET}}.categories WHERE parent_id IS NULL
    UNION ALL
    SELECT c.id, c.name, c.parent_id, ct.depth + 1
    FROM {{DATASET}}.categories c
    JOIN category_tree ct ON c.parent_id = ct.id
)
SELECT * FROM category_tree ORDER BY depth, name
OPTION (MAXRECURSION 1000)
```

### CROSS APPLY / OUTER APPLY (correlated subquery in FROM)

```sql
-- Top 3 orders per customer (SQL Server equivalent of LATERAL JOIN)
SELECT c.id, c.name, o.amount, o.created_at
FROM {{DATASET}}.customers c
CROSS APPLY (
    SELECT TOP 3 amount, created_at
    FROM {{DATASET}}.orders
    WHERE customer_id = c.id
    ORDER BY amount DESC
) o
```

`OUTER APPLY` keeps the left row when the right side returns no rows (analogous to `LEFT JOIN LATERAL`).

### PIVOT / UNPIVOT

```sql
-- Rotate rows into columns
SELECT * FROM (
    SELECT user_id, status, amount FROM {{DATASET}}.orders
) src
PIVOT (SUM(amount) FOR status IN ([pending], [completed], [cancelled])) AS p
```

### Window Functions

```sql
SELECT
    user_id,
    created_at,
    ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC) AS rn,
    SUM(amount) OVER (PARTITION BY user_id ORDER BY created_at) AS running_total
FROM {{DATASET}}.orders
```

SQL Server has no `QUALIFY` clause — use a CTE or subquery to filter on window results (see Common Errors #9 below).

### Date/Time Functions

```sql
SYSUTCDATETIME()                                -- current UTC datetime2(7) — PREFERRED for comparisons
SYSDATETIME()                                   -- current server local datetime2(7)
GETUTCDATE()                                    -- current UTC datetime (lower precision)
GETDATE()                                       -- current server local datetime
CAST(SYSUTCDATETIME() AS date)                  -- current date
DATEADD(day, 7, created_at)                     -- add interval
DATEADD(month, -1, created_at)                  -- subtract interval
DATEDIFF(day, start_date, end_date)             -- difference in units
DATEPART(weekday, created_at)                   -- day of week (1..7 per DATEFIRST)
YEAR(created_at), MONTH(created_at), DAY(created_at)
FORMAT(created_at, 'yyyy-MM-dd HH:mm:ss')       -- format (slow — use CONVERT when possible)
CONVERT(varchar, created_at, 120)               -- ODBC canonical format
DATEFROMPARTS(2026, 1, 15)                      -- construct date
DATETIMEFROMPARTS(2026, 1, 15, 10, 30, 0, 0)    -- construct datetime
SWITCHOFFSET(created_at_dto, '+00:00')          -- convert datetimeoffset timezone
TODATETIMEOFFSET(created_at_dt, '+00:00')       -- attach timezone to naïve datetime
EOMONTH(created_at)                             -- last day of month
```

**Always prefer `SYSUTCDATETIME()` over `GETDATE()`** for comparisons — it is unaffected by the server's time-zone setting. For date-only math use `CAST(... AS date)`.

### Type Conversion

```sql
CAST(column AS int)                             -- standard cast (raises error on failure)
CONVERT(int, column)                            -- T-SQL cast with optional style arg
TRY_CAST(column AS int)                         -- returns NULL on failure instead of erroring
TRY_CONVERT(int, column)                        -- same but with CONVERT's style arg
PARSE(s AS date USING 'en-US')                  -- culture-aware parse (slow, CLR-based)
TRY_PARSE(s AS date USING 'en-US')              -- same but returns NULL on failure
```

**Always use `TRY_CAST`/`TRY_CONVERT` when the input may not match the target type.** `CAST`/`CONVERT` fail the whole query on a single bad row.

### NULL Handling

```sql
ISNULL(col, 'default')                          -- T-SQL two-arg null coalesce
COALESCE(a, b, c, 'default')                    -- ANSI multi-arg coalesce
NULLIF(col, 0)                                  -- NULL if col = 0 (use to avoid div/0)
col IS NULL                                     -- correct null test
col IS NOT NULL
```

**Never use `= NULL` or `<> NULL` — always use `IS NULL` / `IS NOT NULL`.** Under the default `ANSI_NULLS ON` setting, any comparison with NULL yields UNKNOWN, which filters out all rows.

### JSON (SQL Server 2016+)

JSON is stored in `NVARCHAR(MAX)`, not a dedicated type.

```sql
-- Extract scalar values
SELECT JSON_VALUE(data, '$.user.id')            -- returns nvarchar(4000)
SELECT JSON_VALUE(data, '$.items[0].price')
-- Extract objects/arrays as JSON text
SELECT JSON_QUERY(data, '$.user')               -- returns nvarchar(max)
-- Modify JSON
SELECT JSON_MODIFY(data, '$.status', 'active')
-- Check validity / existence
SELECT ISJSON(data)                             -- 1 if valid JSON, else 0
SELECT * FROM {{DATASET}}.events WHERE JSON_VALUE(data, '$.type') = 'login'
-- Expand JSON to rows
SELECT event_id, k.[key], k.[value]
FROM {{DATASET}}.events e
CROSS APPLY OPENJSON(e.data) AS k
```

### String Aggregation

```sql
-- Concatenate grouped values (SQL Server 2017+)
SELECT user_id, STRING_AGG(tag, ', ') WITHIN GROUP (ORDER BY tag) AS tags
FROM {{DATASET}}.tags
GROUP BY user_id

-- Split delimited string into rows
SELECT value FROM STRING_SPLIT('a,b,c', ',')
```

## Common SQL Server Errors

### 1. Invalid Object Name (Msg 208 — MOST COMMON)

**Error**: `Invalid object name 'sessions'.`

**Cause**: Missing schema prefix, wrong database context, typo, or object created in a different schema.

**Fix**: Always schema-qualify table names. Bracket reserved keywords.
```sql
-- WRONG:  SELECT * FROM sessions
-- RIGHT:  SELECT * FROM {{DATASET}}.sessions

-- Reserved keyword as a table name:
-- WRONG:  SELECT * FROM {{DATASET}}.user
-- RIGHT:  SELECT * FROM {{DATASET}}.[user]
```

### 2. Column Must Appear in GROUP BY (Msg 8120)

**Error**: `Column '{{DATASET}}.events.name' is invalid in the select list because it is not contained in either an aggregate function or the GROUP BY clause.`

**Fix**: Every non-aggregated column in SELECT must be in GROUP BY. The same rule applies to ORDER BY and HAVING.
```sql
-- WRONG:  SELECT user_id, name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id
-- FIX 1:  Add to GROUP BY
SELECT user_id, name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id, name
-- FIX 2:  Wrap in aggregate
SELECT user_id, MAX(name) AS name, COUNT(*) FROM {{DATASET}}.events GROUP BY user_id
-- FIX 3:  Use a window function (no GROUP BY needed)
SELECT DISTINCT user_id, name, COUNT(*) OVER (PARTITION BY user_id) AS cnt
FROM {{DATASET}}.events
```

### 3. Aggregate in WHERE (Msg 147)

**Error**: `An aggregate may not appear in the WHERE clause unless it is in a subquery contained in a HAVING clause or a select list...`

**Fix**: Use HAVING to filter on aggregate results. WHERE filters rows before grouping; HAVING filters after.
```sql
-- WRONG:  SELECT user_id, COUNT(*) FROM {{DATASET}}.events
--         WHERE COUNT(*) > 5 GROUP BY user_id
-- RIGHT:  SELECT user_id, COUNT(*) FROM {{DATASET}}.events
--         GROUP BY user_id HAVING COUNT(*) > 5
```

### 4. Ambiguous Column Name (Msg 209)

**Error**: `Ambiguous column name 'user_id'.`

**Fix**: Add table aliases and qualify every column after a join.
```sql
-- WRONG:  SELECT user_id FROM {{DATASET}}.a JOIN {{DATASET}}.b ON a.id = b.id
-- RIGHT:  SELECT a.user_id FROM {{DATASET}}.a AS a JOIN {{DATASET}}.b AS b ON a.id = b.id
```

### 5. Conversion Failed When Converting (Msg 245)

**Error**: `Conversion failed when converting the nvarchar value '2026-13-01' to data type datetime.` / `Conversion failed when converting the varchar value 'abc' to data type int.`

**Causes**: Wrong regional date format (mm/dd vs dd/mm), invalid calendar value, or a non-numeric string in a numeric column. SQL Server's interpretation depends on `SET DATEFORMAT` and `SET LANGUAGE`.

**Fix**: Use `TRY_CAST` / `TRY_CONVERT` to get NULL on failure, and use unambiguous date literals (ISO 8601: `'2026-01-15'` or `'20260115'`).
```sql
-- WRONG:  SELECT CAST(user_input AS int) FROM {{DATASET}}.raw_events
-- RIGHT:  SELECT TRY_CAST(user_input AS int) FROM {{DATASET}}.raw_events

-- WRONG:  WHERE created_at = '13/01/2026'          -- locale-dependent
-- RIGHT:  WHERE created_at = '2026-01-13'          -- ISO 8601 is unambiguous
-- RIGHT:  WHERE created_at = CONVERT(datetime, '2026-01-13', 23)  -- style 23 = ODBC date
```

### 6. NOT IN Returns No Rows (NULL Trap)

**Error**: Query returns 0 rows unexpectedly when using `NOT IN` with a subquery.

**Cause**: With `ANSI_NULLS ON` (the default), if any value in the subquery is NULL, `NOT IN` evaluates to UNKNOWN for every row and returns nothing. `x NOT IN (1, NULL)` is equivalent to `x <> 1 AND x <> NULL`, and `x <> NULL` is UNKNOWN.

**Fix**: Use `NOT EXISTS` (safe with NULLs) or add an explicit NULL filter to the subquery.
```sql
-- WRONG (returns 0 rows if bar.x has any NULLs):
SELECT * FROM {{DATASET}}.foo WHERE col NOT IN (SELECT x FROM {{DATASET}}.bar)

-- RIGHT — preferred:
SELECT * FROM {{DATASET}}.foo f
WHERE NOT EXISTS (SELECT 1 FROM {{DATASET}}.bar b WHERE b.x = f.col)

-- RIGHT — alternative:
SELECT * FROM {{DATASET}}.foo
WHERE col NOT IN (SELECT x FROM {{DATASET}}.bar WHERE x IS NOT NULL)
```

### 7. BETWEEN with Datetimes (Boundary Double-Counting)

**Error**: Rows at midnight boundaries are double-counted or missed across consecutive periods.

**Cause**: `BETWEEN` is a closed interval (inclusive on both ends). A row at exactly `2026-01-08 00:00:00` is included in both `BETWEEN '2026-01-01' AND '2026-01-08'` and `BETWEEN '2026-01-08' AND '2026-01-15'`.

**Fix**: Use half-open intervals with `>=` and `<`.
```sql
-- WRONG:  WHERE created_at BETWEEN '2026-01-01' AND '2026-01-08'
-- RIGHT:  WHERE created_at >= '2026-01-01' AND created_at < '2026-01-08'
```

### 8. NULL Comparison with `=` / `<>`

**Error**: Query silently returns 0 rows when filtering NULL columns.

**Cause**: Under `ANSI_NULLS ON` (the default, and required for modern features), `col = NULL` is UNKNOWN, not TRUE.

**Fix**: Use `IS NULL` / `IS NOT NULL`. For default values use `ISNULL(col, 'default')` or `COALESCE(col, 'default')`.
```sql
-- WRONG:  WHERE col = NULL
-- RIGHT:  WHERE col IS NULL
-- WRONG:  WHERE col <> NULL
-- RIGHT:  WHERE col IS NOT NULL
```

### 9. Window Function in WHERE (Msg 4108)

**Error**: `Windowed functions can only appear in the SELECT or ORDER BY clauses.`

**Fix**: SQL Server has no `QUALIFY` clause. Wrap the window function in a CTE or derived table and filter there.
```sql
-- WRONG:  WHERE ROW_NUMBER() OVER (...) = 1
-- RIGHT:
WITH ranked AS (
    SELECT *,
           ROW_NUMBER() OVER (PARTITION BY user_id ORDER BY created_at DESC) AS rn
    FROM {{DATASET}}.events
)
SELECT * FROM ranked WHERE rn = 1
```

### 10. Division by Zero (Msg 8134)

**Error**: `Divide by zero error encountered.`

**Fix**: Wrap the denominator in `NULLIF(x, 0)` so the division yields NULL rather than erroring.
```sql
-- WRONG:  SELECT total / count AS avg FROM {{DATASET}}.stats
-- RIGHT:  SELECT total / NULLIF(count, 0) AS avg FROM {{DATASET}}.stats
```

### 11. Missing ORDER BY with OFFSET/FETCH (Msg 102)

**Error**: `Incorrect syntax near 'OFFSET'.` or `The OFFSET clause must be used with ORDER BY.`

**Fix**: Add `ORDER BY`. Use `ORDER BY (SELECT NULL)` if no natural sort key exists.
```sql
-- WRONG:  SELECT * FROM {{DATASET}}.orders OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY
-- RIGHT:  SELECT * FROM {{DATASET}}.orders ORDER BY id OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY
```

Also: do not combine `TOP` with `OFFSET/FETCH` in the same query — remove the `TOP`.

### 12. Subquery Must Have Alias (Msg 102/156)

**Error**: `Incorrect syntax near ')'.` when using a derived table without an alias.

**Fix**: Every subquery in FROM must have an alias.
```sql
-- WRONG:  SELECT * FROM (SELECT id FROM {{DATASET}}.users)
-- RIGHT:  SELECT * FROM (SELECT id FROM {{DATASET}}.users) AS sub
```

### 13. Reserved Keyword Used as Identifier (Msg 156)

**Error**: `Incorrect syntax near the keyword 'user'.` (or `order`, `group`, `key`, `index`, `desc`, `asc`, `case`).

**Fix**: Wrap reserved identifiers in square brackets.
```sql
-- WRONG:  SELECT * FROM {{DATASET}}.user
-- RIGHT:  SELECT * FROM {{DATASET}}.[user]
-- WRONG:  SELECT order FROM {{DATASET}}.events
-- RIGHT:  SELECT [order] FROM {{DATASET}}.events
```

### 14. Arithmetic Overflow (Msg 8115)

**Error**: `Arithmetic overflow error converting expression to data type int.` / `...to data type bigint.`

**Cause**: An aggregate sum exceeds the target type's range, e.g. `SUM(int_col)` where the total overflows `int`.

**Fix**: Cast the argument to a bigger type before aggregation.
```sql
-- WRONG:  SELECT SUM(amount) FROM {{DATASET}}.orders          -- int SUM overflow
-- RIGHT:  SELECT SUM(CAST(amount AS bigint)) FROM {{DATASET}}.orders
-- RIGHT:  SELECT SUM(CAST(amount AS decimal(38,2))) FROM {{DATASET}}.orders
```

### 15. String Truncation (Msg 2628/8152)

**Error**: `String or binary data would be truncated.`

**Cause**: Inserting/comparing a string longer than the target column width. Also applies to implicit CAST inside expressions.

**Fix**: Cast to a wider type, or use `LEFT(s, n)` to truncate safely.
```sql
-- Use a wider cast for comparisons:
WHERE CAST(short_col AS nvarchar(max)) = long_expr
```

## Instructions

1. Identify the error code/message and the root cause.
2. Schema-qualify every table reference with `{{DATASET}}`.
3. Bracket reserved keywords used as identifiers: `[user]`, `[order]`.
4. Use `@p1`, `@p2`, `@Name` placeholders (never `?` or `$1`).
5. Use `TRY_CAST` / `TRY_CONVERT` for any type conversion that might fail.
6. Use `NOT EXISTS` instead of `NOT IN` with subqueries.
7. Use half-open intervals (`>=` / `<`) instead of `BETWEEN` for timestamps.
8. Use `NULLIF(x, 0)` to guard against division by zero.
9. Filter window-function results via CTE/subquery (no QUALIFY in T-SQL).
10. Always pair `OFFSET/FETCH` with `ORDER BY` (use `ORDER BY (SELECT NULL)` if needed).
11. Never combine `TOP` and `OFFSET/FETCH` in the same query.
12. Use `SYSUTCDATETIME()` for current-time comparisons.
13. Use `IS NULL` / `IS NOT NULL` instead of `= NULL` / `<> NULL`.
14. Preserve the original query intent.
15. Ensure the filter clause is present if required.

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
