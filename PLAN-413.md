# Plan — #413: persistent per-datasource schema-index history + status

## 1. Problem

After editing a datasource and re-indexing, there is **no durable record** of what
happened per datasource. Today:

- Status is **project-level** (`Project.SchemaIndexStatus`:
  `pending_indexing` / `indexing` / `ready` / `failed` / `cancelled` /
  `needs_reindex`), even though indexing runs **per datasource** —
  `services/agent/agentserver/index_schema.go` iterates `warehousesToIndex(project)`
  and calls `indexWarehouse(…)` once per `WarehouseConfig`, each scoped to a
  `WarehouseID`.
- The live counters (`tables_total` / `tables_done`, `phase`, `input_tokens` /
  `output_tokens`) live in the single-doc-per-project `project_schema_index_progress`
  collection, which is **`Reset()` on every run** (`SchemaIndexProgressRepository.Reset`
  in both `services/api/database/schema_index_progress.go` and
  `services/agent/internal/database/schema_index_progress_repo.go`, and again at the
  top of `SchemaIndexer.BuildIndex`). It is **live-only**, overwritten on the next
  run, and never per datasource.
- On the project page the panel is mounted with `hideWhenReady`
  (`ui/dashboard/src/app/projects/[id]/page.tsx:345`), so a **successful re-index
  shows nothing** afterwards — only the transient toast.

So you cannot answer *"how many objects were indexed from which datasource, when,
and did it succeed?"* There is no history and no audit trail.

`SchemaIndexer.BuildIndex` already returns a per-datasource `discovery.Stats`
(`Tables`, `Dropped`, `BlurbTokensIn`, `BlurbTokensOut`, `Duration`), and
`index_schema.go` already logs it per warehouse. The change is to **stamp that
result as a durable per-(datasource × run) record on completion** (success **or**
failure) instead of discarding it, then expose it for the dashboard.

## 2. Approach (overview)

Add a durable, append-only **`project_schema_index_runs`** collection — one document
per `(datasource × index run)`, **stamped by the agent when each datasource finishes**
(ready or failed). The agent is the only place that knows the per-datasource outcome
and stats; the API's worker (`services/api/internal/schemaindex/worker.go`) only knows
the project-level outcome, so stamping stays in the agent.

Then:

- **API** gains a read-only list endpoint
  `GET /api/v1/projects/{id}/schema-index/runs?datasource_id=…&limit=…`.
- **Dashboard** surfaces it in two places: a persistent per-datasource status
  roll-up on the project page (replacing `hideWhenReady`), and a per-datasource
  "Indexing" section with an expandable history table on the Data Warehouse
  settings panel.

Naming note for review: the issue calls the collection `schema_index_runs`. The
existing sibling collections are `project_schema_index_progress` and
`project_schema_index_logs`, so this plan proposes the family-consistent
**`project_schema_index_runs`**. Flag if you'd rather keep the literal
`schema_index_runs`.

## 3. Data model

