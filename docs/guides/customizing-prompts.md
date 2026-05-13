# Customizing Prompts

> **Version**: 0.1.0

Every prompt in DecisionBox can be customized per-project. This lets you fine-tune how the AI reasons about your specific data without modifying the domain pack.

## How It Works

When a project is created, the domain pack's default prompts are **copied** into the project. These copies are independent — editing them only affects that project. The domain pack defaults are not changed.

**Important:** Updating the `.md` files in the domain pack on disk does NOT update existing projects. It only affects newly created projects. To update an existing project's prompts, edit them via the dashboard or the API.

## Platform-Enforced Claim Discipline Rules

In addition to the pack-supplied and project-supplied prompts above, DecisionBox appends a set of **platform-enforced claim-discipline rules** to every writer and verifier prompt at runtime. These rules are NOT part of any pack file and cannot be edited away through the dashboard or the API — they are applied in code (`services/agent/internal/discipline/rules.go`) immediately before the prompt is sent to the LLM.

The rules cover:

- **Scope-binding superlatives** — every "highest" / "lowest" / "all-time" claim must name the exact data window it was derived from.
- **No claim beyond the query window** — if your widest query covers 11 weeks, you may not speak of "last year" or "ever".
- **Re-ranking from raw rows** — when writing a top-N claim, the LLM must walk the actual result rows, not its earlier prose summaries.
- **Addressing counter-evidence** — silent dismissal of contradicting rows as "outlier" or "holiday" is forbidden.
- **Cross-field self-consistency** — `description`, `indicators`, `metrics`, and `name` must agree on quantifier and window.
- **Citing the step for every number** — every quantitative figure must trace to a row in `source_steps`.
- **Partial-period hygiene** — partial periods may not enter a ranking unless normalized.
- **Non-dramatic prose** — describe findings via numbers; encode importance in the structured `severity` field, not in prose adjectives.

The verifier prompt additionally carries V1–V4 rules that test the headline claim against the warehouse (not just the affected count).

**Implications when customizing prompts:**

- The prompt you see in the dashboard editor is NOT the full prompt the LLM receives. The platform appends the discipline rules below your content before sending.
- These rules apply across **all output languages**. The rules themselves explicitly tell the LLM to apply the constraints to whatever language the project is configured to emit insights in — the English examples in the rule text are illustrations of the principle, not the principle itself.
- You do not need to (and should not) re-implement these rules in your pack prompts. Editing your `base_context.md` to add "no dramatic words" is redundant; the platform already enforces it.
- If you want to inspect or update the canonical rule text, the source of truth is `services/agent/internal/discipline/rules.go` in the platform repository.

## Editing Prompts via Dashboard

Go to your project's **Prompts** page (left sidebar → Prompts). You'll see tabs:

| Tab | Prompt | Purpose |
|-----|--------|---------|
| **Base Context** | `base_context.md` | Profile + previous context. Prepended to ALL prompts. |
| **Exploration** | `exploration.md` | System prompt for autonomous data exploration. |
| **Recommendations** | `recommendations.md` | How to generate recommendations from insights. |
| **Churn Risks** | `analysis_churn.md` | How to analyze churn patterns (area-specific). |
| **Engagement** | `analysis_engagement.md` | How to analyze engagement patterns. |
| *...per area* | `analysis_{area}.md` | One tab per analysis area. |

Each tab has a markdown editor with syntax highlighting. Edit, then click **Save All**.

## Editing Prompts via API

```bash
# Get current prompts
curl http://localhost:8080/api/v1/projects/{id}/prompts

# Update a specific prompt
curl -X PUT http://localhost:8080/api/v1/projects/{id}/prompts \
  -H "Content-Type: application/json" \
  -d '{
    "base_context": "## Project Profile\n\n{{PROFILE}}\n\nCustom instructions here...",
    "analysis_areas": {
      "churn": {
        "prompt": "# Updated Churn Analysis\n\n..."
      }
    }
  }'
```

Partial updates are supported — only include the fields you want to change.

