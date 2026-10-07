# Discovery run resume (v1): checkpoint exploration & crash recovery

Implementation plan for issue #438. Verified against `main` @ `3964272`.

---

## 1. Problem

A discovery run's cost is concentrated in **Phase 3 (exploration)**: N agentic LLM calls plus N warehouse queries. Today that phase has no durability.

What exists on `main`:

- `ExplorationEngine.Explore` (`services/agent/internal/ai/exploration.go:575`) holds every step in memory (`result.Steps`) and returns them to the orchestrator.
- The only out-of-process trace during exploration is (a) a live `RunStep` row per step in `discovery_run_steps` (summary text, no digest, no replayable args — written by `StatusReporter.AddExplorationStep`), and (b) a per-run Qdrant collection of step *vectors* (`discovery.RunStepIndex`), which carries a payload of `step`/`purpose`/`row_count`/`has_error` and nothing replayable.
- The durable record of what exploration actually produced is written only at the **tail**: `orchestrator.go:1439` (`discoveryRepo.Save`) and `orchestrator.go:1444` (`persistSplitLogs` → `discovery_exploration_steps`).
- The per-run Qdrant collection is dropped **unconditionally** on any exit, success or failure (`orchestrator.go:964-973`).
- A process that dies anywhere before the tail loses 100% of exploration. The API's crash paths (`runner` `OnFailure` → `runRepo.Fail`, and `RunRepository.CleanupStaleRuns` on API boot) mark the run `failed`, and `failed` is terminal — there is no way back in.

The cheap failure modes already degrade gracefully: a failed analysis area → `partial` (`orchestrator.go:1377-1383`); broken recommendation citations → citation recovery (`orchestrator.go:1301-1313`); agent-never-launched → `policy.RefundIfMetered` (`discoveries.go:443`). The unhandled gap is exactly one thing:

> **the most expensive phase, combined with whole-process death, loses paid-for work with no retry.**

### What v1 must deliver

1. A run killed mid-exploration can be resumed and completes, re-using already-executed steps instead of re-querying them.
2. A run that died *after* exploration completed resumes into analysis, making **zero** exploration LLM calls and **zero** warehouse queries.
3. Resume re-enters the **same `runID`**; insights/recommendations are not duplicated and the re-save does not crash on the `discovery_recommendation_log` unique index on `discovery_id`.
4. Cancel stays a terminal hard-kill, unchanged.
5. Self-hosted: resume works with no metering interaction. Metered: no double-charge, no refund-then-free-resume leak.

---

## 2. Design decisions (and where this plan deviates from the issue)

The issue's design is sound and this plan follows it. Five places where implementation detail forced a decision, plus three gaps found while verifying against `main`:

### 2.1 Replay rule: re-execute the cheap actions, replay the expensive one from its digest

`Explore` builds the conversation as `initial message` → (`assistant` action, `user` result)*. To re-enter at step N+1 the engine must reconstruct those turns. The result message for a `query_data` step can be rendered from the persisted digest, but a `lookup_schema` step's result is a block of column/sample detail we deliberately do not persist, and a `search_tables` step's result is a ranked table list.

So replay is split by cost:

| Action | Replay |
|---|---|
| `query_data` | **Never re-executed.** Result message rendered from the persisted `CompactResult` digest. This is the whole point of the feature. |
| `lookup_schema` | **Re-executed** against `CacheSchemaProvider` — a map + Mongo read, no warehouse traffic, no LLM. |
| `search_tables` | **Re-executed** — one embedding call + one Qdrant query. No LLM, no warehouse. |
| `get_correlations` | **Re-executed** — an in-process plugin lookup. Free. |
| `complete_rejected` | Result message is the nudge, re-derived from a persisted reason class. |

Re-executing the schema actions through their real code path means `lookupsUsed`, `searchesUsed`, `correlationLookupsUsed` and `fetchedTables` are restored **as a side effect** rather than being separately seeded — so a resumed run cannot be handed a fresh schema-lookup budget, and the "N/M used, K remaining" lines the engine appends (`exploration.go:1864,1914`) come back correct without any special-casing. That removes a whole class of state-seeding bugs.

### 2.2 No resume preamble in the conversation

The replayed conversation *is* the transcript: the model sees its own prior actions and the last result, and answers with the next action. No synthetic "you were resumed" message is injected. Nothing in the loop needs it, and `buildInitialMessage`'s budget announcement is superseded by the per-action "remaining" lines that replay re-renders.

### 2.3 `RunOptions.Resume bool`, not `RunOptions.ResumeRunID string`

The issue specifies `RunOptions.ResumeRunID`. Resume re-enters the **same** `runID`, which `RunOptions.RunID` already carries — a second field holding the same id is a divergence waiting to happen. The runner gets `Resume bool` + `Attempt int`; the agent gets `--resume`. Deliberate deviation, flagged here.

### 2.4 Phase 7/8 idempotency: write the new attempt, then retire the superseded one

The issue says "upsert by `(run_id, …)`". Upserting the `discoveries` document in place keeps `discovery_id` stable, but the derived rows (`insights`, `recommendations`, the four split-log collections, the Qdrant points) carry freshly-minted UUIDs on the new attempt, so an in-place upsert still requires deleting the old derived rows **before** writing the new ones — which leaves a window where the project has no visible result at all.

This plan inverts the order:

1. `Save` the new attempt's `DiscoveryResult` (a fresh `_id`, stamped with `run_id`).
2. `persistSplitLogs` under that new `discovery_id` — a clean insert, so the unique index on `discovery_recommendation_log.discovery_id` is never violated. **The duplicate-key crash is structurally impossible rather than ordering-dependent.**
3. Phase 8 writes `insights` / `recommendations` / Qdrant points under the new `discovery_id`.
4. **Then** retire every *other* `discoveries` document with this `run_id`, along with its split-log rows, its standalone insights/recommendations, and their Qdrant points.

There is no moment where the project is missing a result, retrying the retire step is a no-op, and the common resume case (crash mid-exploration, no prior `discoveries` doc) short-circuits to nothing. Noted as a deviation; the in-place upsert is in §10 Alternatives.

### 2.5 Gap found: **the verifier reads raw rows, so a digest-only checkpoint silently degrades validation**

The issue states "Analysis needs only the digest" — true for Phase 4 (`render_query_results.go:77-83` reuses `CompactResult`). It does not hold for Phase 4.5 / 5.5. Two places in `services/agent/internal/validation/verifier/` read `step.QueryResult` directly:

