# Plan — Discovery run resume (v2): per-area analysis resume

Issue: #506. Epic: enterprise#387. Builds on **v1** (#438, merged as PR #440 onto
`epic/discovery-run-resume`).

---

## 0. Base branch — read this first

**This PR targets `epic/discovery-run-resume`, not `main`.**

v1 was merged into the epic branch (`a03e13f`), and `main` has none of it: no
`discovery_checkpoints` collection, no `ResumeState`, no `orchestrator_resume.go`,
no `POST /api/v1/runs/{id}/resume`. `grep -ril checkpoint --include=*.go` on `main`
returns nothing. v2 is a strict extension of that plumbing, so a branch cut from
`main` would have nothing to build on. `jale/issue-506` has therefore been rebased
onto `origin/epic/discovery-run-resume` (which is `main` + v1 + two merges of
`main` back in, so it is current).

CI already expects this: `.github/workflows/ci.yml` lists `epic/**` in both the
`pull_request` and `push` branch filters precisely so the PRs that build an epic
get the same gates a PR to `main` would.

If the intent is for v2 to land on `main` instead, v1 has to merge to `main`
first — flag it and the branch will be rebased.

---

## 1. Problem restatement

v1 made the **expensive** phase durable. Every completed exploration step lands in
its own `discovery_checkpoints` document as it finishes, plus one summary document
(`step_number 0`) when exploration ends. A resumed run replays the contiguous
prefix, and a run whose summary is present skips Phase 3 entirely — zero
exploration LLM calls, zero warehouse queries.

Everything after that is still start-from-scratch. `docs/concepts/discovery-lifecycle.md`
says so in as many words:

> - **Analysis restarts.** A resumed run re-runs the whole analysis phase; only
>   exploration is checkpointed today.

So a run that dies in or after Phase 4 having completed K of N areas re-pays all N
on resume: one analysis LLM call per area, plus the per-insight validation calls
Phase 4.5 makes inside the same loop body (up to three verifier/refuter calls per
insight, bounded by `RunCaps.MaxInsightsPerRun`). Phase 4.5 is the part that
actually costs — the issue's "area analysis is comparatively cheap" is true of the
single analysis call, less true of the validation fan-out behind it.

**Goal.** On resume, an analysis area that already completed is restored from a
durable per-area checkpoint instead of re-run. The run re-enters at the areas
without a marker. Same `runID`, same attempt plumbing, no new run control, no new
status.

**Honest scope note (as the issue asks).** This is a lower-value increment than
v1. It saves reasoning over cached digests, not fresh warehouse work. What raises
its value above the issue's own estimate is the validation fan-out above, and one
failure mode v1 cannot help with at all: a run killed by `DISCOVERY_MAX_DURATION`
expiring *during* analysis currently resumes, re-runs area 1 from the top, and
stands a good chance of running out of time again at the same place. §4.4 is
designed around that case.

---

## 2. What v1 already gives us (verified against the epic branch)

Facts the design leans on. All checked in code, not assumed.

| Fact | Where |
|---|---|
| Checkpoint rows are one-doc-per-unit, never an embedded array (16MB BSON) | `services/agent/internal/database/discovery_checkpoint_repo.go` package header |
| Writes are attempt-fenced: `ReplaceOne` upsert filtered on `attempt: {$lte: n}`, duplicate-key ⇒ `ErrSupersededAttempt` ⇒ the run stops | `SaveStep`, `SaveExplorationSummary` |
| Checkpoint writes are otherwise **best-effort** — logged and swallowed so they can never abort a working run | `checkpointStep` callers, `checkpointExplorationSummary` |
| Expensive phases are gated on ownership, including **per analysis area** | `Orchestrator.ownershipLost`, called with `"analysis area "+area.ID` |
| Retention is a TTL on `created_at`, interval from `DISCOVERY_CHECKPOINT_RETENTION` (default 48h), with the IndexOptionsConflict drop-and-recreate dance | `DiscoveryCheckpointRepository.EnsureIndexes` |
| A resumed run that only *reads* its rows must re-anchor the TTL | `refreshCheckpointTTL` → `TouchByRun` |
| Rows are deleted on a terminal-and-not-resumable outcome; TTL is the backstop | `discardCheckpoints`, API `DeleteByRun` from `CancelRun` / `DiscardRun` |
| The live step feed is **attempt-scoped** — the API filters `discovery_run_steps` on the run's current attempt | `services/api/database/run_step_repo.go` `ListByRun(…, attempt)` |
| …so a resume must **re-emit** the work it skipped under its own attempt, rows only, original timestamps preserved | `replayLiveFeedForResume` → `StatusReporter.ReplayExplorationStep` |
| The tail writes a **fresh `discovery_id`** per attempt and retires the superseded one afterwards | `orchestrator.go` Phase 7 + `retireSuperseded` |

That last row is load-bearing for §3.1 and resolves one of the issue's design
bullets before we start — see §3.2.

### Codebase facts that shape the design

