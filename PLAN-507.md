# Discovery run resume (v3): per-insight / per-recommendation resume

Plan for #507, on top of v1 (#505) and v2 (#510). Verified against `main` at `ffd48d6`.

## 1. Problem

`checkpointAnalysisArea` fires only once an area is wholly finished (`orchestrator.go:1827`),
so an area killed after validating J of M insights comes back unmarked and re-pays its
analysis call, its repair calls and all 2M validation calls. Phases 5 / 5.5 have no
checkpoint at all: a death there re-pays the generation call and 2 calls per recommendation.

**Two corrections to the issue's design section:**

- The duplicate-key hazard is already closed. v1 writes a fresh `discovery_id` per attempt
  and retires the superseded one, so the unique index on
  `discovery_recommendation_log.discovery_id` is unviolatable rather than merely unviolated.
  v2 met the same criterion by *not adding* the write that would have needed an upsert; v3
  does the same. No per-item write goes into a live result collection, so no upsert is needed
  anywhere.
- Per-item validation cannot be resumed without first persisting the item. An area's insights
  are one LLM call's output, all of them at once; so is Phase 5's recommendation list. So v3
  is two **draft** checkpoints — the area's post-repair pre-validation output, and the run's
  post-filter recommendation list — carrying the issue's per-item markers on top. The markers
  are the ask; the drafts are what make them reachable.

## 2. Design

### 2.1 Area drafts: their own collection

**The problem.** A draft and a completed area are read differently: a completed area is
restored *and skipped*, a draft is restored *and finished*. If a reader cannot tell them
apart, a draft gets restored as completed — its insights enter the result with no verdicts,
`filterEligibleInsights` fails open on a nil `Validation`, and unvalidated findings reach the
recommender. So any design that puts both under `discovery_analysis_checkpoints`' existing
`(run_id, area_id)` key needs a discriminator, and every reader of that collection — now and
later, in this version and the previous one — has to honour it.

So drafts get their own collection, `discovery_analysis_drafts`, unique on
`(run_id, area_id)`, same TTL setting, same attempt and exploration-attempt fences.
`discovery_analysis_checkpoints` and its repository are then **not modified at all**, which
is the main thing this buys: v2's storage layer cannot regress, and no v2 reader can
misinterpret a v3 row because it has never heard of the collection (see §6). The
`missingValueFigures` / `applyMissingValueFigures` helpers are in the same Go package, so the
draft repo reuses them for the `bson:"-"` `ValueMissing` flag without touching their file.

Two rules replace what a shared key would have given for free:

- **A completed row wins.** At load, drop any draft whose area also has a
  `discovery_analysis_checkpoints` row. Three lines, and it makes a stale draft harmless.
- **Delete the draft when the area completes**, best-effort, with the TTL as the backstop and
  the rule above as the correctness guarantee — v1/v2's discipline everywhere else.

It also gives an operational lever a shared key cannot: dropping `discovery_analysis_drafts`
degrades every in-flight run to exactly v2 behaviour. Dropping
`discovery_analysis_checkpoints` would take v2 down with it.

**Alternatives.**

- **A `stage: "draft"|"completed"` field on the shared row** — my first choice, now rejected.
  It reads well (`SaveArea`'s existing `ReplaceOne` upsert makes the completed write *replace*
  the draft, markers included, so no delete and no precedence rule) but those benefits come
  from reusing the collection, not from the field; the field is the *cost* of the reuse. And
  it is the version-skew hazard in §6: a v2 agent does not know `stage` and restores the
  draft as a finished area. I also overstated the alternative's price — "precedence logic"
  turned out to be the three-line rule above.
- **Derive the state instead of storing a flag** — an area is finished iff every insight it
  carries has a verdict or was cap-skipped, which is readable off the row and would collapse
  v2's restore and v3's into one path. Genuinely tidier than `stage`, and still rejected,
  because it shares `stage`'s actual defect: a v2 agent applies v2's meaning to the row
  regardless of what a v3 agent could have derived from it.
