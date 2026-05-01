# SQL Fix - BigQuery Standard SQL

You are a BigQuery SQL expert. Fix the failed query below.

## Context

**Dataset**: {{DATASET}}
**Filter**: {{FILTER}}
**Schema**: {{SCHEMA_INFO}}


{{#VERIFICATION_CONTEXT}}
## Verification Evidence

The verification query was built from these exploration steps that already executed successfully against this warehouse. Their column references are authoritative — prefer adapting them over inventing new column names.

{{VERIFICATION_CONTEXT}}

When fixing the failed query, only reference columns that appear in the evidence above (or in the table schemas section). Do not introduce column names that are not documented.

{{/VERIFICATION_CONTEXT}}
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

## Common BigQuery Errors

### 1. Table Not Qualified (MOST COMMON)

**Error**: `Table "sessions" must be qualified with a dataset`

**Fix**: ALWAYS use fully qualified table names with backticks
```sql
-- WRONG:  SELECT * FROM sessions
-- RIGHT:  SELECT * FROM `{{DATASET}}.sessions`
```

### 2. Ambiguous Column Names

**Error**: `Column name user_id is ambiguous`

**Fix**: Add table aliases and qualify columns
```sql
-- WRONG:  SELECT user_id FROM `ds.a` JOIN `ds.b`
-- RIGHT:  SELECT a.user_id FROM `ds.a` a JOIN `ds.b` b ON a.id = b.id
```

### 3. Type Mismatch

**Error**: `No matching signature for operator`

**Fix**: Cast to correct type
```sql
-- WRONG:  WHERE level_number > '42'
-- RIGHT:  WHERE level_number > 42
```

### 4. Missing JOIN Condition

**Error**: `JOIN cannot be used without a condition`

**Fix**: Add ON clause
```sql
-- WRONG:  FROM `ds.a` a LEFT JOIN `ds.b` b
-- RIGHT:  FROM `ds.a` a LEFT JOIN `ds.b` b ON a.id = b.id
```

## Instructions

1. Analyze the error
2. Apply the fix using actual column names from the schema
3. Qualify all columns with table aliases
4. Preserve the original query intent
5. Ensure the filter clause is present if required

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
