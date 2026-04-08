# Creating Domain Packs

> **Version**: 0.4.0

A domain pack teaches DecisionBox how to analyze data for a specific industry. This guide walks through creating one.

Domain packs are JSON documents stored in MongoDB. No Go code is needed. You can create domain packs in two ways:

1. **Dashboard** -- Navigate to `/domain-packs` and use the visual editor
2. **JSON import** -- Prepare a portable JSON file and import via the API

Three complete reference packs ship as built-ins: Gaming, Social Network, and Ecommerce.

## What You'll Build

A domain pack provides:
1. **Categories** -- Sub-types within your domain (e.g., B2C vs marketplace for e-commerce)
2. **Analysis areas** -- What patterns to find (id, name, keywords, priority, prompt)
3. **Prompts** -- How the AI reasons about your data (markdown content)
4. **Profile schema** -- What context users provide about their product (JSON Schema)

## Step 1: Define Analysis Areas

Create `prompts/base/areas.json` — the patterns your domain should discover:

```json
[
  {
    "id": "conversion",
    "name": "Conversion Funnel",
    "description": "Drop-offs in the purchase funnel from browse to checkout",
    "keywords": ["conversion", "funnel", "cart", "checkout", "purchase", "browse", "add_to_cart"],
    "priority": 1,
    "prompt_file": "analysis_conversion.md"
  },
  {
    "id": "retention",
    "name": "Customer Retention",
    "description": "Repeat purchase patterns and customer lifetime analysis",
    "keywords": ["retention", "repeat", "returning", "lifetime", "ltv", "cohort", "churn"],
    "priority": 2,
    "prompt_file": "analysis_retention.md"
  },
  {
    "id": "revenue",
    "name": "Revenue Optimization",
    "description": "Pricing, discounting, and revenue distribution patterns",
    "keywords": ["revenue", "price", "discount", "aov", "arpu", "margin", "spend"],
    "priority": 3,
    "prompt_file": "analysis_revenue.md"
  }
]
```

### Field Reference

| Field | Description |
|-------|-------------|
| `id` | Unique identifier. Used in API responses, prompts, insight IDs. Lowercase, no spaces. |
| `name` | Display name shown in the dashboard. |
| `description` | What this area looks for. Shown in the UI and fed to the AI during exploration. |
| `keywords` | The agent filters exploration query results by these keywords to find results relevant to this area. Choose keywords that appear in table/column names in typical warehouses. |
| `priority` | Execution order (1 = first). Lower priority areas run first. |
| `prompt_file` | Filename of the analysis prompt markdown file (relative to this `areas.json`). |

### Category-Specific Areas

If your domain has sub-types, add category-specific areas in `prompts/categories/{category}/areas.json`:

```json
[
  {
    "id": "cart_abandonment",
    "name": "Cart Abandonment",
    "description": "Analyze cart abandonment patterns and recovery opportunities",
    "keywords": ["cart", "abandon", "drop", "checkout", "recovery"],
    "priority": 4,
    "prompt_file": "analysis_cart.md"
  }
]
```

These are merged with base areas at runtime. A B2C e-commerce project would get: conversion + retention + revenue (base) + cart_abandonment (B2C specific).

## Step 2: Write Prompts

### Base Context (`base_context.md`)

This is prepended to ALL analysis and recommendation prompts. It provides the project profile and previous discovery context.

```markdown
## Project Profile

{{PROFILE}}

**IMPORTANT**: Use the project profile above to understand this specific business — its model, products, target audience, and goals. Tailor all analysis and recommendations to THIS business.

## Previous Discovery Context

{{PREVIOUS_CONTEXT}}
```

You must include `{{PROFILE}}` and `{{PREVIOUS_CONTEXT}}` — these are the only variables used in base context.

### Exploration Prompt (`exploration.md`)

The system prompt for autonomous data exploration. This tells the AI how to explore the warehouse.

```markdown
# E-Commerce Analytics Discovery Agent

You are an autonomous data exploration agent for an e-commerce business. Your job is to discover actionable insights by querying the data warehouse.

## Available Data

**Datasets**: {{DATASET}}

**Tables and Schemas**:
{{SCHEMA_INFO}}

## Data Filtering

{{FILTER_CONTEXT}}
{{FILTER_RULE}}

## Analysis Areas to Explore

{{ANALYSIS_AREAS}}

## Your Process

1. Start by understanding the data landscape — what tables exist, how many records, date ranges
2. Look for patterns in each analysis area
3. Cross-reference findings across areas
4. Focus on actionable insights with specific numbers

## Response Format

For each exploration step, respond with JSON:
```json
{
  "thinking": "Your reasoning for this query",
  "query": "SELECT ... FROM ..."
}
```

## Rules

- Write valid SQL for the {{DATASET}} warehouse
- Always include date ranges in queries (don't scan all history)
- Use COUNT(DISTINCT user_id) for user counts, not row counts
- {{FILTER_RULE}}
```

