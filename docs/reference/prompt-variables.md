# Prompt Variables Reference

> **Version**: 0.1.0

Template variables in prompt files use the `{{VARIABLE_NAME}}` syntax. The agent replaces them with project-specific values at runtime before sending to the LLM.

## All Variables

| Variable | Used In | Replaced With | Type |
|----------|---------|---------------|------|
| `{{PROFILE}}` | `base_context.md` | JSON-encoded project profile | JSON string |
| `{{PREVIOUS_CONTEXT}}` | `base_context.md` | Previous discoveries + user feedback | Multi-line text |
| `{{SCHEMA_CATALOG}}` | `exploration.md` | Level-0 catalog: one line per table (name, column count, row count, keyword hints) | Plain text |
| `{{SCHEMA_RETRIEVED}}` | `exploration.md` | Level-1 details for top-K tables the retriever matched against the current task | Plain text |
| `{{SCHEMA_INFO}}` | `exploration.md` | **Deprecated alias.** Renders `{{SCHEMA_CATALOG}}\n\n{{SCHEMA_RETRIEVED}}` back-to-back so pre-v0.4 templates keep working. New templates should use the explicit variables. | Plain text |
| `{{DATASET}}` | `exploration.md`, `analysis_*.md` | Dataset/schema names | Comma-separated string |
| `{{FILTER}}` | `exploration.md` | SQL WHERE clause | SQL fragment |
| `{{FILTER_CONTEXT}}` | `exploration.md` | Human-readable filter description | Text |
| `{{FILTER_RULE}}` | `exploration.md` | SQL construction rule for the filter | Text |
| `{{ANALYSIS_AREAS}}` | `exploration.md` | List of analysis areas with descriptions | Multi-line text |
| `{{TOTAL_QUERIES}}` | `analysis_*.md` | Count of relevant exploration queries | Integer |
| `{{QUERY_RESULTS}}` | `analysis_*.md` | Exploration query results for this area | JSON array |
| `{{DISCOVERY_DATE}}` | `recommendations.md` | Current date (ISO format) | Date string |
| `{{INSIGHTS_SUMMARY}}` | `recommendations.md` | Text summary of insight counts | Text |
| `{{INSIGHTS_DATA}}` | `recommendations.md` | Full insight array with IDs | JSON array |

## Detailed Reference

### {{PROFILE}}

**Source:** `project.profile` from MongoDB

The project profile serialized as JSON. Contains domain-specific fields defined by the domain pack's profile schema.

**Example value:**
```json
{
  "basic_info": {
    "genre": "puzzle",
    "sub_genre": "match-3",
    "platforms": ["iOS", "Android"],
    "target_audience": "Adult Female 30-65+"
  },
  "gameplay": {
    "core_mechanic": "match3",
    "game_type": "level_based",
    "session_type": "short",
    "avg_session_duration_minutes": 6
  },
  "monetization": {
    "model": "freemium",
    "has_ads": true,
    "has_iap": true
  },
  "boosters": [
    {"name": "Magnet", "usage": "consumable", "starting_amount": 3, "can_purchase": true}
  ],
  "kpis": {
    "retention_d1_target": 40,
    "arpu_target": 0.50
  }
}
```

**If profile is empty:** Shows `"No project profile configured. Provide general analysis."`.

### {{PREVIOUS_CONTEXT}}

**Source:** Last 5 discoveries + all feedback for the project

Built by the agent's `buildPreviousContext()` function. Contains:

1. **Discovery count and date:** "This is discovery run #5. Last discovery: 2026-03-12."
2. **Previous insights:** Names, areas, severity, dates — with dedup instruction
3. **Disliked insights:** With user comments — "AVOID similar conclusions"
4. **Liked insights:** "MONITOR for changes"
5. **Previous recommendations:** "Don't repeat unless changed"

**Example value:**
```
This is discovery run #5. Last discovery: 2026-03-12.

### Previously Found Insights
These insights were already discovered. Do NOT repeat them unless the data has significantly changed.

- **Day 0-to-Day 1 Drop: 67% Never Return** [churn, critical] — 8298 affected (2026-03-11)
- **Level 11 Difficulty Cliff** [churn, high] — 642 affected (2026-03-12)

### User Feedback — Disliked Insights (AVOID)
- **Low engagement pattern** — user comment: "not relevant to our game"

### User Feedback — Liked Insights (MONITOR)
- **Day 0-to-Day 1 Drop: 67% Never Return**

### Previously Given Recommendations
Don't repeat these unless the situation has changed.
- P1: Reduce Level 11-15 Difficulty by 25% (difficulty)
```

**If first run:** Empty string (no previous context).

### {{SCHEMA_CATALOG}}

**Source:** Schema renderer, Level 0 (see `docs/guides/schema-indexing.md`)

Compact catalog of every table the project indexed in Qdrant.
One line per table: `name | Nc | Xk/M rows | keyword1, keyword2 | -> joins_to`.

**Example value:**
```
analytics_data.users       | 14c | 50K rows   | users, accounts       | -> sessions
analytics_data.sessions    |  9c | 500K rows  | sessions, activity    | -> users, events
analytics_data.events      | 22c | 4.2M rows  | events, clickstream
```