New document, persisted durably (no TTL — this is the audit record; the progress
and logs collections keep their 7-day TTLs because they're live/debug data).

```
project_schema_index_runs
{
  project_id,        // string
  datasource_id,     // warehouse id ("default" for legacy/primary)
  datasource_name,   // WarehouseConfig.Label, falling back to provider, then id
  run_id,            // the agent run id (timestamp id minted by the API worker)
  kind,              // "tables" today — generic, extensible to other object kinds
  objects_indexed,   // points upserted into Qdrant for this datasource (Stats.Tables)
  blurbs_generated,  // successful blurbs for this datasource (new Stats.Blurbs)
  status,            // "ready" | "failed"
  error,             // failure reason (empty on success)
  phase_durations,   // map[string]int64 — phase name → milliseconds
  tokens_in,         // blurb-LLM input tokens (Stats.BlurbTokensIn)
  tokens_out,        // blurb-LLM output tokens (Stats.BlurbTokensOut)
  started_at,        // when this datasource's BuildIndex began
  finished_at        // when it finished (success or failure)
}
```

`kind` + `objects_indexed` are deliberately generic (per the issue's non-goals) so a
future non-table object kind slots in without a schema change. `phase_durations` is a
map rather than fixed columns for the same reason.

New Go model `SchemaIndexRun`, mirrored in both modules (same pattern as
`SchemaIndexProgress` / `BlurbLLMConfig`, which are defined in both
`services/api/models/schema_index.go` and
`services/agent/internal/models/schema_index.go`):

```go
type SchemaIndexRun struct {
    ProjectID       string           `bson:"project_id" json:"project_id"`
    DatasourceID    string           `bson:"datasource_id" json:"datasource_id"`
    DatasourceName  string           `bson:"datasource_name,omitempty" json:"datasource_name,omitempty"`
    RunID           string           `bson:"run_id" json:"run_id"`
    Kind            string           `bson:"kind" json:"kind"`
    ObjectsIndexed  int              `bson:"objects_indexed" json:"objects_indexed"`
    BlurbsGenerated int              `bson:"blurbs_generated" json:"blurbs_generated"`
    Status          string           `bson:"status" json:"status"`
    Error           string           `bson:"error,omitempty" json:"error,omitempty"`
    PhaseDurations  map[string]int64 `bson:"phase_durations,omitempty" json:"phase_durations,omitempty"`
    TokensIn        int              `bson:"tokens_in,omitempty" json:"tokens_in,omitempty"`
    TokensOut       int              `bson:"tokens_out,omitempty" json:"tokens_out,omitempty"`
    StartedAt       time.Time        `bson:"started_at" json:"started_at"`
    FinishedAt      time.Time        `bson:"finished_at" json:"finished_at"`
}

const SchemaIndexRunKindTables = "tables"
```

Collection constant `CollectionSchemaIndexRuns = "project_schema_index_runs"` added to
`services/agent/internal/database/mongodb.go` (alongside `CollectionSchemaIndexProgress`).

### Indexes (added to `services/api/database/init.go` `schema` slice)

```go
{
    Name: "project_schema_index_runs",
    Indexes: []mongo.IndexModel{
        // Project roll-up: latest runs across datasources.
        {Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "finished_at", Value: -1}}},
        // Per-datasource history list (the primary query).
        {Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "datasource_id", Value: 1}, {Key: "finished_at", Value: -1}}},
        // Idempotent upsert guard: one doc per (project, datasource, run).
        {
            Keys:    bson.D{{Key: "project_id", Value: 1}, {Key: "datasource_id", Value: 1}, {Key: "run_id", Value: 1}},
            Options: options.Index().SetUnique(true),
        },
    },
},
```

The API owns `InitDatabase` (startup, idempotent) so the index exists before the agent
writes — exactly how `project_schema_index_progress` works today (API creates the
index, agent writes the doc).

## 4. Backend — agent stamps the result

### 4.1 Widen `discovery.Stats` (`services/agent/internal/discovery/schema_indexer.go`)

Add two fields; both are already computable inside `BuildIndex`:

```go
type Stats struct {
    Tables         int
    Blurbs         int                       // NEW: len(kept) — successful blurbs
    Dropped        int
    BlurbTokensIn  int
    BlurbTokensOut int
    Duration       time.Duration
    PhaseDurations map[string]time.Duration  // NEW: phase → elapsed
}
```

`BuildIndex` already has the timing anchors (`discoveryStart`, `blurbStart`, `start`).
Add an `embedStart`, compute three durations, and populate the map:

- `schema_discovery` — `resolveSchemas` leg (listing is folded in here; `DiscoverSchemas`
  does table listing internally, so there is no separate measurable "listing" leg —
  documented as such).
- `describing_tables` — `Blurber.Generate`.
- `embedding` — embed + Qdrant upsert.

`Blurbs = len(kept)`. Keys reuse the existing `SchemaIndexPhase*` constants so the names
stay consistent with the progress doc. This is additive — existing callers/tests of
`Stats` keep compiling.

### 4.2 New agent repo (`services/agent/internal/database/schema_index_run_repo.go`)

```go
type SchemaIndexRunRepository struct { db *DB }
func NewSchemaIndexRunRepository(db *DB) *SchemaIndexRunRepository
// Record upserts by (project_id, datasource_id, run_id) so a retried
// stamp can't duplicate the row; the unique index backs this.
func (r *SchemaIndexRunRepository) Record(ctx context.Context, run *models.SchemaIndexRun) error
```

Mirrors the structure of `schema_index_progress_repo.go` (validates `project_id`,
uses `r.db.Collection(CollectionSchemaIndexRuns)`, wraps errors `%w`, structured logs).

### 4.3 Stamp in `runIndexSchema` (`services/agent/agentserver/index_schema.go`)

Construct the run repo next to the progress repo (line ~149):
`runRepo := database.NewSchemaIndexRunRepository(db)`.

Stamp **once per datasource**, covering every exit path of `indexWarehouse`, by giving
it a named return and a `defer`. The defer uses a **detached, short-timeout context**
(like the worker's `transitionCtx`) so a per-run context that was cancelled mid-flight
doesn't also fail the audit write:

```go
indexWarehouse := func(ctx context.Context, wh models.WarehouseConfig, reportProgress bool) (retErr error) {
    whID := warehouseIDOrDefault(wh)
    start := time.Now()
    var stats *discovery.Stats
    defer func() {
        recordCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        stampSchemaIndexRun(recordCtx, runRepo, wh, whID, runID, start, stats, retErr)
    }()
    // … existing provider init (returns retErr on failure) …
    // … existing indexer setup …
    stats, retErr = indexer.BuildIndex(ctx, discovery.IndexOptions{ … })
    if retErr != nil {
        return fmt.Errorf("build index: %w", retErr)
    }
    // existing success log …
    return nil
}
```

`stampSchemaIndexRun` (new unexported helper in the same file) builds the doc:

- `status` = `ready` when `err == nil`, else `failed`.
- `error` = `err.Error()` on failure (empty on success).
- `datasource_name` = `firstNonEmpty(wh.Label, wh.Provider, whID)`.
- counts / tokens / phase_durations from `stats` when non-nil; zeros when `stats == nil`
  (a discovery/provider-init failure returns before any stats). `phase_durations`
  converted to ms.
- `started_at = start`, `finished_at = time.Now()`, `kind = SchemaIndexRunKindTables`.
- On `runRepo.Record` error: log a warning and continue — a failed audit write must not
  fail the index run itself (same best-effort posture as `IncrementTokens`).

This correctly records **secondaries too**: a secondary datasource that fails is logged
(not propagated) today, and the defer still stamps its `failed` record — so "primary
ready + one secondary failed" yields two accurate docs. The primary's failure still
propagates (fails the run) *after* its record is stamped.

Cancellation nuance (documented, not engineered around): when the user cancels, the API
worker kills the agent subprocess. If the process dies before the defer runs, no record
is written — consistent with "partial progress is thrown away on failure/cancel". If the
defer does run with an error, it records `failed`; we don't try to distinguish a
cancel-kill from a genuine failure in the per-datasource record (the project-level
`cancelled` status already conveys user intent).

Out of scope: partial token accounting on failure. `BuildIndex` returns `nil` stats on
error, so a failed record carries zero counts; the live progress doc still holds partial
blurb tokens for the in-flight view. Capturing partial stats on failure would change the
`BuildIndex` contract and isn't needed for the acceptance criteria.

## 5. Backend — API read path

### 5.1 New repo (`services/api/database/schema_index_runs.go`)

```go
type SchemaIndexRunRepository struct { col *mongo.Collection } // project_schema_index_runs
func NewSchemaIndexRunRepository(db *DB) *SchemaIndexRunRepository
// List returns a project's run records, newest finished_at first.
// datasourceID == "" returns all datasources; a non-empty value filters.
// limit <= 0 or > maxSchemaIndexRunLimit is clamped.
func (r *SchemaIndexRunRepository) List(ctx context.Context, projectID, datasourceID string, limit int) ([]models.SchemaIndexRun, error)
```

`find({project_id[, datasource_id]})` sorted `{finished_at: -1}` with a limit
(default 50, hard cap e.g. 200). Returns `[]` (never nil) on no matches.

### 5.2 Handler (`services/api/internal/handler/schema_index.go`)

Add a nullable in-handler interface + field (mirrors `SchemaIndexLogLister`):

```go
type SchemaIndexRunLister interface {
    List(ctx context.Context, projectID, datasourceID string, limit int) ([]models.SchemaIndexRun, error)
}
```

Add `runs SchemaIndexRunLister` as a new `NewSchemaIndexHandler` parameter (nullable —
when nil, `ListRuns` returns `{"runs": []}`, matching the community-smoke-build posture
of the other nullable deps).

```go
// GET /api/v1/projects/{id}/schema-index/runs?datasource_id=…&limit=…
func (h *SchemaIndexHandler) ListRuns(w http.ResponseWriter, r *http.Request)
```

Behaviour: validate `id`; 404 when the project doesn't exist (same as the other handler
methods); read optional `datasource_id` + `limit` query params; call
`h.runs.List(...)`; respond `{"runs":[ …wire rows… ]}`. Use a `SchemaIndexRunView` wire
type with RFC3339 `started_at` / `finished_at` strings (same pattern as
`SchemaIndexStatusResponse`/`SchemaIndexProgressView`, which format times explicitly and
decouple the wire shape from the Mongo doc).

### 5.3 Interface registry + wiring

- Add `SchemaIndexRunRepo` interface + compile-time check in
  `services/api/database/interfaces.go` (consistency with `SchemaIndexProgressRepo`),
  even though the handler uses its own in-package interface.
- `services/api/internal/server/server.go`: build
  `schemaIndexRunRepo := database.NewSchemaIndexRunRepository(db)` (next to
  `schemaIndexProgressRepo`, ~line 146) and pass it into `NewSchemaIndexHandler(...)`.
- Register the route next to the other schema-index routes (~line 231):
  `mux.HandleFunc("GET /api/v1/projects/{id}/schema-index/runs", withRole(viewer, schemaIndex.ListRuns))`
  (viewer — read-only).

No change to `apiserver.go` (it calls `server.New*` which owns the repo wiring).

## 6. Frontend — dashboard

### 6.1 API client (`ui/dashboard/src/lib/api.ts`)

```ts
export interface SchemaIndexRun {
  project_id: string;
  datasource_id: string;
  datasource_name?: string;
  run_id: string;
  kind: string;
  objects_indexed: number;
  blurbs_generated: number;
  status: string;              // "ready" | "failed"
  error?: string;
  phase_durations?: Record<string, number>; // phase → ms
  tokens_in?: number;
  tokens_out?: number;
  started_at: string;
  finished_at: string;
}

listSchemaIndexRuns: (projectId: string, datasourceId?: string, limit?: number) =>
  request<{ runs: SchemaIndexRun[] }>(`/api/v1/projects/${projectId}/schema-index/runs${qs}`)
```

### 6.2 Per-datasource history section — `SchemaIndexHistory.tsx` (new, `components/projects/`)

Props: `{ projectId: string; datasourceId?: string; datasourceName?: string }`.
Renders an "Indexing" section:

- **Current status** line: reuse `api.getSchemaIndexStatus(projectId)` for the
  project-level live state (the panel's single community datasource is the primary, so
  the project status is its status).
- **Expandable history table** (collapsed by default, fetched on first expand via
  `api.listSchemaIndexRuns(projectId, datasourceId)`): columns
  **finished_at · objects indexed · blurbs · duration · status · error**.
  - `duration` = `finished_at − started_at`, humanised.
  - `status` → a green/red pill (reuse token CSS vars; no inline magic colors per the
    TS style rule).
  - failed rows show `error` (truncated with title tooltip).
  - empty state: "No index runs yet."
  - runs-fetch error degrades to an inline message, never throws.

Mounted inside `WarehouseConfigPanel` (`components/projects/WarehouseConfigPanel.tsx`)
below the Test-Connection button, co-locating the re-index trigger with its result.
Community projects are single-datasource, so the section omits the `datasource_id`
filter (the endpoint then returns that one datasource's runs).

### 6.3 Persistent project-page roll-up (`components/SchemaIndexPanel.tsx` + project page)

Replace `hideWhenReady` with a persistent per-datasource roll-up by enhancing the
panel's steady state (the panel is only used on the project page — grep confirms no other
mount — so this is a contained change):

- In `SchemaIndexPanel`, when `status.status` is a settled state (`ready` and the other
  terminal states), fetch `api.listSchemaIndexRuns(projectId)` once and render a compact
  per-datasource roll-up: one line per `datasource_id` (latest run), e.g.
  **`Redshift ✅ 42 tables · 14:20 → history`** (`❌` for failed), each linking to the
  Data Warehouse settings tab (`/projects/{id}/settings#warehouse`) where the full
  history table lives. Grouping uses the `datasource_id` / `datasource_name` on the run
  docs, so it degrades to a single line for single-datasource projects and needs no
  separate project-warehouse fetch.
- Keep the existing **Re-index** action and the live progress bar / failed-state
  affordances exactly as they are — the roll-up augments the ready state instead of
  returning `null`.
- Project page (`app/projects/[id]/page.tsx:345`): drop the `hideWhenReady` prop and
  update the explanatory comment. `onStatusChange` wiring is unchanged.

`hideWhenReady` stays a supported prop (no other caller uses it; removing it entirely is
unnecessary churn), but the project page no longer passes it.

## 7. Step-by-step phases

1. **Models + collection constant** — `SchemaIndexRun` in both model files;
   `CollectionSchemaIndexRuns` in agent `mongodb.go`; index entry in API `init.go`.
2. **Agent stats** — widen `discovery.Stats` (`Blurbs`, `PhaseDurations`), populate in
   `BuildIndex`.
3. **Agent repo + stamping** — `schema_index_run_repo.go`; `stampSchemaIndexRun` helper
   + `defer` wiring in `index_schema.go`.
4. **API repo + handler + route** — `schema_index_runs.go`; `ListRuns` + nullable
   `SchemaIndexRunLister`; constructor param; `interfaces.go`; route + repo wiring in
   `server.go`.
5. **Dashboard** — `lib/api.ts` type + client; `SchemaIndexHistory.tsx`; mount in
   `WarehouseConfigPanel`; enhance `SchemaIndexPanel` roll-up; drop `hideWhenReady` on
   the project page.
6. **Tests** — §8.
7. **Docs** — §9.
8. **Local checks** — `make build`, `make test-go`, `make lint-go`, `make test-ui`,
   `make lint-ui`, `make test-integration`.

## 8. Test strategy (Rule 9 — failure + edge, not just happy path)

**Agent unit (`services/agent/agentserver/index_schema_test.go`)**
- `stampSchemaIndexRun`: success path maps `Stats` → doc fields (status `ready`,
  counts/tokens/phase_durations, datasource_name fallback order `Label→Provider→id`);
  failure path (`stats == nil`, non-nil err) → status `failed`, `error` set, zero counts;
  uses a fake `runRepo` capturing the recorded doc.

**Agent unit (`schema_indexer` — integration, real Qdrant+Mongo testcontainers in
`schema_indexer_integration_test.go`)**
- Assert `BuildIndex` now returns `Stats.Blurbs > 0` and a populated `PhaseDurations`
  (keys `schema_discovery` / `describing_tables` / `embedding`) on success.

**Agent integration (`schema_index_run_repo_integration_test.go`, testcontainer Mongo)**
- `Record` inserts a readable doc; re-`Record` with the same `(project,datasource,run)`
  upserts (no duplicate — unique index).

**API repo integration (`schema_index_runs_integration_test.go`, testcontainer Mongo)**
- Multiple runs across two datasources + runs: `List` returns newest-`finished_at`
  first; `datasource_id` filter narrows correctly; `limit` clamps; unknown project → `[]`.

**API handler (`services/api/internal/handler/schema_index_test.go`)**
- Add `mockRunLister`. `ListRuns`: happy path returns rows; `datasource_id` + `limit`
  passthrough; project-not-found → 404; nil lister → `{"runs":[]}` (200); malformed
  `limit` → ignored/clamped, not 400.

**Model round-trip (`schema_index_test.go`, both modules)**
- `SchemaIndexRun` bson/json marshal↔unmarshal incl. `phase_durations` map and
  empty-`error` omitempty.

**Dashboard Jest**
- `SchemaIndexPanel.test.tsx`: `ready` status + mocked runs renders the per-datasource
  roll-up line(s) with objects count + history link; **Re-index** still present; runs
  endpoint failure doesn't crash the panel.
- `SchemaIndexHistory.test.tsx` (new): renders a row per run (objects/blurbs/duration/
  status); failed row surfaces `error`; empty state renders; expand/collapse toggles the
  fetch.

## 9. Docs (Rule 4)

- `docs/guides/schema-indexing.md`: new "Per-datasource run history" subsection —
  describe the durable `project_schema_index_runs` record (stamped per datasource on
  completion, success or failure, never reset) and the `GET …/schema-index/runs`
  endpoint; correct the implication that indexing state is purely live/project-level.
- `docs/reference/api.md`: add the
  `GET /api/v1/projects/{id}/schema-index/runs?datasource_id=&limit=` endpoint with a
  sample response (it's the first schema-index endpoint documented there; scope this PR
  to the new endpoint only).
- `CHANGELOG.md` `[Unreleased] → Added`: a durable per-datasource schema-index run
  history + list endpoint + dashboard history/roll-up.

## 10. Risks / trade-offs

- **Unbounded growth (no TTL).** Durability is the point, and the docs are tiny (one per
  datasource per re-index). If retention becomes a concern later, a long partial TTL can
  be added without touching the write path.
- **Two `SchemaIndexRun` definitions** (api + agent modules) must stay in sync — standard
  for this repo (same as `SchemaIndexProgress`); both covered by round-trip tests.
- **Failed records carry zero counts** (no partial-token capture on failure) — acceptable;
  live partials remain on the progress doc. Noted as out of scope.
- **Cancel-kill** may skip the audit write — consistent with "partial progress is thrown
  away."
- **Collection naming** — proposing `project_schema_index_runs` over the issue's literal
  `schema_index_runs` for family consistency; flagged for review.

## 11. Alternatives considered

- **Stamp in the API worker** (`schemaindex/worker.go`) — rejected: the worker only sees
  the project-level exit code, not per-datasource stats or per-secondary outcomes.
- **Reuse / append to the progress doc** — rejected: it's single-doc-per-project,
  `Reset()` every run, and live-only — the exact problem.
- **Derive history from `project_schema_index_logs`** — rejected: unstructured text with
  a 7-day TTL; no reliable per-datasource counts.
- **Store a runs array on the `projects` doc** — rejected: unbounded growth on a hot,
  frequently-updated document with a poor query/index story.

## 12. Scope / non-goals

- **In:** the result record + list endpoint, per-datasource history UI, persistent
  project-page roll-up.
- **Out:** changing the indexing pipeline itself; per-object drill-down beyond counts +
  errors; partial-token accounting on failed runs.

## 13. Acceptance criteria → where met

- *Every index/re-index stamps a `project_schema_index_runs` doc per datasource (ready or
  failed), never lost on the next run* → §4.3 (`defer`-stamped per datasource, append-only
  collection).
- *`GET …/schema-index/runs` returns the history, filterable by datasource* → §5.
- *`WarehouseConfigPanel` shows per-datasource current status + index-run history (when ·
  objects · blurbs · duration · status · error)* → §6.2.
- *Project page shows a persistent per-datasource status roll-up (no more hide-on-ready)
  linking to the history* → §6.3.
- *A successful re-index leaves a visible, durable record — not just a toast* → §3 + §6.

---

This is a **PLAN for review** — implementation follows after approval.

Closes #413