1. **Area order is nondeterministic.** `Orchestrator.resolvePrompts` builds
   `[]AnalysisArea` by ranging over the `o.projectPrompts.AnalysisAreas` **map**,
   and nothing sorts the result. So "the first unfinished area" is not a positional
   concept in this codebase. Markers must be keyed by `area_id`, and any design
   that stored "resume at index K" would be wrong.
2. **Each loop iteration produces exactly three things** that outlive it:
   the `models.AnalysisStep` appended to `analysisLog`, the insights appended to
   `allInsights`, and a `insightsValidatedThisRun` increment threaded across areas.
   Everything downstream — `allValidation`, `analysisErrors`, `failedAreas`,
   `runType`, the Phase 7 `DiscoveryResult`, the split logs, Phase 8 — is derived
   from those. There is no other per-area state.
3. **`models.AnalysisStep` already carries all of it**: `Insights` (with
   `Validation` stamped in place by `validateInsights` and `Quality` stamped by
   `attachSourceQuality`), `ValidationResults`, `Prompt`, `Response`, token
   counts, picker telemetry, `Error`.
4. **Everything carries its own timestamp** — `AnalysisStep.RunAt`,
   `Insight.DiscoveredAt` (always set; `parseInsights` backfills a zero value),
   `ValidationResult.ValidatedAt` — which is what makes an honest feed replay
   possible.
5. **`insightsValidatedThisRun` is incremented once per insight actually submitted
   to the verifier** (`validation_phase.go`: `runValidated++` after the cap check,
   skipped entirely when `p.agent == nil`). It is the per-run validation budget.

---

## 3. Approach

### 3.1 Where the per-area output is persisted — and where it is **not**

Area output goes into a **new checkpoint collection**,
`discovery_analysis_checkpoints`, one document per `(run_id, area_id)`. On resume
the rows are read back and **merged into the in-memory accumulators** at the top of
the Phase 4 loop. Phases 5 → 8 and the whole tail are untouched.

This is the decision the rest of the plan hangs on, so here is why it is not the
other thing.

The issue's design section says per-area insight/rec writes "must upsert by
`(run_id, area_id, …)`", which reads as writing insights into the **live result
collections** per area. That would fight the architecture v1 settled on. Today each
attempt writes its own `discoveries` document under a freshly minted
`discovery_id`, with every derived row (standalone insights, recommendations, the
four split logs, Qdrant points) keyed to it, and the *previous* attempt's result is
retired afterwards. Phase 7's own comment states the consequence:

> A clean insert under a freshly minted `discovery_id`, which is what makes the
> unique index on `discovery_recommendation_log.discovery_id` unviolatable on a
> resumed run rather than merely unviolated.

Writing per-area results into those collections incrementally would mean partial
results visible under a `discovery_id` that does not exist yet, upserts on
collections whose ids are also Qdrant point ids, and re-opening the duplicate-key
hazard v1 closed structurally. Checkpointing instead keeps the tail a clean insert.

### 3.2 How the "hard requirement" is met

The issue makes idempotent area writes a hard requirement, specifically calling out
`discovery_recommendation_log`'s unique index on `discovery_id`. With the design
above it is satisfied twice over, and the second one is by construction:

- **The checkpoint write** is a `ReplaceOne(…, upsert)` against a unique
  `(run_id, area_id)` index, attempt-fenced exactly like v1's `SaveStep`. Re-running
  an area that was already persisted replaces its row. It cannot duplicate and
  cannot raise a duplicate-key error except as the deliberate supersession signal.
- **The result writes are unchanged.** A resumed run still does one clean insert of
  one `discoveries` document and its split logs under a new `discovery_id` at the
  tail. `discovery_recommendation_log` gets exactly one row, as it does today.
  There is no re-save path to make idempotent, so there is no re-embed path into
  Qdrant to make idempotent either — Phase 8 embeds the tail result once.

So the acceptance criterion "insights/recommendations for already-completed areas
are not duplicated on resume (idempotent upsert; no duplicate-key crash)" is met
by *not adding* the write that would have needed the upsert. The plan will say this
on the PR rather than quietly diverging from the issue text.

### 3.3 Why a separate collection rather than a `kind` in `discovery_checkpoints`

Rejected after reading the two readers. `discovery_checkpoints` has a **unique
`(run_id, step_number)`** index, and area rows have no natural step number. Worse,
both the agent's `LoadPrefix` and the API's `ResumeState` scan *every* row for a
run sorted by `step_number` and end the replayable prefix at the first row that is
not the expected number — the gap and stale-tail invariants that took v1 three
review rounds to get right. Mixing a second row kind into that scan puts those
invariants at risk for no gain. A sibling collection reuses the *pattern* (which is
what the issue asks for) and leaves v1's hardest-won code alone.

### 3.4 The staleness fence — the one real correctness hazard

A restored area's insights cite exploration step numbers via `SourceSteps`, and
`BuildInsightBundle` / `attachSourceQuality` / the recommender all resolve them
through `stepByID`. **If the resumed run re-explores, those citations may point at
different steps.** Restoring an area under a different exploration would attribute
evidence to the wrong queries — the exact failure v1's gap check exists to prevent,
one layer up.

