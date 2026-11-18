# Plan — #522: Discovery dies when the schema cache expires

> **This document is a PLAN for review. No implementation yet.**

## 1. What is actually broken

A project indexed more than seven days ago cannot run discovery, and nothing in the
product says why. Three separate defects chain into that, plus two more that only
showed up once I read the code.

### 1.1 Verified against the current tree

**The reaper.** `services/api/database/init.go:364-382` (the TTL itself at `:376-380`) puts a 7-day TTL on
`project_schema_cache.cached_at`:

```go
{
    Keys:    bson.D{{Key: "cached_at", Value: 1}},
    Options: options.Index().SetExpireAfterSeconds(7 * 24 * 60 * 60),
},
```

`cached_at` is stamped at write time by `SchemaCacheRepository.Save` /
`SaveCatalog` (`services/agent/internal/database/schema_cache_repo.go`) and is
never refreshed on read. So the clock runs from the last **index**, not from the
last **use**: a project that runs discovery every day still loses its cache on
day eight.

**The TTL's stated purpose is not true any more.** The comment says *"a warehouse
whose physical schema has drifted without the config changing still gets
rediscovered at least weekly"*. `git log -S` shows the TTL and the strict-cached
discovery path landed in the **same commit** — `dbd5c6a` (#184, on-demand schema
retrieval). Since that commit `Orchestrator.discoverSchemas` has had no
live-warehouse fallback *by design*
(`services/agent/internal/discovery/orchestrator.go:3801-3814`), so nothing
rediscovers anything when the TTL fires. The reaper deletes; nobody rebuilds.
The only thing a cold cache still triggers is a re-discovery inside the **next
index run** (`SchemaIndexer.resolveSchemas`), and index runs are user-triggered.
The weekly-refresh premise is not stale — it was never reached.

**The fatal.** `services/agent/internal/discovery/orchestrator.go:1061-1069`:

```go
applog.Info("Phase 2: Loading schemas from cache")
o.statusReporter.SetPhase(ctx, models.PhaseSchemaDiscovery, "Loading cached warehouse schemas...", 8)
schemas, err := o.discoverSchemas(ctx)
if err != nil {
    return nil, fmt.Errorf("schema cache lookup failed: %w", err)
}
```

and `discoverSchemas` (same file, `:3853-3880`) returns
`schema cache is empty for this project — re-index required (POST …/reindex)`.
The multi-datasource path has its own copy of the same decision
(`orchestrator_multiwarehouse.go:97-129`): a cold **primary** is fatal, a cold
**secondary** is dropped from the run with a `Warn` and no user-visible trace at
all.

**Catalog-shaped datasources break too** — the issue does not mention this.
`SaveCatalog` stamps `cached_at` on the catalog row as well
(`schema_cache_repo.go:400-408`), so the TTL reaps it, `FindCatalog` goes empty,
`datasourceIsIndexed` says no, and a GA4-class source fails identically on day
eight.

**The unreadable error.** `services/api/internal/runner/kubernetes.go:696-715`
(`getPodErrorMessage`) returns `Terminated.Message` when set and otherwise
`fmt.Sprintf("Container exited with code %d: %s", ExitCode, Reason)`. Nothing
sets `TerminationMessagePolicy` on the agent container in `buildJob`
(`kubernetes.go:130-150`), so it defaults to `File`, nothing writes
`/dev/termination-log`, `Message` is empty, and Kubernetes' `Reason` for any
non-zero exit is the literal string `Error`. Hence
`"Container exited with code 1: Error"`.

**The Docker and subprocess runners already solve this.** `DockerRunner` keeps a
64 KiB rolling stderr `tailBuffer` and runs it through
`extractErrorMessage(tail.String(), exitStatusError(status.StatusCode))`
(`docker.go:444-497` + `tailBuffer` at `docker.go:857-876`, `subprocess.go:427-467`). `extractErrorMessage` walks
backwards for a `FATAL` line and pulls its `error` field — which on this exact
failure yields `discovery run failed: schema cache lookup failed: schema cache
is empty for this project — re-index required (…)`. The Kubernetes runner is the
only one that does not do this. It is a parity gap, not a missing feature.

**Nothing writes the run's own failure for an early-phase error** — also not in
the issue, and it is why the watcher's message was the *only* record.
`finalizeStatus` (`orchestrator.go:110-127`) is reached only after the compute
phase produced a result; a Phase-2 error returns from `RunDiscovery` long before
that, `agentserver.runDiscovery` notifies + returns
(`agentserver.go:1206-1253`), the process exits 1, and **no `failed` status is
ever written by the agent**. On Docker/subprocess the watcher's message happens
to be good; on Kubernetes it is `Error`. `debug-logs` and `steps` are empty for
the same reason — the run died before writing a row.