- **A sentinel `area_id`** (`"draft:<id>"`) on the shared row. Rejected: it makes the draft
  and the completed row two different keys, which loses the replace that was the whole
  argument for sharing the collection, and leaves a row a v2 reader would key as an area
  named `draft:conversion`.

### 2.2 Per-insight markers

**The problem.** Once the area's insights are durable, each insight's verifier+refuter verdict
(2 LLM calls) has to become durable *as it lands*, keyed so a later attempt can tell which
insights still need validating — and the writes come from `runBounded`'s workers, so they
have to be safe concurrently.

```go
// AnalysisAreaDraftDoc
InsightVerdicts map[string]models.ValidationResult `bson:"insight_verdicts,omitempty"`

func (r *DiscoveryAnalysisDraftRepository) SaveInsightVerdict(ctx, in InsightVerdictInput) error
// filter: {run_id, area_id, attempt: {$lte: n}}
// update: {$set: {"insight_verdicts.<insight_id>": vr}}
```

A map of distinct dotted paths on the draft's own document. `ValidationResult` is fully
BSON-tagged and carries both `StructuredVerdict`s; insight ids are UUIDs, so legal BSON field
names. `runBounded` joins before returning, so no marker can land after the area completes. A
failed write is logged and swallowed (cost: one re-validated insight); the
`ErrSupersededAttempt` escalation stays on the area-level write, next to the existing
`ownershipLost` gate at `orchestrator.go:1674`.

A marker is stored **only for an insight actually submitted to the verifier**, never for one
stamped `skipped_budget_cap` — so `len(InsightVerdicts)` is an exact budget count and a draft
needs no `insights_validated` field. The opposite of v2's choice for completed areas, whose
`ValidationResults` *do* include cap-skipped entries, which is why v2 records the number.

**Alternatives.**

- **A third collection keyed `(run_id, area_id, insight_id)`** — the issue's literal spec.
  Rejected: its own index, TTL and purge entries, orphan rows to reason about, and a second
  read on a path that already reads the draft. Embedding gets the issue's logical key and
  dies with the row it describes.
- **Re-write the whole draft row after each verdict** (`ReplaceOne` with the updated step) —
  no map, no dotted paths, the row simply *is* the current state. Rejected on correctness,
  not cost: two workers each replacing the document with their own copy of the verdict set
  lose one of the two. Latent at the default `VALIDATION_CONCURRENCY=1`, real on any
  deployment that raises it. The per-field `$set` is what makes concurrent verdict writes
  safe without a lock.
- **An array with `$push`** — also concurrency-safe, but not idempotent: a retried write
  appends a second entry, and the reader needs a scan plus a dedup. A map keyed by id is
  idempotent by construction.
- **Keying on the insight's ordinal in the draft's list** rather than its id. Viable, and
  strictly safer in one respect: an ordinal survives reminting with no bookkeeping. Id chosen
  because §2.4 defers the remint to the merge boundary, which makes the id stable for the
  area's whole in-flight life, and because it is the key the issue asks for. The two
  decisions are coupled — if the remint ever moves to restore time, this key must become the
  ordinal.

### 2.3 When the draft is written

After `repairRefutedInsights`, before `validateInsights` (`orchestrator.go:1788-1797`).
Skipped when the repo is nil, `explorationSummaryAttempt <= 0`, `len(insights) == 0`,
`valPhase.agent == nil`, or `OwnsRun` is false (asked before writing, per `checkpointStep`).
`step.Error` is not a gate — a draft is only reachable on the success path.

The `valPhase.agent == nil` gate is the one asymmetry with the rec draft: each draft is
written only when the phase it protects can cost something, and with validation off an area's
draft protects two in-memory passes.

### 2.4 Restoring a draft, and where the remint goes

Restoring is not v2's merge-and-skip: the loop skips the picker, the analysis call, the figure
passes and repair, and re-enters at the validation block.