### Category Context (`exploration_context.md`)

Optional. Appended to the base exploration prompt for category-specific guidance:

```markdown
## E-Commerce B2C Context

This is a B2C (business-to-consumer) e-commerce business. Key concepts:
- **Cart**: Items added but not yet purchased
- **Checkout**: The purchase completion process
- **AOV**: Average Order Value
- **Customer Lifetime Value**: Total revenue from a customer over time

Focus on shopping behavior, cart-to-purchase conversion, and repeat purchase patterns.
```

### Analysis Prompts (`analysis_{area}.md`)

One per analysis area. Tells the AI how to generate insights from exploration data.

```markdown
# Conversion Funnel Analysis

You are an e-commerce analytics expert analyzing conversion funnel patterns.

## Context

**Dataset**: {{DATASET}}
**Exploration Queries**: {{TOTAL_QUERIES}}

## Your Task

Analyze the query results below and identify **specific conversion patterns** with exact numbers and percentages.

## Required Output Format

Respond with ONLY valid JSON (no markdown, no explanations):

```json
{
  "insights": [
    {
      "name": "Specific descriptive name (e.g., 'Mobile Cart Abandonment at 73% vs Desktop 45%')",
      "description": "Detailed description with exact percentages and user counts.",
      "severity": "critical|high|medium|low",
      "affected_count": 2847,
      "risk_score": 0.68,
      "confidence": 0.85,
      "metrics": {
        "conversion_rate": 0.032,
        "cart_abandonment_rate": 0.73,
        "avg_order_value": 45.50
      },
      "indicators": [
        "Mobile conversion dropped 15% in last 30 days",
        "Cart page has 8.2s average load time on mobile"
      ],
      "target_segment": "Mobile users who add items but don't purchase",
      "source_steps": [1, 3, 5]
    }
  ]
}
```

- **source_steps**: List the step numbers from the query results that support this insight.

## Quality Standards

- Use ONLY data from the queries below — don't make up numbers
- Be extremely specific — exact percentages, counts, time periods
- affected_count must be COUNT(DISTINCT user_id), not total rows
- Minimum affected: 50+ users

## Query Results

{{QUERY_RESULTS}}

Now analyze the data above and respond with valid JSON.
```

### Recommendations Prompt (`recommendations.md`)

You can copy the gaming domain's recommendations prompt as a starting point. The key sections:

```markdown
# Generate Actionable Recommendations

You are an e-commerce analytics expert creating **specific, actionable recommendations**.

## Context

**Discovery Date**: {{DISCOVERY_DATE}}
**Insights Found**: {{INSIGHTS_SUMMARY}}

## Output Format

```json
{
  "recommendations": [
    {
      "title": "Action - Context",
      "description": "Detailed explanation with numbers.",
      "category": "conversion|retention|revenue|growth",
      "priority": 1,
      "target_segment": "Exact segment definition",
      "segment_size": 1234,
      "expected_impact": {
        "metric": "conversion_rate|revenue|retention",
        "estimated_improvement": "15-20%",
        "reasoning": "Why we expect this"
      },
      "actions": ["Step 1", "Step 2", "Step 3"],
      "related_insight_ids": ["conversion-1", "retention-2"],
      "confidence": 0.85
    }
  ]
}
```

**IMPORTANT:** Each recommendation MUST include `related_insight_ids` — copy the exact `id` values from the insights below.

## Discovered Insights

{{INSIGHTS_DATA}}
```

## Step 3: Create Profile Schema

### Base Schema (`profiles/schema.json`)