- `bundle.go:282-283` — `digestStep` sets `FullRowCount: len(s.QueryResult)` and samples up to `cfg.SampleRows` rows into the verifier's evidence bundle.
- `tools.go:178-191` — the `read_step_rows` tool pages `s.QueryResult`.

A replayed step has no raw rows. Left alone, the verifier would be told a 50 000-row result had **0 rows** and would refute or mark unverifiable insights that are in fact sound — and validation gates recommendation generation (`filterEligibleInsights`, `orchestrator.go:1287`). That is a correctness regression introduced *by* resume, so it is in scope.

**Fix — v1 persists the sample, it does NOT re-verify.** §2.4 keeps a **bounded ≤50-row sample** of `QueryResult` in the checkpoint (`cfg.SampleRows`, default 50, cell-char-normalised) rather than stripping rows entirely. The verifier's evidence bundle is *already* a ≤50-row sample on the live path (`digestStep` caps at `cfg.SampleRows`), so on resume `digestStep` rebuilds a **byte-identical bundle** from the persisted sample — no re-query, no regression, for the whole bundle path (Phase 4.5 and 5.5).

**One line makes "byte-identical" literally true, and it is not optional.** `digestStep` derives the full count from the slice it holds — `fullCount := len(s.QueryResult)` (`bundle.go:282`) — and `ReadStepRows` does the same with `total := len(s.QueryResult)` (`tools.go:178`). Fed a 50-row sample of a 50 000-row step, both report **`full_row_count: 50`** and **`truncated: false`**: the verifier is told the step returned exactly fifty rows and that it holds all of them. That is a *confident* falsehood, strictly worse than the "0 rows" it replaces, because nothing in the bundle flags it. Both sites therefore read the authoritative count instead:

```go
fullCount := s.RowCount                                   // == len(QueryResult) on the live path
if fullCount < len(s.QueryResult) { fullCount = len(s.QueryResult) }  // defensive: odd historical rows
rows := s.QueryResult
if len(rows) > sampleCap { rows = rows[:sampleCap] }
truncated := len(rows) < fullCount
```

Live path: identical output for every case (`RowCount == len(QueryResult)`, `queryexec/query_executor.go:262`). Resumed step: `full_row_count: 50000`, `truncated: true`, fifty sample rows — byte-identical to live. This also protects the **manual re-validation** path, which reads `discovery_exploration_steps` (`validate_doc.go:175`) and will find the ≤50-row sample there for a resumed run's pre-crash slice.

The only residual is `read_step_rows` paging **past** the sample (it reads the full in-memory `QueryResult`, ≤200/call). On a replayed step a page past the retained sample returns `rows: [], truncated: true`, which the verifier is already told to turn into `unverifiable` — **not** a false `rejected` (`tools.go:148-156`, `bundle.go:80`). So deep paging degrades gracefully; it does **not** re-run SQL. `RowsRetained: false` on `SourceStepDigest` marks the out-of-sample case; `ReadStepRows` returns `rows_retained: false` for pages past the sample.

Re-query is explicitly **rejected as the default**: it spends warehouse money on the common path and re-runs the SQL against **drifted** data (the warehouse may have changed since the insight was computed), so it can confirm/refute against different rows than the insight was built on — a correctness risk, not a clean fallback. The drift risk is reduced, not abolished: `query_warehouse` stays in the verifier's tool set and `SourceStepDigest`'s own doc comment (`bundle.go:78-81`) tells the agent an out-of-snapshot offset means "run query_warehouse **or** mark unverifiable". So the model may still re-query on its own initiative — what changes is that nothing in the design *pushes* it there.

**Instrument so the full-row question stays an evidence decision, not a guess** (see epic follow-up): record per verdict the max `read_step_rows` offset reached, whether a call hit the 200 clamp, and whether a truncated read forced a claim to `unverifiable`. Today nothing records paging depth — `verdict.go:23-25` keeps only the `step_reads_used` count.

**Audit-fidelity facts (accepted, documented):** a normal run, and the *live* portion of a resumed run, keep full rows at the tail (`discovery_exploration_steps`) unchanged. Only the **pre-crash slice of a resumed run** carries the ≤50-row sample there instead of full rows (its rows died with the crashed process). That is strictly better than a digest-only checkpoint — a sample, not nothing — and it affects only replay/fine-tuning over that slice, never the run's insights/recommendations.

### 2.6 Gap found: the resumed run would re-read its own partial result as "previous context"

`loadPreviousDiscoveryContext` (`orchestrator.go:2511`) loads the last 5 discoveries for the project and feeds them into the exploration + analysis prompts as "do not re-tread these". On a resumed run the prior attempt's own partial `discoveries` document is in that list, so the run would be told not to repeat its own findings. Fixed by filtering `disc.RunID == o.runID` in the orchestrator — which needs §3.2's `run_id` on `DiscoveryResult` anyway. No repo change.

### 2.7 Gap found: the K8s Job name collides on resume

`KubernetesRunner.Run` names the Job `discovery-<runID[:20]>` and sets `ttl: 3600` (`kubernetes.go:234,272`). Re-entering the same `runID` within an hour of the previous Job finishing fails with `AlreadyExists` — i.e. resume would be broken on the production runner. The name gains an attempt suffix for attempts > 1 only (`discovery-<runID[:20]>-a2`, 33 chars, well inside the DNS-1123 63-char limit), so attempt 1's name is byte-identical to today. The Docker runner passes an empty container name (auto-generated, `docker.go:263`) and the subprocess runner keys a map by `runID`, so neither collides.

A suffixed name immediately breaks the other half of the contract: `KubernetesRunner.Cancel` re-derives the un-suffixed name (`kubernetes.go:703`) and would 404 on a resumed run — so "Cancel remains a terminal hard-kill" would quietly stop being true for exactly the runs this feature creates. Cancel therefore stops deriving a name and deletes by the `run-id` label the Job already carries (`kubernetes.go:266`), which is attempt-agnostic and matches what the Docker runner's Cancel already does (`docker.go:786`). One Job matches on a non-resumed run, so behaviour there is unchanged; the "no such Job" signal is preserved by listing first and returning `NotFound` when nothing matches, so the existing runner test expectations still hold.

### 2.8 Gap found: the run document does not record the run's own parameters

`RunRepository.Create(ctx, projectID)` (`services/api/database/run_repo.go:27`) stores nothing about `max_steps`, `min_steps`, `areas` or `effort`. A resumed run would therefore be spawned with the agent's defaults (`--max-steps 100`, no floor) rather than the budget the operator chose. `Create` gains those parameters and persists them; resume replays them verbatim.

