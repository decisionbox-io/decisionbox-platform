# API Reference

> **Version**: 0.1.0
>
> Base URL: `http://localhost:8080` (direct) or `http://localhost:3000/api` (via dashboard proxy)
>
> All endpoints return JSON. Error responses use `{"error": "message"}`.

## Health

### GET /health

Liveness probe. Returns immediately, does not check dependencies.

```bash
curl http://localhost:8080/health
```

```json
{"status": "ok"}
```

### GET /health/ready

Readiness probe. Checks MongoDB connectivity.

```bash
curl http://localhost:8080/health/ready
```

```json
{"status": "ok", "checks": {"mongodb": "ok"}}
```

Returns `503` if MongoDB is unreachable.

---

## Providers

### GET /api/v1/providers/llm

List registered LLM providers with metadata and config fields.

```bash
curl http://localhost:8080/api/v1/providers/llm
```

```json
{
  "data": [
    {
      "id": "claude",
      "name": "Claude (Anthropic)",
      "description": "Anthropic Claude API - direct access",
      "config_fields": [
        {"key": "api_key", "label": "API Key", "required": true, "type": "string", "placeholder": "sk-ant-..."},
        {"key": "model", "label": "Model", "required": true, "type": "string", "default": "claude-sonnet-4-20250514"}
      ],
      "default_pricing": {
        "claude-sonnet-4": {"input_per_million": 3.0, "output_per_million": 15.0},
        "claude-opus-4": {"input_per_million": 15.0, "output_per_million": 75.0}
      }
    }
  ]
}
```

### GET /api/v1/providers/warehouse

List registered warehouse providers with metadata and config fields.

```bash
curl http://localhost:8080/api/v1/providers/warehouse
```

```json
{
  "data": [
    {
      "id": "bigquery",
      "name": "Google BigQuery",
      "description": "Google Cloud BigQuery data warehouse",
      "config_fields": [
        {"key": "project_id", "label": "GCP Project ID", "required": true, "type": "string"},
        {"key": "location", "label": "Location", "type": "string", "default": "US"},
        {"key": "dataset", "label": "Default Dataset", "type": "string"}
      ]
    }
  ]
}
```

---

## Domains

### GET /api/v1/domains

List available domains with their categories.

```bash
curl http://localhost:8080/api/v1/domains
```

```json
{
  "data": [
    {
      "id": "gaming",
      "categories": [
        {"id": "match3", "name": "Match-3", "description": "Puzzle games with match-3 mechanics (e.g., Candy Crush, Homescapes)"},
        {"id": "idle", "name": "Idle / Incremental", "description": "Games focused on resource accumulation, prestige cycles, and offline progression"},
        {"id": "casual", "name": "Casual / Hyper-Casual", "description": "Simple, accessible games with short sessions and broad appeal, often ad-monetized"}
      ]
    },
    {
      "id": "social",
      "categories": [
        {"id": "content_sharing", "name": "Content Sharing", "description": "Platforms focused on creating and sharing content — photos, videos, stories"}
      ]
    }
  ]
}
```

### GET /api/v1/domains/{domain}/categories/{category}/schema

Get profile JSON Schema for a domain/category. Used by the dashboard to render dynamic forms.

```bash
curl http://localhost:8080/api/v1/domains/gaming/categories/match3/schema
```

Returns a JSON Schema object defining the profile fields (basic_info, gameplay, monetization, boosters, etc.).

### GET /api/v1/domains/{domain}/categories/{category}/areas

Get analysis areas for a domain/category.

```bash
curl http://localhost:8080/api/v1/domains/gaming/categories/match3/areas
```

```json
{
  "data": [
    {"id": "churn", "name": "Churn Risks", "description": "Players at risk of leaving the game", "keywords": ["churn", "retention", "cohort"], "is_base": true, "priority": 1},
    {"id": "engagement", "name": "Engagement Patterns", "is_base": true, "priority": 2},
    {"id": "monetization", "name": "Monetization Opportunities", "is_base": true, "priority": 3},
    {"id": "levels", "name": "Level Difficulty", "is_base": false, "priority": 4},
    {"id": "boosters", "name": "Booster Usage", "is_base": false, "priority": 5}
  ]
}
```