**The dashboard's READY.** Both panels already derive their status line from the
*live* `GET /schema-index/status` rather than from `runs[0]`
(`SchemaIndexHistory.tsx:80-87`, `SchemaIndexPanel.tsx:85-120`) — that part was
fixed earlier. The problem is that the endpoint itself is wrong:
`project.schema_index_status` has no expiry paired to the cache's, so it still
says `ready` over an empty collection. Worse, `SchemaIndexPanel` is mounted with
`hideWhenReady` on the project page (`app/projects/[id]/page.tsx:536`), so in
this state the panel renders **nothing at all** — the operator is not merely
misinformed, they are shown no surface to act on.

**Garbage collection does not depend on the TTL.** `project_schema_cache` is in
the project-delete purge list (`services/api/database/database.go:273`), and
`/reindex` calls the project-wide `cacheRepo.Invalidate`
(`schema_index.go:510-517`). So removing the TTL leaks nothing that is not
already cleaned by a delete or the next re-index.

### 1.2 What the chain costs

Day six works, day eight does not, nobody changed anything, the dashboard says
READY, and the recorded error is five words that mean nothing. The real message
existed only in a pod log inside a Job with `TTLSecondsAfterFinished: 3600`
(`kubernetes.go:284`).

---

## 2. The one decision that needs sign-off

The issue asks for: *"treat an empty schema cache as a cache miss — re-index,
then continue the run"*, and offers the alternative: *"If a rebuild is too
expensive to do implicitly, then the cache should not silently expire underneath
a project that depends on it."*

**This plan does both, and the rebuild lives in the agent, inside the run.**
That reverses a documented architectural decision from #184 (`orchestrator.go`
3800-3813 and `docs/architecture/agent-on-demand-schema.md`), so it is the thing
to agree before I write code. The reasons it is the right call here:

- **The expensive half of an index did not expire.** Qdrant has no TTL. The
  blurbs and their embeddings — the LLM spend — are still there; `TableSchema`
  carries no blurb field, blurbs live only in the Qdrant payload. Only the Mongo
  table/catalog rows were deleted. So the repair is *schema discovery only*: no
  blurb LLM, no embedding calls, no Qdrant write. **Zero LLM cost.**
- **The alternative — auto-triggering a full `/reindex` from the API — would
  spend money the user did not ask to spend.** `/reindex` drops the Qdrant
  collection and regenerates every blurb; on a 1,400-table warehouse that is a
  real bill incurred as a side effect of clicking "Run discovery". A targeted
  cache rebuild is strictly cheaper and strictly less destructive.
- **The run the operator asked for actually happens**, which is what the issue
  is asking for.
- **A cache-only divergence degrades gracefully.** If the warehouse's table set
  drifted since the last index, the rebuilt cache and the surviving Qdrant
  points disagree at the edges. `cache_schema_provider.go:273` already **drops
  search hits for tables not in the cached schemas map**, so a dropped table
  stops answering, and a newly-added table appears in the prompt catalog but is
  not yet searchable. The run works; the next real index run reconciles it.

What I am **not** doing: making the agent run the full indexer (blurbs,
embeddings, Qdrant) inline. That duplicates a lifecycle the API's
`schemaindex.Worker` owns — the `BeginReindex` lock, the progress document, the
`project_schema_index_runs` record, the Qdrant drop — and would make a discovery
run silently cost an index run.

See §9 for the alternatives and why each lost.

---

## 3. Proposed approach — four changes

### Fix A — stop the cache expiring (root cause)

Delete the TTL, keep a plain index on the same key so no query plan changes, and
drop the legacy TTL index on existing deployments so they heal on the next API
start. `Indexes().CreateMany` never removes an index, so leaving the drop out
would fix new installs only — every deployment that has already booted the
current release keeps `cached_at_1` with `expireAfterSeconds: 604800` forever.

- `services/api/database/init.go`
  - `project_schema_cache`: replace the TTL index with
    `{Keys: bson.D{{Key: "cached_at", Value: 1}}}` and **no** options. The field
    stays — `/cache-info` renders "Last cached: …" from `LastCachedAt`, and the
    schema editor's "which of my edits are still live" cutoff is
    `DatasourceCachedAt` — and both sort on it, so the plain index preserves
    today's plans exactly.
  - New, explicit migration step, run **before** the create loop:

    ```go
    // legacyIndexDrops names indexes an earlier release created that the
    // current schema must NOT have. CreateMany only ever adds, so a
    // deployment that once ran that release keeps the index forever unless
    // it is dropped here.
    var legacyIndexDrops = []legacyIndexDrop{
        {Collection: "project_schema_cache", Index: "cached_at_1", DropWhenTTL: true, …},
    }
    ```

    `DropWhenTTL` makes it conditional on the index actually carrying
    `expireAfterSeconds`: list the indexes, drop only if the expiry is present.
    Without that condition every API start would drop and rebuild the index.
    `IndexNotFound` (27) and `NamespaceNotFound` (26) are the steady state and
    are not errors. Any other failure fails the boot, which matches how
    `InitDatabase` already treats `CreateMany` errors — and failing closed is
    right: running the new code with the reaper still armed is the bug.
  - The decision is a pure helper (`ttlDropNeeded(specs []bson.M, name string) bool`)
    so both branches are unit-testable without Mongo.
  - `dropIndex` is in `readWrite`, the same role `createIndexes` already needs —
    no new Mongo privilege.