---

## 3. Data model

### 3.1 New collection: `discovery_checkpoints`

One small document per exploration step, plus one summary document. **Never an embedded array** — embedded `[]RunStep` / `[]ExplorationStep` arrays already hit the 16 MB BSON limit historically and forced the collection split (`services/agent/internal/database/discovery_log_repo.go:1-10`, `libs/go-common/mongodb/collections.go:14-20`).

Constant `CollectionDiscoveryCheckpoints = "discovery_checkpoints"` added to `libs/go-common/mongodb/collections.go` (both services address it).

Document shape (`services/agent/internal/database/discovery_checkpoint_repo.go`), embedding `models.ExplorationStep` inline exactly as `ExplorationStepDoc` does so the existing BSON tags stay authoritative:

```go
type ExplorationCheckpointDoc struct {
    RunID      string    `bson:"run_id"`
    ProjectID  string    `bson:"project_id"`
    StepNumber int       `bson:"step_number"`   // 1..N; 0 = the exploration summary
    Attempt    int       `bson:"attempt"`       // which attempt wrote it (observability)
    Kind       string    `bson:"kind"`          // "step" | "exploration_summary"
    CreatedAt  time.Time `bson:"created_at"`    // TTL anchor

    models.ExplorationStep `bson:",inline"`     // full rows truncated to a ≤50-row sample; fix history dropped — see below

    // Replay args the step struct does not carry — the action the model
    // emitted, so the turn can be reconstructed.
    Datasource     string   `bson:"datasource_id,omitempty"`
    LookupSchema   []string `bson:"lookup_schema,omitempty"`
    SearchTables   string   `bson:"search_tables,omitempty"`
    SearchTopK     int      `bson:"search_top_k,omitempty"`
    CorrelationA   string   `bson:"correlation_a,omitempty"`
    CorrelationB   string   `bson:"correlation_b,omitempty"`
    RejectReason   string   `bson:"reject_reason,omitempty"` // floor|productive|unproven

    // kind == "exploration_summary" only
    Completed     bool   `bson:"completed,omitempty"`
    CompletionMsg string `bson:"completion_msg,omitempty"`
    TotalSteps    int    `bson:"total_steps,omitempty"`
    DurationMs    int64  `bson:"duration_ms,omitempty"`
}
```

**What is deliberately stripped** before writing, in a pure helper `checkpointPayload(models.ExplorationStep) models.ExplorationStep` so it is unit-testable without Mongo:

- `QueryResult` (raw rows) — **truncated to a bounded ≤50-row sample** (`cfg.SampleRows`, cell-char-normalised); the full set is dropped and the digest (`CompactResult`) rides alongside. The cap and the per-cell char cap are **parameters** of `checkpointPayload`, not constants in it: `verifier`'s `normaliseRow` is unexported and `database` must not import `verifier`, so the orchestrator — which already holds `o.validationCfg.Bundle` — passes `SampleRows` and `CellCharCap` down. One pure, testable function owns the invariant; the numbers come from the verifier's own config so the two cannot drift. The digest is what `RenderCompactedSteps` prefers for analysis; the sample is what the verifier's bundle consumes on resume (§2.5). Both are O(1) in result size, so the document stays small no matter how many rows the step returned.
- `FixHistory` — each entry carries a full SQL-fix prompt and response; it is audit data, written once at the tail, not checkpoint data.
- `LLMRequest` / `LLMResponse` — unpopulated by `Explore` today; stripped so they stay that way here.

**Kept** (all load-bearing): `Step`, `Timestamp`, `Action`, `Thinking`, `Query`, `QueryPurpose`, `WarehouseID`, `RowCount`, `ExecutionTimeMs`, `Fixed`, `FixAttempts`, `Error`, `TokensIn`, `TokensOut`, `CompactResult`, and **`Quality`** — the issue's field list omits `Quality`, but `attachSourceQuality` (`orchestrator.go:1242`) derives every insight's evidence label from it, and it is knowable nowhere else (`models/discovery.go:516-528`). A resumed run without it would silently relabel findings computed over withheld rows as sound.

Size: one step = the digest (≤20 rows verbatim below `CompactInlineThreshold`, else 5 head + 5 tail + column statistics) plus thinking text. Orders of magnitude below 16 MB, and bounded independently of run length because each step is its own document.

**Indexes** (`EnsureIndexes`, called at agent and API startup):

| Index | Purpose |
|---|---|
| `{run_id: 1, step_number: 1}` **unique** | Ordered read of the prefix; makes the per-step write a `ReplaceOne(upsert)` so a retried write replaces rather than duplicates. |
| `{created_at: 1}` TTL, named `ttl_discovery_checkpoint` | Retention. |

### 3.2 `discoveries` gains `run_id`

`DiscoveryResult` (`services/agent/internal/models/discovery.go:24`) and its API mirror (`services/api/models/discovery.go`) gain `RunID string bson:"run_id,omitempty"`. Today the link is one-way (`DiscoveryRun.DiscoveryID` forward only), which is why nothing can find "the discoveries this run produced". Needed by §2.4 (retire superseded attempts) and §2.6 (exclude own partial result). Index `{run_id: 1}` on `discoveries` in `services/api/database/init.go`.

### 3.3 `discovery_runs` lifecycle metadata

Added to both `services/agent/internal/models/run.go` and `services/api/models/run.go` (same collection, mirrored schemas):

| Field | Written by | Meaning |
|---|---|---|
| `attempt int` | API (`Create` = 1, `BeginResume` `$inc`) | Attempt counter. The handle a future resume charge would key on (`runID:attempt`). |
| `last_resumed_at *time.Time` | API `BeginResume` | When the latest attempt was requested. |
| `last_checkpoint_step int` | agent, in the per-step run-doc write `AddExplorationStep` already makes | Highest checkpointed step. Drives the dashboard's Resume affordance; zeroed on `Complete`. |
| `active_ms int64` | agent, `$inc` on Complete/Fail | Cumulative **active** compute time across attempts. |
| `lifecycle []RunLifecycleEvent` | both, `$push` | Append-only `{status, at, reason, attempt, llm_provider, llm_model}`. Bounded by attempt count. |
| `max_steps`, `min_steps`, `areas`, `effort` | API `Create` | The run's own parameters, so resume replays the same budget (§2.8). |

Two same-`runID` gotchas the issue names, resolved:

