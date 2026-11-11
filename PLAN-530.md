# Implementation plan — issue #530: grounded insight confidence (v1)

> Scope of record for the invariants is the roadmap epic named in the issue; this
> plan references its sections as **§S1 … §S12** and does not restate them. What
> follows is the codebase-specific build: exact types, exact seams, exact files,
> the tests, and the decisions review has to rule on.

---

## 1. The problem, restated — with the measurement

An insight's `confidence` is the analysis model's own guess. Nothing recomputes
it, and the run already holds four row-grounded evidence layers that never reach
it: the validation verdict, the figure verdicts Go re-evaluates, the quantifier
verdicts, and the source-quality caveats.

The number is not merely unchecked — it is **flat**. Measured over the 160
insights stored on a dev stack's four discoveries:

| | |
|---|---|
| model-assigned `confidence` | min **0.75**, mean **0.93**, max **0.99** |
| insights carrying a still-refuted figure verdict | **43 / 160 (27%)** |
| verdict distribution | `partial` 56 · `skipped_budget_cap` 40 · `supported` 39 · `confirmed` 15 · `rejected` 9 · `unverifiable` 1 |

So a user sorting 20–40 findings by confidence is sorting by a number that spans
a quarter of its range and correlates with nothing. Meanwhile 9 insights the
warehouse **contradicted** carry a model confidence of 0.75–0.99, and 43 carry
arithmetic Go refuted and could not repair.

Applying §S4 to that same stored sample gives real separation: **≈34% high ·
35% medium · 6% low · 25% not-confirmed · 0.6% unverifiable** — and a
`supported` insight with one refuted figure lands at 0.70, i.e. **medium**,
which is exactly §S4's "`supported` is High but one dock from Medium".

## 2. What ships, and what deliberately does not

**Ships.** `confidence` stops being model-authored and becomes a function of the
validation verdict, docked for source-quality caveats, refuted arithmetic,
refuted quantifiers, unresolved figures and a sample that ended early. It is
rendered as a **band with its reason**, with `unverifiable` as its own state,
with a **"How this was scored"** breakdown (base → each dock with its amount →
final → the cutoff crossed → engine version), and with legacy documents showing
their old number marked *legacy*. Recommendations get the same treatment from
their **own** verdict.

**Does not ship.** `severity` and `risk_score` stay model-authored (v2), so the
`critical` severity vocabulary does not move, the Qdrant `severity` match filter
cannot drift, and `scoring.severity_*`, `scoring.priority_*`, `scoring.area_weight`,
`scoring.weight_source` and `scoring.risk_flags` are declared but never written.
No pack config contract (v3), no per-customer tuning (v4), no backfill.

**A clarification §S3 needs for v1.** §S3's discipline is "the scorer clears and
overwrites `confidence`, `risk_score`, `severity` and `scoring`". In v1 the
scorer clears and overwrites **`confidence` and `scoring` only** — clearing
`severity`/`risk_score` now would blank the only values that exist for them.
A test pins the freeze (§7, C4).

---

## 3. Decisions this plan settles

These are the calls a reviewer should accept or overturn. Each names the
alternative and why it loses.