---

## Projects

### POST /api/v1/projects

Create a new project.

```bash
curl -X POST http://localhost:8080/api/v1/projects \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Puzzle Quest Analytics",
    "description": "Match-3 puzzle game analytics",
    "domain": "gaming",
    "category": "match3",
    "warehouse": {
      "provider": "bigquery",
      "project_id": "my-gcp-project",
      "datasets": ["analytics_data", "features"],
      "location": "US",
      "filter_field": "app_id",
      "filter_value": "my-app-123"
    },
    "llm": {
      "provider": "claude",
      "model": "claude-sonnet-4-20250514",
      "config": {}
    },
    "schedule": {
      "enabled": false,
      "cron_expr": "0 2 * * *",
      "max_steps": 100
    }
  }'
```

```json
{
  "data": {
    "id": "507f1f77bcf86cd799439011",
    "name": "Puzzle Quest Analytics",
    "domain": "gaming",
    "category": "match3",
    "created_at": "2026-03-14T10:00:00Z"
  }
}
```

Domain pack prompts are automatically seeded into the project on creation.

### GET /api/v1/projects

List all projects.

```bash
curl http://localhost:8080/api/v1/projects
```

```json
{
  "data": [
    {
      "id": "507f1f77bcf86cd799439011",
      "name": "Puzzle Quest Analytics",
      "domain": "gaming",
      "category": "match3",
      "status": "active",
      "last_run_at": "2026-03-14T10:30:00Z",
      "last_run_status": "completed"
    }
  ]
}
```

### GET /api/v1/projects/{id}

Get a project with full configuration.

```bash
curl http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011
```

Returns the complete project object including warehouse, LLM, schedule, and profile configuration.

### PUT /api/v1/projects/{id}

Update a project. Supports partial updates — only fields present in the request body are updated. Prompts and profile are preserved if not included.

```bash
curl -X PUT http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011 \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Puzzle Quest (Updated)",
    "schedule": {"enabled": true, "cron_expr": "0 3 * * *", "max_steps": 150}
  }'
```

### DELETE /api/v1/projects/{id}

Delete a project and all its data (discoveries, feedback, secrets, prompts).

```bash
curl -X DELETE http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011
```

---

## Prompts

### GET /api/v1/projects/{id}/prompts

Get the project's editable prompts. These are copies of the domain pack defaults, customizable per-project.

```bash
curl http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/prompts
```

```json
{
  "data": {
    "exploration": "# Gaming Analytics Discovery Agent\n\nYou are an autonomous...",
    "recommendations": "# Generate Actionable Recommendations\n\n...",
    "base_context": "## Project Profile\n\n{{PROFILE}}\n\n...",
    "analysis_areas": {
      "churn": {
        "name": "Churn Risks",
        "description": "Players at risk of leaving the game",
        "keywords": ["churn", "retention"],
        "prompt": "# Churn Pattern Analysis\n\n...",
        "is_base": true,
        "enabled": true,
        "priority": 1
      }
    }
  }
}
```

### PUT /api/v1/projects/{id}/prompts

Update prompts. Any field can be updated independently.

```bash
curl -X PUT http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/prompts \
  -H "Content-Type: application/json" \
  -d '{
    "base_context": "## Updated Project Profile\n\n{{PROFILE}}\n\n...",
    "analysis_areas": {
      "churn": {
        "name": "Churn Risks",
        "prompt": "# Updated Churn Analysis\n\n...",
        "enabled": true
      }
    }
  }'
```

---

## Discoveries

### POST /api/v1/projects/{id}/discover

Trigger a discovery run. Spawns the agent as a subprocess or K8s Job.

```bash
# Run all areas with default steps
curl -X POST http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/discover

# Run specific areas with custom step count
curl -X POST http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/discover \
  -H "Content-Type: application/json" \
  -d '{"areas": ["churn", "monetization"], "max_steps": 50}'
```

```json
{
  "status": "started",
  "run_id": "507f1f77bcf86cd799439012",
  "message": "Discovery agent started"
}
```

Returns `409 Conflict` if a discovery is already running for this project.

### GET /api/v1/projects/{id}/discoveries