The draft's insight ids must not survive into this attempt's result. When
`DISCOVERY_MAX_DURATION` expires mid-validation the loop does not stop — the area reaches
`allInsights`, `checkpointAnalysisArea` fails on the dead context and is swallowed (so the
area stays unmarked, observed twice in v2's live verification), and Phase 7 saves a partial
result under its own `persistContext`, writing those ids into `insights` and Qdrant. Reusing
them is an `E11000` on `insights._id` in Phase 8, which is non-fatal: it logs and returns,
dropping the standalone rows and the embedding, and the retire step then deletes the
predecessor's. A successful run ends with nothing in search.

So **remint, at the merge boundary rather than at restore** — which keeps the stored ids live
for the area's whole in-flight life, so marker writes key on `ins.ID` and a third attempt
restoring the same immutable draft finds every marker under one set of keys. The remint
happens once, before the `analysisLog` / `allInsights` appends, reusing v2's
`reidentifyRestoredArea` verbatim. A freshly-analysed area is not reminted.

### 2.5 Recommendation checkpoint: the second new collection

Run-scoped, different payload — so neither `discovery_analysis_drafts` (keyed by `area_id`,
inlines `AnalysisStep`) nor `discovery_checkpoints` (both readers scan in `step_number`
order) fits. Kept separate from the area drafts rather than discriminated by a `kind` field,
for §2.1's reason: each then gets its natural unique index and its own inlined payload, and
the marginal cost of the second collection is one more entry in lists this PR already edits.
`discovery_recommendation_checkpoints`, unique on `run_id`, TTL from the same `DISCOVERY_CHECKPOINT_RETENTION` with the same drop-and-recreate
dance. `SaveDraft` is an attempt-fenced `ReplaceOne` upsert; `SaveRecVerdict` is a `$set` on
`rec_verdicts.<rec_id>`.

```go
type RecommendationCheckpointDoc struct {
    RunID              string   `bson:"run_id"`
    ProjectID          string   `bson:"project_id"`
    Attempt            int      `bson:"attempt"`
    ExplorationAttempt int      `bson:"exploration_attempt"`
    EligibleInsightIDs []string `bson:"eligible_insight_ids"` // the restore gate, §2.6
    RecVerdicts map[string]models.ValidationResult `bson:"rec_verdicts,omitempty"`
    CreatedAt   time.Time                          `bson:"created_at"`

    // Inlined so its tags stay authoritative. It already owns
    // `bson:"recommendations"` and both applyRecommendationDropStats and
    // applyUnresolvedFigureDropStats resync it to the surviving slice, so there
    // is deliberately NO outer field of that name. A disjoint-tags test pins it.
    models.RecommendationStep `bson:",inline"`
}
```

Written after `applyUnresolvedFigureDropStats` (`orchestrator.go:1946`), and only when
Phase 5 banked something — skipped when `recStep == nil`, when the phase was skipped for no
eligible insights (no LLM call), when `recStep.Error != ""`, or when
`recStep.Status == statusRecommendationParseError`. Same rule `checkpointAnalysisArea`
applies to a failed area. A *clean* zero-rec result **is** checkpointed: re-running it costs
a real call and the honest answer is "none".

Not gated on the validation agent, unlike the area draft: the window it protects runs to the
terminal claim, which even with validation off is Phase 6, Phase 7 and Phase 8's embed +
Qdrant upsert + duplicate check.

### 2.6 The restore gate for recommendations

Restored recs cite insight ids belonging to the generating attempt, and restored insights are
reminted. Both "is this the same insight body" and "does every citation resolve" are answered
by one map, `storedIDByLive map[string]string` (populated only for insights restored from a
**completed** area row). `storedID(id)` is `storedIDByLive[id]` when present, else `id`. The
invariant: **`storedID` is the id carried by the checkpoint row that currently holds this
insight's area.**

| Area | `storedID` | Row that holds it |
|---|---|---|
| analysed fresh here | identity | the completed row this attempt writes |
| restored from a **completed** row | pre-remint id | that row, which v2 never rewrites |
| restored from a **draft**, finished here | identity (reminted id) | the completed row this attempt writes |

So `EligibleInsightIDs` is `storedID` of each insight in `recommenderInput`, and is the same
list on every attempt — which lets the gate be a set comparison rather than a hash with an
opaque failure:

```
liveByStored := invert(storedIDByLive) ∪ identity-for-the-rest
pass iff  len(EligibleInsightIDs) == len(recommenderInput)
     AND  every EligibleInsightIDs entry maps into recommenderInput
     AND  every RelatedInsightIDs entry of every stored rec maps through it
```

On a pass, citations are rewritten through `liveByStored`. On a fail the draft is ignored
(not deleted) and Phase 5 runs normally, logging which clause failed.
`recoverRelatedInsightIDs` is deliberately **not** re-run on the restored path: its fail-open
backfill would attach the whole eligible set and paper over a broken remap.
`validateRelatedInsightIDs` stays as the existing net. A changed `recommendation_verdicts`
between attempts therefore fails the gate and re-runs the recommender, which is right.

### 2.7 Per-rec markers and the rec remint

Same shape as §2.2, keyed by rec id. Rec ids are also minted UUIDs reused as
`recommendations._id` and Qdrant point ids (`orchestrator.go:3017`) and the same deadline path
persists them, so a restored draft's recs are reminted too — after Phase 5.5 returns and
before Phase 7, re-pointing `recValidationResults[i].InsightID`, each `rec.Validation` and the
nested `DocID`s. `recStep.Recommendations` shares the backing array, so one in-place pass
keeps `discovery_recommendation_log` consistent with the result.

### 2.8 Smaller pieces

- **Shared verdict rendering.** Every field of the `*InsightValidation` that
  `validateInsights` builds inline (`validation_phase.go:171-190`) is derivable from the
  stored `ValidationResult` (incl. `Reasoning` = `vr.Verifier.OverallReason`), so it is
  extracted to `insightValidationFrom(vr)` and used by both paths — two renderings of one
  verdict can drift, one cannot.
- **Budget.** v2 seeds completed areas' spend before the loop because area order is a map
  range; a draft's spend is consumed inside its own iteration, so `validateInsights`
  increments `runValidated` per restored marker as it walks them. The cap is then re-decided
  in affected-count order over the remainder — never exceeded, and a previously cap-skipped
  insight is validated if the cap was raised between attempts. Same for
  `validateRecommendations`.
- **No feed replay needed.** A drafted area emitted no rows (`AddAnalysisStep` /
  `AddInsightStep` / `AddValidationStep` all run after the area's checkpoint) and the
  restoring attempt emits them when it finishes the area, restored markers included. A
  restored rec draft replays itself — `AddRecommendationStep` is emitted from `recStep`
  outside the generate/skip branch (`orchestrator.go:1964`). Plus two one-line `phase_detail`
  strings, matching v2's `analysisDetail`.

### 2.9 Not built

No new config (`DISCOVERY_CHECKPOINT_RETENTION` governs the third collection too). No API,
request/response or dashboard change — resumability stays decided by the exploration prefix,
so `api/database/discovery_checkpoint_repo.go`'s `ResumeState` is untouched. No overlay sync
(no dashboard file). No `MissingValueFigures` map for the rec draft: the area draft reuses
v2's field unchanged, and every reader of that `bson:"-"` flag (`figure_template.go:251,586`,
`figure_ref.go:149,369,492,644`, `figure.go:61`) runs inside Phase 5's render, i.e. before the
draft is written — `BuildRecommendationBundle` ignores figures, Phase 7 drops the flag and
`StandaloneRecommendation` has none. Named so the audit is re-runnable.

## 3. Files

**New**

- `services/agent/internal/database/discovery_analysis_draft_repo.go` — `SaveDraft`,
  `SaveInsightVerdict`, `LoadDrafts`, `DeleteByArea`, `DeleteByRun`, `TouchByRun`,
  `EnsureIndexes`, `Retention`. Reuses the package's existing `missingValueFigures` /
  `applyMissingValueFigures` and `isIndexOptionsConflict`.
- `services/agent/internal/database/discovery_recommendation_checkpoint_repo.go` —
  `SaveDraft`, `SaveRecVerdict`, `Load`, `DeleteByRun`, `TouchByRun`, `EnsureIndexes`,
  `Retention`.
- `services/agent/internal/discovery/analysis_draft_resume.go` — `checkpointAreaDraft`,
  `checkpointInsightVerdict`, `loadAreaDrafts` (with the completed-row-wins narrowing),
  `restoreAreaDraft`, `discardAreaDrafts`, `refreshAreaDraftTTL`.
- `services/agent/internal/discovery/recommendation_resume.go` —
  `checkpointRecommendationDraft`, `checkpointRecommendationVerdict`,
  `loadRecommendationDraft`, the §2.6 gate + citation remap,
  `reidentifyRestoredRecommendations`, `refreshRecommendationCheckpointTTL`,
  `discardRecommendationCheckpoint`.

**Not touched:** `discovery_analysis_checkpoint_repo.go` and `analysis_resume.go`'s existing
restore path. v2's storage layer and semantics are unchanged, which is §2.1's point. The only
edit near them is exporting `reidentifyRestoredArea`'s old→new id map for `storedIDByLive`.

**Edited**

| File | Change |
|---|---|
| `libs/go-common/mongodb/collections.go` + `services/agent/internal/database/mongodb.go` | two collection constants and their aliases |
| `models/checkpoint.go` | `AnalysisAreaDraft`, `RecommendationCheckpoint` |
| `discovery/analysis_resume.go` | return `reidentifyRestoredArea`'s old→new map so §2.6 can build `storedIDByLive` |
| `discovery/validation_phase.go` | extract `insightValidationFrom`; a `perItemCheckpoint` param (`done` map + concurrency-safe `save` hook) on `validateInsights` / `validateRecommendations`; restored-marker branches |
| `discovery/orchestrator.go` | §4 step 2's head extraction + draft branch; per-item wiring; remint at the merge boundary; `storedIDByLive`; Phase 5 restore branch; Phase 5.5 wiring + rec remint; two repo fields, two `Opts` fields, typed-nil normalization |
| `discovery/orchestrator_resume.go` | `discardCheckpoints` also drops the area drafts and the rec checkpoint |
| `agentserver/agentserver.go` | construct both repos (~:933), `EnsureIndexes` (~:961), `Opts` (~:1134) |
| `api/database/discovery_checkpoint_repo.go`, `database.go`, `init.go` | purge both new collections; two cascade-list entries; two index blocks (not the TTLs — the agent owns that dance) |
| `docs/concepts/discovery-lifecycle.md`, `docs/reference/data-models.md` | what a draft and a marker keep; `AnalysisAreaDraft` and `RecommendationCheckpoint` sections; the completed-row-wins rule |
| `docs/reference/configuration.md`, `docs/reference/helm-values.md`, `helm-charts/decisionbox-api/values.yaml` | the retention TTL now covers four collections (helm-values.md still says exploration only — v2's omission) |
| `CHANGELOG.md` | Unreleased entry |

## 4. Build steps

1. **Storage.** The repo changes above + their unit and integration tests. No orchestrator
   change, so it reviews on its own.
2. **Phase 4 loop.** Extract the pre-validation half (`orchestrator.go:1541-1794` — prompt
   lookup, picker, prompt build, LLM call, figure settle/correct/render, quantifier verdicts,
   repair) into `o.analyseArea(ctx, areaAnalysisInput) areaAnalysisOutcome`, with the input
   struct built once before the loop and a `disposition` of `ok`/`skipArea`/`failedArea` so
   the existing `continue`s stay at the call site. A pure move. The loop body becomes:

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
       if err := o.checkpointAreaDraft(ctx, step, insights, valPhase.agent != nil); err != nil {
           return nil, err
       }
   }
   // validation block onward, plus per-item wiring and the remint when fromDraft
   ```

   The alternative is wrapping those 250 lines in an `else` and taking an indentation-only
   diff. I picked the extraction; it is the riskiest change here, so it is flagged to be
   overruled — nothing else in the plan depends on which way it goes.
3. **Phase 4.5.** `perItemCheckpoint` through `validateInsights`; `insightValidationFrom`
   extracted; restored-marker branch; `save` hook; remint when `fromDraft`; populate
   `storedIDByLive`.
4. **Phase 5.** The §2.6 gate before the generate/skip branch; on a pass set
   `recommendations` / `recStep` from the draft and skip the block (the feed row and the
   `discovery_recommendation_log` payload then come out of the restored `recStep`). On a miss,
   run normally and checkpoint after the post-processing.
5. **Phase 5.5.** `perItemCheckpoint` through `validateRecommendations`; the rec remint after
   it returns.
6. **Lifecycle wiring.** `agentserver.go`; `discardCheckpoints`; the resume-time TTL refresh;
   the API purge, cascade and `init.go`.
7. **Docs + CHANGELOG** (Rule 4).
8. **Verification** (§5).

## 5. Tests

**Repositories (unit).** `SaveDraft` input validation on both new repos;
`SaveInsightVerdict`'s dotted path and its `$set`-only update (no `attempt`,
`exploration_attempt`, `created_at`); disjoint-tags tests for both new docs — live for
`RecommendationCheckpointDoc`, which inlines a struct already owning
`bson:"recommendations"`; each loader's exploration-attempt fence both ways, and a missing
row returning nil not an error; and an assertion that no test touches
`discovery_analysis_checkpoint_repo.go`'s behaviour, since §2.1 leaves it unmodified.

**Orchestrator seams (fakes, no Mongo).** `checkpointAreaDraft`'s five no-ops and both
superseded paths; a swallowed write failure; `restoreAreaDraft` returning step, insights and
markers with `Quality` absent at the restore point and attached after the tail; the remint
(fresh ids, `ValidationResults[i].InsightID`, each `*InsightValidation`, all four nested
`DocID`s, stored draft unmutated); `storedIDByLive` as the §2.6 table; the gate — pass, size
mismatch, unmappable eligible id, unmappable citation, the rewrite on a pass, and an explicit
assertion `recoverRelatedInsightIDs` is *not* called; `insightValidationFrom` field-for-field
equal to the old inline construction, with/without a refuter and with `RefuterDisabled`;
`validateInsights` with markers — M−J submitted, restored verdicts in the right slots with
their original `ValidatedAt`/`ClaimedMetric`/`WarehouseID`, `runValidated` advanced
correctly, no `save` for a restored item, the cap re-decided including the raised-cap case;
same for `validateRecommendations`; a cap-skipped insight storing no marker; `Concurrency > 1`
asserting one `save` per submitted doc, all landed before return.

**Integration (real Mongo + Qdrant, testcontainers).** Round trip of a fat draft and a fat
rec checkpoint; **completed-row-wins**: an area with both a draft and a completed row loads
as completed, including when the delete-on-complete is made to fail; markers accumulating across three attempts on one immutable
draft; the attempt fence on both writes, both directions; the staleness fence as the real
three-attempt TTL sequence; `TouchByRun` moving only `created_at`; `EnsureIndexes` including
the TTL drop-and-recreate; a purge and a project cascade taking all four collections;
**J-of-M end to end** (exactly M−J verifier calls, no duplicated `ValidationResult`);
**the `E11000` regression proven by reverting the remint** for insight and rec ids; a
restored-and-reminted insight not self-matching in the duplicate check.

**Live.** On the stack v1 and v2 used — Bedrock `claude-sonnet-4-6`, Postgres warehouse with
92k rows, real Qdrant and Mongo, agent `SIGKILL`ed, 5 areas. Killed in Phase 4.5: the
verifier-call count per attempt shows M−J, one `ValidationResult` per insight, each verdict's
`validated_at` in the attempt that produced it. Killed in Phase 5.5: one `prompt`/`response`
pair in `discovery_recommendation_log` (generation not repeated), only unvalidated recs
re-checked. Throughout: one run id, `total_queries` flat, zero `E11000`, no duplicated `_id`
in `insights`/`recommendations`, no dangling `duplicate_of`, and —
the assertion that matters most — `related_insight_ids: UNRESOLVED=0` with reminted ids on
both sides. Plus the deadline case (the path that writes a partial result carrying draft ids)
and the re-explore fence (delete one mid-prefix step row; nothing restored).

**Local gates.** `make build`, `make test-go`, `make lint-go`, `make lint-docs`, and
`go vet -tags=integration ./...` on both services — the Integration Tests job is skipped on
PRs, so that vet is the only pre-merge compile check those files get.

## 6. Risks

| Risk | Mitigation |
|---|---|
| The head extraction changes behaviour in the hottest loop in the product | Pure move, no reorder; `continue`s stay at the call site; its own commit so the diff reads as a move. The `else` variant is the fallback. |
| A restored draft's ids collide in the tail — silent, since Phase 8 is non-fatal, ending with a successful run that has nothing in search | Remint on both sides, with the regression test proven by reverting the fix. The defect class v3 is most likely to introduce. |
| The rec gate is too permissive → recs about findings the run no longer has | §2.6's exact set comparison, with `recoverRelatedInsightIDs` off the restored path so a broken remap fails loudly; `validateRelatedInsightIDs` is the net. |
| Four collections to keep purged | Both new ones added to all five wiring sites in one commit, cascade test extended — the same miss v2's review caught. |
| A stale draft outliving its area (the delete-on-complete is best-effort) | Correctness does not depend on it: a completed `discovery_analysis_checkpoints` row wins at load (§2.1), and the TTL reclaims the row. Covered by an integration test that completes an area with the delete made to fail. |

**Version skew is designed out rather than mitigated,** which is why it is no longer a row
here. Backward: a v2 agent cannot misread a v3 draft, because drafts live in collections it
never queries — it resumes such a run exactly as it does today, re-analysing the drafted area
from scratch. Forward: a v3 agent reads v2's rows unchanged, since v2's collection and
loader are untouched. Dropping the two new collections returns any deployment to v2
behaviour.

Lower-order risks are handled where they arise and not restated here: the validation cap
under changed config (§2.8), draft document growth and concurrent marker writes (§2.2), and
`bson:"-"` fields dropped through a draft (§2.9).

## 7. Alternatives

The per-section alternatives are in §2.1 (where the draft rows live: a `stage` field on the
shared row, deriving the state, a sentinel `area_id`) and §2.2 (how a marker is stored: a
third collection, re-writing the row per verdict, a `$push` array, an ordinal key). The
remaining cross-cutting ones:

- **Idempotent per-item writes into the live collections** (the issue's design). Rejected for
  the reason v2 rejected it: an upsert lets a superseded attempt reclaim shared-id rows under
  its own `discovery_id`, and its cleanup then deletes what are by then the live attempt's
  findings.
- **Shipping only the insight side.** The criteria ask for both, so this is not the plan — but
  steps 4-5 are separable from 2-3 if you want the scope cut.

## 8. Out of scope

Operator Pause (v4). Orphan supersession, two live attempts on one run id — a pre-existing v1
gap tracked as enterprise#397. Metered deployments and the Kubernetes runner beyond what
their existing integration tests cover. Resuming *inside* one LLM call: not a granularity
this engine has, and the draft is the finest durable unit below an area.

---

This is a **PLAN for review** — no implementation is included; the build follows approval.

Closes #507

— Co-coded with Jale 🤖