- `services/agent/internal/database/schema_cache_repo.go` — two doc comments
  assert the TTL as a live mechanism (lines ~24-26 "A 7-day TTL caps staleness…",
  ~116-117 "TTL handles absolute-age cleanup if the project is abandoned").
  Both become false; rewrite them to name the real mechanisms (config-hash
  miss, `/reindex`, `Invalidate`, project delete). Rules 4 and 6.

### Fix B — Phase 2 treats a cold cache as a cache miss and rebuilds it

One helper, used by both the single- and multi-datasource paths, mirroring what
`agentserver/index_schema.go:190-276` already does for the index run.

- **New** `services/agent/internal/discovery/schema_cache_rebuild.go`

  ```go
  // rebuildTarget is everything the rebuild needs about one datasource.
  type rebuildTarget struct {
      provider     gowarehouse.Provider
      warehouseID  string
      hash         string
      providerSlug string
      datasets     []string
      filterField  string
      filterValue  string
  }

  // rebuildSchemaCache re-discovers one datasource's schemas (or its catalog)
  // and writes them back to the project's schema cache. Blurbs and vectors are
  // deliberately NOT rebuilt: Qdrant has no TTL, so they never expired.
  func (o *Orchestrator) rebuildSchemaCache(ctx context.Context, t rebuildTarget) (map[string]models.TableSchema, []string, error)
  ```

  Behaviour:
  - Catalog-shaped source (`gowarehouse.AsCatalogSource(t.provider)` — the same
    probe `index_schema.go:253` uses): call `Catalog(ctx)`, run it through the
    package's existing `indexableCatalogItems` + `dimensionRefs`, and
    `SaveCatalog(…, refs, dimensionRefs)` exactly as `saveCatalogCache` (`schema_indexer.go:545-557`) does.
    Returns the refs.
  - Table-shaped source: build a sampling `queryexec.QueryExecutor` with the
    datasource's own `ProviderSlug` / `FilterField` / `FilterValue` — the filter
    **must** match the discovery filter, which `queryexec` verifies before every
    sample query (`index_schema.go:185-188`) — then
    `discovery.NewSchemaDiscovery(...).DiscoverSchemas(ctx)` and `Save(…)`.
  - A `Save` failure is **not** fatal: the schemas are in hand and the run can
    use them. Log a `Warn` saying the next run will rebuild again. (Mirrors the
    indexer's best-effort cache save and PR #195's reasoning.)
  - Returns `(nil, nil, err)` when the warehouse itself cannot answer, so the
    caller keeps today's error with the rebuild failure wrapped in.

- `services/agent/internal/discovery/orchestrator.go`
  - `discoverSchemas`: on the empty-cache branch, **after** the existing
    indexed-catalog check (a catalog source with refs is already a legitimate
    empty table map and must not trigger a rebuild), attempt the rebuild.
    Guarded on `o.warehouse != nil` — with no provider wired there is nothing to
    rebuild and today's error is still the right answer. That guard is also what
    keeps every existing unit test in `orchestrator_schema_cache_test.go` and
    `orchestrator_catalog_test.go` meaningful: they all construct a bare
    `&Orchestrator{}` with no provider.
  - Report it where an operator will see it, before the slow part starts:
    `o.statusReporter.SetPhase(ctx, models.PhaseSchemaDiscovery, "Schema cache was empty — re-discovering warehouse schemas…", 8)`
    (writes `phase_detail` on the run document, which the dashboard's run panel
    renders) plus an `applog.Warn` naming the datasource and the table count.
  - On success, log the count and continue into the existing cached-schema
    filter path unchanged. On failure, return
    `schema cache is empty for this project and the rebuild failed: %w — re-index required (POST …/reindex)`.
  - `discoverSchemas` has a single caller, so the executor built at
    `orchestrator.go:963` is threaded in as a parameter rather than parked on
    the struct as new mutable state. (Alternative if that signature churn is
    unwelcome in review: the rebuild builds its own sampling executor, which is
    what the indexer does anyway — I will take whichever reviewers prefer.)

- `services/agent/internal/discovery/orchestrator_multiwarehouse.go`
  - `buildDatasourceContext`: a cold datasource gets the same rebuild. The
    primary's failure stays fatal. A **secondary's** failure keeps the current
    skip-with-a-warning, but the warning gains the rebuild failure so the log
    says why a datasource vanished from the run.

- Bounds: the rebuild runs under the run's own `discoveryCtx`
  (`DISCOVERY_MAX_DURATION`, default 24h, inside the 25h Job deadline) and under
  the provider's per-query timeout. **No new env var** — Rule 8; the existing
  budgets are the right ones and a new knob would be a second place to get it
  wrong.