Always included in every exploration prompt (plan §5). Lets the LLM
see the names of tables it didn't retrieve in Level 1 and request
them on demand via the `inspect_table` tool.

### {{SCHEMA_RETRIEVED}}

**Source:** Schema renderer, Level 1 — per-project Qdrant retrieval
against the current task's keywords + domain-pack keywords.

Full column list + 3 sample rows for the top-K tables (K=40 by
default for tool-capable models, 60 for tool-disabled models;
override via project `schema_retrieval.top_k`).

**Example value:**
```
TABLE analytics_data.users (50K rows)
  columns:
    - user_id STRING NOT NULL [primary_key]
    - created_at TIMESTAMP NOT NULL [time]
    - country STRING NULL [dimension]
  sample rows:
    country=US, created_at=2026-01-15T12:00:00Z, user_id=u123
    country=TR, created_at=2026-02-03T09:30:00Z, user_id=u456
```

### {{SCHEMA_INFO}} (deprecated alias)

Renders `{{SCHEMA_CATALOG}}\n\n{{SCHEMA_RETRIEVED}}` back-to-back so
templates shipped before v0.4 still work. New templates should use
the explicit variables.

### {{DATASET}}

**Source:** `project.warehouse.datasets` array

Comma-separated dataset names (BigQuery) or schema names (Redshift).

**Example values:**
- BigQuery: `"analytics_data, features_prod"`
- Redshift: `"public"`

### {{FILTER}}

**Source:** `project.warehouse.filter_field` and `filter_value`

SQL WHERE clause for multi-tenant data filtering.

**Example value:** `"WHERE app_id = '68a42f378e3b227c8e41b0e5'"`

**If no filter configured:** Empty string.

### {{FILTER_CONTEXT}}

**Source:** Same as `{{FILTER}}`

Human-readable explanation of the filter for the AI.

**Example value:** `"Data is filtered to app_id='68a42f378e3b227c8e41b0e5'. Always include this filter in your queries."`

### {{FILTER_RULE}}

**Source:** Same as `{{FILTER}}`

SQL construction rule.

**Example value:** `"Always include: WHERE app_id = '68a42f378e3b227c8e41b0e5' in all queries."`

### {{ANALYSIS_AREAS}}

**Source:** Domain pack analysis areas

Formatted list of all analysis areas the agent should explore.

**Example value:**
```
- Churn Risks: Players at risk of leaving the game
- Engagement Patterns: Player behavior and session trends
- Monetization Opportunities: Revenue optimization and conversion opportunities
- Level Difficulty: Difficulty spikes and frustration points
- Booster Usage: Power-up usage patterns and purchase opportunities
```

### {{TOTAL_QUERIES}}

**Source:** Count of exploration queries relevant to this analysis area

**Example value:** `"6"`

### {{QUERY_RESULTS}}

**Source:** Exploration results filtered by area keywords

JSON array of exploration steps relevant to the current analysis area. Each entry includes:

**Example value:**
```json
[
  {
    "step": 1,
    "timestamp": "2026-03-14T10:30:05Z",
    "action": "query_data",
    "thinking": "Let me check retention rates by cohort...",
    "query": "SELECT cohort_date, day_1_retention FROM retention_cohorts WHERE app_id = '...' ORDER BY cohort_date DESC LIMIT 30",
    "query_result": [
      {"cohort_date": "2026-03-01", "day_1_retention": 33.2},
      {"cohort_date": "2026-02-28", "day_1_retention": 31.8}
    ],
    "row_count": 30,
    "execution_time_ms": 450
  },
  {
    "step": 3,
    "thinking": "Retention is declining. Let me look at session patterns...",
    "query": "SELECT user_id, total_sessions, days_active FROM ...",
    "row_count": 100
  }
]
```

### {{DISCOVERY_DATE}}

**Source:** Current date

**Example value:** `"2026-03-14"`

### {{INSIGHTS_SUMMARY}}

**Source:** All insights from the analysis phase

Text summary with counts per area.

**Example value:** `"Total: 7 insights (churn: 3, engagement: 2, monetization: 2)"`

### {{INSIGHTS_DATA}}

**Source:** All validated insights from the analysis phase

Full JSON array of all insights, including their IDs. The LLM uses insight IDs to populate `related_insight_ids` on recommendations.

**Example value:**
```json
[
  {
    "id": "churn-1",
    "analysis_area": "churn",
    "name": "Day 0-to-Day 1 Drop: 67% Never Return",
    "description": "67% of new players...",
    "severity": "critical",
    "affected_count": 8298,
    "risk_score": 0.67,
    "confidence": 0.85,
    "metrics": {"churn_rate": 0.67, "avg_sessions_before_churn": 1.2},
    "indicators": ["Only 33% return after Day 1", "Avg session: 4.2 minutes"],
    "source_steps": [1, 3, 5]
  }
]
```

## Variable Substitution Code

Variables are substituted in `services/agent/internal/discovery/orchestrator.go` using `strings.ReplaceAll()`. The substitution happens at runtime, just before sending prompts to the LLM.

## Next Steps

- [Prompts Concept](../concepts/prompts.md) — How prompts are assembled and overridden
- [Customizing Prompts](../guides/customizing-prompts.md) — Edit prompts and add custom areas
- [Creating Domain Packs](../guides/creating-domain-packs.md) — Write prompts for a new domain
