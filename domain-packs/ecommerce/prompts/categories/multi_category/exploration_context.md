## Multi-Category Store Context

This is a **multi-category online retail store** selling across diverse product categories (e.g., electronics, appliances, computers, fashion, home goods). Key aspects to explore:

- **Category ecosystem**: The store spans many product categories with different purchase dynamics. High-ticket categories (electronics) may have high AOV but low repeat rates, while consumable categories have low AOV but high frequency. Analyze how categories differ in conversion, revenue, and retention characteristics.
- **Cross-category behavior**: Do customers shop across multiple categories or stick to one? Cross-category shoppers are typically more valuable and retained longer. Identify which category combinations are most common.
- **Category hierarchy**: Product categories may be hierarchical (e.g., top-level "Electronics" containing subcategories like "Smartphones", "Audio"). Discover the hierarchy structure from the data — it may be encoded as separate columns, a dot/slash-separated string, or a parent-child relationship. Analyze at both the top level and subcategory level for granular insights.
- **Brand dynamics**: Brands compete within categories. Identify which brands dominate views vs purchases (brand awareness vs conversion), and whether customers show brand loyalty across sessions.
- **Price range diversity**: Multi-category stores have enormous price ranges — from low-cost accessories to high-ticket electronics. Analyze shopping behavior within and across price ranges.
- **Missing categorization data**: Product category and brand fields may have NULL or missing values. Track the proportion of events with missing values — if significant, insights about uncategorized products may be important.
- **Session browsing patterns**: In a large catalog, session behavior reveals discovery effectiveness. How many categories does a typical session span? Do users who browse deeply convert better?
- **Seasonal and temporal patterns**: Multi-category stores often see category-level seasonality (electronics spikes during holidays, etc.). Check for time-based patterns.

### Multi-Category Example Queries

> **Important**: Adapt all column names, table names, and SQL functions to match the actual schema in {{SCHEMA_INFO}} and the connected warehouse's SQL dialect. These examples illustrate the types of analysis to perform.

**Top Categories by Event Type**:
```sql
-- Discover top-level categories and their funnel metrics
-- Adapt the category extraction to your schema:
--   If categories are hierarchical strings, split to get the top level
--   If separate columns exist for category levels, use the top-level column
SELECT
  top_level_category,
  COUNT(DISTINCT CASE WHEN event_type = 'view' THEN customer_id END) as viewers,
  COUNT(DISTINCT CASE WHEN event_type = 'purchase' THEN customer_id END) as buyers
FROM `{{DATASET}}.events`
{{FILTER}}
  AND top_level_category IS NOT NULL
GROUP BY top_level_category
ORDER BY viewers DESC
```

**Cross-Category Shopping**:
```sql
-- How many categories do customers browse, and does it correlate with purchasing?
SELECT
  categories_browsed,
  COUNT(DISTINCT customer_id) as customers,
  AVG(total_purchases) as avg_purchases,
  AVG(total_spent) as avg_spent
FROM (
  SELECT
    customer_id,
    COUNT(DISTINCT top_level_category) as categories_browsed,
    COUNT(DISTINCT CASE WHEN event_type = 'purchase' THEN product_id END) as total_purchases,
    SUM(CASE WHEN event_type = 'purchase' THEN price ELSE 0 END) as total_spent
  FROM `{{DATASET}}.events`
  {{FILTER}}
    AND top_level_category IS NOT NULL
  GROUP BY customer_id
) sub
GROUP BY categories_browsed
ORDER BY categories_browsed
```

**Brand Performance Within a Category**:
```sql
-- Compare brand conversion rates within a specific category
-- Replace the category filter with an actual category from the data
SELECT
  brand,
  COUNT(DISTINCT CASE WHEN event_type = 'view' THEN customer_id END) as viewers,
  COUNT(DISTINCT CASE WHEN event_type = 'purchase' THEN customer_id END) as buyers
FROM `{{DATASET}}.events`
{{FILTER}}
  AND category = 'example_category'
  AND brand IS NOT NULL
GROUP BY brand
HAVING COUNT(DISTINCT CASE WHEN event_type = 'view' THEN customer_id END) > 100
ORDER BY buyers DESC
LIMIT 15
```

**Session Browsing Depth**:
```sql
-- How does the number of product views per session relate to purchase likelihood?
-- Use whatever session identifier exists in the schema
SELECT
  CASE
    WHEN views_in_session = 1 THEN '1_view'
    WHEN views_in_session BETWEEN 2 AND 5 THEN '2_to_5_views'
    WHEN views_in_session BETWEEN 6 AND 15 THEN '6_to_15_views'
    ELSE 'over_15_views'
  END as session_depth,
  COUNT(DISTINCT session_id) as sessions,
  COUNT(DISTINCT CASE WHEN has_purchase = 1 THEN session_id END) as purchase_sessions
FROM (
  SELECT
    session_id,
    COUNT(CASE WHEN event_type = 'view' THEN 1 END) as views_in_session,
    MAX(CASE WHEN event_type = 'purchase' THEN 1 ELSE 0 END) as has_purchase
  FROM `{{DATASET}}.events`
  {{FILTER}}
  GROUP BY session_id
) sub
GROUP BY session_depth
ORDER BY sessions DESC
```

**Category and Brand NULL Rate**:
```sql
-- Check data quality: how much category/brand data is missing?
SELECT
  COUNT(*) as total_events,
  COUNT(CASE WHEN category IS NULL THEN 1 END) as null_category,
  COUNT(CASE WHEN brand IS NULL THEN 1 END) as null_brand
FROM `{{DATASET}}.events`
{{FILTER}}
```