- Comment debt: the header comment on `orchestrator_schema_cache_test.go` and
  the design note at `orchestrator.go:3801-3814` both state the no-fallback rule
  as absolute. Both get rewritten to say what is now true: no *routine*
  fallback — a cold cache is repaired once, loudly, and the catalog pass is
  still never re-run on a cache hit.

### Fix C — record the agent's own words

Two independent captures, so the message survives whichever layer notices first.

- `services/agent/agentserver/agentserver.go` — in the `runDiscovery` error
  seam (`~1206`), after the `ErrAttemptSuperseded` / `ErrPauseRequested` early
  returns and before `notify.NotifyAll`, write the run's own terminal status:
  `runRepo.Fail(ctx, runID, "", err.Error(), attempt)`. `RunRepository.Fail`
  (`run_repo.go:306-334`) is attempt-fenced and only writes over `pending` /
  `running`, so it cannot clobber a status the orchestrator already wrote, and
  the K8s watcher's later `FailAttempt` becomes the no-op it should be. This is
  the fix that makes the message **runner-independent**: the real error lands in
  the run document even on a deployment with no container-log collection at all.
  `runRepo` is already in scope at that point.

- `services/api/internal/runner/kubernetes.go`
  - `buildJob`: `TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError`
    on the `agent` container. The kubelet then fills `Terminated.Message` with
    the tail of the container log on any non-zero exit — no API call, no RBAC,
    and it works for every mode (`discovery`, `index-schema`, `validate-doc`,
    `test-connection`) because they all go through `buildJob`.
  - `getPodErrorMessage`: three ordered sources instead of two.
    1. `Terminated.Message` non-empty → `extractErrorMessage(msg, exitStatusError(code))`.
       With the policy above the message is a raw log tail, so it must be parsed,
       not pasted. A short hand-written message passes through unchanged
       (`extractErrorMessage` falls back to the last non-routine line), so
       `TestKubernetesRunner_PodErrorMessage_…` attempt-scoping tests keep their
       expectations.
    2. Message empty → bounded pod-log tail → `extractErrorMessage(tail, exitStatusError(code))`.
       This is the belt to the policy's braces: it also covers a kubelet that
       lost the message and a pod the policy did not apply to (a Job created
       before this release, still being watched across an API restart).
    3. Nothing usable → today's `Container exited with code %d: %s`. Keeps
       `OOMKilled` readable, which is the one case where `Reason` beats the log.
  - New `tailPodLogs(ctx, jobName) []byte`: `PodLogOptions{TailLines: 200}`,
    read into a buffer and keep the **last** 64 KiB — the same budget as the
    Docker runner's `tailBuffer`, hoisted to a shared named constant so the two
    runners cannot drift. `TailLines` rather than `LimitBytes` because
    `LimitBytes` truncates from the *front* of the stream and would cut off the
    `FATAL` line, which is the one line that matters. 200 lines covers zap's
    `AddStacktrace(ErrorLevel)` frames that follow the FATAL message. The
    existing unbounded `readPodLogs` (used by `RunSync`, which needs whole
    stdout) is left alone.
  - The full tail also goes to `apilog` at `Warn` — free, and an operator with
    API log collection gets the whole picture without a blob in Mongo.
  - A small injectable seam (`podLogTail func(context.Context, string) []byte`,
    defaulting to the real reader) so both the "log available" and "no log
    available" branches are unit-testable: `client-go`'s fake clientset always
    returns a non-empty `"fake logs"` body and cannot express the empty case.
  - RBAC needs nothing: `helm-charts/decisionbox-api/values.yaml:200-202`
    already grants `get`/`list` on `pods` and `pods/log`. The Job's
    `TTLSecondsAfterFinished` is 3600s against a 30s watch tick, so the pod is
    reliably still there when the watcher reacts.

  **Trade-off, stated plainly:** this records the *decisive line*, not the last
  N lines. The issue asked for "the last lines"; `extractErrorMessage` already
  exists, already knows how to find the FATAL line's `error` field, and already
  defines what the other two runners record. A one-line error that says
  `schema cache is empty for this project — re-index required` is the
  thirty-second diagnosis the issue is asking for; 2 KB of raw JSON logs in a
  Mongo string field is not. The full tail goes to the API log, where blobs
  belong.

### Fix D — the dashboard stops claiming a ready index over a vanished cache

- `services/api/internal/handler/schema_index.go` — `SchemaIndexStatusResponse`
  gains `cached bool` and `last_cached_at string`, filled from the
  `h.cacheRepo.LastCachedAt` the handler already has wired. `cached: true` when
  `h.cacheRepo == nil` so a Qdrant-less community build does not start warning
  about a cache it cannot see. `GetStatus` is the one endpoint both panels poll,
  so putting the truth there fixes both with one round-trip instead of two —
  and `/cache-info` stays exactly as it is for Settings → Advanced.
  `status.status` is **not** changed: `app/projects/[id]/page.tsx` gates the Run
  button on `status === 'ready'`, and with Fix B a cold cache is no longer a
  reason to block a run — the run repairs it.
  `LastCachedAt` counts catalog rows too, so a catalog-only project reports
  `cached: true` correctly.

