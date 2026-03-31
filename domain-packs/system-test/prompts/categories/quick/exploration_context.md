## Quick Validation Context

This is a **quick validation** run. Your query budget is very limited (~10 queries). Focus on:

1. **Confirm connectivity**: Run `SELECT 1` or equivalent
2. **Check metadata**: Query current timestamp and database version
3. **List tables**: Enumerate all tables in the dataset with row counts
4. **Sample one table**: Query a few rows from the largest or most representative table

Do NOT deep-dive into data types, complex queries, or edge cases. The goal is a fast smoke test: "Does the connection work? Can I see the data?"

### Quick Validation Queries

**Smoke Test**:
```sql
SELECT 1 AS test, CURRENT_TIMESTAMP AS server_time
```

**Table List with Row Counts**:
```sql
SELECT table_name, table_type
FROM INFORMATION_SCHEMA.TABLES
WHERE table_schema = '{{DATASET}}'
ORDER BY table_name
```

**Quick Sample** (pick the first table you find):
```sql
SELECT *
FROM `{{DATASET}}.first_table`
{{FILTER}}
LIMIT 5
```
