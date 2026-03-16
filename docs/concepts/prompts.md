# Prompts

> **Version**: 0.1.0

Prompts control how the AI reasons about your data. DecisionBox uses markdown template files with variables that are replaced at runtime with project-specific context.

## Prompt Types

| Prompt | File | Purpose | When used |
|--------|------|---------|-----------|
| **Base Context** | `base_context.md` | Project profile + previous discovery context | Prepended to ALL prompts |
| **Exploration** | `exploration.md` | System prompt for autonomous data exploration | Phase 4 (Exploration) |
| **Category Context** | `exploration_context.md` | Additional exploration context for the specific category | Appended to exploration prompt |
| **Analysis** | `analysis_{area}.md` | Generate insights for a specific area (churn, engagement, etc.) | Phase 5 (Analysis) |
| **Recommendations** | `recommendations.md` | Generate actionable recommendations from insights | Phase 7 (Recommendations) |

## How Prompts Are Assembled

### Exploration Prompt

```
base_context.md             ← Profile + previous discoveries
  +
exploration.md              ← Main exploration system prompt
  +
exploration_context.md      ← Category-specific context (e.g., match-3)
```

Variables substituted: `{{PROFILE}}`, `{{PREVIOUS_CONTEXT}}`, `{{SCHEMA_INFO}}`, `{{DATASET}}`, `{{FILTER}}`, `{{FILTER_CONTEXT}}`, `{{FILTER_RULE}}`, `{{ANALYSIS_AREAS}}`

### Analysis Prompt (per area)

```
base_context.md             ← Profile + previous discoveries
  +
analysis_{area}.md          ← Area-specific analysis prompt
```

Variables substituted: `{{PROFILE}}`, `{{PREVIOUS_CONTEXT}}`, `{{DATASET}}`, `{{TOTAL_QUERIES}}`, `{{QUERY_RESULTS}}`

### Recommendations Prompt

```
base_context.md             ← Profile + previous discoveries
  +
recommendations.md          ← Recommendation generation prompt
```

Variables substituted: `{{PROFILE}}`, `{{PREVIOUS_CONTEXT}}`, `{{DISCOVERY_DATE}}`, `{{INSIGHTS_SUMMARY}}`, `{{INSIGHTS_DATA}}`

## Template Variables

All variables use the `{{VARIABLE_NAME}}` syntax and are replaced by the agent at runtime.

### Base Context Variables

Used in `base_context.md`, which is prepended to every prompt.

| Variable | Source | Description | Example |
|----------|--------|-------------|---------|
| `{{PROFILE}}` | `project.profile` (MongoDB) | JSON-encoded project profile. Contains domain-specific fields (genre, mechanics, boosters, KPIs, etc.). | `{"basic_info": {"genre": "puzzle", "platforms": ["iOS", "Android"]}, "gameplay": {"core_mechanic": "match3"}, ...}` |
| `{{PREVIOUS_CONTEXT}}` | Last 5 discoveries + feedback | Previous insights (dedup instructions), liked/disliked insights with comments, previous recommendations. Built by the agent's `buildPreviousContext()` function. | `"This is discovery run #5. Last discovery: 2026-03-12.\n\n### Previously Found Insights\n- **High churn at level 42** [churn, critical] — 642 affected\n\n### User Feedback — Disliked\n- **Low engagement pattern** — user comment: 'not relevant to our game'\n..."` |

### Exploration Variables

Used in `exploration.md` and `exploration_context.md`.

| Variable | Source | Description | Example |
|----------|--------|-------------|---------|
| `{{SCHEMA_INFO}}` | Warehouse schema discovery | JSON-encoded table schemas (table names, column names/types, row counts). | `{"tables": [{"name": "users", "columns": [{"name": "user_id", "type": "STRING"}, {"name": "created_at", "type": "TIMESTAMP"}], "row_count": 50000}]}` |
| `{{DATASET}}` | `project.warehouse.datasets` | Comma-separated dataset/schema names. | `"analytics_data, features_prod"` |
| `{{FILTER}}` | `project.warehouse.filter_field/value` | SQL WHERE clause for multi-tenant filtering. Empty if no filter configured. | `"WHERE app_id = '68a42f378e3b227c8e41b0e5'"` |
| `{{FILTER_CONTEXT}}` | Same as filter | Human-readable explanation of the filter. | `"Data is filtered to app_id='68a42f378e3b227c8e41b0e5'. Always include this filter in your queries."` |
| `{{FILTER_RULE}}` | Same as filter | SQL rule for constructing queries with the filter. | `"Always include: WHERE app_id = '68a42f378e3b227c8e41b0e5' in all queries."` |
| `{{ANALYSIS_AREAS}}` | Domain pack areas | Description of analysis areas the agent should explore. | `"- Churn Risks: Players at risk of leaving\n- Engagement Patterns: Player behavior and session trends\n- Level Difficulty: Difficulty spikes and frustration points"` |

