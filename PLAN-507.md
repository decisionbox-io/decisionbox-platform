# Discovery run resume (v3): per-insight / per-recommendation resume

Implementation plan for #507. **v3** of the resume roadmap (epic enterprise#387), on top of
**v1** (#438 / #505) and **v2** (#510). Verified against `main` at `ffd48d6`.

---

## 1. The problem, restated against the code that now exists

v1 made exploration durable: a step per `discovery_checkpoints` row, a summary row when
exploration ends, and a resumed run replays the prefix instead of re-querying it. v2 made
each **completed analysis area** durable: a `discovery_analysis_checkpoints` row per
`(run_id, area_id)`, and a resumed run restores those and analyses only the rest.

What is still lost is everything *inside* an area that did not finish, and everything in
Phases 5 / 5.5. Concretely, for one area the Phase 4 loop body
(`orchestrator.go:1510-1857`) spends:

| Work | LLM calls |
|---|---|
| `analyzeAreaInsights` | 1, plus up to `ANALYSIS_PARSE_MAX_RETRIES` corrective re-prompts |
| `repairRefutedInsights` | up to `ANALYSIS_REPAIR_MAX_ROUNDS` per insight whose own evidence refuted a declared claim |
| `valPhase.validateInsights` (Phase 4.5) | **2 per insight** — verifier, then refuter (`checkDoc`), each of which can itself issue lookup rounds |

and for the run as a whole:

| Work | LLM calls |
|---|---|
| `generateRecommendations` (Phase 5) | 1, plus parse retries and the citation self-heal retry |
| `valPhase.validateRecommendations` (Phase 5.5) | **2 per recommendation** |

Today a death anywhere in that span re-pays all of it. `checkpointAnalysisArea` fires only
once the area is wholly finished (`orchestrator.go:1827`), so an area killed after
validating 4 of 6 insights comes back completely unmarked — by design, and correctly for
v2, but it means the per-item work is the largest remaining unprotected spend.

### The issue's design section needs one correction

The issue says v3 is "largely an idempotency / completion-marker exercise, not new engine
mechanics", and that "`discovery_recommendation_log`'s unique `discovery_id` index makes a
naïve re-save a duplicate-key crash".

That hazard is already closed, and not by an upsert. v1 writes a **fresh `discovery_id` per
attempt** and retires the superseded attempt's result afterwards, so the tail is still one
clean insert, one recommendation-log row, one pass of embedding — the unique index is
*unviolatable* rather than merely unviolated (see `SaveArea`'s doc comment and
`orchestrator_resume.go:513`). v2 therefore met the same acceptance criterion by **not
adding** the write that would have needed the upsert. v3 does the same: no per-item write
goes into a live result collection, and all idempotency lives at the checkpoint layer.

So v3 is *not* mainly an idempotency exercise. The real work is this:

> **Per-item validation cannot be resumed without first persisting the item.** An area's
> insights are the output of a single LLM call that produces all of them at once. There is
> no way to "re-enter at insight 5 of 6" unless insights 1-6 themselves are durable. The
> same is true of recommendations: Phase 5 is one call producing the whole list.

That makes v3 two **draft** checkpoints — the area's post-repair, pre-validation output, and
the run's post-filter recommendation list — each carrying **per-item completion markers** on
top. The markers are the issue's ask; the drafts are what make them reachable.

### Honest value

Bigger than the issue estimates, for a reason the issue did not account for: the draft rows
also protect the **analysis call and the repair calls** of an area that died in validation,
and the **generation call** of a run that died in 5.5 or later. On the measured v2
verification stack (5 areas, ~24 insights, 6 recommendations) a resumed run killed mid-area
re-pays ~1 analysis call + ~8 validation calls for that area today; killed in 5.5 it
re-pays 1 generation call + ~12 validation calls. v3 takes both to zero.

Still the smallest rung of the roadmap, and still the largest surface. It is worth building
because the motivating incident of the whole epic was a proxy 504 **on a later LLM call** —
which is exactly a Phase 4.5 / 5.5 death.

---

## 2. Design

### 2.1 The analysis-area draft — a `stage` on the existing collection, not a new one

`discovery_analysis_checkpoints` is already keyed `(run_id, area_id)` unique, already carries
an inline `models.AnalysisStep`, already has the attempt fence, the exploration-attempt
staleness fence, the TTL, `TouchByRun`, and purge wiring in four places. A draft row is the
*same* key describing the *same* area at an earlier stage of the same iteration. So the draft
goes in that collection, with a discriminator:

```go
// discovery_analysis_checkpoint_repo.go
const (
    areaStageDraft     = "draft"     // analysed + repaired, validation not finished
    areaStageCompleted = "completed" // the v2 row: everything the loop body produced
)
```

A `Stage string` field tagged `bson:"stage,omitempty"` goes on
`AnalysisAreaCheckpointDoc`. The read rule is **"draft, or else completed"**, so rows written by a pre-v3 agent (no `stage`) read as
completed — which they are. That is the rolling-upgrade story and it needs nothing else.

Three consequences fall out for free, and they are why this beats a third collection:

- **The completed write replaces the draft.** `SaveArea` is already a `ReplaceOne` upsert on
  `(run_id, area_id)` with the `attempt: {$lte: n}` fence. When the area finishes, the whole
  draft document — including its per-insight markers — is replaced by the completed row. No
  orphan rows, ever, and no precedence puzzle between a draft and a completed row for the
  same area: the unique index makes two impossible.
- **The attempt fence already does the right thing.** A superseded attempt writing a draft
  where a newer attempt owns the area gets a duplicate key → `ErrSupersededAttempt` →
  `ai.ErrAttemptSuperseded`, exactly as `checkpointAnalysisArea` does today.
- **Purge, TTL and cascade are unchanged.** One collection, four existing call sites.

### 2.2 Per-insight completion markers — embedded, keyed by insight id

```go
// AnalysisAreaCheckpointDoc, draft rows only
InsightVerdicts map[string]models.ValidationResult `bson:"insight_verdicts,omitempty"`
```

Keyed `(run_id, area_id, insight_id)` as the issue asks, stored inside the area's own
document because that is the unit that lives and dies together. One method writes one
marker:

```go
func (r *DiscoveryAnalysisCheckpointRepository) SaveInsightVerdict(ctx context.Context, in InsightVerdictInput) error
// filter: {run_id, area_id, stage: "draft", attempt: {$lte: n}}
// update: {$set: {"insight_verdicts.<insight_id>": vr}}
```

`models.ValidationResult` is fully BSON-tagged and carries everything needed, including both
`*valmodels.StructuredVerdict`s. Insight ids are UUIDs, so they are legal BSON field names
(no `.`, no leading `$`).

A verdict is stored **only for an insight actually submitted to the verifier** — never for
one stamped `skipped_budget_cap`, which cost nothing. That makes `len(InsightVerdicts)` an
exact count of the draft's slice of the run's validation budget, so a draft needs no
`insights_validated` field. Worth spelling out because it is the *opposite* of v2's choice
for completed areas: a completed row's `ValidationResults` **do** include cap-skipped
entries, so `len` would overcount there, which is precisely why v2 records the number.

`validateInsights` runs its checks with bounded concurrency (`RunCaps.Concurrency`), so the
marker writes happen from worker goroutines. Distinct dotted paths on one document are safe
under Mongo's document-level locking, and `runBounded` joins before returning, so no write
can land after the area's completed row.

A failed marker write is logged and swallowed — the cost is one re-validated insight, and a
checkpoint must never be able to abort a working run. The `ErrSupersededAttempt`
escalation stays where v2 put it: on the area-level write, next to the `ownershipLost`
gate at `orchestrator.go:1674`.

### 2.3 When the draft is written, and when it is not

Immediately after `repairRefutedInsights` and before `validateInsights`
(`orchestrator.go:1788-1797`), gated the same way `checkpointAnalysisArea` is, plus two
gates of its own:

| Gate | Why |
|---|---|
| `analysisCheckpointRepo == nil` | per-area resume disabled for this deployment |
| `explorationSummaryAttempt <= 0` | no durable exploration summary → nothing could ever restore it |
| `len(insights) == 0` | a legitimately empty area runs no validation; the draft would protect nothing and be replaced seconds later |
| `valPhase.agent == nil` | validation disabled (no verdicts to resume), so the window between the draft and the completed row is two in-memory passes |
| `!statusReporter.OwnsRun(ctx)` | asked **before** writing, for the reason `checkpointStep` gives at length |

Note what is *not* a gate: `step.Error`. A draft is only reachable on the success path — a
`chatErr` or a total parse failure `continue`s before it.

The `valPhase.agent == nil` gate is the one asymmetry with the recommendation draft (§2.5),
and it is principled rather than incidental: each draft is written when the phase it
protects can actually cost something. With validation off, an area's draft protects nothing.

### 2.4 Restoring a draft — and where the remint goes

Restoring a draft is **not** a merge-and-skip like v2's completed area. The area is
unfinished, so the loop has to *finish* it: skip the picker, the analysis call, the figure
passes and repair; re-enter at the validation block with the draft's `step` and `insights`
and the markers it already has.

The draft's `insights` arrive with the ids the generating attempt minted, and those ids must
not survive into this attempt's result. The reasoning is v2's, and it is reachable rather
than theoretical:

> When `DISCOVERY_MAX_DURATION` expires mid-validation, the loop does **not** stop. The area
> is appended to `allInsights`, `checkpointAnalysisArea` fails on the dead context and is
> swallowed (so the area stays unmarked — observed twice in v2's live verification), and
> Phase 7 then saves a partial result under its own `persistContext`, writing those insight
> ids into `insights` and into Qdrant. A later attempt that restored the draft and reused
> those ids would hit `E11000` on `insights._id` in Phase 8, which is non-fatal: it logs,
> returns, drops the standalone recommendation rows and the embedding with them — and the
> retire step then deletes the predecessor's rows. A successful run ends with no findings in
> search at all.

So: **remint, but at the merge boundary, not at restore.** The stored ids stay live for the
whole of the area's in-flight work, which is what makes the marker keys self-consistent —
this attempt's own marker writes key on `ins.ID`, which is still the draft's id, so a *third*
attempt restoring the same immutable draft finds every marker any attempt wrote under the
one set of keys it knows. The remint then happens once, just before
`analysisLog = append(...)` / `allInsights = append(...)`, reusing v2's
`reidentifyRestoredArea` verbatim (it already re-points `ValidationResults[i].InsightID`,
each insight's `*InsightValidation`, and the four nested `StructuredVerdict.DocID`s a shallow
copy would share).

Consequence worth stating: the **completed** row this attempt writes for a draft-restored
area carries the *reminted* ids. That is correct and is what v2 already does for a
freshly-analysed area — the checkpoint stores whatever was live.

A freshly-analysed area is **not** reminted. Its ids were minted this attempt and nothing has
written them.

### 2.5 The recommendation checkpoint — a new collection

Phase 5 / 5.5 are run-scoped, not area-scoped, and the payload is a `RecommendationStep` plus
a verdict map plus the insight set the recommendations were generated over. It does not fit
`discovery_analysis_checkpoints` (keyed by `area_id`, inlines `AnalysisStep`), and it cannot
go in `discovery_checkpoints` (both readers scan rows in `step_number` order and end the
replayable prefix at the first unexpected number). So:

```
discovery_recommendation_checkpoints   # one row per run_id
```

```go
type RecommendationCheckpointDoc struct {
    RunID              string    `bson:"run_id"`
    ProjectID          string    `bson:"project_id"`
    Attempt            int       `bson:"attempt"`
    ExplorationAttempt int       `bson:"exploration_attempt"`

    // EligibleInsightIDs is the insight set Phase 5 was generated over, in the
    // ids the AREA CHECKPOINTS hold for them. The restore gate; see below.
    EligibleInsightIDs []string `bson:"eligible_insight_ids"`

    // RecVerdicts is one Phase 5.5 verdict per recommendation, by rec id.
    RecVerdicts map[string]models.ValidationResult `bson:"rec_verdicts,omitempty"`

    CreatedAt time.Time `bson:"created_at"`

    // Inlined so its BSON tags stay authoritative, the same pattern
    // AnalysisAreaCheckpointDoc uses. It already carries the kept
    // recommendation list under `recommendations` — applyRecommendationDropStats
    // and applyUnresolvedFigureDropStats both resync step.Recommendations to the
    // surviving slice — so there is deliberately NO outer field of that name.
    // A disjoint-tags test pins it, as it does for the area doc.
    models.RecommendationStep `bson:",inline"`
}
```

Unique index on `run_id`; TTL `ttl_discovery_recommendation_checkpoint` from the same
`DISCOVERY_CHECKPOINT_RETENTION`, with the same drop-and-recreate dance on
`IndexOptionsConflict`. Written once per attempt, so `SaveDraft` is a `ReplaceOne` upsert
fenced `attempt: {$lte: n}` returning `ErrSupersededAttempt` on a duplicate key, and
`SaveRecVerdict` is a `$set` on `rec_verdicts.<rec_id>`.

**Written** after Phase 5's post-processing is complete — after
`dropRecommendationsWithUnresolvedFigures` / `applyUnresolvedFigureDropStats`
(`orchestrator.go:1946`) — and only when Phase 5 produced a result the attempt would have
carried forward:

| Skip the write when | Why |
|---|---|
| `recStep == nil`, or the phase was skipped for no eligible insights | no LLM call was made; nothing banked |
| `recStep.Error != ""` | a failed generation call banks nothing, and marking it would make the resume reproduce the failure — the same rule `checkpointAnalysisArea` applies to a failed area |
| `recStep.Status == statusRecommendationParseError` | same |
| no durable exploration summary / nil repo | same as the area rows |

A clean zero-recommendation result **is** checkpointed. Re-running it costs a real LLM call
and the honest answer is "none" — and `recStep.Status` carries the reason the dashboard
renders.

Unlike the area draft, this is **not** gated on `valPhase.agent != nil`: the window it
protects runs from the end of Phase 5 to the terminal claim, which with validation disabled
is still Phase 6 (project context), Phase 7 (save) and Phase 8 (embed + Qdrant upsert + the
duplicate check) — a span that dies for real.

### 2.6 The restore gate for recommendations — exact, and loud when it fails

Restored recommendations cite insights through `RelatedInsightIDs`, and those ids belong to
the attempt that generated them. Two things must hold before the draft can be reused:

1. **The insight body is the same one.** A re-analysed area produces different findings, so
   recommendations drawn from the old set are about findings this attempt no longer has.
2. **Every citation resolves.** Restored insights are reminted, so every id in the stored
   recommendations has to be translated.

Both are answered by one mechanism. Every restore records the mapping from the id the
checkpoint holds to this attempt's live id:

```go
// Orchestrator, per run
storedIDByLive map[string]string // only for insights restored from a COMPLETED area row
```

and `storedID(id)` is `storedIDByLive[id]` when present, else `id`. The rule that makes this
stable is one sentence: **`storedID` is the id the checkpoint row that currently holds this
insight's area carries.**

| Case | `storedID` | The row that holds it |
|---|---|---|
| area analysed fresh by this attempt | identity | the completed row this attempt writes, with these ids |
| area restored from a **completed** row | the pre-remint id | that row, untouched (v2 deliberately never rewrites it) |
| area restored from a **draft** and finished here | identity (the reminted id) | the completed row this attempt writes, with the reminted ids |

So `EligibleInsightIDs` is written as `storedID` of each insight in `recommenderInput`, and
it is the *same list* on every attempt — which is what lets the gate be a plain set
comparison rather than a hash with an opaque failure mode:

```
liveByStored := invert(storedIDByLive) ∪ identity-for-the-rest
gate passes iff
  len(EligibleInsightIDs) == len(recommenderInput)
  AND every id in EligibleInsightIDs maps to an insight in recommenderInput
  AND every RelatedInsightIDs entry of every stored recommendation maps through it
```

On a pass, every citation is rewritten through `liveByStored`. On a fail, the draft is
**ignored and Phase 5 runs normally**, with a log line naming which of the three clauses
failed. Nothing is deleted — v2's rule, so a transient misread cannot lose paid-for work.

Deliberately **not** done on the restored path: `recoverRelatedInsightIDs`. It fail-open
backfills a recommendation with zero valid citations by attaching the entire eligible set,
which on a restored draft would silently paper over a broken remap. The gate refusing the
whole draft is the right failure. `validateRelatedInsightIDs` stays untouched as the
defensive guard it already is.

This also gives configuration drift the right answer for free: change
`recommendation_verdicts` between attempts and the eligible set changes, the gate fails, and
the recommender is re-run over the set the operator now wants.

### 2.7 Per-recommendation markers, and the rec remint

Phase 5.5's verdicts are checkpointed exactly as Phase 4.5's are, keyed by rec id, and
restored the same way. Recommendation ids are also minted UUIDs reused as
`recommendations._id` and as Qdrant point ids (minted at `orchestrator.go:3017`, reused by
`denormalizeRecommendations`), and the deadline path can persist them before dying, so a restored
draft's recs are **reminted too** — after Phase 5.5 returns and before Phase 7, re-pointing
`recValidationResults[i].InsightID`, each `rec.Validation`, and the nested verdict `DocID`s.
`recStep.Recommendations` shares the backing array with `recommendations`, so one in-place
pass covers both and `discovery_recommendation_log` stays consistent with the result.

### 2.8 One shared rendering of a verdict

`validateInsights` builds `*valmodels.InsightValidation` from the live verdict inline
(`validation_phase.go:171-190`). Every field of it is derivable from the `ValidationResult`
that is stored, including `Reasoning` (= `vr.Verifier.OverallReason`). So that construction
is extracted:

```go
func insightValidationFrom(vr models.ValidationResult) *valmodels.InsightValidation
```

and used by both the live path and the restore path — the same discipline that keeps
`analysisRunStep` shared between the live hook and `ReplayAnalysisArea`, for the same reason:
two renderings of one verdict can drift, one cannot.

### 2.9 Budget accounting

v2 seeds `insightsValidatedThisRun` with `restoredValidationBudget(completedAreas)` **before**
the loop, because area order comes from a map range and an unfinished area can run first.
A draft's spend needs no such care — it is consumed inside that area's own iteration — so
`validateInsights` increments `runValidated` for each restored marker as it walks them. One
place owns the arithmetic, and `insightsValidatedThisRun - validatedBefore` stays the area's
true spend, so the completed row's `insights_validated` is right.

The cap is then re-decided, in affected-count-desc order, over whatever is left. An insight
the previous attempt skipped on the cap stays skipped if the budget is still exhausted, and
gets validated if `VALIDATION_MAX_INSIGHTS_PER_RUN` was raised between attempts. Both are
correct; neither can exceed the cap.

Phase 5.5's `validated` counter is local to `validateRecommendations`, so it is incremented
for restored markers in the same planning loop.

### 2.10 The live feed

The feed is attempt-scoped (the API filters `discovery_run_steps` on the run's current
attempt), which is why v1 and v2 need replay functions. v3 needs **almost none**, and that is
a property of where the drafts sit rather than an oversight:

- **A draft area emitted no feed rows at all.** `AddAnalysisStep` / `AddInsightStep` /
  `AddValidationStep` all run at the *end* of the loop body, after the area's checkpoint. The
  attempt that drafted the area died before them. The restoring attempt emits them itself
  when it finishes the area — including an `AddValidationStep` for each restored marker, since
  they are in `step.ValidationResults` by then. Nothing is double-emitted and nothing is
  missing.
- **A restored recommendation draft replays itself.** `AddRecommendationStep` is emitted from
  `recStep` *after* the Phase 5 block, outside the generate/skip branch
  (`orchestrator.go:1964`). Restore `recStep` and the row is emitted under this attempt with
  no new code. Phase 5.5 verdicts have never had a live row.

Two one-line `phase_detail` strings are added, matching v2's `analysisDetail` precedent, so
the live panel explains why the run is doing less work than its counts suggest rather than
looking like it skipped something.

### 2.11 What is deliberately not built

- **No new config.** `DISCOVERY_CHECKPOINT_RETENTION` governs the third collection too, for
  the reason it already governs the second: these rows are only ever usable alongside an
  exploration prefix the run can replay, so separate retentions would mostly be a way to
  misconfigure the set.
- **No API, request/response or dashboard change.** A run that died in analysis or
  recommendations already has `last_checkpoint_step > 0`, so Resume was always offered.
  Resumability stays decided by the exploration prefix alone — `ResumeState` in
  `services/api/database/discovery_checkpoint_repo.go` is untouched.
- **No enterprise overlay sync.** No dashboard file is touched.
- **No per-item write into a live result collection**, hence no upsert anywhere near
  `insights`, `recommendations`, `discovery_recommendation_log` or Qdrant. §1 explains why
  that is what satisfies the acceptance criterion rather than dodging it.
- **No change to `Figure.ValueMissing` handling for recommendations.** v2 carries
  `MissingValueFigures` because the flag is `bson:"-"` and Phase 5's `usableFigures` /
  `buildFigureRefIndex` read it. The area draft reuses that field unchanged (it stores
  insights at the same post-render stage the completed row does). For the **recommendation**
  draft I audited every reader of the flag — `figure_template.go:251`, `figure_template.go:586`,
  `figure_ref.go:149/369/492/644`, `figure.go:61` — and all of them run inside Phase 5's
  settle/render pass, i.e. before the draft is written. After that point nothing reads it:
  `BuildRecommendationBundle` does not look at figures, Phase 7 drops the flag anyway and
  `StandaloneRecommendation` has no figures. So no map is carried for recs. Named here so a
  reviewer can re-run the audit rather than take it on trust.

---

## 3. Files to change

### New

| File | Contents |
|---|---|
| `services/agent/internal/database/discovery_recommendation_checkpoint_repo.go` | `DiscoveryRecommendationCheckpointRepository`: `SaveDraft`, `SaveRecVerdict`, `Load`, `DeleteByRun`, `TouchByRun`, `EnsureIndexes`, `Retention` |
| `services/agent/internal/discovery/recommendation_resume.go` | `checkpointRecommendationDraft`, `checkpointRecommendationVerdict`, `loadRecommendationDraft`, `restorableRecommendations` (the §2.6 gate + citation remap), `reidentifyRestoredRecommendations`, `refreshRecommendationCheckpointTTL`, `discardRecommendationCheckpoint` |

### Edited

| File | Change |
|---|---|
| `libs/go-common/mongodb/collections.go` | `CollectionDiscoveryRecommendationCheckpoints`, documented in the house style of its two siblings |
| `services/agent/internal/database/mongodb.go` | re-export alias |
| `services/agent/internal/database/discovery_analysis_checkpoint_repo.go` | `Stage` + `InsightVerdicts` on the doc; `SaveAreaDraft`; `SaveInsightVerdict`; `LoadAreas` splits rows into completed vs. draft; `SaveArea` stamps `stage: "completed"`; package doc extended |
| `services/agent/internal/models/checkpoint.go` | `AnalysisAreaDraft`, `RecommendationCheckpoint` |
| `services/agent/internal/discovery/analysis_resume.go` | `checkpointAreaDraft`, `checkpointInsightVerdict`, draft half of `loadCompletedAreas` / `restorableAreas`, `restoreAreaDraft`, expose `reidentifyRestoredArea`'s old→new map for `storedIDByLive` |
| `services/agent/internal/discovery/validation_phase.go` | extract `insightValidationFrom`; add a `perItemCheckpoint` parameter (`done` map + concurrency-safe `save` hook) to `validateInsights` and `validateRecommendations`; restored-marker branches in both planning loops |
| `services/agent/internal/discovery/orchestrator.go` | §4 step 2's Phase 4 head extraction + draft branch; per-item wiring; remint at the merge boundary; `storedIDByLive`; Phase 5 restore branch; Phase 5.5 wiring + rec remint; `recommendationCheckpointRepo` field, `Opts` field, typed-nil normalization |
| `services/agent/internal/discovery/orchestrator_resume.go` | `discardCheckpoints` also drops the recommendation checkpoint, from the same place it drops the area rows and for the same stated reason |
| `services/agent/agentserver/agentserver.go` | construct the repo (~:933), `EnsureIndexes` (~:961), pass into `Opts` (~:1134) |
| `services/api/database/discovery_checkpoint_repo.go` | purge the third collection in `DeleteByRun` |
| `services/api/database/database.go` | add to the project delete-cascade list |
| `services/api/database/init.go` | add the unique `run_id` index (not the TTL — the agent owns that dance) |
| `docs/concepts/discovery-lifecycle.md` | extend "Checkpointing and resume": what a draft keeps, what a per-item marker keeps, why a draft is replaced rather than deleted, why a restored draft's ids are reminted |
| `docs/reference/data-models.md` | `stage` + `insight_verdicts` on `AnalysisAreaCheckpoint`; a new `RecommendationCheckpoint` section |
| `docs/reference/configuration.md` | the `DISCOVERY_CHECKPOINT_RETENTION` row now governs three collections |
| `docs/reference/helm-values.md` | its row still says the TTL covers "exploration checkpoints" only — v2's omission; corrected to all three here |
| `helm-charts/decisionbox-api/values.yaml` | the same one-line correction in the commented env block (`:73-88`) |
| `CHANGELOG.md` | Unreleased entry, in the voice of the v1/v2 entries above it |

---

## 4. Build steps

**Step 1 — storage.** Extend `discovery_analysis_checkpoint_repo.go` with `stage`,
`insight_verdicts`, `SaveAreaDraft` and `SaveInsightVerdict`; split `LoadAreas`' output. Add
`discovery_recommendation_checkpoint_repo.go` and the collection constant/alias. Unit tests
for input validation and the disjoint-tags invariants; integration tests for the round trips
and the fences. No orchestrator change yet — this step is independently reviewable.

**Step 2 — the Phase 4 loop.** The one structural decision in this plan.

The pre-validation half of the loop body is ~250 lines (`orchestrator.go:1541-1794`:
prompt lookup, picker, prompt build, LLM call, figure settle/correct/render, quantifier
verdicts, repair). The draft branch has to choose between that half and a restore, then fall
through to the shared tail. I will **extract the head** into

```go
func (o *Orchestrator) analyseArea(ctx context.Context, in areaAnalysisInput) areaAnalysisOutcome
```

where `areaAnalysisInput` is built **once** before the loop (base context, datasets string,
reference dataset, prompt set, picker, exploration steps, `stepByID`, currency plan,
smart-overflow flag) plus the per-area `AnalysisArea`, and `areaAnalysisOutcome` carries
`step`, `insights` and a `disposition` of `ok` / `skipArea` / `failedArea` so the loop keeps
its existing `continue` behaviour at the call site. A pure move: no logic change, no reorder.

The loop body then reads:

```go
if cp, ok := completedAreas[area.ID]; ok { /* v2, unchanged */ continue }

var step models.AnalysisStep
var insights []models.Insight
var draftMarkers map[string]models.ValidationResult
fromDraft := false

if d, ok := draftAreas[area.ID]; ok {
    step, insights, draftMarkers, fromDraft = d.Step, d.Step.Insights, d.InsightVerdicts, true
} else {
    out := o.analyseArea(ctx, in.forArea(area))
    switch out.disposition {
    case skipArea:   continue
    case failedArea: analysisLog = append(analysisLog, out.step); continue
    }
    step, insights = out.step, out.insights
    if err := o.checkpointAreaDraft(ctx, step, insights, valPhase.agent != nil); err != nil { return nil, err }
}
// ... validation block onward, unchanged except for the per-item wiring
// and the remint when fromDraft
```

I am choosing the extraction over the lower-risk alternative (wrap the existing 250 lines in
an `else`, which keeps the diff to an indentation shift) because a 250-line `else` inside a
4 222-line file is a worse thing to leave behind, and because the extracted function is the
first time that head has had a name. It is flagged here precisely so a reviewer can overrule
it — the `else` variant is a mechanical fallback and the rest of the plan is unaffected
either way.

**Step 3 — Phase 4.5 wiring.** `perItemCheckpoint` through `validateInsights`;
`insightValidationFrom` extracted; the restored-marker branch; the `save` hook calling
`checkpointInsightVerdict`. Remint at the merge boundary when `fromDraft`; populate
`storedIDByLive` from both restore paths.

**Step 4 — Phase 5.** `loadRecommendationDraft` + the §2.6 gate before the generate/skip
branch; on a pass, set `recommendations` / `recStep` from the draft and skip the block
(the feed row and the `discovery_recommendation_log` payload then come out of the restored
`recStep` with no further change). On a miss, run normally and `checkpointRecommendationDraft`
after the post-processing.

**Step 5 — Phase 5.5.** `perItemCheckpoint` through `validateRecommendations`; the rec
remint after it returns; `recStep.Recommendations` consistency check.

**Step 6 — lifecycle wiring.** `agentserver.go` construction + `EnsureIndexes` + `Opts`;
`discardCheckpoints`; the TTL refresh on resume; the API purge; the cascade; `init.go`.

**Step 7 — docs + CHANGELOG.** Rule 4. Includes the one-line retention corrections in
`configuration.md`, `helm-values.md` and the Helm `values.yaml` comment.

**Step 8 — verification.** §5, then the live run in §5.4.

---

## 5. Test strategy

`RunDiscovery` is not driven by any test in this repository (it needs a warehouse, an LLM
client, a schema cache and a prompt set), so the split v1 and v2 used applies: pure
functions and repositories are tested directly, the orchestrator seams through fakes, and the
acceptance criteria on a real stack.

### 5.1 Unit — repositories

- `SaveAreaDraft`: missing `run_id` / `area_id` / `exploration_attempt <= 0` rejected with
  the same messages as `SaveArea`; `stage` stamped `"draft"`.
- `SaveInsightVerdict`: empty ids rejected; the dotted path built from the insight id;
  a `$set`-only update that touches neither `attempt` nor `exploration_attempt` nor
  `created_at`.
- Disjoint-tags tests for both docs — `AnalysisAreaCheckpointDoc` (already present, extended
  for `stage`/`insight_verdicts`) and `RecommendationCheckpointDoc`, where the hazard is
  live: `RecommendationStep` already owns `bson:"recommendations"`.
- `Load` on the recommendation repo: the exploration-attempt fence in both directions; a
  missing row returning nil, not an error.

### 5.2 Unit — orchestrator seams (fake stores, no Mongo)

- `checkpointAreaDraft` no-ops: nil store, no durable summary, zero insights, nil validation
  agent, empty area id; and the `OwnsRun == false` path returning `ai.ErrAttemptSuperseded`,
  and `ErrSupersededAttempt` from the store mapping to the same.
- A swallowed write failure leaving the run untouched.
- `restoreAreaDraft`: `step`, `insights` and markers come back; `Quality` is absent (it is
  attached after validation, which is the restore point) and present again after the tail
  runs.
- The remint on a draft-restored area: fresh ids, `ValidationResults[i].InsightID`
  re-pointed, each insight's `*InsightValidation` deep-copied, all four nested
  `StructuredVerdict.DocID`s remapped, and the **stored** draft left unmutated so a third
  attempt remints from the same baseline.
- `storedIDByLive`: identity for fresh areas, pre-remint for completed-restored areas,
  identity for draft-restored areas — the §2.6 table, as a table test.
- The §2.6 gate: pass; fail on a size mismatch; fail on an unmappable eligible id; fail on an
  unmappable citation; and the citation rewrite on a pass. Plus the explicit assertion that
  `recoverRelatedInsightIDs` is **not** called on the restored path (a rec with one bad
  citation must fail the gate, not get backfilled).
- `insightValidationFrom`: field-for-field equal to what the live path used to build inline, over a
  verdict with a refuter, without one, and with `RefuterDisabled`.
- `validateInsights` with markers: M−J submitted; the J restored verdicts land in the right
  result slots with their original `ValidatedAt` / `ClaimedMetric` / `WarehouseID`;
  `runValidated` advanced by J+the fresh ones; no `save` hook call for a restored item; and
  the cap re-decided over the remainder — including the case where a previously cap-skipped
  insight is validated because the cap was raised between attempts.
- `validateRecommendations` with markers: same shape.
- `len(InsightVerdicts)` as the budget: a draft with a cap-skipped insight stores no marker
  for it.
- Concurrency: `RunCaps.Concurrency > 1` with a hook recording its calls, asserting one
  `save` per submitted document and all of them landed before the function returns.

### 5.3 Integration — real MongoDB and real Qdrant, testcontainers

In `services/agent/internal/database` and `services/agent/internal/discovery`, behind the
`integration` tag, mirroring `analysis_resume_integration_test.go`:

- Full round trip of a fat draft (a large prompt, insights with figures, quantifier claims
  and quality caveats) and of a fat recommendation checkpoint.
- **A draft replaced by its completed row leaves exactly one document, and the markers go
  with it.** The structural claim of §2.1.
- Accumulating markers across three attempts on one immutable draft, then completing.
- The attempt fence on the draft write and on the marker write, in both directions.
- The staleness fence built as the real three-attempt TTL sequence (v2's test, extended to
  drafts and to the recommendation row).
- `TouchByRun` on the recommendation row moving only `created_at`.
- `EnsureIndexes` including the TTL drop-and-recreate at a changed retention.
- A purge taking all three collections; the project delete cascade likewise.
- **J-of-M regression, end to end:** a draft with J markers restored, asserting exactly M−J
  verifier invocations and no duplicated `ValidationResult`.
- **The `E11000` regression, proven by reverting the fix** — v2's discipline. Write a partial
  result carrying a draft's insight ids, restore the draft, complete it, run the tail, and
  assert no duplicate key; then confirm the test fails with the exact
  `E11000 duplicate key ... _id` when the remint is removed. Same for recommendation ids.
- Qdrant: a restored-and-reminted insight does not self-match in the duplicate check.

### 5.4 Acceptance criteria — live

On the isolated stack v1 and v2 used (Bedrock `claude-sonnet-4-6`, a Postgres warehouse with
92k rows, real Qdrant and Mongo, the agent spawned as a sibling container and killed with
`SIGKILL`), 5 areas:

| Criterion | How it is proven |
|---|---|
| A run killed after validating J of M insights in an area resumes and validates only the remaining M−J; the J are neither re-validated nor duplicated | kill inside Phase 4.5 of one area; count verifier calls in the agent log per attempt; assert each insight has exactly one `ValidationResult` and each verdict's `validated_at` lands in the attempt that produced it |
| Same for recommendations across Phases 5 / 5.5 | kill inside Phase 5.5; assert the generation call is not repeated (one `prompt`/`response` pair in `discovery_recommendation_log`) and only the unvalidated recs are re-checked |
| Same `runID`; exploration and completed areas are not re-run; no duplicate-key crash | one run id throughout; `total_queries` flat across the resume; zero `E11000` in any attempt; one `discovery_recommendation_log` row; no duplicated `_id` in `insights` / `recommendations`; no dangling `duplicate_of` |
| The assertion that matters most | `related_insight_ids: UNRESOLVED=0` across every recommendation, with reminted ids on both sides |

Plus the deadline case (`DISCOVERY_MAX_DURATION` expiring mid-validation, which is the path
that writes a partial result carrying draft ids) and the re-explore fence (delete one
mid-prefix step row; assert no draft and no recommendation row is restored).

### 5.5 Local gates before the PR

`make build`, `make test-go`, `make lint-go` (all modules), `make lint-docs`, and
`go vet -tags=integration ./...` on both services — the Integration Tests CI job is skipped
on PRs, so that vet is the only pre-merge compile check those files get.

---

## 6. Risks

| Risk | Mitigation |
|---|---|
| **The head extraction changes behaviour in the hottest loop in the product.** | A pure move with no reordering; the `continue` cases stay at the call site via the disposition enum; reviewed as its own commit so the diff is readable as a move. The `else`-wrapped fallback is one commit away if a reviewer prefers it. |
| **A restored draft's ids collide in the tail.** The failure is silent-ish (Phase 8 is non-fatal) and ends with a successful run having no searchable findings. | Remint, with the regression test proven by reverting the fix, on both insights and recommendations. This is the single defect class v3 is most likely to introduce. |
| **The recommendation gate is too permissive** and recommendations about findings the run no longer has reach the result. | Exact set comparison plus a full citation remap, with `recoverRelatedInsightIDs` deliberately off the restored path so a broken remap fails loudly. `validateRelatedInsightIDs` remains the net. |
| **The gate is too strict** and the recommendation draft is rarely reused. | It is reused whenever the generating attempt's area rows are the ones restored, which is the common and motivating case. `storedID` (§2.6) is what extends that to chains of resumes; without it the draft would only ever be reusable by the immediately following attempt. |
| **Validation config changed between attempts** (`VALIDATION_MAX_INSIGHTS_PER_RUN`, `RefuterEnabled`, `recommendation_verdicts`). | The cap is re-decided over the remainder, so it cannot be exceeded. Each verdict is self-describing (`RefuterDisabled`), so a mixed set is readable. A changed eligibility set fails the recommendation gate, which re-runs Phase 5 — the right answer. |
| **Document growth / concurrent writes on the draft row.** | Bounded by insights-per-area, itself bounded by the analysis prompt and by `VALIDATION_MAX_INSIGHTS_PER_RUN=30` across the run. Distinct dotted paths are safe under document-level locking; `runBounded` joins before returning. |
| **A `bson:"-"` field silently dropped through a draft** — the defect class v2's `MissingValueFigures` exists for. | The area draft reuses that field unchanged. For recommendations the audit is written out in §2.11 with every reader named, so it is checkable rather than asserted. |
| **Three collections to keep purged.** A missed site leaks a deleted project's reasoning until the TTL. | The new collection is added to all five wiring sites in one commit, with the cascade test extended — the same miss v2's review caught. |
| **Version skew, one direction only.** Forward is free: a pre-v3 row has no `stage` and is read as completed, which it is. Backward is the hazard — a **pre-v3 agent resuming a run a v3 agent drafted** does not know `stage`, so it restores the draft as a completed area, merging its insights in with no verdicts at all; `filterEligibleInsights` fails open on a nil `Validation`, so those findings reach the recommender unvalidated. | Narrow: it needs a deployment rolled back *between* the crash and the resume, and releases are lockstep across repos. Stated in the PR body rather than papered over. It is also the one thing a separate draft collection would make impossible — see §7, where that trade is spelled out so it can be overruled. |

---

## 7. Alternatives considered

- **A third collection for the per-insight markers,** keyed `(run_id, area_id, insight_id)`
  as the issue literally specifies. Rejected: it needs its own index, TTL dance and five
  purge sites, it leaves orphan rows when an area completes, and it costs a second read on a
  path that already reads the area rows. Embedding in the area's own document gets the
  issue's logical key, the automatic cleanup, and zero new infrastructure.
- **A sentinel `area_id` row for the recommendation draft** inside
  `discovery_analysis_checkpoints`. Rejected: that document inlines `AnalysisStep`, so a
  recommendation row would have to inline a different struct into the same collection and
  every reader would decode rows into the wrong shape — the exact invariant risk the v2
  comment declines for `discovery_checkpoints`.
- **Idempotent per-item writes into the live collections** (the issue's design section).
  Rejected for the reason v2 rejected it, with a review finding behind it: an upsert lets a
  superseded attempt reclaim shared-id rows under its own `discovery_id`, and its cleanup
  then deletes what are by then the live attempt's findings. Fresh `discovery_id` per attempt
  plus reminted ids is the established answer.
- **Reminting at restore rather than at the merge boundary.** Rejected: it forces every
  marker write to translate back to the stored id, so the restoring attempt has to carry a
  reverse map and every write goes through it. Deferring the remint makes the marker keys
  self-consistent by construction.
- **A separate collection for the area drafts** instead of a `stage` on the existing one.
  Rejected, but it is the closest call in this plan. It would make the backward version-skew
  row in §6 impossible — an old agent cannot misread a collection it has never heard of — at
  the cost of a new index, a new TTL dance, two more purge sites, an explicit
  delete-the-draft-when-the-area-completes write (best-effort, so orphan drafts become a
  state that has to be reasoned about), and precedence logic between a draft and a completed
  row for the same area. The `stage` approach gets all of that for free from the unique index
  that is already there. If you would rather pay for the immunity, say so and only §2.1 and
  §3's storage rows change.
- **A content hash instead of an id set for the recommendation gate.** Rejected: the
  citation remap needs the id mapping anyway, so the id set is both the stronger check and
  the cheaper one, and its failure names the actual reason.
- **Skipping the recommendation half** and shipping only the insight side. The acceptance
  criteria ask for both, so this is not a plan, but it is a clean seam: steps 4-5 above are
  separable from steps 2-3 if the scope should be cut. Flagged for your call rather than
  taken.

---

## 8. Out of scope

- Operator **Pause** → v4.
- **Orphan supersession** (two live attempts on one run id) — a pre-existing v1 gap, tracked
  as enterprise#397.
- Metered deployments and the Kubernetes runner beyond the plumbing their existing
  integration tests already cover.
- Resuming *inside* a single analysis or generation LLM call. Not a granularity this engine
  has; the draft is the finest durable unit below an area.

---

This is a **PLAN for review** — no implementation is included, and the build follows after
approval.

Closes #507

— Co-coded with Jale 🤖