Reachable path: attempt 1 explores 40 steps, writes the summary, analyses areas
1–3, dies. Later, early step rows TTL-expire. Attempt 2 now reads a prefix with a
gap, `LoadPrefix` drops the summary, so it re-explores and reaches a *different*
45-step exploration and writes a new summary. Attempt 3 reads a trusted summary —
over attempt 2's exploration — and must **not** restore attempt 1's areas.

**Fence.** Each area row is stamped with `exploration_attempt`: the attempt that
wrote the exploration summary the area's analysis was computed over. A resumed run
restores only rows whose `exploration_attempt` equals the attempt of the summary it
is itself trusting. Attempt numbers increase monotonically per resume, so a
re-explored run always writes its summary under a higher attempt and the stale rows
simply stop matching. Nothing is deleted; the rows age out on their own TTL.

Two consequences, both deliberate:

- **Area checkpointing is enabled only once a durable exploration summary exists.**
  `checkpointExplorationSummary` currently swallows a write failure and returns
  `nil` (the run carries on, a later resume just re-explores). If the summary did
  not land there is nothing valid to stamp, so area checkpointing stays off for
  that run. Correct: such a run will re-explore on resume anyway, so its area
  markers could never be used.
- **Restoration requires the skip-exploration path.** `o.resume.explorationComplete()`
  false ⇒ no restore. Falls out of the fence automatically (the summary this attempt
  trusts does not exist), and is asserted directly as well.