- `ui/dashboard/src/lib/api.ts` — the two fields on `SchemaIndexStatus`.

- `ui/dashboard/src/components/SchemaIndexPanel.tsx` — a derived state for
  `status === 'ready' && cached === false`: amber, actionable, and **exempt from
  `hideWhenReady`** so it is visible on the project page, which is the whole
  point. Copy along the lines of *"Indexed, but the cached schemas are gone —
  the next discovery will re-discover them, which is slow on a large warehouse.
  Re-index now to avoid that."* The existing Re-index button is already rendered
  for `ready`, so there is no new action to build.

- `ui/dashboard/src/components/projects/SchemaIndexHistory.tsx` — the same note
  on the per-datasource status line, next to "Edit indexed schema" (the page
  that says "No indexed tables for this data source yet" — the other half of the
  contradiction in the issue).

**Enterprise overlay — checked, not assumed.** `decisionbox-enterprise/ui/src/`
overlays `app/projects/[id]/page.tsx` and `components/projects/WarehouseConfigPanel.tsx`,
but **not** `components/SchemaIndexPanel.tsx`, **not**
`components/projects/SchemaIndexHistory.tsx`, and **not** `lib/api.ts` (verified
via `gh api …/contents/ui/src/...`). Every file Fix D touches is community-only,
so this needs **no enterprise sync and no second PR**. If review pushes any of
this into `page.tsx` or `WarehouseConfigPanel.tsx`, that becomes a bridge-branch
change across both repos and I will say so before writing it.

---

## 4. Files to change

| File | Change |
|---|---|
| `services/api/database/init.go` | TTL → plain index on `project_schema_cache.cached_at`; `legacyIndexDrops` + conditional drop before the create loop |
| `services/agent/internal/database/schema_cache_repo.go` | doc comments that assert the TTL as a live mechanism |
| `services/agent/internal/discovery/schema_cache_rebuild.go` | **new** — `rebuildSchemaCache` (tables + catalog paths) |
| `services/agent/internal/discovery/orchestrator.go` | `discoverSchemas` rebuild branch; phase/status reporting; the no-fallback design note |
| `services/agent/internal/discovery/orchestrator_multiwarehouse.go` | per-datasource rebuild in `buildDatasourceContext` |
| `services/agent/agentserver/agentserver.go` | write the run's own `failed` status on an early-phase error |
| `services/api/internal/runner/kubernetes.go` | `TerminationMessagePolicy`; `getPodErrorMessage` three-source order; `tailPodLogs`; shared tail-size constant |
| `services/api/internal/handler/schema_index.go` | `cached` / `last_cached_at` on the status response |
| `ui/dashboard/src/lib/api.ts` | `SchemaIndexStatus` fields |
| `ui/dashboard/src/components/SchemaIndexPanel.tsx` | cold-cache state, exempt from `hideWhenReady` |
| `ui/dashboard/src/components/projects/SchemaIndexHistory.tsx` | cold-cache note on the status line |
| `docs/guides/schema-indexing.md` | the cache does not expire; what does refresh it |
| `docs/deployment/production.md` | `project_schema_cache` in the backup table |
| `CHANGELOG.md` | `## [Unreleased] → ### Fixed` |

Tests (§7) add: `services/api/database/init_test.go`,
`services/api/database/init_integration_test.go`,
`services/agent/internal/discovery/schema_cache_rebuild_test.go`, plus additions
to `orchestrator_schema_cache_test.go`, `orchestrator_catalog_test.go`,
`services/api/internal/runner/runner_test.go`,
`services/api/internal/runner/integration_test.go`,
`services/api/internal/handler/schema_index_test.go`,
`ui/dashboard/src/__tests__/SchemaIndexPanel.test.tsx`.

---

## 5. Phases

Each phase builds, tests and lints green on its own, so review can stop at any
boundary.

1. **Fix A — the TTL.** Index change + conditional legacy drop + the pure
   `ttlDropNeeded` helper + unit and integration tests + the two stale repo
   comments. Nothing else depends on it; it is the change that stops the
   bleeding on every deployment.
2. **Fix C — the diagnostics.** The agent's own `failed` write, then the
   Kubernetes capture. Deliberately before Fix B, as the issue suggests:
   everything after this is easier to verify because failures finally say what
   they are. Includes updating `TestKubernetesRunner_PodErrorMessage_FallsBackToExitCode`,
   which pins the old contract.
3. **Fix B — the rebuild.** The helper, both call sites, the status reporting,
   the comment debt, unit + real-Mongo integration tests.