List all discoveries for a project (newest first). Excludes heavy log fields for performance.

```bash
curl http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/discoveries
```

```json
{
  "data": [
    {
      "id": "507f1f77bcf86cd799439013",
      "project_id": "507f1f77bcf86cd799439011",
      "run_type": "full",
      "discovery_date": "2026-03-14T10:30:00Z",
      "total_steps": 42,
      "duration": 480000000000,
      "insights": [...],
      "recommendations": [...],
      "summary": {"total_insights": 7, "total_recommendations": 5, "queries_executed": 42}
    }
  ]
}
```

### GET /api/v1/discoveries/{id}

Get a single discovery with full data (including exploration log, analysis log, validation log).

```bash
curl http://localhost:8080/api/v1/discoveries/507f1f77bcf86cd799439013
```

Returns the complete `DiscoveryResult` including all logs.

### GET /api/v1/projects/{id}/discoveries/latest

Get the most recent discovery for a project.

### GET /api/v1/projects/{id}/discoveries/{date}

Get a discovery by date (format: `YYYY-MM-DD`).

### GET /api/v1/projects/{id}/status

Get the current discovery status for a project (running, completed, or null).

```bash
curl http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/status
```

```json
{
  "data": {
    "run": {
      "id": "507f1f77bcf86cd799439012",
      "status": "running",
      "phase": "exploration",
      "progress": 45,
      "total_queries": 22,
      "insights_found": 3,
      "steps": [...]
    }
  }
}
```

---

## Runs

### GET /api/v1/runs/{runId}

Get live discovery run status with step-by-step progress.

```bash
curl http://localhost:8080/api/v1/runs/507f1f77bcf86cd799439012
```

```json
{
  "data": {
    "id": "507f1f77bcf86cd799439012",
    "project_id": "507f1f77bcf86cd799439011",
    "status": "running",
    "phase": "exploration",
    "phase_detail": "Step 22/100",
    "progress": 45,
    "started_at": "2026-03-14T10:30:00Z",
    "updated_at": "2026-03-14T10:35:00Z",
    "steps": [
      {
        "phase": "exploration",
        "step_num": 1,
        "timestamp": "2026-03-14T10:30:05Z",
        "type": "query",
        "message": "Checking retention rates...",
        "llm_thinking": "Let me start by looking at retention cohorts...",
        "query": "SELECT cohort_date, day_1_retention FROM ...",
        "row_count": 30,
        "query_time_ms": 450
      }
    ],
    "total_queries": 22,
    "successful_queries": 21,
    "failed_queries": 1,
    "insights_found": 3
  }
}
```

### DELETE /api/v1/runs/{runId}

Cancel a running discovery. Kills the agent process and updates the run status.

```bash
curl -X DELETE http://localhost:8080/api/v1/runs/507f1f77bcf86cd799439012
```

```json
{"status": "cancelled"}
```

---

## Feedback

### POST /api/v1/discoveries/{runId}/feedback

Submit feedback on an insight, recommendation, or exploration step.

```bash
curl -X POST http://localhost:8080/api/v1/discoveries/507f1f77bcf86cd799439013/feedback \
  -H "Content-Type: application/json" \
  -d '{
    "target_type": "insight",
    "target_id": "churn-1",
    "rating": "like"
  }'
```

```bash
# Dislike with comment
curl -X POST http://localhost:8080/api/v1/discoveries/507f1f77bcf86cd799439013/feedback \
  -H "Content-Type: application/json" \
  -d '{
    "target_type": "insight",
    "target_id": "churn-2",
    "rating": "dislike",
    "comment": "This metric definition is wrong for our game"
  }'
```

| Field | Required | Values | Description |
|-------|----------|--------|-------------|
| `target_type` | Yes | `insight`, `recommendation`, `exploration_step` | What is being rated |
| `target_id` | Yes | string | ID of the target (insight ID, recommendation index, step number) |
| `rating` | Yes | `like`, `dislike` | The rating |
| `comment` | No | string | Optional comment (typically with dislikes) |

Feedback is upserted — one rating per (discovery, target_type, target_id). Submitting again replaces the previous rating.