Define what users tell the AI about their business:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "E-Commerce Project Profile",
  "type": "object",
  "properties": {
    "business_info": {
      "type": "object",
      "title": "Business Information",
      "properties": {
        "industry": {
          "type": "string",
          "title": "Industry",
          "enum": ["fashion", "electronics", "food", "health", "home", "other"]
        },
        "business_model": {
          "type": "string",
          "title": "Business Model",
          "enum": ["b2c", "b2b", "marketplace", "subscription"]
        },
        "target_market": {
          "type": "string",
          "title": "Target Market",
          "description": "Primary customer demographic"
        }
      }
    },
    "kpis": {
      "type": "object",
      "title": "Target KPIs",
      "properties": {
        "conversion_rate_target": { "type": "number", "title": "Conversion Rate Target (%)" },
        "aov_target": { "type": "number", "title": "AOV Target ($)" },
        "retention_30d_target": { "type": "number", "title": "30-Day Retention Target (%)" }
      }
    }
  }
}
```

The dashboard renders a dynamic form from this schema. Users fill it in, and the data is injected into prompts via `{{PROFILE}}`.

### Category Extensions (`profiles/categories/b2c.json`)

Add fields specific to B2C:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "B2C E-Commerce Profile Extensions",
  "type": "object",
  "properties": {
    "product_catalog": {
      "type": "object",
      "title": "Product Catalog",
      "properties": {
        "total_products": { "type": "integer", "title": "Total Products" },
        "avg_price": { "type": "number", "title": "Average Price ($)" },
        "categories": {
          "type": "array",
          "title": "Product Categories",
          "items": { "type": "string" }
        }
      }
    },
    "shipping": {
      "type": "object",
      "title": "Shipping",
      "properties": {
        "free_shipping_threshold": { "type": "number", "title": "Free Shipping Threshold ($)" },
        "avg_delivery_days": { "type": "integer", "title": "Average Delivery Days" }
      }
    }
  }
}
```

## Step 4: Create via Dashboard

The simplest way to add a domain pack is through the dashboard:

1. Navigate to `/domain-packs` in the dashboard
2. Click **Create Domain Pack**
3. Fill in the pack name, slug, description, and categories
4. Add analysis areas with their prompt content using the markdown editor
5. Define the profile schema (JSON Schema)
6. Click **Publish** to make it available for new projects

## Step 5: Import via API (Alternative)

You can also prepare a portable JSON file and import it via the API.

### Export Format

The `decisionbox-domain-pack` format is a self-contained JSON document:

```json
{
  "format": "decisionbox-domain-pack",
  "version": 1,
  "domain_pack": {
    "name": "E-Commerce",
    "slug": "ecommerce",
    "description": "Purchase funnel, revenue, and retention analytics for online stores",
    "categories": [
      {
        "id": "b2c",
        "name": "B2C Retail",
        "description": "Direct-to-consumer e-commerce"
      }
    ],
    "prompts": {
      "base": {
        "areas": [...],
        "exploration": "# E-Commerce Analytics Discovery Agent\n...",
        "base_context": "## Project Profile\n\n{{PROFILE}}\n...",
        "recommendations": "# Generate Actionable Recommendations\n...",
        "analysis": {
          "conversion": "# Conversion Funnel Analysis\n...",
          "retention": "# Customer Retention Analysis\n..."
        }
      },
      "categories": {
        "b2c": {
          "areas": [...],
          "exploration_context": "## E-Commerce B2C Context\n...",
          "analysis": {
            "cart_abandonment": "# Cart Abandonment Analysis\n..."
          }
        }
      }
    },
    "profiles": {
      "base": { ... },
      "categories": {
        "b2c": { ... }
      }
    }
  }
}
```

### Import

```bash
curl -X POST http://localhost:8080/api/v1/domain-packs/import \
  -H "Content-Type: application/json" \
  -d @my-domain-pack.json
```

### Export an Existing Pack

```bash
curl http://localhost:8080/api/v1/domain-packs/ecommerce/export -o ecommerce-pack.json
```

## Step 6: Test

Create a project using your new domain pack via the dashboard, then run a discovery to verify the prompts produce good results:

```bash
# Via API
curl -X POST http://localhost:8080/api/v1/projects/{id}/discover

# Or via make
make agent-run PROJECT_ID=your-project-id
```

No Go compilation is needed -- domain packs are loaded from MongoDB at project creation time.

## Prompt Writing Tips

1. **Be specific about SQL dialect** — Your exploration prompt should mention the warehouse's SQL dialect
2. **Include realistic examples** — Show example insight names, metrics, segments
3. **Keywords matter** — Area keywords filter exploration results. Choose words that appear in warehouse table/column names
4. **Test with real data** — Run discoveries and iterate on prompts based on results
5. **Check source_steps** — Make sure the AI cites which exploration steps support each insight
6. **Review `{{QUERY_RESULTS}}`** — Before writing analysis prompts, run an exploration and look at what data the AI actually finds

## Next Steps

- [Domain Packs Concept](../concepts/domain-packs.md) — How the hierarchy works
- [Prompt Variables](../reference/prompt-variables.md) — All template variables
- [Customizing Prompts](customizing-prompts.md) — Edit prompts per-project