4. **Fix D — the dashboard.** API field, then the two panels, then the UI tests.
5. **Docs + CHANGELOG** across all four (Rule 4).
6. **Verification** (§8) and the review loop.

---

## 6. Impact

**Data / schema.** No document shape changes. One index changes shape:
`project_schema_cache.cached_at_1` loses `expireAfterSeconds`. `cached_at`
itself keeps being written and read. First API start after deploy does one
`dropIndex` + one `createIndex` on that collection; every start after that is a
no-op. Already-expired caches are **not** restored by this — Fix B restores them
on the project's next discovery run, and `/reindex` still restores them
immediately.

**API.** One additive, backward-compatible change: two optional fields on
`GET /api/v1/projects/{id}/schema-index/status`. No status string changes, no
endpoint added or removed, no request shape touched. `docs/reference/api.md`
documents `/schema-index/runs` but has no section for `/schema-index/status`
(verified), so there is no stale field list to update there.

**UI.** Two community-only components gain one state each. No enterprise overlay
file touched (§3, Fix D).

**Behaviour, user-visible.**
- A project whose cache expired now runs. Its first run after the expiry spends
  the schema-discovery time it would have spent in an index run — minutes on a
  small warehouse, up to ~50 minutes on a 1,400-table one — with
  `phase_detail` saying exactly that. Subsequent runs are normal.
- A failed run on Kubernetes records the agent's own error instead of
  `Container exited with code 1: Error`.
- The Indexing panel stops being silent about a cache that is gone.

**Operational.** `project_schema_cache` becomes durable storage rather than a
reaped cache. It is rebuildable (re-index, or now a run), so it is "Important,
not Critical" for backup — which is what the production doc will say.

---

## 7. Test strategy

Rule 9: every branch below is a failure or edge case, not a happy path.

### Fix A
- **Unit** (`init_test.go`): `ttlDropNeeded` over a synthetic `listIndexes`
  result — index absent → false; present with `expireAfterSeconds` → true;
  present without it → false (the steady state, which is what stops a rebuild on
  every boot); a different index that *does* have a TTL → false (name-scoped).
- **Integration** (`init_integration_test.go`, `//go:build integration`, real
  Mongo via the existing `testDB` harness):
  - pre-create `cached_at_1` **with** `expireAfterSeconds: 604800`, run
    `InitDatabase`, assert the index exists and the expiry is gone;
  - run `InitDatabase` on a clean database, assert the index is created without
    an expiry;
  - run `InitDatabase` twice and assert the second run errors not at all (the
    real idempotency claim);
  - assert `project_schema_cache` has **no** index carrying `expireAfterSeconds`
    afterwards — the regression guard that would have caught this bug.
  - insert a row with `cached_at` eight days old, run `InitDatabase`, assert it
    is still there after a wait longer than Mongo's 60s TTL sweep. *(This one is
    slow; I will only include it if it can be made deterministic — otherwise the
    index assertion above is the real guard and I will say so rather than ship a
    flaky test.)*

### Fix B
- **Unit** (`schema_cache_rebuild_test.go`, extending the package's existing
  `fakeProvider`):
  - table source: cold cache → `DiscoverSchemas` called once → `Save` called
    with the discovered map → returned to the caller;
  - catalog source: cold cache → `Catalog()` read → `SaveCatalog` with the
    indexable refs and the dimension refs → refs returned, table map empty;
  - `Save` fails → schemas still returned, warning logged, run not failed;
  - `DiscoverSchemas` fails → error wrapped, `/reindex` instruction preserved;
  - warehouse returns zero tables → today's re-index error, **not** a run that
    explores nothing;
  - the sampling executor's filter matches the discovery filter (the
    `queryexec` pre-condition).
- **Unit** (`orchestrator_schema_cache_test.go`):
  - no provider wired → unchanged error (pins that all the pre-existing tests
    still mean what they say);
  - provider wired + cold cache → rebuild path, schemas returned, `SetPhase`
    called with the rebuild message;
  - rebuild returns schemas → the cached-schema filter still runs over them (a
    discovery scope must not be bypassed by the rebuild path);
  - rebuild returns schemas that a filter then drops to zero → the existing
    explicit scope error, not a silent empty run.
- **Unit** (multi-warehouse): cold primary repaired; cold primary that cannot be
  repaired → fatal; cold secondary repaired; cold secondary that cannot be
  repaired → skipped, and the warning names the rebuild failure.
- **Unit** (`orchestrator_catalog_test.go`): a source with an **indexed catalog**
  and no tables still takes the no-rebuild path — the rebuild must not fire for
  a datasource that is correctly indexed and simply has no tables.
- **Integration** (`services/agent/internal/discovery/`, real Mongo): write a
  cache, delete the rows to simulate the reaper, run `discoverSchemas` against a
  scripted provider, assert the rows are back in `project_schema_cache` with a
  fresh `cached_at` and that the returned map matches. Then assert a second call
  is a plain cache hit (no second `DiscoverSchemas`) — i.e. the repair sticks.