Alternative considered and rejected: delete the run's area rows whenever an attempt
writes a *new* exploration summary. Simpler in field count, but it introduces a
destructive write with no safe ordering — delete-after-write leaves a window where
a trusted new summary coexists with stale area rows (the bad state), and
delete-before-write forces a choice between skipping the summary write on a
transient Mongo error (losing the exploration skip, i.e. v1's whole point) and
accepting that window anyway. One integer beats that.

### 3.5 What counts as a completed area — and why a failed area is deliberately **not** one

The issue asks the marker to carry a status. In practice the only status that may
be marked is "completed", and the reason is the most important product decision in
this plan.

The loop body has four early exits. Three are *before* the LLM call and cost
nothing to redo: no prompt for the area, `picker.Pick` error, zero picked steps.
None of them appends to `analysisLog`. They are left unmarked and simply
re-evaluated — on a resume that also skips the picker's vector search for them.

The fourth is *after* the call: `outcome.chatErr != nil`, or zero insights with a
`parseErr`. These append an `AnalysisStep` with `Error` set. It is tempting to mark
them done — the LLM call was paid for, and v1's own discipline is that a recorded
outcome is a finished one (a failed exploration step is replayed, not re-executed).

**Marking them would break the feature's best case.** When
`DISCOVERY_MAX_DURATION` expires mid-analysis, the loop does not stop: every
remaining area's analysis call returns `context.DeadlineExceeded` and gets an
error-marked step, then Phase 7 saves a thin partial result and fails the run. If
error areas were marked complete, the resume would restore all of them, re-run
nothing, and reproduce the same near-empty discovery — defeating the resume the
operator just paid attention to. A failed analysis call also produces nothing
usable, so there is no banked work to protect.

**Rule: an area is marked iff its `AnalysisStep` has no `Error`** — i.e. the
analysis response parsed, Phase 4.5 validation finished, and `attachSourceQuality`
ran. An area that legitimately produced zero insights (empty response, no
`parseErr`) *is* complete and is marked; re-running it would re-pay for "nothing
here".

Because the condition is constant, no `status` field is stored. The marker's
existence *is* the status, and the inlined `AnalysisStep.Error` is always empty —
asserted in a test so the invariant cannot rot.

### 3.6 Restoring the validation budget

A resumed run that skips K areas must not hand the remaining areas a fresh
validation budget. The delta is recorded, not inferred: `insights_validated` on the
area row is `insightsValidatedThisRun` after the area minus before it, captured at
the call site where `validateInsights` returns it. Deriving it from the stored
`ValidationResults` instead would re-implement the cap rule in a second place and
drift the day it changes.

---

## 4. Implementation

### 4.1 Collection + models

**`libs/go-common/mongodb/collections.go`** — add
`CollectionDiscoveryAnalysisCheckpoints = "discovery_analysis_checkpoints"` with a
header comment in the style of the `CollectionDiscoveryCheckpoints` block: one row
per completed analysis area of an in-flight run; agent writes, API purges; sibling
of `discovery_checkpoints` and why it is not the same collection (§3.3).

**`services/agent/internal/database/mongodb.go`** — re-export the constant
alongside `CollectionDiscoveryCheckpoints`.

**`services/agent/internal/models/checkpoint.go`** — two additions:

```go
// AnalysisAreaCheckpoint is one completed analysis area as a resumed run
// reads it back. (Declared in package models, so the field type is the
// local AnalysisStep.)
type AnalysisAreaCheckpoint struct {
	Step AnalysisStep
	// InsightsValidated is how many of this area's insights were submitted
	// to the verifier, so a resumed run restores the per-run validation
	// budget instead of handing the remaining areas a fresh one.
	InsightsValidated int
}
```

and `Attempt int` on the existing `ExplorationCheckpointSummary` — the attempt that
wrote the summary, which is the value area rows are stamped and filtered on (§3.4).

### 4.2 `services/agent/internal/database/discovery_analysis_checkpoint_repo.go` (new)

Mirrors v1's repo file, including a package-header comment explaining what the
collection is and what it is *not* (it is not `discovery_analysis_steps`, the
tail-written audit log keyed by `discovery_id`).

```go
type AnalysisAreaCheckpointDoc struct {
	RunID     string `bson:"run_id"`
	ProjectID string `bson:"project_id"`
	Attempt   int    `bson:"attempt"`
	// ExplorationAttempt is the attempt that wrote the exploration summary
	// this area was analysed over — see LoadAreas.
	ExplorationAttempt int       `bson:"exploration_attempt"`
	InsightsValidated  int       `bson:"insights_validated,omitempty"`
	CreatedAt          time.Time `bson:"created_at"`

	models.AnalysisStep `bson:",inline"`
}
```

`AnalysisStep` is inlined for the same reason `ExplorationStep` is in
`ExplorationCheckpointDoc`: its BSON tags stay authoritative. **The outer struct
must not declare its own `area_id`** — the inlined `AnalysisStep.AreaID` already
carries `bson:"area_id"` and is the key half of the unique index. The two tag sets
are otherwise disjoint (`run_id`, `project_id`, `attempt`, `exploration_attempt`,
`insights_validated`, `created_at` appear on neither `AnalysisStep` nor anything it
embeds) — pinned by a unit test, as v1 does for its own doc.

Methods:

- `SaveArea(ctx, AnalysisAreaCheckpointInput) error` — `ReplaceOne` with upsert,
  filter `{run_id, area_id, attempt: {$lte: in.Attempt}}`, duplicate-key ⇒
  `ErrSupersededAttempt` (the package-level error v1 already defines). Rejects an
  empty `run_id` / `area_id` / non-positive `exploration_attempt`.
- `LoadAreas(ctx, runID string, explorationAttempt int) (*AnalysisCheckpointSet, error)` —
  returns `{Areas map[string]models.AnalysisAreaCheckpoint, Attempt int}`.
  `explorationAttempt <= 0` returns an empty set without querying: there is no
  durable summary, so no row can be valid. Filters
  `{run_id, exploration_attempt: explorationAttempt}` — served off the `run_id`
  prefix of the unique index, with the equality as a cheap post-filter over at most
  N rows, so no second index is needed. Logs at Info when it drops rows for a
  mismatched exploration attempt, because that is the §3.4 hazard firing and an
  operator should be able to see it.
- `DeleteByRun`, `TouchByRun` — byte-for-byte the roles v1's equivalents play, with
  `TouchByRun` setting `created_at` **and nothing else** for the same reason
  (restamping `attempt` would corrupt the fence).
- `EnsureIndexes(ctx)` — unique `(run_id, area_id)` named `uq_run_area`, plus the
  `created_at` TTL named `ttl_discovery_analysis_checkpoint`. Retention comes from
  the **same** `DISCOVERY_CHECKPOINT_RETENTION` — one feature, one knob (no new
  config surface; Rule 2 and Rule 8). Reuses v1's `isIndexOptionsConflict` helper
  for the drop-and-recreate dance so the env var is a real knob here too.

`ListRunIDsWithCheckpoints` is **not** added. The boot-time orphan sweep's keep-set
exists to protect a resumable run's per-run Qdrant collection, and a run is
resumable only while its *exploration* rows live — `loadResumeState` refuses a
resume with neither prefix nor summary. Area rows alone cannot make a run
resumable, so they must not extend the keep-set. Called out in the docs so the
asymmetry is deliberate rather than an oversight.

### 4.3 `services/agent/internal/discovery/analysis_resume.go` (new)

Kept out of `orchestrator.go` (3,740 lines) for the reason
`orchestrator_resume.go` already gives, and separate from it because that file is
exploration's story.

- `analysisCheckpointStore` interface (`SaveArea` / `LoadAreas` / `DeleteByRun` /
  `TouchByRun`) so unit tests inject a fake and a nil store cleanly disables the
  feature.
- `func (o *Orchestrator) loadCompletedAreas(ctx) map[string]models.AnalysisAreaCheckpoint` —
  nil store, non-resume, or `!o.resume.explorationComplete()` ⇒ nil. Otherwise
  `LoadAreas(ctx, o.runID, o.explorationSummaryAttempt)`. On error: log and return
  nil — a read failure costs money (areas re-run), never correctness.
- `func (o *Orchestrator) checkpointAnalysisArea(ctx, step models.AnalysisStep, validated int) error` —
  no-op when the store is nil, `o.explorationSummaryAttempt <= 0`, or
  `step.Error != ""` (§3.5). Probes `o.statusReporter.OwnsRun(ctx)` before writing,
  exactly as `checkpointStep` does and for the same stated reason (the row-level
  fence only refuses a write where a higher-attempt row *already* exists, so a dead
  attempt running ahead of the live one would otherwise splice its work in).
  Translates `database.ErrSupersededAttempt` → `ai.ErrAttemptSuperseded`; logs and
  swallows every other error.
- `func (o *Orchestrator) replayAnalysisFeedForResume(ctx, completed map[string]models.AnalysisAreaCheckpoint)` —
  the §2 counterpart of `replayLiveFeedForResume`. Emits each restored area's rows
  under **this** attempt, areas ordered by `AnalysisStep.RunAt` so the feed stays
  monotonic, called once before the area loop rather than per iteration (area order
  is nondeterministic — §Codebase facts 1 — so per-iteration emission would scramble
  the displayed order). Rows only; failures logged and swallowed like every other
  live-feed write.
- `func (o *Orchestrator) refreshAnalysisCheckpointTTL(ctx)` — `TouchByRun`,
  best-effort, called on the skip-exploration path where the rows are read without
  being rewritten. Same reasoning as `refreshCheckpointTTL`.

### 4.4 `services/agent/internal/discovery/orchestrator.go`

**Wiring.** `Config.DiscoveryAnalysisCheckpointRepo *database.DiscoveryAnalysisCheckpointRepository`,
assigned into `o.analysisCheckpointRepo analysisCheckpointStore` through the same
explicit nil check v1 uses at `orchestrator.go:593` — a typed-nil pointer boxed into
an interface is non-nil and would silently enable the path. Plus
`o.explorationSummaryAttempt int`.

**Setting `explorationSummaryAttempt`** (§3.4), in the Phase 3 branch around
`orchestrator.go:1289`:
- skip-exploration path: `o.resume.Checkpoints.Summary.Attempt`;
- explore path: this attempt's number, **only if** `checkpointExplorationSummary`
  reports the row landed (§4.5).

**Phase 4 loop.** Before the loop:

```go
completedAreas := o.loadCompletedAreas(ctx)
o.replayAnalysisFeedForResume(ctx, completedAreas)
```

and the existing `SetPhase(ctx, models.PhaseAnalysis, "Analyzing discoveries by
category...", 65)` detail gains ` (K of N areas already complete)` when `K > 0`, so
the live panel explains itself. Existing field, existing call, no API or UI change.

Inside the loop, immediately after the existing
`if o.ownershipLost(ctx, "analysis area "+area.ID)` gate:

```go
if cp, ok := completedAreas[area.ID]; ok {
	analysisLog = append(analysisLog, cp.Step)
	allInsights = append(allInsights, cp.Step.Insights...)
	insightsValidatedThisRun += cp.InsightsValidated
	continue
}
```

Three lines of merge and a `continue` — because of §Codebase facts 2, that is the
whole of it. The picker, the prompt build, the analysis call, Phase 4.5, the status
rows and the per-area counters are all skipped, which is also what keeps
`step_index_search_calls` / `steps_dropped` from double-counting (the run document
carries them across attempts, same as v1's replayed-step counters).

At the end of the loop body, after `analysisLog`/`allInsights` are appended and
**before** the status rows — v1's checkpoint-before-shared-writes ordering, so a
superseded attempt stops before writing display rows:

```go
if err := o.checkpointAnalysisArea(ctx, step, validatedThisArea); err != nil {
	return nil, err   // ai.ErrAttemptSuperseded only
}
```

`validatedThisArea` is the `insightsValidatedThisRun` delta captured around the
existing `valPhase.validateInsights` call (§3.6).

**Nothing downstream changes.** `allValidation`, `analysisErrors`, `failedAreas`,
`runType`, the Phase 7 `DiscoveryResult`, `persistSplitLogs` and Phase 8 all read
`analysisLog` / `allInsights`, which now hold the union. A restored insight keeps
its original `ID`, so Phase 5's `related_insight_ids` resolution and Phase 5.5's
`validateRecommendations(…, allInsights, stepByID)` work unchanged.

### 4.5 `services/agent/internal/discovery/orchestrator_resume.go`

- `checkpointExplorationSummary` returns enough for the caller to know the row
  landed. Currently `error`, with `nil` meaning both "written" and "write failed,
  carry on". Narrowest honest change: keep the signature and set
  `o.explorationSummaryAttempt` inside the function on success only. Its comment
  gains the sentence that area checkpointing depends on it.
- `discardCheckpoints` also purges the analysis collection — the same `why`, the
  same best-effort logging. This is the single place a terminal-and-not-resumable
  run drops its rows, so the two collections cannot drift.
- `refreshCheckpointTTL`'s doc comment points at its analysis sibling.

### 4.6 `services/agent/internal/discovery/status.go`

One new method, mirroring `ReplayExplorationStep`:

```go
// ReplayAnalysisArea re-emits one already-completed area's rows into this
// attempt's live feed.
func (s *StatusReporter) ReplayAnalysisArea(ctx context.Context, step models.AnalysisStep)
```

It writes the area's analysis row, one insight row per `step.Insights`, and one
validation row per `step.ValidationResults`, each with the original timestamp
(`RunAt` / `DiscoveredAt` / `ValidatedAt`) rather than now. Rows only: no
`SetPhase`, no counters — those happened on the attempt that did the work and the
run document carries them forward.

To keep the live and replayed renderings from drifting, the row-building bodies of
`AddAnalysisStep` / `AddInsightStep` / `AddValidationStep` are extracted into
`analysisRunStep` / `insightRunStep` / `validationRunStep` pure helpers (exactly
what v1 did with `explorationRunStep`), and both paths call them. One public method
rather than three keeps "everything this area originally emitted" in one testable
place.

### 4.7 `services/agent/agentserver/agentserver.go`

Construct `database.NewDiscoveryAnalysisCheckpointRepository(db)` next to the
exploration one (~line 929), call its `EnsureIndexes` in the same block (~line 950)
with the same retention log line, and pass it into the orchestrator `Config`
(~line 1124). `loadResumeState` is untouched: resumability is still decided by the
exploration prefix/summary, which is the §4.2 keep-set asymmetry stated as code.

### 4.8 API

- **`services/api/database/discovery_checkpoint_repo.go`** — the repo gains a
  second collection handle and `DeleteByRun` purges both, returning the total. Done
  here rather than as a second repo with two new call sites precisely so a purge
  site cannot forget half the feature; the package doc gains a sentence saying the
  API's view now spans both collections. `ResumeState` is **unchanged** —
  resumability is still "is there an exploration prefix", and a run that died in
  analysis has a complete one, so the existing `last_checkpoint_step > 0`
  affordance already covers it.
- **`services/api/database/init.go`** — a `discovery_analysis_checkpoints` schema
  entry with the unique `(run_id, area_id)` index, carrying the same comment v1's
  entry carries about why the **TTL is not created here** (its expiry comes from
  `DISCOVERY_CHECKPOINT_RETENTION` and the agent owns the drop-and-recreate; two
  creators with two values would conflict on every boot).
- **No handler, route, request or response change.** No new run control, no new
  status, per the issue.

### 4.9 Dashboard

**No change.** `canResume` is `status === 'failed' && (run.last_checkpoint_step ?? 0) > 0`,
which is already true for a run that died in analysis, and "Resume from step N"
remains accurate — N is the last exploration step. What the run then *does* is
carried by `phase_detail` (§4.4). So nothing in `ui/dashboard/src/app/projects/[id]/page.tsx`
or `src/lib/api.ts` is touched, and therefore **no enterprise dashboard overlay
sync is needed** — worth stating because that overlay replaces whole files and a
missed sync breaks the enterprise build.

### 4.10 Docs (Rule 4)

- **`docs/concepts/discovery-lifecycle.md`** — the "**Analysis restarts.**" caveat
  (line 214) is now false and gets rewritten: completed areas are restored, failed
  areas are retried, and **why** (§3.5, including the `DISCOVERY_MAX_DURATION`
  case). The "Checkpointing and resume" section gains a short "What a completed
  area keeps" subsection and the staleness fence (§3.4). The `(§4.2)` keep-set
  asymmetry is stated in the caveat list.
- **`docs/reference/data-models.md`** — a new `AnalysisAreaCheckpoint` section
  beside `ExplorationCheckpoint`: collection, fields, the unique `(run_id, area_id)`
  index, the TTL name, and that it is not `discovery_analysis_steps`. The
  `ExplorationCheckpoint` summary-fields row gains `attempt`.
- **`docs/reference/api.md`** line 805 — "A run that died after exploration
  finished goes straight to analysis" gains "…and restores the analysis areas it had
  already completed instead of re-running them".
- **`docs/reference/configuration.md`** — the `DISCOVERY_CHECKPOINT_RETENTION` row
  says it governs both checkpoint collections.
- **`helm-charts/decisionbox-api/values.yaml`** — the same one-line widening of the
  existing comment block.
- **`CHANGELOG.md`** — one entry under the unreleased heading.

No new env var, no new Helm value, no Terraform variable.

None of the doc text references this plan document or any plan item (Rule 10), and
nothing in it mentions an enterprise-only capability (Rule 11) — `make lint-docs`
scans `docs/` and `CHANGELOG.md` for exactly those two leakage classes, so it is
the gate for both.

---

## 5. Phasing

Each phase builds and its tests pass before the next; each is one commit.

1. **Storage.** Collection constant, `models` additions, the new repo, its unit +
   integration tests, `init.go` index, `agentserver` wiring + `EnsureIndexes`.
   Nothing reads it yet.
2. **Write path.** `explorationSummaryAttempt`, `checkpointAnalysisArea`, the
   call site at the end of the Phase 4 loop, the `discardCheckpoints` purge and the
   API `DeleteByRun` purge. Rows are now written and cleaned up; still nothing reads
   them, so a bug here cannot change a run's result.
3. **Read path.** `loadCompletedAreas`, the loop-top merge, the validation-budget
   delta, the phase-detail text.
4. **Feed replay.** `StatusReporter.ReplayAnalysisArea` + the extracted row
   builders + `replayAnalysisFeedForResume`.
5. **Docs + CHANGELOG.**

---

## 6. Test strategy (Rule 9)

Unit tests run on every PR; the integration job is label-gated
(`run-integration-tests`) or push-only, so `make test-integration` is run locally
and the label is added to the PR.

### Unit — `services/agent/internal/database/discovery_analysis_checkpoint_repo_test.go`
- BSON tag disjointness between `AnalysisAreaCheckpointDoc` and the inlined
  `models.AnalysisStep` (reflection over both tag sets) — the duplicate-`area_id`
  trap in §4.2.
- `SaveArea` input validation: empty `run_id`, empty `area_id`, non-positive
  `exploration_attempt`.
- `LoadAreas` with `explorationAttempt <= 0` makes no query and returns empty.

### Unit — `services/agent/internal/discovery/analysis_resume_test.go` (fake store)
- `checkpointAnalysisArea` **does not write** when `step.Error != ""` — the §3.5
  invariant, and the one test that protects the `DISCOVERY_MAX_DURATION` case.
- …nor when `explorationSummaryAttempt == 0` (summary never landed).
- …nor when the store is nil; and a typed-nil repo does not enable the path.
- Writes carry this attempt, the exploration-summary attempt, and the
  `insights_validated` delta.
- `ErrSupersededAttempt` → `ai.ErrAttemptSuperseded`; any other store error is
  swallowed and the run continues.
- `OwnsRun` false ⇒ no write at all.
- `loadCompletedAreas` returns nil for a non-resume, for a resume that must
  re-explore (`explorationComplete` false), and on a store error.
- `replayAnalysisFeedForResume` orders areas by `RunAt`, emits 1 + |insights| +
  |validations| rows, preserves all three timestamp kinds, and bumps no counter.

### Unit — orchestrator loop (extending `orchestrator_resume_test.go` style)
- K of N restored: the analysis LLM layer is called exactly N−K times, and for the
  right area ids.
- `allInsights` is the union, with restored insights keeping their original ids,
  `Validation` and `Quality`.
- `insightsValidatedThisRun` starts at the sum of restored deltas, so the per-run
  cap applies across attempts — asserted by a case where the restored deltas
  exhaust the cap and every re-run area's insights come back
  `skipped_budget_cap`.
- An area that **failed** on attempt 1 is re-run on attempt 2, and a clean result
  there makes the run `full` rather than leaving it `partial` — the §3.5 invariant
  seen from the `runType` side.
- An area in the checkpoint set that is no longer in `runAreas` (domain pack edited,
  or a narrower `SelectedAreas`) is ignored, and an area in `runAreas` with no row
  is analysed.
- All N areas restored ⇒ zero analysis calls and the run proceeds into Phase 5.
- The three pre-LLM skips write no checkpoint.

### Unit — `status_test.go`
- `ReplayAnalysisArea` row shapes/messages match what the live `Add*` methods
  produce for the same input (the no-drift test the extracted builders exist for).

### Integration (testcontainers; real MongoDB — Rule 9, no mocked database)
`services/agent/internal/database/discovery_analysis_checkpoint_repo_integration_test.go`:
- Round-trip of a full `AnalysisStep` — insights with `Validation` + `Quality`,
  validation results, picker telemetry, a large `Prompt` — comes back equal.
- **Idempotency / the duplicate-key acceptance criterion:** `SaveArea` twice for the
  same `(run_id, area_id)` leaves exactly one row, no error.
- Attempt fence: attempt 1 cannot overwrite attempt 2's row and gets
  `ErrSupersededAttempt`; attempt 2 *can* replace attempt 1's.
- `LoadAreas` drops rows whose `exploration_attempt` differs (§3.4, built as the
  three-attempt TTL-expiry scenario, not a synthetic value).
- `EnsureIndexes` creates `uq_run_area` unique and the TTL at the configured
  retention, and recreates the TTL when `DISCOVERY_CHECKPOINT_RETENTION` changes
  (the IndexOptionsConflict path).
- `TouchByRun` moves `created_at` and leaves `attempt` and
  `exploration_attempt` alone.
- `DeleteByRun` removes only the named run's rows.

`services/agent/internal/discovery/analysis_resume_integration_test.go` — the
acceptance criteria end to end, in the shape `orchestrator_resume_integration_test.go`
already uses (real Mongo + real Qdrant, scripted LLM):
- **AC1:** a run killed after K of N areas resumes, analyses exactly N−K, and the
  final discovery holds the union once — K areas neither re-run nor duplicated.
- **AC2:** exactly one `discovery_recommendation_log` row for the resumed attempt's
  discovery, one `discoveries` document after the retire, no duplicated standalone
  insight rows, and no duplicate-key error anywhere in the run.
- **AC3:** same `runID`; zero exploration LLM calls and zero warehouse queries on
  the resumed attempt (v1 preserved).
- A resume that must **re-explore** restores no area (§3.4), and its discovery
  cites only its own exploration.
- `discardCheckpoints` on a completed run leaves both collections empty.

`services/api/database/discovery_checkpoint_repo_integration_test.go` —
`DeleteByRun` purges both collections and reports the combined count.

### Local gates before pushing
`make build`, `make test-go`, `make lint-go` (after
`export PATH=$PATH:$(go env GOPATH)/bin`), `make test-integration`, plus
`go vet -tags=integration ./...` in both services. No dashboard change ⇒ no
`make test-ui` / `lint-ui` needed; `make lint-docs` for the doc edits.

---

## 7. Risks

| Risk | Mitigation |
|---|---|
| **Restoring an area under a different exploration** — wrong evidence attributed to insights | The `exploration_attempt` fence (§3.4), restoration only on the skip-exploration path, and an integration test built from the real three-attempt scenario |
| **A deadline-killed run resumes and re-runs nothing** | Failed areas are never marked (§3.5), with a dedicated unit test |
| **Per-run validation budget resets across attempts**, so a resumed run validates more than the cap | The recorded `insights_validated` delta (§3.6) and a cap-exhaustion test |
| **Doc size.** A restored area doc carries the full `Prompt` | Bounded by construction: `picker.BudgetTokens` is coupled to the model window with the output cap reserved, and `discovery_analysis_steps` already stores the identical payload per area in production. A write that did somehow exceed 16MB is logged and swallowed, so that area is simply re-analysed on resume — degradation, not failure |
| **Live feed misleads** — restored areas invisible under the new attempt | `ReplayAnalysisArea`, emitted once in `RunAt` order before the loop (§4.3/4.6) |
| **A purge site forgets the new collection**, leaking rows past a terminal run | Exactly two purge sites — `discardCheckpoints` and the API's `DeleteByRun`, both of which now purge both collections in one call — plus the TTL as backstop and tests on both |
| **Orphaned previous-attempt agent writes area rows** | `OwnsRun` probe before the write plus the row-level attempt fence, same two-layer pattern (and same documented one-row residual) as v1's `checkpointStep` |
| **Area rows outlive the exploration rows** and look resumable | They cannot: `loadResumeState` refuses a resume with no prefix and no summary, and the keep-set is deliberately not extended (§4.2) |

---

## 8. Alternatives considered

1. **Write per-area insights/recommendations into the live collections and upsert**
   (the issue's literal design). Rejected — §3.1/§3.2. It re-opens the
   duplicate-key hazard v1 closed structurally, needs idempotent Qdrant re-embeds
   that the current design does not need at all, and makes partial results visible
   under a `discovery_id` that does not exist yet.
2. **Reuse `discovery_checkpoints` with a `kind` discriminator.** Rejected —
   §3.3: the unique `(run_id, step_number)` index and the two prefix scanners whose
   gap / stale-tail invariants are v1's most reviewed code.
3. **Delete stale area rows when a new exploration summary is written**, instead of
   stamping `exploration_attempt`. Rejected — §3.4: no safe ordering.
4. **Positional resume ("resume at area index K").** Wrong in this codebase — area
   order comes from a Go map range and is nondeterministic (§Codebase facts 1).
5. **Strip `Prompt` / `Response` from the area checkpoint**, as v1 strips
   `QueryResult` / `FixHistory` / `LLMRequest` / `LLMResponse`. Rejected: unlike a
   raw result set these are bounded by the picker budget, and dropping them would
   make a resumed run's `discovery_analysis_steps` rows show an empty prompt and
   response in the dashboard's analysis debug view — a silent audit-trail
   regression for exactly the runs most in need of inspection.
6. **Derive the validation delta from the stored `ValidationResults`** rather than
   recording it. Rejected — §3.6: it duplicates the cap rule.
7. **A separate `DISCOVERY_ANALYSIS_CHECKPOINT_RETENTION`.** Rejected — one
   feature, one knob (Rule 8). Area rows are only ever useful alongside exploration
   rows, so two independently settable retentions would mostly be a way to
   misconfigure the pair.
8. **Also resume Phase 5 / 5.5 (per-insight, per-recommendation).** Out of scope:
   that is **v3** on the epic roadmap, and the Operator Pause is **v4**.

---

## 9. Acceptance criteria → where each is proven

| Criterion (issue #506) | Proof |
|---|---|
| A run killed after completing K of N areas resumes and analyses only the remaining N−K; the K completed areas are not re-run and not duplicated | §4.4 loop merge; integration **AC1** |
| Insights/recommendations for already-completed areas are not duplicated on resume (idempotent upsert; no duplicate-key crash) | §3.2 (upsert on `(run_id, area_id)`; the tail stays a clean insert under a fresh `discovery_id`); integration **AC2** |
| Same `runID`; exploration is not re-run (v1 behaviour preserved) | Nothing in v1's exploration path changes; integration **AC3** |

---

*This is a **PLAN for review** — no implementation is included in this PR.
Implementation follows after approval.*

Closes #506

— Co-coded with Jale 🤖
