# Discovery Log Collections

> **Version**: 0.5.0
>
> **See also**: [agent-analysis-compaction.md](agent-analysis-compaction.md)
> bounds the per-area analysis prompt; [agent-on-demand-schema.md](agent-on-demand-schema.md)
> bounds the exploration system prompt. This doc bounds the persisted
> discovery document.

## Why

The agent stores a complete LLM dialog for traceability + fine-tuning:
the SQL of every exploration step, the full prompt + response of every
analysis area, every verification result, and the recommendation phase's
input/output. Through v0.4 these all sat as embedded arrays inside the
`discoveries` document:

```yaml
discoveries:
  _id: ObjectId(...)
  project_id: ...
  insights: [...]
  recommendations: [...]
  exploration_log: [ {step:1, llm_request, llm_response, query_result, ...}, ... ]
  analysis_log: [ {area_id, prompt, response, ...}, ... ]
  validation_log: [ ... ]
  recommendation_log: { ... }
```

A live customer run (Novo Nordisk, BigQuery, 97-step exploration with
the new Layer 3 verifier loop) blew past Mongo's 16MB-per-document
limit on the first save attempt:

```
Failed to save discovery result: an inserted document is too large
```

The fix: lift each log type into its own collection, one row per step
/ area / validation, keyed by the parent discovery's `_id`. The same
treatment was needed on the `discovery_runs` document: `StatusReporter`
$push'd individual `RunStep`s into an embedded `steps` array, which
ran into the same ceiling under streaming live-status updates.

## Collections

| Collection                          | Row | Indexed by                          | Owner |
|-------------------------------------|-----|-------------------------------------|-------|
| `discoveries`                       | one per discovery (no log fields) | `(project_id, discovery_date)` | agent writes / api reads |
| `discovery_exploration_steps`       | one per `ExplorationStep`         | `(discovery_id, step)` + `(project_id, created_at)` | agent writes / api reads |
| `discovery_analysis_steps`          | one per `AnalysisStep` (one per area) | `(discovery_id, run_at)` | agent writes / api reads |
| `discovery_validation_results`      | one per `ValidationResult`        | `(discovery_id, validated_at)` | agent writes / api reads |
| `discovery_recommendation_log`      | one per discovery (singular)      | `(discovery_id)` unique | agent writes / api reads |
| `discovery_runs`                    | one per run (no `steps` field)    | `(project_id, started_at)` | agent writes / api reads |
| `discovery_run_steps`               | one per live `RunStep`            | `(run_id, _id)`       | agent writes / api reads |

Every per-step / per-area / per-result row carries `project_id`,
`discovery_id` (or `run_id`), and `created_at` so cross-collection
queries (per-project rollups, retention sweeps) work without a join.

## Write path

The agent owns the writes. After the parent `DiscoveryResult` is saved
and its `_id` is known, the orchestrator persists the four log types to
their split collections. Failures are logged but do **not** roll back
the discovery — the parent doc + structured outputs (insights,
recommendations, summary) are already on disk, and re-deriving the LLM
dialog from a partial save would be worse than losing it.

```go
// services/agent/internal/discovery/orchestrator.go
result := &models.DiscoveryResult{ /* no log fields */ }
if err := o.discoveryRepo.Save(ctx, result); err != nil {
    return nil, fmt.Errorf("failed to save discovery result: %w", err)
}
o.discoveryLogRepo.SaveExplorationSteps(ctx, projectID, result.ID, runID, explorationResult.Steps)
o.discoveryLogRepo.SaveAnalysisSteps  (ctx, projectID, result.ID, runID, analysisLog)
o.discoveryLogRepo.SaveValidationResults(ctx, projectID, result.ID, runID, allValidation)
o.discoveryLogRepo.SaveRecommendationLog(ctx, projectID, result.ID, runID, recStep)
```

The live-status path (StatusReporter) inserts each `RunStep` directly
into `discovery_run_steps`:

```go
// services/agent/internal/discovery/status.go
func (s *StatusReporter) AddStep(ctx context.Context, step models.RunStep) {
    if !s.enabled() { return }
    s.runStepRepo.AddStep(ctx, s.runID, s.projectID, step) // single InsertOne, no $push
}
```

## Read path

The dashboard polls dedicated paginated endpoints rather than dragging
the parent doc:

```
GET /api/v1/discoveries/{id}/exploration-steps[?limit=N]
GET /api/v1/discoveries/{id}/analysis-steps
GET /api/v1/discoveries/{id}/validation-results
GET /api/v1/discoveries/{id}/recommendation-log
GET /api/v1/runs/{runId}/steps[?since=<id>&limit=N]
```

The `since` cursor on `/runs/{runId}/steps` is the opaque `id` field
of the last row the dashboard has rendered (the document's ObjectID
rendered as a hex string). On the next poll, the server filters
`_id > ObjectID(since)` and sorts ascending.

ObjectID, not timestamp: BSON datetimes are millisecond-precision, so
two `AddStep` calls inside the same ms produce two rows with the same
`timestamp`. A `timestamp > since` cursor would silently drop any
later row that fell in that ms — the dashboard's live panel would
permanently lose step rows. ObjectIDs are monotonic per writer process
(timestamp + counter), so the `_id`-based cursor is collision-free for
the agent's single-process-per-run model. The dashboard treats `id`
as opaque and just echoes it back. A malformed cursor surfaces as
`database.ErrInvalidCursor` and the handler maps it to `400`.

## Migration

DecisionBox is pre-1.0; no backward-compatibility shim was carried.
Discoveries written before this change retain their embedded log
fields in Mongo (orphan data — readers ignore them). New runs use the
split collections from the moment the upgraded image rolls out. The
auto-rebuild tooling in `services/api/internal/schemaindex/migration.go`
covers the project-level schema-index migration; nothing here moves old
log data into the new collections — the log dialog from pre-split runs
isn't accessible via the new dashboard endpoints, only by direct
collection queries on the legacy `discoveries` document.

## Indexes

`DiscoveryLogRepository.EnsureIndexes` and `RunStepRepository.EnsureIndexes`
create indexes on agent startup. Idempotent — Mongo silently no-ops when
an index already exists. The recommendation-log index is unique on
`discovery_id` (one row per discovery).

## Why agent-side `,inline`

Each split-log document carries a small wrapper (project_id,
discovery_id, run_id, created_at) plus the existing model struct
embedded via `bson:",inline"`. This keeps the existing per-field BSON
tags stable — `step`, `action`, `query`, `llm_request` survive
unchanged in the wire format — so dashboards and downstream consumers
(fine-tuning exporters, audit log scrapers) don't re-learn the schema.

## Tests

- `services/agent/internal/database/discovery_log_repo_integration_test.go`
  (`//go:build integration`) — round-trips each log type against a
  Mongo testcontainer, plus per-discovery isolation + empty-input
  no-op contracts.
- `services/agent/internal/database/run_step_repo_integration_test.go`
  (`//go:build integration`) — round-trips run steps against a Mongo
  testcontainer, including the `since` cursor and limit clamping.
- `services/api/internal/handler/discoveries_split_logs_test.go` —
  unit-level handler tests with mock repos covering happy path, nil
  repo, missing IDs, invalid since cursor, limit clamping, and repo
  errors for every new endpoint.
- Existing JSON/BSON round-trip tests now assert the legacy log fields
  are **absent** from the wire format (regression guard against future
  re-additions).