### Fix C
- **Unit** (`runner_test.go`, fake clientset + the log seam):
  - `Terminated.Message` carries a zap `FATAL` line → the extracted `error`
    field is recorded, not the raw tail;
  - `Message` empty, log tail carries the FATAL line → extracted from the tail;
  - `Message` empty, log tail empty → `Container exited with code 137: OOMKilled`
    (the existing expectation, now explicitly the third source);
  - `Message` empty, log tail is routine `INFO` progress only → must **not**
    report a progress line as the failure reason (the bug
    `TestExtractErrorMessage_RoutineLogLineIsNotAFailureReason` already guards
    for the other runners);
  - a clean `ExitCode: 0` container → still no message;
  - tail longer than 64 KiB → the **end** is kept (so the FATAL line survives);
  - `buildJob` sets `TerminationMessagePolicy: FallbackToLogsOnError` on the
    agent container, for discovery and for `RunSync`.
- **Integration** (`integration_test.go`, the existing K3s testcontainer, `make test-k8s`):
  create a Job whose container writes a realistic zap `FATAL` line to stderr and
  exits 1; assert `getPodErrorMessage` returns the agent's message and **not**
  `"Container exited with code 1: Error"`. This is the test that would have made
  the original diagnosis thirty seconds long, run against a real kubelet.
- **Unit** (agentserver): an early-phase error writes `failed` with the real
  message; `ErrAttemptSuperseded` and `ErrPauseRequested` write **nothing** (the
  existing exemptions must survive); a `Fail` that matches nothing (already
  terminal) is logged and does not change the exit path.

### Fix D
- **Unit** (`schema_index_test.go`): `ready` + empty cache → `cached: false`;
  `ready` + cache present → `cached: true` + `last_cached_at`; `cacheRepo == nil`
  → `cached: true` (no false alarm on a Qdrant-less build); a `LastCachedAt`
  error → 500 rather than a quietly wrong `cached` (do not invent readiness from
  a failed read).
- **UI** (`SchemaIndexPanel.test.tsx`): `ready` + `cached: false` renders the
  warning **even with `hideWhenReady`**; `ready` + `cached: true` + `hideWhenReady`
  renders nothing (no regression); the existing `needs_reindex` / `failed` /
  `indexing` states unchanged.

### Local gates before the PR
`make build`, `make test-go`, `make lint-go` (after
`export PATH=$PATH:$(go env GOPATH)/bin`), `make test-ui`, `make lint-ui`,
`make lint-docs`, and the Docker-backed `make test-integration` and
`make test-k8s`.

---

## 8. End-to-end verification

Unit and integration tests do not prove the chain is broken end to end, so:

1. Stand up the dev stack (Mongo + Qdrant on free ports — this VM runs several
   stacks; pick unused ports), index a small real warehouse, confirm
   `project_schema_cache` is populated and the panel says Ready.
2. `db.project_schema_cache.deleteMany({project_id: …})` — the reaper, by hand.
3. Confirm the panel now shows the cold-cache warning instead of a silent Ready.
4. Start a discovery run. Confirm: `phase_detail` says the cache is being
   re-discovered, the run completes, and `project_schema_cache` is repopulated
   with a fresh `cached_at`.
5. Boot the API against a database where `cached_at_1` still carries the TTL;
   confirm the drop happens once, the plain index is there, and a second boot is
   a clean no-op.
6. Force an agent failure (bad `QDRANT_URL`) on the Docker runner and confirm
   the run document carries the agent's own message from the new
   `runRepo.Fail` — the Kubernetes half is covered by the K3s integration test
   rather than a live cluster.

Anything I cannot verify will be stated as unverified in the PR, not implied to
be done.

---

## 9. Risks

| Risk | Mitigation |
|---|---|
| **A rebuild inside a run consumes the run's budget.** ~50 min on a 1,400-table warehouse. | Announced in `phase_detail` + a `Warn` before the slow part starts; bounded by `DISCOVERY_MAX_DURATION` (24h) inside the 25h Job deadline; a timeout now fails with a message that names the rebuild. Still strictly better than exiting 1 after 30s. After Fix A this is a one-time event per already-affected project. |
| **Cache / Qdrant divergence** when the warehouse's table set drifted since the last index. | Already handled: `cache_schema_provider.go:273` drops hits for tables absent from the cached map. Dropped tables stop answering; added tables are in the prompt catalog but not yet searchable. The next index run reconciles. Documented in the guide. |
| **Two runs rebuilding concurrently** could interleave `Save`'s delete-then-insert. | Narrow by construction: the API refuses a second concurrent discovery per project, and the index worker only claims `pending_indexing` while discovery requires `ready`. Noted in the rebuild helper's comment rather than papered over with a lock nobody can observe. |
| **Dropping the TTL leaks rows for a datasource removed from a project.** | Self-limiting: `/reindex` calls the project-wide `Invalidate`, and project delete purges the collection. No action; documented. |
| **`InitDatabase` fails the boot if the drop fails.** | Deliberate, and consistent with how `CreateMany` errors are already treated: running the new code with the reaper still armed is the bug this issue is about. `IndexNotFound` / `NamespaceNotFound` are not failures. |
| **Index rebuild on a large collection at API start.** | Conditional drop — it happens exactly once, on the first boot after deploy, on a single-field index. |
| **`extractErrorMessage` over a kubelet-supplied tail could mis-pick a line.** | It is the function the other two runners already trust, with existing tests for the routine-progress trap; the third source (exit code + reason) still backstops it, and the full tail goes to `apilog`. |
| **Reversing #184's documented no-fallback rule.** | This is the decision in §2, surfaced for sign-off rather than buried. The rule is narrowed, not deleted: the catalog pass is still never re-run on a cache hit, and the repair is once-per-cold-cache, loud, and LLM-free. |