### Analysis Variables

Used in `analysis_{area}.md` (e.g., `analysis_churn.md`, `analysis_engagement.md`).

| Variable | Source | Description | Example |
|----------|--------|-------------|---------|
| `{{DATASET}}` | `project.warehouse.datasets` | Same as exploration. | `"analytics_data, features_prod"` |
| `{{TOTAL_QUERIES}}` | Exploration results count | Number of relevant exploration queries for this area. | `"6"` |
| `{{QUERY_RESULTS}}` | Exploration results (filtered) | JSON array of exploration step results relevant to this area (filtered by area keywords). Each entry includes step number, timestamp, AI thinking, SQL query, and result rows. | `[{"step": 1, "thinking": "Check retention...", "query": "SELECT ...", "query_result": [...], "row_count": 10}]` |

### Recommendations Variables

Used in `recommendations.md`.

| Variable | Source | Description | Example |
|----------|--------|-------------|---------|
| `{{DISCOVERY_DATE}}` | Current date | ISO date string of when this discovery run started. | `"2026-03-14"` |
| `{{INSIGHTS_SUMMARY}}` | All insights from analysis | Text summary with counts per area. | `"Total: 7 insights (churn: 3, engagement: 2, monetization: 2)"` |
| `{{INSIGHTS_DATA}}` | All insights from analysis | Full JSON array of all validated insights, including their IDs. The LLM uses these IDs to populate `related_insight_ids` on recommendations. | `[{"id": "churn-1", "name": "Day 0-to-Day 1 Drop", "severity": "critical", "affected_count": 8298, ...}]` |

## Per-Project Prompt Overrides

When a project is created, the domain pack's prompts are **copied** into the project's MongoDB document. Users can edit these copies via the dashboard's **Prompts** page without affecting other projects or the domain pack defaults.

**Important:** Editing the `.md` files in the domain pack on disk does NOT update existing projects. It only affects newly created projects. To update an existing project's prompts, edit them via the dashboard or update MongoDB directly.

### What Can Be Edited

- **Base Context** — Change what profile/context information is included
- **Exploration Prompt** — Change how the AI approaches data exploration
- **Recommendations Prompt** — Change how recommendations are generated
- **Analysis Prompts** — Change how each area analyzes data
- **Area Metadata** — Enable/disable areas, change names, edit keywords

### Adding Custom Analysis Areas

Via the dashboard Prompts page, you can add custom analysis areas that don't exist in the domain pack. For example, adding a "Social Features" area to a gaming project:

1. Click "Add Custom Area"
2. Set area ID (e.g., `social`), name, description, keywords
3. Write the analysis prompt (the `{{QUERY_RESULTS}}` variable is automatically available)
4. Save — the area appears in the run menu and analysis pipeline

Custom areas are stored per-project and merged with domain pack areas.

## Writing Good Prompts

### Analysis Prompts

Analysis prompts should:
- Tell the AI what patterns to look for
- Specify the exact JSON output format (with field types and examples)
- Include `source_steps` — instruct the AI to cite which exploration steps support each insight
- Set quality standards (minimum affected count, exact percentages, no estimates)

### Recommendations Prompts

Recommendation prompts should:
- Reference `related_insight_ids` — instruct the AI to link recommendations to specific insights
- Request specific, numbered action steps
- Include priority scale (P1 = critical, P5 = optional)
- Ask for measurable expected impact

### Base Context

The base context should:
- Include `{{PROFILE}}` so the AI knows about the specific product
- Include `{{PREVIOUS_CONTEXT}}` so the AI doesn't repeat findings
- Keep instructions clear and concise (this is prepended to every prompt)

## Next Steps

- [Prompt Variables Reference](../reference/prompt-variables.md) — Complete variable reference with all values
- [Customizing Prompts](../guides/customizing-prompts.md) — Step-by-step guide to editing prompts
- [Creating Domain Packs](../guides/creating-domain-packs.md) — Write prompts for a new domain
- [Discovery Lifecycle](discovery-lifecycle.md) — How prompts are used in each phase