**P1 — `Scoring` is a pointer with `bson:"scoring,omitempty"`.**
`scoring == nil` is the legacy marker (§S2, AC "a legacy insight shows its old
number marked legacy"). A value type makes nil indistinguishable from a
zero-valued struct and the legacy marker unimplementable. *Alternative:* a value
type plus an explicit `engine_version > 0` check — two sources of truth for one
fact.

**P2 — the agent's `Confidence` field becomes `bson:"confidence" json:"-"`.**
§S3 asks for the `confidence` coercion to leave `UnmarshalJSON`. But the field
must stay (it is persisted, §S2), and a field that keeps `json:"confidence"`
with no coercion means a model emitting `"confidence": "0.9"` **fails the whole
insight decode** — the exact issue-#342 failure the coercion was added for. With
`json:"-"` the key becomes an unknown field and `encoding/json` ignores it: the
model cannot author the number *at all*, which is the stronger form of what §S3
asks for. The API mirror and both standalone copies keep `json:"confidence"` —
they are never decoded from model output and they serve clients.
*Alternative:* keep the tag and keep the coercion — leaves the model authoring a
field the scorer then overwrites, i.e. one defence instead of two.

**P3 — `confidence` leaves the recommender prompt projection.**
P2 forces a choice, because `TestRecommenderInsightProjectionIsExhaustive`
asserts `recommenderInsight` only emits json names `models.Insight` emits. The
honest answer is to drop it, for the reason the test already gives for
`validation`: *"the eligibility filter has already applied the verdict; the
prompt never refers to it."* After v1, confidence **is** a function of that
verdict, and the recommender's input is already filtered to
`{confirmed, supported}` — so the field is near-constant there and carries no
information the filter has not applied. It also saves tokens.
*Alternative:* relax the exhaustiveness test — weakens a guard that exists
because the prompt once carried whole validation transcripts.

**P4 — the wire name for the sub-document is `evidence_scoring`.**
bson is `scoring` (§S2, non-negotiable). The agent's json tag follows the
house convention for derived fields the model must not author — `evidence_quality`,
`evidence_checks`, `evidence_figures`, `evidence_repair` — because a plain
`"scoring"` key is an obvious thing for a model to volunteer, and a scalar
(`"scoring": "high"`) would fail the decode and drop the insight, the hazard
`Insight.Quality`'s own comment names. The API mirror, `StandaloneInsight` and
`StandaloneRecommendation` use **the same json name**, so there is exactly one
wire name for the dashboard to read across the two endpoints it reads from.
*Alternative:* json `scoring` everywhere, relying on clear-then-write alone —
one defence, and it reintroduces the scalar-decode hole.

**P5 — band cutoffs and dock amounts are engine constants, not env knobs.**
Rule 2 forbids magic numbers, so they are declared once in
`scoring.Config` and never written as literals at a call site. They are **not**
env-tunable, because §S11 requires that *"an insight scored by an earlier engine
version explains itself with the cutoffs that actually scored it"* — an
env-overridable cutoff makes the stored `engine_version` a lie, and the
re-calibration axis in the roadmap is a version bump, not a deployment setting.
They are exposed read-only through the API (§S11) rather than duplicated as TS
constants. *Alternative:* `SCORING_CONFIDENCE_*` env vars — a speculative knob
(Rule 8) that breaks per-version explainability.

**P6 — `Input` carries only what the confidence half reads.**
§S1 lists the eventual `Input` (per-claim verdicts, `[]Figure`, `affected_count`,
area id). Those are the severity/weight half. Adding them now means four fields
nothing sets correctly and no test exercises. The locked contract is the
*signature* `Score(Input) models.Scoring`, which is stable; v2/v3 add fields.
One exception: `RefuterDisabled` **is** carried, because §S4 says it must get no
dock, and a field present with a test that it changes nothing pins that
invariant against a future change that adds one.

**P7 — the docks always sum to exactly the drop the score took.**
§S11 renders *base → each dock with its amount → final*. Three rules can make
that arithmetic untrue: the quality cap (−0.20), the combined refuted cap
(−0.15) and the floor (0.05). So the engine **trims the last dock** so that
`base − Σdocks == final` always, and drops any dock trimmed to zero. A rejected
insight (base 0.05) with a refuted figure therefore shows no dock — honest: the
dock changed nothing. *Alternative:* record untrimmed amounts and have the UI
show "cap/floor applied" — two places that must agree on arithmetic, and the
breakdown stops adding up for the reader.

**P8 — `questions.go` reads the band, the threshold stays live on the derived
score, and `validation_disabled` is explicitly excluded.**
Detail and the one behaviour change in §5, step 4.

**P9 — the two "terminal" bands come from the verdict, the other three from the
score.** `unverifiable` → band `unverifiable`; the non-verdict states → band
`not_confirmed`; everything else → `high` ≥ 0.75, `medium` ≥ 0.40, else `low`.
This is what reconciles §S4's band column (which is the band *at the base*) with
§S4's cutoffs, and it is what keeps `unverifiable` out of low confidence
(acceptance criterion) even though 0.20 is below the low cutoff.

---

## 4. The engine

### 4.1 `libs/go-common/models/scoring.go` (new)

Types only — no logic, so the agent, the API and both standalone copies share
one struct on the wire (the reason `models/validation` and `models/figure.go`
already live here).

```go
// Scoring is the derived scoring sub-document. A document with scoring == nil
// was written before the scorer existed and is rendered as legacy.
type Scoring struct {
    ConfidenceBand   string           `bson:"confidence_band,omitempty"   json:"confidence_band,omitempty"`
    ConfidenceBase   float64          `bson:"confidence_base,omitempty"   json:"confidence_base,omitempty"`
    ConfidenceScore  float64          `bson:"confidence_score,omitempty"  json:"confidence_score,omitempty"`
    ConfidenceReason string           `bson:"confidence_reason,omitempty" json:"confidence_reason,omitempty"`
    ConfidenceDocks  []ConfidenceDock `bson:"confidence_docks,omitempty"  json:"confidence_docks,omitempty"`

    // --- declared, never written in this version (severity stays model-authored) ---
    MagnitudeScore float64        `bson:"magnitude_score,omitempty" json:"magnitude_score,omitempty"`
    SeverityBand   string         `bson:"severity_band,omitempty"   json:"severity_band,omitempty"`
    SeverityScore  float64        `bson:"severity_score,omitempty"  json:"severity_score,omitempty"`
    SeverityDriver *SeverityDriver `bson:"severity_driver,omitempty" json:"severity_driver,omitempty"`
    AreaWeight     float64        `bson:"area_weight,omitempty"     json:"area_weight,omitempty"`
    WeightSource   *WeightSource  `bson:"weight_source,omitempty"   json:"weight_source,omitempty"`
    PriorityTier   string         `bson:"priority_tier,omitempty"   json:"priority_tier,omitempty"`
    PriorityScore  float64        `bson:"priority_score,omitempty"  json:"priority_score,omitempty"`
    RiskFlags      []string       `bson:"risk_flags,omitempty"      json:"risk_flags,omitempty"`

    EngineVersion int `bson:"engine_version" json:"engine_version"`
}

type ConfidenceDock struct {
    Code   string  `bson:"code"   json:"code"`
    Label  string  `bson:"label"  json:"label"`
    Amount float64 `bson:"amount" json:"amount"`
}
type SeverityDriver struct { /* §S2 shape; fields declared, unwritten in v1 */ }
type WeightSource   struct { Kind, Category string }
```

Why the forward fields are declared now rather than added in v2: §S2's whole
argument is that *"a field absent from any of the four copies is a field BSON
silently drops"*, and declaring the sub-document once means v2 and v3 add
**logic**, not a four-copy schema migration. They cost nothing (`omitempty`) and
the mirror guard covers them from day one.

`services/agent/internal/models/scoring.go` (new) aliases them, exactly as
`models/figure.go` and `models/quantifier.go` already do.

### 4.2 `libs/go-common/scoring/config.go` (new)

```go
const EngineVersion = 1

type Config struct {
    Confidence ConfidenceConfig
}
type ConfidenceConfig struct {
    Bases   map[valmodels.Status]float64 // the §S4 table
    Default float64                      // 0.25 — the non-verdict states
    HighAt  float64                      // 0.75
    MediumAt float64                     // 0.40
    Floor   float64                      // 0.05
    Docks   DockConfig                   // amounts + caps
}

func DefaultConfig() Config
func ConfigForVersion(v int) (Config, bool)   // v1 → DefaultConfig()
func KnownVersions() []int
```

`ConfigForVersion` is what §S11's "explained with that version's cutoffs"
rests on, and what the read-only API serves. One entry today; the table is the
place a re-calibration adds a version.

### 4.3 `libs/go-common/scoring/confidence.go` + `score.go` (new)

```go
type Input struct {
    Verdict                valmodels.Status   // InsightValidation.Combined; "" when absent
    RefuterDisabled        bool               // deliberately not docked — §S4
    QualityCaveats         []gowarehouse.QualityCaveat
    FigureVerdicts         []gomodels.FigureVerdict
    QuantifierVerdicts     []gomodels.QuantifierVerdict
    UnresolvedFigures      int                // references the rendered prose still carries
    StepReadBeyondRetained bool               // Verifier.StepReadBeyondRetained
    Config                 Config
}

func Score(in Input) gomodels.Scoring  // pure: no Mongo, no context, no clock
```

`Score` fills the confidence fields and `EngineVersion`, and nothing else.

**Why the dock reads the persisted figure verdicts and needs no correction
carve-out.** `figure_repair.go:150-152` and `figure_ref.go:558-565` both
**re-settle** the verdicts after Go corrects a value — "so the verdicts describe
the values that will be rendered rather than the ones that were refuted". So
`FigureVerdict.Status == fails` on a stored document means a figure that is
**still** refuted in the shipped prose. That is why the measured 43/160 is a
real signal and not a count of repairs that worked, and why §S4's dock is right
as written.

**Why `UnresolvedFigures` is a count the caller supplies.** For insights, an
unresolved reference *ships as written* (`figure_template.go:134-154`), so the
honest detector is "the stored prose still carries `{{ref}}`" — which works
identically on a live insight and on one re-read from Mongo (the on-demand
path). It needs the reference regex and the prose, not the engine's pure data,
so the caller computes it and `Input` carries the number. `ValueMissing` is
`bson:"-"` and cannot be recovered from storage, which rules out deriving it
from `[]Figure`.

### 4.4 The band rule (P9)

| `Combined` | base | band |
|---|---|---|
| `confirmed` | 0.95 | by cutoff |
| `supported` | 0.80 | by cutoff |
| `partial` | 0.55 | by cutoff |
| `rejected` | 0.05 | by cutoff |
| `unverifiable` | 0.20 | **`unverifiable`** (terminal) |
| `validation_disabled` · `skipped_budget_cap` · `""` · legacy `unverified` / `error` / unknown | 0.25 | **`not_confirmed`** (terminal) |

Cutoffs: `high` ≥ 0.75, `medium` ≥ 0.40, else `low`. Inclusive, pinned at the
boundary.

### 4.5 The docks

| Code | Signal | Amount | Cap |
|---|---|---|---|
| `degraded_evidence` | each distinct `QualityCaveat.Kind` on `Insight.Quality`, kinds sorted for determinism | −0.10 each | −0.20 total (so at most two entries) |
| `refuted_arithmetic` | any `FigureVerdict.Status == fails` | −0.10 | — |
| `refuted_quantifier` | any `QuantifierVerdict.Status == fails` | −0.10 | combined with the row above: −0.15, so the quantifier entry is the one trimmed |
| `unresolved_figure` | `UnresolvedFigures > 0` | −0.05 | — |
| `sample_ended_early` | `Verifier.StepReadBeyondRetained` | −0.05 | — |

`RefuterDisabled` is never docked — `Combine` already accounts for the refuter's
absence, and docking again charges the same fact twice. Floor 0.05, and the
last dock is trimmed so `base − Σdocks == final` (P7).

Recommendations carry no `Quality` and no `QuantifierClaims`, so
`degraded_evidence` and `refuted_quantifier` never fire for them. That is
correct, not a gap: a recommendation is shown insights, not warehouse rows.

`ConfidenceReason` and each dock's `Label` are **stored on the document**, so the
dashboard renders the engine's words rather than inventing a second wording —
§S11's "the explanation copy is written once and reused".

---

## 5. Phases

Each phase is independently reviewable and leaves `main` green. Steps 1–2 add
nothing anyone can see; step 3 is the behaviour change.

### Step 1 — the engine, called by nothing (§S1, §S2, §S4)

| File | Change |
|---|---|
| `libs/go-common/models/scoring.go` | **new** — §4.1 |
| `libs/go-common/scoring/config.go` | **new** — §4.2 |
| `libs/go-common/scoring/confidence.go` | **new** — the band rule + the dock rules |
| `libs/go-common/scoring/score.go` | **new** — `Input`, `Score` |
| `libs/go-common/scoring/{confidence,config,score}_test.go` | **new** — §7 A |

### Step 2 — the field on all four persisted copies, written by nothing (§S2)

| File | Change |
|---|---|
| `services/agent/internal/models/scoring.go` | **new** — alias block |
| `services/agent/internal/models/discovery.go` | `Scoring *Scoring` on `Insight` (after `Repair`) and on `Recommendation` (after `Validation`), tagged `bson:"scoring,omitempty" json:"evidence_scoring,omitempty"` |
| `services/api/models/discovery.go` | the same field on the `Insight` and `Recommendation` mirrors, with the existing "a field absent from THIS struct is a field BSON drops" comment extended |
| `libs/go-common/models/insight.go` | `Scoring` on `StandaloneInsight` |
| `libs/go-common/models/recommendation.go` | `Scoring` on `StandaloneRecommendation` |
| `services/api/models/insight_mirror_test.go` | **new** — the source-reading mirror guard (§7 B2) |
| `services/api/models/evidence_trail_test.go` | extend: `scoring` survives BSON decode; absent `scoring` decodes to nil |

The guard is modelled on `analysis_step_mirror_test.go` (read the agent source,
not the type — different modules behind `internal`) but shaped as an exhaustive
partition like `recommenderDroppedInsightFields`: every bson key on the agent's
`Insight`/`Recommendation` is either on the mirror or in a table with the reason
it is not. Two pre-existing entries go in the table rather than widening this
PR's scope: `sql_metadata` (agent-only; no API or dashboard consumer —
confirmed by grep) and `created_at` on `Recommendation` (the API reads the
discovery's own). The guard is what makes the next field's omission a test
failure instead of a silent BSON drop.

### Step 3 — the scorer, the seams, and the model stops authoring (§S3, §S9)

**3a. The two seams.**

`services/agent/internal/discovery/scoring_phase.go` (**new**):

```go
func (o *Orchestrator) scoreInsights(insights []models.Insight)
func (o *Orchestrator) scoreRecommendations(recs []models.Recommendation)
func insightScoringInput(ins models.Insight, cfg scoring.Config) scoring.Input
func recommendationScoringInput(rec models.Recommendation, cfg scoring.Config) scoring.Input
```

Both clear before they write, unconditionally and including the zero case:
`ins.Scoring = nil; ins.Confidence = 0` then the derived values. The
unresolved-figure count comes from the existing `strayRefs` over the rendered
prose.

*Seam 1 — per area.* `orchestrator.go`, immediately after
`attachSourceQuality(insights, stepByID)` (line 1785) and before
`checkpointAnalysisArea` (line 1849). Position is load-bearing in both
directions: after the quality label because the `degraded_evidence` dock reads
it, and before the checkpoint because a completed area is restored with
`continue` (line 1665) and never re-enters the loop body — a score computed
after the write would be absent on every restored area. The scored insights
therefore ride inside `AnalysisAreaCheckpoint.Step.Insights` with no checkpoint
schema change.

*Seam 2 — recommendations.* `orchestrator.go`, after
`valPhase.validateRecommendations` and after `reidentifyRestoredRecommendations`
(line ~2118), before Phase 6. After the remint because a restored
recommendation's ids are only safe from that point.

*Config resolved once per run.* `scoringCfg scoring.Config` on the Orchestrator,
set in `New` from `scoring.DefaultConfig()`. One field, and it is what §S9's
"two areas of one run can never disagree" requires; v3 replaces the right-hand
side.

**3b. The ask is removed.**

| File | Change |
|---|---|
| `schema_properties.go:62` | `Confidence` out of `insightItemProperties` |
| `schema_properties.go:83` | `Confidence` out of `insightRepairItemProperties` |
| `schema_properties.go:137` | `Confidence` out of `recommendationItemProperties` |
| `insight_schema.go:116` | the `Confidence` node goes |
| `insight_schema.go:329` | the repair schema's `keep("confidence")` goes |
| `recommendation_schema.go:114` | the `Confidence` node goes |
| `models/discovery.go:133` | `Confidence` → `bson:"confidence" json:"-"` (P2) |
| `models/discovery.go:267,277` | `confidence` out of `Insight.UnmarshalJSON` |
| `models/discovery.go:302` | the same tag change on `Recommendation` |
| `models/discovery.go:357,365` | `confidence` out of `Recommendation.UnmarshalJSON` |
| `repair_prompt.go:120-122` | `confidence` out of `repairInsightJSON` |
| `repair.go:427-429` | the carry-over out of `mergeRepairedInsight` |
| `orchestrator.go:2536,2565` | `Confidence` out of `recommenderInsight` and the projection (P3) |

`confidence` is in neither the insight nor the recommendation `required` set, so
no `required` list changes in this version (`severity` leaves both in v2).

**3c. Persistence and the Qdrant payload.**

`phase_embed_index.go`: `Scoring: ins.Scoring` / `Scoring: rec.Scoring` in
`denormalizeInsights` (line ~112) and `denormalizeRecommendations` (line ~158).
The Qdrant payload's `confidence` (lines 256, 282) now carries the derived
number, which is strictly better; nothing changes structurally, and the live
`severity` match filter (`qdrant/provider.go:436`) is untouched because severity
is frozen.

### Step 4 — `questions.go` reads the band (P8)

`uncertaintyReason` takes the document's `*Scoring` alongside the legacy
`Confidence` and the threshold:

- a verdict-derived band supplies the **reason** (the stored
  `ConfidenceReason`), so there is one wording;
- `DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT` still applies, now to
  `Scoring.ConfidenceScore` when the document is scored and to the stored
  `Confidence` when it is not — the contract is preserved rather than bypassed;
- `validation_disabled` is **excluded explicitly**. Its derived score is 0.25,
  below the 50% default, so without the guard *every* insight on a
  validation-off run becomes an uncertainty item and the digest stops
  distinguishing anything. This preserves today's behaviour, where the model's
  own high number kept those out — accidentally, but for the right reason.

**The one behaviour change, named so review can veto it.** A `rejected`
document derives 0.05 and therefore becomes an uncertainty item, where today its
model-assigned 0.75–0.99 kept it out — 9 of 160 (6%) in the measured sample.
This is kept: "the warehouse contradicted this claim" is a grounded prompt for a
clarifying question, and the digest is capped. The alternative (a second
exclusion beside `validation_disabled`) is one special case more than the
problem needs.

Also updated: the stale comment at `questions.go:324-327` ("a finding the model
gave no confidence for") — Rule 4; no scored document has no confidence.

### Step 5 — on-demand "Run validation" rescores and persists (§S9.3)

`discovery.ValidateSingleDoc` scores the target in place after stamping
`Validation` — **confidence axis only**. `Severity`, `RiskScore` and the
severity/weight/flag fields stay frozen, which is also why no
`vectorstore.Provider` method is needed (an interface change there would
silently un-implement the optional interface on every provider).

| File | Change |
|---|---|
| `discovery/validate_single_doc.go` | after `target.Validation = …`, call the same `scoreInsights` / `scoreRecommendations` path on the one-element slice |
| `database/discovery_repo.go:82,109` | both update methods take a `DocVerdictUpdate{Validation any; Scoring *gomodels.Scoring; Confidence float64}` and `$set` all three on the positional array element, keeping the `insights.id` / `recommendations.id` predicate that makes a bad id a `MatchedCount == 0` error |
| `discovery/embed_index_store.go` + `embed_index_repo.go` | new `UpdateDocVerdict(ctx, collection, id string, u DocVerdictUpdate) error`, beside the existing `UpdateEmbedding` / `UpdateDuplicate` which already write these two collections |
| `agentserver/validate_doc.go` | pass the new struct to both repo methods, then update the standalone row |

The standalone-doc update is not optional polish: without it the `insights`
collection (what the insights list page and Ask read) would carry a
`confidence` derived from verdict *X* beside a `validation` that says *Y* — an
internally contradictory document, which is worse than either staleness alone.
So the same call writes `validation`, `confidence` and `scoring` together.

**Accepted, documented lag:** the Qdrant payload's `confidence` is not rewritten
on an on-demand rescore. It is write-only — no code path reads it (grepped:
only `type`, `discovery_id` and `severity` are ever read back) — and the
alternative is a payload-update method on `vectorstore.Provider`, which is the
interface hazard §S9.3 exists to avoid. Recorded in the docs.

### Step 6 — dashboard (§S11)

**New read-only API first**, so no cutoff is duplicated as a TS constant:

| File | Change |
|---|---|
| `services/api/internal/handler/scoring.go` | **new** — `GET /api/v1/scoring/engines` → `{current_version, engines:[{version, confidence:{bases, cutoffs, floor, docks}}]}`, built from `scoring.ConfigForVersion`, `Cache-Control: no-store` (the pattern `handler/config.go` uses) |
| `services/api/internal/server/server.go` | `mux.HandleFunc("GET /api/v1/scoring/engines", withRole(viewer, handler.ScoringEngines))` beside `/api/v1/config` (line 211) |

**Components** (new dir `ui/dashboard/src/components/scoring/`):

| File | Role |
|---|---|
| `confidenceMeta.ts` | band → label + semantic tone, via `styles/tokens.css` custom properties. Labels only — the **reason** and the dock **labels** come from the document, never re-worded here |
| `ConfidenceBadge.tsx` | the band pill + its reason; `unverifiable` as its own labelled state; opens the breakdown |
| `ScoringBreakdown.tsx` | base → each dock with its amount → final → the cutoff crossed → engine version |
| `LegacyConfidenceBadge.tsx` | `evidence_scoring` absent → the old number via the existing `ConfidenceBar`, with a *legacy* pill and a one-line tooltip |
| `useScoringEngines.ts` | fetch-once hook over the new endpoint |

**Wiring**: `lib/api.ts` (the `Scoring` / `ConfidenceDock` types,
`evidence_scoring?` on `Insight`, `Recommendation`, `StandaloneInsight`,
`StandaloneRecommendation`, and `getScoringEngines()`); the insights list
(`projects/[id]/insights/page.tsx:333`), the run page's insight table and
recommendation rows (`discoveries/[runId]/page.tsx:481,559`), the insight detail
Assessment card (`…/insights/[insightId]/page.tsx:267-268`), and the
recommendation detail (`…/recommendations/[recommendationId]/page.tsx:215`).

`ConfidenceBar` and `normalizeConfidence` in `components/common/UIComponents.tsx`
**stay** — they are the legacy path. The `Confidence` sort keeps reading the
continuous score, which is what "the continuous score is kept internally" buys.
The "Avg. Confidence" stat card keeps working and starts reading lower (~60%
rather than ~93%) because the number is now honest; left alone (Rule 8), noted
in the CHANGELOG.

**The not-validated states are untouched.** `ValidationRouter` →
`NoValidationCard` keeps its copy and its **Run validation** / **Settings →
Advanced** CTA exactly as they are; the confidence layer maps all non-verdict
states to `not_confirmed` for ranking while the card still explains and offers
the action.

**Enterprise overlay: no sync needed.** Checked against the overlay's actual
file list — it carries no `components/common`, no `components/validation`, no
`components/scoring`, no `lib/api.ts` and no `projects/[id]/insights` or
`discoveries/[runId]/page.tsx`. Its only `[runId]` entry is
`executive-summary`. Re-verified at build time before the PR is marked ready.

### Step 7 — docs + CHANGELOG

| File | Change |
|---|---|
| `docs/reference/data-models.md:43,84` | `confidence` is derived from the validation verdict, not model-assigned; the `scoring` sub-document documented (confidence half only; the severity half named as unwritten) |
| `docs/concepts/discovery-lifecycle.md:343,403,436` | `confidence` out of the worked insight/recommendation JSON and out of the recommender's field list |
| `docs/reference/configuration.md:89` | `DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT` now reads the derived score; `validation_disabled` excluded |
| `docs/reference/api.md` (or the reference index) | `GET /api/v1/scoring/engines` |
| `CHANGELOG.md` | one `### Changed` entry under `[Unreleased]`, in the house long-form style |
| `domain-packs/**/prompts/**` (**31 files**) and `services/api/internal/handler/seed/*.json` (**5 files**) | the `"confidence": 0.85` line out of every worked JSON example |

The pack sweep is **token hygiene and doc truth, not the mechanism** (§S3): seeds
are never overwritten on upgrade and a project snapshots its area prompts at
creation, so existing deployments keep asking the model for the number forever —
which is exactly why correctness comes from the scorer clearing and overwriting.
`seed_packs_drift_test.go` asserts each seed's prompts equal the authored file,
so the two move in lockstep; `digital-analytics.json` has no authored source
(the drift test skips it) and is edited directly.

`scripts/lint-docs.sh` forbids the word "enterprise" and plan filenames in
`docs/` and `CHANGELOG.md`, so neither names the roadmap epic. `make lint-docs`
is in the local gate.

---

## 6. Impact summary — what must keep working (§S12)

| Consumer | Effect |
|---|---|
| The four persisted copies | gain `scoring`; guarded by the new mirror test |
| Qdrant payload | `confidence` now derived (write-only, no reader); `severity` untouched, so the live match filter cannot drift |
| Dedup embedding text | `BuildEmbeddingText` uses name/description/area/**severity**/segment — severity is frozen, so dedup is byte-identical |
| Ask retrieval / `insightsearch` | read `type`, `discovery_id`; unaffected |
| Clarifying questions | step 4; contract preserved, one named behaviour change |
| Ledger rank | `discovery_ledger.go:248` switches on `Severity` — frozen |
| Notification counts / exec summary / email | no confidence reference (grepped) |
| API + dashboard | step 6; legacy documents render their old number marked legacy, and nothing is rewritten |
| Resume | the score rides inside the existing area checkpoint; a restored area comes back scored |
| Mixed image versions | the dashboard reads band, reason, base, docks and final **off the document**; only the cutoff line needs the API. An API older than the agent returns no entry for the newer engine version, and the breakdown renders without that one line — tested |

---

## 7. Test strategy

### A. `libs/go-common/scoring` — table tests, pure, no I/O

1. Every `Combined` value → base + band, exhaustively, including `""`, the two
   legacy values (`unverified`, `error`) and an unrecognised string.
2. Each dock in isolation: fires, correct amount, correct code and label.
3. Caps: three distinct caveat kinds → two entries totalling −0.20; refuted
   arithmetic + refuted quantifier → −0.15 with the quantifier entry trimmed to
   −0.05.
4. Distinct-kind counting: two caveats of the same `Kind` with different
   `Detail` dock once.
5. Floor: `rejected` with every dock → 0.05 exactly, never below, and the
   zero-amount docks are dropped.
6. **`base − Σdocks == final`, over every verdict × every dock subset** (P7) —
   a generated matrix, since this is the invariant §S11's breakdown rests on.
7. Cutoff boundaries: exactly 0.75 → high, exactly 0.40 → medium (inclusive).
8. `unverifiable` stays `unverifiable` and `not_confirmed` stays
   `not_confirmed` even though both scores fall under the low cutoff.
9. `RefuterDisabled` changes nothing: two inputs differing only in that flag
   score identically (§S4).
10. Determinism and purity: the same input scored twice is `DeepEqual`; the dock
    order is stable with caveats supplied in reverse order.
11. `EngineVersion` is stamped; `ConfigForVersion(1)` resolves and an unknown
    version does not.

### B. Models and the mirror

1. BSON round-trip of `Scoring` with docks; a stored document with no `scoring`
   decodes to `nil` (the legacy marker).
2. The source-reading mirror guard: every bson key on the agent's
   `Insight`/`Recommendation` is on the API mirror or in the reasoned-omission
   table (step 2).
3. `StandaloneInsight` / `StandaloneRecommendation` carry `scoring` through BSON.

### C. The agent seam — `scoring_phase_test.go`

1. **Clears and overwrites**: an insight arriving with `Confidence: 0.92` and a
   stale `Scoring` comes out with the derived value and a fresh sub-document.
2. **The zero case**: `Validation == nil` → band `not_confirmed`, score 0.25,
   reason set — not the model's number, and not 0.
3. **Recommendations** score from their **own** verdict, and a recommendation
   with no verdict lands `not_confirmed`.
4. **The v1 freeze**: `Severity` and `RiskScore` are byte-identical before and
   after, for insights and recommendations.
5. **Unresolved figures**: an insight whose rendered description still carries
   `{{f1}}` takes the `unresolved_figure` dock; one whose figures all rendered
   does not.

### D. Orchestrator / resume (existing harnesses)

1. The area checkpoint carries **scored** insights — i.e. the seam is before
   `checkpointAnalysisArea`. Extends `orchestrator_persist_test.go` /
   `discovery_analysis_checkpoint_repo_test.go`.
2. A completed-area restore returns insights already scored and does **not**
   re-score them (no double-dock).
3. A draft-restored area is scored on the attempt that finishes its validation,
   and the resulting checkpoint carries the score.
4. The recommendation seam runs after the remint: the scored recommendations
   carry the reminted ids.

### E. Prompt / schema contracts

1. `schema_property_order_test.go`: `confidence` out of
   `generationOrder["insight"]`.
2. **New inverse guard** — the insight, repair and recommendation schemas ask
   for no `confidence`, so the ask cannot come back silently (the shape
   `TestTheRepairSchemaAsksForNoReference` uses).
3. `UnmarshalJSON`: an envelope carrying `"confidence": "0.9"` still decodes the
   insight (P2's whole point) and leaves `Confidence` at 0; same for a
   recommendation. This is the issue-#342 regression guard.
4. `recommender_insight_test.go` + `orchestrator_test.go:522`: `confidence`
   moves to the dropped table with its reason, and `evidence_scoring` is
   classified (P3, P4).
5. `questions_test.go`: a scored `partial` reports the verdict reason; a
   `validation_disabled` document is **not** an uncertainty item; a `rejected`
   one **is**; a legacy document still honours
   `DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT` against its stored number; the
   existing zero-confidence legacy case keeps passing.
6. `seed_packs_drift_test.go` stays green after the 36-file sweep.

### F. Integration (testcontainers — real, per Rule 9)

1. Mongo testcontainer: `UpdateInsightValidation` / `UpdateRecommendationValidation`
   write `validation`, `confidence` **and** `scoring` on the right array element
   and leave siblings untouched; a wrong id still errors.
2. Mongo testcontainer: `UpdateDocVerdict` updates the standalone row, and the
   embedded and standalone copies agree afterwards.
3. The existing analysis-resume integration test extended to assert a restored
   area's insights come back scored.
4. `go vet -tags=integration ./...` locally before the PR is marked ready — the
   Integration Tests CI job is skipped on PRs.

### G. Dashboard (Jest — the community suite is the gate)

1. The band renders with its reason, per band.
2. `unverifiable` renders as its own labelled state, not as low confidence.
3. `evidence_scoring` absent → the old number with the *legacy* marker.
4. The breakdown shows base, every dock with its amount, the final, the cutoff
   crossed and the engine version — and the displayed numbers add up.
5. The cutoff is unavailable (an older API, or an unknown engine version) → the
   breakdown renders everything else and omits only the cutoff line.
6. `NoValidationCard` / `ValidationRouter` tests unchanged and still green — the
   not-validated copy and CTA are untouched.

### H. Live acceptance (required before the PR leaves draft)

On a dev stack, on a **freshly created project** — prompts and area config are
snapshotted at project creation, so an existing project proves nothing about
this build:

1. A full run: every insight and recommendation carries a band and a reason, and
   no model-assigned number survives anywhere.
2. An `unverifiable` document shows its own badge.
3. A not-validated document still shows the existing copy and the **Run
   validation** CTA; clicking it rescores the confidence axis, persists it to
   both the embedded and standalone copies, and leaves `severity` / `risk_score`
   unchanged.
4. A legacy insight from a pre-change run shows its old number marked *legacy*,
   and the stored document is **not** rewritten.
5. Search, Ask, the questions page and the insights/recommendations lists all
   still work.

---

## 8. Calibration and the yield measurement

Both are PR gates, and neither needs an LLM call for the calibration half —
verdicts, figure verdicts, quantifier verdicts and quality caveats are all
already persisted.

**Band calibration (no LLM calls).** A throwaway Go program under the scratchpad
reads stored discoveries straight out of a dev-stack Mongo, builds a
`scoring.Input` per insight and recommendation from the persisted fields, and
reports: the verdict histogram, the derived band histogram, which docks fire and
how often, how many documents sit within one dock of a band boundary, and the
before/after spread of `confidence`. The first pass over one stack (160
insights) is in §1 and already says two useful things — the `degraded_evidence`,
`refuted_quantifier` and `sample_ended_early` docks fired **zero** times there,
while `refuted_arithmetic` fired on 27% — so the report will be run over every
reachable stack and will state honestly which docks are unexercised in
production data rather than implying all five were calibrated.

**Yield measurement (one real small-model run).** The only model-visible change
is `confidence` leaving the schema, and property order on a grammar wire is
load-bearing (the run-22 measurement in `schema_properties.go`: a small model
lost half its figures to a key order nobody chose). `confidence` sits
second-to-last among the optional properties, so the expected effect is nil —
but "expected nil" is not a measurement. So: one real analysis step on a small
model against the same warehouse and the same cache, this branch vs `main`,
reporting insights per area and figures per insight. Both numbers go on the PR
whichever way they come out.

---

## 9. Risks

| Risk | Containment |
|---|---|
| A model-emitted `"confidence"` string fails the whole insight decode | P2's `json:"-"` makes the key unknown and ignored; guard E3 |
| A model volunteers `"scoring"` and authors its own derivation | P4's `evidence_scoring` tag plus clear-then-write — two defences, the discipline already applied to `QuantifierVerdicts` |
| A restored area comes back unscored | the seam is before the checkpoint write; guard D1–D3 |
| The breakdown's numbers do not add up for a reader | P7's trim invariant; guard A6 and G4 |
| An API older than the agent breaks the breakdown | band/reason/base/docks/final live on the document; only the cutoff line needs the API; guard G5 |
| Clarifying questions flood on a validation-off run | step 4's explicit `validation_disabled` exclusion; guard E5 |
| The 36-file pack sweep drifts from the seeds | `seed_packs_drift_test.go`; guard E6 |
| An on-demand rescore leaves the standalone copy contradicting itself | step 5 writes `validation` + `confidence` + `scoring` together; guard F2 |
| Qdrant payload `confidence` lags a rescore | no reader (grepped); documented, and the alternative is the `vectorstore.Provider` interface hazard §S9.3 names |
| The dashboard's "Avg. Confidence" card changes value | it was always an average of a model's guess; now honest. Noted in the CHANGELOG |
| Three independent wordings of the same explanation | the reason and the dock labels are stored on the document and rendered verbatim (§S11) |

## 10. Alternatives considered

1. **Edit the prompts and trust the model.** Rejected by §S3 and by the code:
   `SeedBuiltInPacks` skips an existing slug and a project snapshots its area
   prompts at creation, so every existing deployment keeps asking forever.
   Correctness has to come from the scorer.
2. **Compute confidence at read time in the API.** Rejected: it desyncs Mongo
   and the Qdrant payload from what is displayed, and makes the stored number a
   different fact from the rendered one. §S10 settles this for the whole engine.
3. **A new field instead of repopulating `confidence`.** Rejected by §S2 — the
   key is a live Qdrant payload field and a sort key in two dashboard pages;
   changing how it is populated keeps every consumer working.
4. **Put the engine under `services/agent/internal/...`.** Rejected by §S1:
   three processes need it and the API cannot import the agent's `internal`
   tree, which is the reason `libs/go-common/models/validation` already exists.
5. **Env-tunable cutoffs.** Rejected — P5.
6. **Fold `unverifiable` into low confidence.** Rejected by the acceptance
   criteria and by P9: it is a different statement ("nothing could check this")
   from low confidence ("this was checked and is weak").
7. **Build the full §S1 `Input` now.** Rejected — P6, Rule 8.
8. **Dock a figure Go corrected.** Rejected: the verdicts are re-settled after a
   correction, so a persisted `fails` is a live defect and no carve-out is
   needed (§4.3).

## 11. Out of scope

Grounded severity, the priority matrix, the default sort and the tier tally
(v2 — #531); the pack config contract, category weights and risk flags (v3); any
per-customer tuning (v4); the backfill of historical documents; and the
`rejected`-hidden-by-default filter (#452). `scoring.severity_*`,
`priority_*`, `area_weight`, `weight_source` and `risk_flags` are declared and
never written here.

## 12. Local checks before the PR leaves draft

`make build` · `make test-go` · `make lint-go` (after
`export PATH=$PATH:$(go env GOPATH)/bin`) · `make lint-docs` · `make test-ui` ·
`make lint-ui` · `go vet -tags=integration ./...` in both services · the Mongo
testcontainer tests in §7 F · then the live acceptance in §7 H and the two
measurements in §8.

---

**This is a PLAN for review — no implementation follows until it is approved.**

Closes #530

— Co-coded with Jale 🤖