---

## 10. Alternatives considered

1. **TTL removal alone, no rebuild.** Smallest possible change and it stops
   future breakage — but every project already past day seven stays broken until
   a human clicks Re-index, and the issue's central complaint is that the
   remedy "just isn't applied automatically". Rejected as a half-fix.
2. **Auto-trigger a full `/reindex` from the discovery gate.** `gateProjectForRun`
   detects `ready` + cold cache, flips to `pending_indexing` via the existing
   `BeginReindex` → `Invalidate` → `DropCollection` path, and returns a 409
   saying a re-index started. Much smaller, uses machinery that already owns the
   lock, the progress document, the run record and the UI. **Rejected because it
   spends LLM money the user did not ask for** — a full re-index drops the
   Qdrant collection and regenerates every blurb — and because the run the
   operator asked for still does not happen. This is the strongest alternative;
   if the implicit-rebuild-in-the-run idea is unwelcome, this is the fallback I
   would build instead, and it would be the whole of Fix B.
3. **Run the full indexer inline from the agent.** Satisfies "re-index, then
   continue" literally. Rejected: it duplicates the API-owned index lifecycle,
   needs an embedding provider and a blurb LLM inside a discovery run, costs
   LLM spend for blurbs that never expired, and would race the index worker's
   Qdrant writes.
4. **Keep a TTL but make it long, or refresh `cached_at` on read.** A refresh on
   read is a write on every discovery and still breaks a project that idles for
   a week; a longer TTL moves the cliff instead of removing it. Rejected: a
   store that discovery hard-depends on should not have a cliff.
5. **Flip `GetStatus` to report `needs_reindex` for a cold cache.** Fixes the UI
   with no new field and no UI change — the panels already render that state.
   Rejected: it would disable the Run button via
   `app/projects/[id]/page.tsx:427-430`, which is exactly the run that now
   repairs the cache; and it would put a status string on the wire that no
   document in the database holds.
6. **Persist the pod-log tail into `discovery_debug_logs`.** The issue offers it
   as an option. Rejected: the API has no writer for that collection (the agent
   owns it), and once the agent writes its own `failed` status with the real
   message the marginal value is small. The full tail goes to `apilog`.
7. **A `source: "cache" | "live"` field on `GET /schema-cache/tables`** to settle
   the two-endpoints-disagree confusion the issue notes. Rejected for this PR:
   that live `--list-tables` fallback is deliberate (it populates the
   discovery-scope picker *before* the first index, `schema_index.go:805-813`),
   the disagreement only appears in the cold-cache state this PR removes, and no
   UI consumes the field today — adding one nobody reads is Rule 6 debt. Happy
   to open a follow-up issue if the diagnostic value is wanted on its own.

---

## 11. Out of scope

- Rebuilding Qdrant points or blurbs during a discovery run (§9, §10.3).
- Auto-starting the requested discovery run *after* an auto-triggered re-index —
  that needs new queued-run state and is a feature, not this fix.
- Per-datasource `?warehouse_id=` on `/cache-info`. `DatasourceCachedAt` already
  exists and would make the per-datasource panel exact rather than project-wide;
  it is not needed to stop the lie, so it is a follow-up if wanted.
- Touching the live-listing fallback on `/schema-cache/tables` (§10.7).

---

## 12. Open questions for review

1. **§2 — is the rebuild-inside-the-run the approach you want**, or the
   API-side auto-reindex (§10.2)? Everything else in this plan is independent of
   that choice.
2. **Fix A — is removing the TTL outright acceptable**, given the collection
   becomes durable storage that project-delete and `/reindex` are the only
   reapers for? The alternative is keeping a very long TTL, which I argue is
   just a further-away cliff.
3. **Fix C — one line or the whole tail in the run's `error`?** I have argued for
   the extracted FATAL line in Mongo and the full tail in the API log; say the
   word and the tail goes in the error field instead.

---

**This is a PLAN for review — no implementation has been written. Implementation
follows after approval.**

Closes #522

— Co-coded with Jale 🤖