### GET /api/v1/discoveries/{runId}/feedback

List all feedback for a discovery.

```bash
curl http://localhost:8080/api/v1/discoveries/507f1f77bcf86cd799439013/feedback
```

### DELETE /api/v1/feedback/{id}

Delete a feedback entry.

```bash
curl -X DELETE http://localhost:8080/api/v1/feedback/507f1f77bcf86cd799439014
```

---

## Pricing

### GET /api/v1/pricing

Get current LLM and warehouse pricing configuration. Auto-seeded from provider defaults on first startup.

```bash
curl http://localhost:8080/api/v1/pricing
```

### PUT /api/v1/pricing

Update pricing (e.g., if your negotiated rates differ from defaults).

```bash
curl -X PUT http://localhost:8080/api/v1/pricing \
  -H "Content-Type: application/json" \
  -d '{
    "llm": {
      "claude-sonnet-4": {"input_per_million": 2.5, "output_per_million": 12.0}
    }
  }'
```

---

## Cost Estimation

### POST /api/v1/projects/{id}/discover/estimate

Estimate the cost of a discovery run without executing it. Spawns the agent with `--estimate` flag.

```bash
curl -X POST http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/discover/estimate \
  -H "Content-Type: application/json" \
  -d '{"max_steps": 100}'
```

```json
{
  "data": {
    "llm": {
      "provider": "claude",
      "model": "claude-sonnet-4-20250514",
      "estimated_input_tokens": 250000,
      "estimated_output_tokens": 50000,
      "cost_usd": 0.825
    },
    "warehouse": {
      "provider": "bigquery",
      "estimated_queries": 100,
      "estimated_bytes_scanned": 5368709120,
      "cost_usd": 0.0375
    },
    "total_cost_usd": 0.8625
  }
}
```

---

## Connection Testing

### POST /api/v1/projects/{id}/test/warehouse

Test the warehouse connection for a project.
Spawns the agent with `--test-connection warehouse` to validate using the agent's IAM context.

```bash
curl -X POST http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/test/warehouse
```

```json
{
  "data": {
    "success": true,
    "provider": "bigquery",
    "datasets": ["events_prod"]
  }
}
```

On failure:

```json
{
  "data": {
    "success": false,
    "error": "bigquery: cannot access dataset events_prod: googleapi: Error 403: Access Denied"
  }
}
```

### POST /api/v1/projects/{id}/test/llm

Test the LLM provider connection for a project.
Spawns the agent with `--test-connection llm` to validate credentials and model access.

```bash
curl -X POST http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/test/llm
```

```json
{
  "data": {
    "success": true,
    "provider": "claude",
    "model": "claude-sonnet-4-20250514"
  }
}
```

---

## Secrets

### PUT /api/v1/projects/{id}/secrets/{key}

Create or update a per-project secret. The value is encrypted at rest.

```bash
# Set LLM API key
curl -X PUT http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/secrets/llm-api-key \
  -H "Content-Type: application/json" \
  -d '{"value": "sk-ant-api03-..."}'
```

### GET /api/v1/projects/{id}/secrets

List secrets for a project. Returns **masked values only** — full values are never exposed via the API.

```bash
curl http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/secrets
```

```json
{
  "data": [
    {
      "key": "llm-api-key",
      "masked": "sk-ant***DwAA",
      "updated_at": "2026-03-14T10:00:00Z"
    }
  ]
}
```

**Note:** There is no DELETE endpoint for secrets. Secrets are removed manually via cloud console, CLI, or direct database access. This is intentional to prevent accidental deletion.

---

## Error Responses

All error responses follow the same format:

```json
{"error": "descriptive error message"}
```

Common HTTP status codes:

| Status | Meaning |
|--------|---------|
| `200` | Success |
| `201` | Created |
| `202` | Accepted (async operation started) |
| `400` | Bad request (invalid input) |
| `404` | Not found |
| `409` | Conflict (e.g., discovery already running) |
| `500` | Internal server error |

## Next Steps

- [Configuration Reference](configuration.md) — All environment variables
- [CLI Reference](cli.md) — Agent command-line flags
- [Data Models](data-models.md) — Insight, Recommendation, Discovery models