## Adding Custom Analysis Areas

You can add analysis areas that don't exist in the domain pack.

### Via Dashboard

1. Go to **Prompts** page
2. Click **Add Custom Area**
3. Fill in:
   - **Area ID**: Lowercase, no spaces (e.g., `social_features`)
   - **Display Name**: Human-readable (e.g., `Social Features`)
   - **Description**: What this area analyzes
   - **Keywords**: Comma-separated search terms (used to filter exploration results)
4. Write the analysis prompt. Use `{{QUERY_RESULTS}}` to receive exploration data.
5. Save

### Via API

```bash
curl -X PUT http://localhost:8080/api/v1/projects/{id}/prompts \
  -H "Content-Type: application/json" \
  -d '{
    "analysis_areas": {
      "social_features": {
        "name": "Social Features",
        "description": "Analyze social interaction patterns",
        "keywords": ["friend", "guild", "clan", "social", "invite", "share"],
        "prompt": "# Social Features Analysis\n\nAnalyze social feature usage patterns...\n\n## Query Results\n\n{{QUERY_RESULTS}}",
        "is_custom": true,
        "enabled": true,
        "priority": 10
      }
    }
  }'
```

Custom areas are merged with domain pack areas at runtime.

## Enabling/Disabling Areas

You can disable analysis areas without deleting them:

```bash
curl -X PUT http://localhost:8080/api/v1/projects/{id}/prompts \
  -H "Content-Type: application/json" \
  -d '{
    "analysis_areas": {
      "monetization": {"enabled": false}
    }
  }'
```

Disabled areas are skipped during discovery runs but preserved in the project config.

## Template Variables

When writing prompts, you can use template variables that the agent replaces at runtime. See [Prompt Variables Reference](../reference/prompt-variables.md) for the complete list.

The most commonly used:

| Variable | Available In | Description |
|----------|-------------|-------------|
| `{{PROFILE}}` | base_context.md | Project profile as JSON |
| `{{PREVIOUS_CONTEXT}}` | base_context.md | Previous insights + feedback |
| `{{QUERY_RESULTS}}` | analysis_*.md | Exploration data for this area |
| `{{DATASET}}` | exploration.md, analysis_*.md | Dataset names |
| `{{DIALECT}}` | any prompt | Warehouse SQL dialect name (e.g. `"BigQuery Standard SQL"`, `"Microsoft SQL Server T-SQL …"`) returned by the connected provider |
| `{{REF:tablename}}` | any prompt | Dialect-correct fully-qualified table reference (`` `ds`.`tablename` `` on BigQuery, `[ds].[tablename]` on SQL Server, `"ds"."tablename"` on PostgreSQL/Redshift/Snowflake). Use this for every SQL table reference in example queries; the orchestrator renders it with the connected warehouse's native quoting. |
| `{{INSIGHTS_DATA}}` | recommendations.md | All insights as JSON (for linking) |

## Tips for Better Prompts

### Exploration Prompt
- Be clear about what data to explore first
- Mention specific table/column name patterns if you know them
- Include examples of good queries for your data

### Analysis Prompts
- Specify the exact JSON output format
- Include `source_steps` instruction — the AI should cite which queries support each insight
- Set minimum thresholds (e.g., "only report patterns affecting 50+ users")
- List specific metrics to calculate

### Recommendations Prompt
- Include `related_insight_ids` instruction (copy IDs from input data)
- Require numbered action steps
- Specify priority scale (P1 = critical through P5 = optional)
- Ask for quantified expected impact

### Base Context
- Keep it concise — this is prepended to every prompt
- Always include `{{PROFILE}}` and `{{PREVIOUS_CONTEXT}}`
- Add project-specific rules ("Our definition of churn is 14 days inactive")

## Next Steps

- [Prompt Variables Reference](../reference/prompt-variables.md) — All template variables
- [Creating Domain Packs](creating-domain-packs.md) — Write prompts for a new domain
- [Project Profiles](project-profiles.md) — Improve insight quality with context