- **Duration must be cumulative active time.** `result.Duration` today is `time.Since(startTime)` for the current process. On resume the orchestrator reads the run's prior `active_ms` and reports `prior + this attempt`, and `$inc`s its own elapsed on the terminal write. The dashboard's `elapsed` (which computes `updated_at - started_at`, `page.tsx:667`) uses `active_ms` when present, so a run resumed the next morning no longer reports 14 hours of "work".
  - *Caveat — best-effort on hard crashes.* The `$inc` lands at the terminal write, so an attempt killed before it reaches `Fail` (OOM / pod kill → marked failed out-of-process by the sweeper) never records its slice; a proxy-504 usually leaves the agent alive enough to write it. So `active_ms` is the active time of attempts that ended cleanly enough to record it — a slight undercount on hard crashes, which we accept (a dead attempt's compute is not worth counting).
- **Stamp the model version per attempt.** Nothing on `main` stamps a run-level LLM model at all (`DiscoveryRun` has no LLM fields; provenance lives on debug-log rows). Rather than inventing a run-level field that only resume reads, the per-attempt `lifecycle` event carries `llm_provider` + `llm_model` — which is exactly the question the gotcha asks ("which model ran which attempt").

### 3.4 Retention

`DISCOVERY_CHECKPOINT_RETENTION` (Go duration, default `48h`), read via `config.GetEnv…`. **Configurable rather than a named constant** because the correct value is a function of `DISCOVERY_MAX_DURATION`, which is itself an env var: a deployment that raises the run cap (or sets it to `0` to disable it) must raise retention to match, or a long run's early checkpoints expire while it is still running. Rule 2.

TTL indexes cannot be mutated in place — `CreateOne` with a different `expireAfterSeconds` returns `IndexOptionsConflict`. `EnsureIndexes` therefore creates the index under a fixed name and, on that specific conflict, drops and recreates it, so changing the env var takes effect on the next agent/API boot instead of failing startup. Covered by an integration test.

Checkpoints are also **deleted explicitly** on terminal-and-not-resumable outcomes, with the TTL as the backstop:

- run completes successfully → orchestrator deletes them after `finalizeStatus` succeeds (best-effort, logged).
- run is cancelled → `CancelRun` deletes them (`cancelled` stays terminal per the issue).

### 3.5 Per-run Qdrant step index

Two changes, and they are complementary rather than redundant:

- **`Drop` becomes conditional** (`orchestrator.go:964-973`): skipped when the run is ending resumable, so a resume reuses the warm index instead of re-embedding.
- **Replay re-indexes anyway.** `embedTextForStep` (`run_step_index.go:166`) uses only `QueryPurpose` + `Query`, both checkpointed, and the point id is deterministic in `(runID, step)` (`stepPointID`). So re-indexing a replayed prefix is exact and idempotent — a no-op upsert when the collection survived, a full rebuild when it was swept. Resume does not *depend* on the collection surviving, which is what makes it robust against a hard kill, a `applog.Fatal` (defers do not run), or a sweep that already fired.

`loadActiveRunIDs` (`agentserver.go:1320`) additionally keeps run ids that still have checkpoints, so the boot sweep does not drop a resumable run's collection. Bounded by the TTL.

---

## 4. Changes by area

### 4.1 `libs/go-common`

- `mongodb/collections.go` — `CollectionDiscoveryCheckpoints`.
- `models/compact_result.go` — unchanged.

### 4.2 Agent — persistence

- **New** `services/agent/internal/database/discovery_checkpoint_repo.go`:
  - `SaveStep(ctx, projectID, runID string, attempt int, step models.ExplorationStep, args CheckpointArgs) error` — `ReplaceOne(filter{run_id, step_number}, upsert)`.
  - `SaveExplorationSummary(ctx, projectID, runID string, attempt int, res ExplorationSummary) error` — `step_number: 0`.
  - `LoadPrefix(ctx, runID) (*CheckpointSet, error)` — reads ascending and returns the **contiguous prefix** `1..K` only, stopping at the first gap, plus the summary if present. A hole means an earlier write failed; replaying a conversation with a hole in it would misnumber every later step, so the honest answer is "resume at the hole".
  - `DeleteByRun(ctx, runID) (int64, error)`.
  - `ListRunIDsWithCheckpoints(ctx) ([]string, error)` — distinct `run_id`, for the sweep.
  - `EnsureIndexes(ctx)` — unique compound + TTL with conflict-recreate.
  - `checkpointPayload(step)` — the pure strip helper.
- `database/discovery_log_repo.go` — `DeleteByRun(ctx, runID)` across the four split collections (every row already carries `run_id`), used by the retire step.
- `database/discovery_repo.go` — `ListByRun(ctx, runID)`, `DeleteByID(ctx, id)`.
- `database/run_repo.go` — `MarkExplorationCheckpoint(ctx, runID, step)`, `AddActiveTime(ctx, runID, d)`, `AppendLifecycle(ctx, runID, ev)`, `GetByID` already exists. `Complete`/`Fail` additionally `$inc` `active_ms`, `$push` the lifecycle event, and `Complete` zeroes `last_checkpoint_step`.
- `database/embed_index_repo.go` / `discovery/embed_index_store.go` — `DeleteByDiscovery(ctx, discoveryID) (insightIDs, recIDs []string, err error)`: collect the point ids, then delete the Mongo rows, so the caller can delete the matching Qdrant points via the existing `vectorstore.Provider.Delete(ctx, ids)`. No new vectorstore method.

### 4.3 Agent — exploration engine (`services/agent/internal/ai/exploration.go`)

- `ExplorationEngineOptions` gains:
  - `PersistStep func(ctx context.Context, step models.ExplorationStep, args StepArgs) error` — the **widened step hook** the issue asks for (design item 4). Called once per completed step with the full `models.ExplorationStep` plus the action args. Failure is logged and swallowed, exactly like a step-index failure: a checkpoint write must never abort the run (and a gap is handled by `LoadPrefix`).
  - `Resume *ResumeState` — the contiguous prefix of steps plus their replay args.
- `StepCallback` (`exploration.go:273`) is unchanged — it stays the live-UI summary hook. The persist hook is a second, separate seam, because the two differ in both payload and failure semantics.
- New `exploration_resume.go`:
  - `replayPrefix(ctx, conversation, resume)` — for each step: append the canonical assistant turn, then the result message (re-executed or digest-rendered per §2.1), append the step to `result.Steps`, and **do not** call `onStep` (the live run-step rows and the run-doc counters already exist for these steps from the previous attempt; re-emitting would duplicate the feed and double-count `total_queries` / `schema_lookup_calls`).
  - `replayedActionJSON(step, args)` — a small `omitempty`-everywhere struct marshalled with `encoding/json`, so the replayed assistant turn is deterministic and golden-testable.
  - `renderReplayedQueryResult(step)` — the user message for a replayed `query_data` step, built from `CompactResult`. Exact when `AllRows` is present (`RowCount <= CompactInlineThreshold`, 20); otherwise 5 head + 5 tail rows against the live path's 10, and the message **says** the rows shown are a retained summary of `RowCount` rows rather than implying the result was that small. Caveats (`gowarehouse.CaveatInstruction`) are re-rendered in front of the rows as `formatQuerySuccess` does.
- `Explore` — when `Resume` is set and non-empty, replay before entering the loop and start the loop at `len(resume.Steps)+1`. `maxSteps` and the stop rule are untouched: `acceptDone(step, …)` already compares the absolute step number against `minSteps`, so a run resumed past its floor accepts a `done` immediately, which is correct.
- The persist hook is also called from the `complete_rejected` branch (`exploration.go:640-648`), with the reason class so the nudge can be re-derived.

### 4.4 Agent — orchestrator (`services/agent/internal/discovery/orchestrator.go`)

- `Orchestrator` gains a `checkpointRepo` field, held as a small interface (`explorationCheckpointStore`) and normalised typed-nil→nil exactly as `discoveryLogRepo` is (`orchestrator.go:446-453`), so unit tests inject a fake without Mongo and a nil repo disables checkpointing.
- `DiscoveryOptions` gains `Resume *ResumeState` (attempt, prior active time, prefix, summary).
- `RunDiscovery`:
  - wires `PersistStep` into the engine;
  - re-indexes the replayed prefix into `runStepIndex` once, before either exploration branch, so the analysis picker sees the whole run;
  - branches: summary says exploration completed → synthesize the `ExplorationResult` from checkpoints and **skip `Explore` entirely** (acceptance criterion 2 — zero LLM calls, zero warehouse queries); otherwise call `Explore` with the prefix;
  - filters the run's own partial result out of previous-context (§2.6);
  - stamps `result.RunID`;
  - reports cumulative duration (§3.3);
  - after a successful `finalizeStatus`: retire superseded attempts (§2.4) and delete the checkpoints;
  - the deferred `Drop` consults a `keepStepIndex` flag set as soon as the first checkpoint lands and cleared once checkpoints are deleted (§3.5).
- `SetPhase` detail on a resumed run names what is happening — `"Resuming exploration at step 42"` / `"Exploration already complete — resuming at analysis"` — so the live panel explains itself.
- New `orchestrator_resume.go` holds the replay/retire helpers; `orchestrator.go` is already 3 085 lines and this keeps the diff readable.

### 4.5 Agent — verifier (`services/agent/internal/validation/verifier/`)

`bundle.go` and `tools.go` per §2.5: both switch to `s.RowCount` as the authoritative full count (the change that makes the bundle byte-identical rather than merely similar), and the bundle is served from the persisted ≤50-row sample; `read_step_rows` past the sample returns `truncated: true` → the verifier marks the claim `unverifiable` (no re-query fallback); `RowsRetained bool` on `SourceStepDigest` marks the out-of-sample case. Plus the paging instrumentation (max offset, 200-clamp hits, truncation→`unverifiable`) so the full-row follow-up is decided on data. Live path unchanged (`RowCount == len(QueryResult)`, and the sample *is* the bundle).

### 4.6 Agent — entrypoint (`services/agent/agentserver/agentserver.go`)

- New flag `--resume` (bool): "Resume the run named by --run-id from its last exploration checkpoint instead of starting fresh."
- `runDiscovery(..., resume bool)`: when set, read the run document (for `attempt` and `active_ms`), `LoadPrefix` the checkpoints, build the `ResumeState`, and fail fast with a clear error when there is nothing to resume from (the API checks first, but a TTL expiry can race).
- `loadActiveRunIDs` keeps run ids with live checkpoints (§3.5).
- `discoveryCheckpointRepo.EnsureIndexes` added to the startup index block.

### 4.7 API

- `services/api/database/run_repo.go`:
  - `Create(ctx, projectID string, params models.RunParams) (string, error)` — persists `max_steps` / `min_steps` / `areas` / `effort`, `attempt: 1`, and the first lifecycle event.
  - `BeginResume(ctx, runID) (*models.DiscoveryRun, error)` — a single `FindOneAndUpdate` with filter `{_id, status: "failed"}`, `$inc{attempt: 1}`, `$set{status: "running", last_resumed_at, phase_detail}`, `$push{lifecycle}`, returning the updated document. **The atomic filter is the race guard**: a double-clicked Resume matches nothing the second time and gets a 409 rather than two agents on one run.
  - `interfaces.go` `RunRepo` grows `Create` (changed signature) + `BeginResume`; the handler fakes are updated.
- `services/api/database/discovery_checkpoint_repo.go` — read-side only: `ResumeState(ctx, runID) (prefixLen int, explorationComplete bool, err error)` and `DeleteByRun`.
  `interfaces.go` gains a `CheckpointRepo` interface.
- `services/api/internal/handler/discoveries.go`:
  - `ResumeRun` — `POST /api/v1/runs/{runId}/resume`. Order: load run (404) → `status == failed` (409 with the actual status) → project exists and passes the **same lifecycle + schema-index gates as `StartRun`** (extracted into a shared `gateProjectForRun(p)` helper — resume re-enters exploration and needs a ready schema index) → checkpoints exist (409 `"no checkpoint to resume from — it expired or was never written"`) → no *other* active run for the project (409 `AlreadyRunningError`) → `BeginResume` (409 if it matched nothing) → spawn via `agentRunner.Run` with `Resume: true`, `Attempt`, and the persisted parameters → 202 `{status: "resumed", run_id, attempt}`. On spawn failure: `runRepo.Fail` with the reason, leaving the run resumable again.
  - **No metering and no new policy reservation.** `policy.ChargeIfMetered` is *not* called (the charge is already keyed on `runID`, so resume is free by construction), `policy.RefundIfMetered` is not called anywhere new, and no `CheckStartDiscoveryRun` reservation is opened — opening one would consume another runs-per-period slot, i.e. a hidden charge. The repo-level one-run-per-project check is applied unconditionally on the resume path (not only under `NoopChecker`), so concurrency is still bounded per project. See §9 for the cross-project limit this leaves.
  - `CancelRun` additionally deletes the run's checkpoints — `cancelled` is terminal and not resumable.
- `services/api/internal/server/server.go` — `mux.HandleFunc("POST /api/v1/runs/{runId}/resume", withRole(member, discoveries.ResumeRun))`. `member`, matching `POST .../discover` (resume starts work); cancel stays `admin`.
- `services/api/internal/runner/` — `RunOptions` gains `Resume bool` + `Attempt int`; all three runners forward `--resume`; `kubernetes.go` suffixes the Job name for attempts > 1 and switches `Cancel` to label-based deletion (§2.7).
- `services/api/database/init.go` — `discovery_checkpoints` entry (unique + TTL) and `{run_id: 1}` on `discoveries`.

### 4.8 Dashboard (`ui/dashboard`)

Minimal, matching the issue's "a Resume affordance on a failed run" — **no Pause, no two-CTA UX** (that is a later increment).

- `src/lib/api.ts` — `resumeRun(runId)`; `DiscoveryRunStatus` gains `attempt?`, `last_checkpoint_step?`, `active_ms?`.
- `src/app/projects/[id]/page.tsx` — `LiveRunPanel` renders a **Resume** button beside Dismiss when `run.status === 'failed' && (run.last_checkpoint_step ?? 0) > 0`, with the sub-label `Resume from step N`. On click: `api.resumeRun`, optimistic `status: 'running'`, resume polling. Errors surface the API's message verbatim (a 409 for an expired checkpoint is a real answer, not a bug). `elapsed` prefers `active_ms`.
- Overlay check: the build step must confirm whether `app/projects/[id]/page.tsx` is present in the private dashboard overlay and sync it if so — an overlaid file completely replaces the community version, so an unsynced change is silently lost.

### 4.9 Docs (Rule 4)

- `docs/concepts/discovery-lifecycle.md` — a "Checkpointing and resume" section after Phase 3 (what is persisted per step, what is deliberately not, what replay re-executes vs. replays, what a resumed run skips), the `run_id` on the Phase 7 payload, and new rows in the Error Handling table for agent crash / API restart ("run marked failed — **resumable** from the last checkpoint").
- `docs/reference/api.md` — `POST /api/v1/runs/{runId}/resume` with all four refusal shapes; the new run-document fields in the `GET /api/v1/runs/{runId}` example.
- `docs/reference/cli.md` — `--resume` in the flag table.
- `docs/reference/configuration.md` — `DISCOVERY_CHECKPOINT_RETENTION` and its relationship to `DISCOVERY_MAX_DURATION`.
- `docs/reference/data-models.md` — the `discovery_checkpoints` collection, the `discovery_runs` lifecycle fields, `discoveries.run_id`.
- `helm-charts/decisionbox-api/values.yaml` — a commented `DISCOVERY_CHECKPOINT_RETENTION` beside the existing `DISCOVERY_MAX_DURATION` block, noting they must be raised together.
- `CHANGELOG.md` — one entry under `[Unreleased] → Added`.

---

## 5. Build phases

Each phase builds, tests and lints on its own.

| # | Phase | Contents |
|---|---|---|
| 1 | **Checkpoint store** | collection constant, `ExplorationCheckpointDoc`, repo + indexes + retention env var, `checkpointPayload`. Unit tests for the strip helper; integration tests for round-trip, unique upsert, prefix-stops-at-gap, TTL creation + retention change. |
| 2 | **Write path** | `PersistStep` on the engine; orchestrator wires it; `last_checkpoint_step`; conditional `Drop`; resume-aware boot sweep. A normal run now leaves a checkpoint trail — no read path yet, so this is independently shippable and observable. |
| 3 | **Replay** | `ResumeState`, `exploration_resume.go`, re-execution of the cheap actions, digest rendering, re-entry at N+1, the skip-exploration branch, re-indexing the prefix, previous-context filter, cumulative duration. |
| 4 | **Idempotent tail** | `discoveries.run_id`; retire-superseded (split logs, standalone docs, Qdrant points); checkpoint deletion on success. The duplicate-key test lands here. |
| 5 | **Verifier honesty** | §2.5. Kept separate so the behaviour change is reviewable on its own. |
| 6 | **Entry points** | `--resume`; `RunOptions.Resume`/`Attempt`; three runners; K8s Job name; `BeginResume`; `RunParams` on `Create`; `ResumeRun` handler + route; cancel purges checkpoints. |
| 7 | **Dashboard** | Resume affordance, API client, `active_ms` elapsed, overlay check. |
| 8 | **Docs + CHANGELOG** | §4.9. |

---

## 6. Test strategy

Rule 9: failure and edge cases, not the happy path; integration tests use the repo's real testcontainers (`setupMongoDB` in `services/agent/internal/database/integration_test.go`, `startQdrant` in `services/agent/internal/discovery/`).

### Unit — agent

`ai/exploration_resume_test.go` (scripted LLM via the existing `testutil.MockLLMProvider` + `ExplorationEngine` fakes):

- Conversation shape: N replayed steps → `1 + 2N` messages, strictly alternating roles, last message is step N's result, and the **first LLM call is step N+1**.
- `result.Steps` begins with the replayed steps and their original token counts; cumulative totals are not double-counted.
- `onStep` is **not** called for replayed steps (asserted by a recording callback) — guards against a duplicated live feed and double-counted query counters.
- Budget restoration: resume over two `lookup_schema` steps with `MaxLookupsPerRun: 2` → the next lookup is refused with "budget exhausted". Same for `search_tables` and `get_correlations`. This is the cost-integrity test.
- `fetchedTables` dedupe survives: re-asking a replayed table short-circuits with the "you already have this" reply.
- Floor: resume at step 61 with `MinSteps: 60` → a `done` on step 61 is accepted, no `complete_rejected`.
- `complete_rejected` replay: the user turn is the nudge for the persisted reason class, for each of `floor` / `productive` / `unproven`.
- Lossy digest: a replayed step with `RowCount: 50_000` and no `AllRows` renders 5+5 rows and a message that states the true row count and that the rows are a retained summary; a `RowCount: 3` step renders all three verbatim.
- Error step: a replayed step with `Error` set renders the failure message, not a success block.
- **Empty `Resume`** (nil and zero-length): behaviour identical to today — the regression guard for every non-resumed run.
- `replayedActionJSON` golden output per action type.

`discovery/orchestrator_resume_test.go`:

- Exploration-complete summary → mock LLM records **zero** exploration calls and the mock warehouse **zero** queries; analysis still runs. (Acceptance criterion 2.)
- `TotalSteps`, `CompletionMsg` and cumulative `Duration` come from the summary + prior `active_ms`.
- The run's own partial `discoveries` document is excluded from previous-context; a *different* run's is not.
- Retire-superseded: fake repos assert the superseded discovery's split logs, standalone docs and Qdrant point ids are deleted and the kept one's are not; a run with no prior discovery deletes nothing.
- A nil `checkpointRepo` disables checkpointing without changing the run (typed-nil normalisation).
- `Drop` is skipped when ending resumable and fires when ending successfully.

`verifier/bundle_test.go`, `verifier/tools_test.go`: **a 50-row sample of a 50 000-row step produces `full_row_count: 50000` + `truncated: true`, byte-identical to what the live 50 000-row step produces** — the regression the §2.5 one-liner exists to prevent, and the one that would otherwise pass every other test; `RowCount == len(QueryResult)` (live) is byte-identical to today for a short result, a sampled result and a failed step; a page past the retained sample returns empty + `truncated: true` + `rows_retained: false` and never a tool error.

`discovery/status_test.go`: `last_checkpoint_step` stamped; lifecycle pushed.

`database/discovery_checkpoint_repo_test.go`: `checkpointPayload` drops rows / fix history / LLM dialog and keeps the digest and `Quality`.

### Unit — API

`handler/discoveries_resume_test.go` with the existing fake-repo pattern: 404 unknown run; 409 for each of `pending` / `running` / `completed` / `cancelled`; 409 no checkpoints; 409 another run active; 409 when `BeginResume` matches nothing (the lost double-click); 409 when the project fails a lifecycle / schema-index gate; 202 happy path asserting the runner received `Resume: true`, the right `Attempt`, and the **persisted** `max_steps` / `min_steps` / `areas`; spawn failure → run marked failed and still resumable; **no `ChargeIfMetered` / `RefundIfMetered` / `CheckStartDiscoveryRun` call on the resume path** (asserted against a recording policy checker — this is the "no double-charge, no refund-then-free-resume leak" criterion).

`runner/runner_test.go`: `--resume` forwarded by all three runners; the K8s Job name is unchanged for attempt 1 and suffixed for attempt 2; the suffixed name is a valid DNS-1123 label; **`Cancel` deletes a suffixed attempt-2 Job** (the regression §2.7 exists to prevent) and still reports not-found when no Job matches.

### Integration (testcontainers)

- `database/discovery_checkpoint_repo_integration_test.go`: round-trip with a real digest; duplicate `(run_id, step_number)` write replaces rather than duplicates; `LoadPrefix` stops at a hole (write 1,2,4 → prefix is 1,2); summary document read back; `DeleteByRun`; `ListRunIDsWithCheckpoints`; TTL index exists with the configured expiry; changing the retention value recreates the index instead of failing startup.
- `database/discovery_log_repo_integration_test.go`: `DeleteByRun` removes rows from all four split collections and leaves another run's rows alone.
- `discovery/orchestrator_resume_integration_test.go` (Mongo + Qdrant): a run interrupted mid-exploration is resumed — replayed steps are re-indexed under deterministic point ids (re-running the re-index is a no-op), the analysis picker retrieves both replayed and new steps, and the warehouse mock records queries only for the new ones.
- **Re-save idempotency**: a resumed run reaching Phase 7/8 twice against a real `discovery_recommendation_log` with its unique index completes both times — the exact crash the issue names.
- `api/internal/handler` integration: two concurrent `ResumeRun` calls → exactly one 202 and one 409, one agent spawned (the `BeginResume` atomicity test).

Note: `services/agent/internal/discovery` is in the `make test-integration` list, so these run in the local integration target. The repo's Integration Tests CI job is skipped on PRs, so `go vet -tags=integration ./...` plus a local `make test-integration` run is the gate, and the PR will report which suites actually ran.

### UI

`__tests__/ProjectPage.test.tsx`: the Resume button renders for `failed` + `last_checkpoint_step > 0`, is absent for `cancelled` / `completed` / `failed` with no checkpoint, calls `api.resumeRun`, and surfaces a 409 message. `__tests__/api.test.ts`: `resumeRun` hits the right URL and method.

### Local gate before the PR

`make build`, `make test-go`, `make lint-go` (after `export PATH=$PATH:$(go env GOPATH)/bin`), `make test-integration`, `make test-ui`, `make lint-ui`, `go vet -tags=integration ./...` in both services.

---

## 7. Data / schema / API / UI impact summary

| Surface | Change | Compatibility |
|---|---|---|
| `discovery_checkpoints` | new collection | additive |
| `discoveries` | `+run_id`, `+{run_id:1}` index | additive; absent on historical docs, and every reader treats absent as "not this run" |
| `discovery_runs` | `+attempt`, `last_resumed_at`, `last_checkpoint_step`, `active_ms`, `lifecycle`, `max_steps`, `min_steps`, `areas`, `effort` | additive, all `omitempty`; a historical run reads as attempt 0 / no checkpoint → no Resume affordance, which is correct |
| `insights` / `recommendations` | no shape change; rows of a superseded attempt are deleted | — |
| API | `+POST /api/v1/runs/{runId}/resume` (member) | additive; `DELETE /api/v1/runs/{runId}` unchanged |
| Agent CLI | `+--resume` | additive, default false |
| Config | `+DISCOVERY_CHECKPOINT_RETENTION` (default `48h`) | additive |
| Dashboard | Resume button on a failed run; `active_ms`-based elapsed | additive |
| Qdrant | per-run step collection survives a resumable failure | storage only, bounded by the sweep |

No migration. No backfill. Nothing is removed.

---

## 8. Risks

| Risk | Mitigation |
|---|---|
| **Replayed conversation is not byte-identical to the original.** A lossy digest shows 5+5 rows where the live path showed 10. | Exact for `RowCount <= 20` (`AllRows`). Above that the message states the true row count and that the rows are a retained summary, so the model is not misled about the size of the result. Persisting raw rows is what the 16 MB history forbids. |
| **A re-executed `lookup_schema` can answer differently** if the schema cache was re-indexed between attempts. | Honest and visible: the replayed turn shows what the cache says *now*, which is also what the resumed run will query against. A re-index that invalidates the warehouse hash already blocks the run at the `schema_index_status` gate. |
| **Resumed run's audit log loses pre-crash raw rows + fix history** (the tail re-writes `discovery_exploration_steps` from replayed steps). | Accepted and documented: the checkpoint is deliberately digest-only. The alternative is persisting rows twice per step. |
| **Novelty stop-rule counters reset on resume**, so a cube-reaching resumed run must re-establish three judged steps. | The rule "only ever lengthens a run" (`exploration_stopping.go`), so resume can never stop a run early. Documented. |
| **TTL expiry mid-run** on an install with `DISCOVERY_MAX_DURATION=0`. | Retention is configurable and documented as needing to exceed the run cap; `LoadPrefix` degrades honestly (prefix 0 → the API refuses with "checkpoint expired" rather than silently re-exploring). |
| **Checkpoint write adds a Mongo round-trip per step.** | One small `ReplaceOne` against a run-scoped unique index, beside an LLM call and a warehouse query that dominate step latency by orders of magnitude. The run doc is already written once per step. |
| **A resumed run that fails again** could loop. | Each attempt increments `attempt` and appends a lifecycle event; resume is operator-initiated, never automatic. Nothing in v1 auto-retries. |
| **Cross-project concurrency cap** is not re-reserved on resume (§4.7). | Deliberate: a new reservation would consume a runs-per-period slot, i.e. a hidden charge, which the issue forbids. Per-project concurrency is still enforced at the repo level. Flagged as a known limitation and the natural seam for a future `CheckResumeDiscoveryRun`. |
| **Dashboard overlay drift** if `page.tsx` is overlaid privately. | Explicit check in the build step before the PR. |

---

## 9. Alternatives considered

1. **In-place upsert of the `discoveries` document** (the issue's literal wording). Keeps `discovery_id` stable, so any link already followed to the partial result survives. Rejected because the derived rows still have to be deleted before the new ones are written, which opens a window where the project shows no result at all — and a *failed* run's partial result is not something anyone has linked to. §2.4.
2. **Persist raw rows in the checkpoint** so replay and the verifier are exact. Rejected: it reintroduces exactly the document-size pressure that forced the split-collection design, and the digest is already what the analysis phase prefers. §2.5 solves the verifier problem without it.
3. **One bulk "here is what you already did" user message** instead of replaying alternating turns. Smaller and simpler, but it changes the conversation shape the domain-pack prompts teach, and the model's own prior turns are the best available statement of what it already tried.
4. **Checkpoint into the existing `discovery_exploration_steps` collection** keyed by `run_id` with the discovery id filled in later. Rejected: that collection is the tail-written audit log with full rows and fix history, read by the dashboard by `discovery_id`; writing provisional rows into it would make "is this an audit row or a checkpoint" a question every reader has to answer, and the two want different retention.
5. **Preserve the per-run Qdrant collection as the source of truth for replay.** Rejected: its payload is not replayable, it is swept on a schedule, and a vector store is the wrong place for the durable record. It stays a derived cache that replay rebuilds. §3.5.
6. **A new run id per resume with a `parent_run_id`.** Rejected by the issue and agreed with: it needs a grouping entity, splits the ledger, and double-counts the run everywhere a run is counted.
7. **Resume on `cancelled` too.** Out of scope by the issue: cancel is a deliberate hard kill and stays terminal. Its checkpoints are deleted.

---

## 10. Out of scope

Tracked on the resume roadmap epic, not touched here:

- **v2** — per-area analysis resume (skip areas already analysed). A resumed run re-runs the whole analysis phase.
- **v3** — per-insight / recommendation resume.
- **v4** — operator **Pause** (cooperative stop → `paused` → resume) and the Pause/Cancel two-CTA UX. No `paused` status, no new run control, and no Pause affordance is added in v1.
- **Resume pricing on metered deployments** — a commercial decision outside this repo. The engine seam stays free-by-construction: the charge is keyed on `runID`, resume reuses it, no refund path is added on resumable failure, and `attempt` is persisted so a future charge can key on `runID:attempt` instead of silently no-op'ing against the original.

---

## 11. Rule compliance notes

- **Rule 2** — `DISCOVERY_CHECKPOINT_RETENTION` is an env var, not a constant, because its correct value depends on `DISCOVERY_MAX_DURATION` (§3.4). The TTL-index conflict path exists so the var actually takes effect.
- **Rule 3** — no `TODO`/`HACK`/`FIXME`. The cross-project reservation question (§8) is stated in the PR body as a known limitation with its seam named; if review wants it closed in v1 it is a handler-level change, and if not it becomes a tracked issue rather than a comment.
- **Rule 4** — doc changes enumerated in §4.9 and part of the same PR.
- **Rule 6** — nothing is left behind: `StepCallback` keeps its single purpose (live UI) rather than growing a second one, and the retire path deletes rows outright rather than flagging them superseded.
- **Rule 8** — the three additions beyond the issue's text (§2.5 verifier honesty, §2.6 previous-context filter, §2.7 K8s Job name) are each a correctness bug that resume would otherwise introduce, not polish. §2.8 is a prerequisite: without it a resumed run silently changes its own step budget.
- **Rule 9** — the tests that matter are the negative ones: restored budgets, no duplicated live feed, the duplicate-key re-save, the double-click race, and the "no metering call on the resume path" assertion.
- **Rule 11** — this is a platform/community capability; nothing private is referenced, and the roadmap is cited without naming it.
- **CI docs gate** — `scripts/lint-docs.sh` runs over `docs/**` and `CHANGELOG.md` (`.github/workflows/ci.yml:48-63`) and fails on the word "enterprise", on `PLAN-*.md` filenames, on `plan v<n>` phrasing and on `Phase <Letter>` labels. The doc changes in §4.9 are written to that constraint: they describe checkpointing and resume as platform behaviour, cite no plan document, and use the architecture's own numeric phase names.

---

## 12. Questions for review

1. **Replay fidelity above 20 rows** (§2.1, §8): 5+5 rows plus an explicit "retained summary of N rows" note, versus raising `CompactInlineThreshold` for checkpoints only. I have chosen the former — the digest is already the evidence the analysis phase reasons over — but it is a judgement call about what the model sees.
2. **Cross-project concurrency on resume** (§4.7, §8): leave the policy reservation untouched (free, but one cap unenforced), or add a resume-specific check that reserves concurrency without consuming a period slot. I have chosen the former because it cannot cost anyone anything; the latter needs a new seam in the policy checker.
3. **Retire-vs-keep the superseded partial result** (§2.4): deleted, so a run has exactly one result. Keeping both as history would double-count insights in project search and show two cards for one run.

---

*This is a **PLAN for review** — no implementation is included. Implementation follows after approval.*

Closes #438

— Co-coded with Jale 🤖
