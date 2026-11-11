# Grounded insight confidence & risk (v2): grounded severity, priority matrix, default sort

Implementation plan for issue #531. Epic `enterprise#453` is the spec of record;
this plan does not restate its invariants, it says what the code does about them.
Base branch: **`feat/grounded-confidence-risk`**, which already carries v1.

---

## 1. What this version changes

v1 made **confidence** a function of the validation verdict. It deliberately left
`severity` and `risk_score` as the analysis model's own words, which is what let it
ship alone — and it declared the severity/weight/priority half of the `scoring`
sub-document while writing none of it (`libs/go-common/models/scoring.go:60-85`,
"Declared, never written in this version").

v2 fills that half in, and the list reorders behind it:

1. **Severity becomes grounded magnitude × weight.** Magnitude comes only from figures
   Go has already re-checked, with the `$`/`%` that produced it shown beside the band.
2. **`severity` drops to three bands** (`high` / `medium` / `low`) and `critical` becomes
   the top **priority tier**. Every site that branches on the literal moves in this
   version — a split would ship a run that tells every notification channel "0 critical".
3. **Priority is a per-insight tier** from a 2×3 matrix over (severity band, confidence
   band), with a continuous sort score.
4. **Recommendations inherit severity** from the insights they cite and derive their own
   priority from their **own** verdict; the long-dormant `priority` int is repopulated
   from the tier.
5. **Priority is the default sort** on both insight lists, the API gains `?sort=priority`
   as its default, and a per-run tier tally renders on the run header.
6. **Both labels explain themselves** — magnitude → × weight → final → the cutoff
   crossed, with the checked figure behind the shown amount and its verdict; and the
   matrix cell for priority.

### Why the model's severity has to go

The same argument v1 made for confidence, one field over. `severity` is a word the
analysis model writes about the finding it has just produced; nothing recomputes it, and
`risk_score` is the same judgement as a float. Meanwhile the run already holds the only
honest magnitude it could use: the figures the model declared as data, whose arithmetic
Go re-evaluated against warehouse rows and stamped `holds` / `fails` / `undecidable`
(`libs/go-common/models/figure.go:232-274`). A finding whose headline number the
warehouse refuted could nonetheless call itself `critical`, and did.

`affected_count` is **not** a magnitude and this version must not treat it as one: it is
a model-authored integer, coerced tolerantly from whatever shape arrived
(`services/agent/internal/models/discovery.go:307-320`), with no denominator anywhere in
the result to turn it into a share. A ratio invented from it would be exactly the defect
this epic exists to remove.

---

## 2. The engine, concretely

All of it lands in `libs/go-common/scoring`, beside v1's `confidence.go`, and stays pure:
no Mongo, no context, no clock, no model. That purity is what lets the calibration be a
table test and what lets a later version re-score a stored run in process.

### 2.1 Magnitude (`severity.go`)

```
candidates = figures whose matching FigureVerdict.Status == FigureHolds

amount = max by |Value| over candidates where Unit == "currency"
                                        and Currency == Input.HomeCurrency != ""
share  = max by |Value| over candidates where Unit == "percent"

magnitude = band(|amount.Value|, cfg.AmountBands)   when amount != nil and AmountBands non-empty
          | band(|share.Value|,  cfg.ShareBands)    when share  != nil
          | cfg.BaselineMagnitude                   otherwise          -> "not quantified"
```

- `|Value|`, not `Value`: a −12.4% move matters as much as a +12.4% one, and
  `FigureExcess` / `FigureDiff` legitimately produce negatives.
- **Band → magnitude**: high `1.00`, medium `0.60`, low `0.25`.
  `ShareBands` default: ≥ 20% high, ≥ 5% medium, else low.
- **`AmountBands` ships empty.** An absolute money cutoff is not a constant — Rule 2 —
  so unconfigured deployments band on share while still *showing* the `$`. The lookup is
  written and table-tested against a configured band set; nothing in this version
  configures one.
- **`BaselineMagnitude = 0.60`**, never auto-low. "We could not quantify this" is not
  evidence that it is small, and defaulting it low is how a real finding gets buried by
  a model that forgot to declare a figure.
- **Home currency**: resolved by the caller from the existing `currencyPlan`
  (`services/agent/internal/discovery/figure_currency.go:98-170`), which already refuses
  to guess when datasources disagree. The engine receives one symbol and compares it to
  `Figure.Currency`, which is settled at ingest for every figure
  (`settleInsightFigureCurrencies`). A figure that *states* a non-local currency on a
  multi-currency warehouse keeps that statement and is therefore correctly excluded;
  `HomeCurrency == ""` excludes every amount.

Then:

```
severityScore = min(1.0, magnitude × areaWeight)        // clamped: risk_score's [0,1] contract
severityBand  = high when >= 0.66 | medium when >= 0.33 | low
```

`areaWeight` is `1.0` with `WeightSource{Kind: "default"}` in this version. The field and
its source are **written**, not left zero, because the breakdown has to show
"× weight (and which category set it)" and because that makes the next version a config
change rather than a schema change.

### 2.2 Priority (`priority.go`)

Confidence collapses to **trusted** = {`high`, `medium`} and **untrusted** = {`low`,
`unverifiable`, `not_confirmed`}.

| severity ↓ / confidence → | trusted | untrusted |
|---|---|---|
| **high** | `critical` | `investigate` |
| **medium** | `high` | `low` |
| **low** | `low` | `low` |

`critical > investigate > high > low`. `investigate` outranks `high` because a
high-severity finding nothing could check must surface rather than be buried — severity
sets the ceiling and confidence refines inside it.

```
PriorityScore = tierWeight + 0.15×severityScore + 0.05×confidenceScore
                tierWeight: critical 3, investigate 2, high 1, low 0
```

Monotone in tier, then severity, then confidence: the refinement maxes at `0.20`, well
inside one tier step, so the continuous score can never reorder two tiers. Range
`[0, 3.20]`.

A document with no severity band (a legacy row, or one this build cannot score) gets **no
tier and no priority score**, rather than a guessed one. It sorts last, which is the
honest place for a finding nothing graded.

### 2.3 What the sub-document carries

`models.Scoring` already declares the fields (v1, `libs/go-common/models/scoring.go`).
Three changes to the declared shape, each flagged in §4:

- `SeverityDriver` gains `Kind`, `Quantified`, `Currency`, `Verdict`, and a second
  figure slot for the shown amount (§4.2).
- `SeverityReason` is **added** — the engine's own sentence, stored on the document for
  the reason `ConfidenceReason` is (§4.3).
- `RiskFlags` stays unwritten. Flags need a pack taxonomy, which is not in this version.

### 2.4 Where it runs

Unchanged from v1's three seams; the severity half rides the same calls.

1. **Per area**, inside `scoreInsights`, after `attachSourceQuality` and **before**
   `checkpointAnalysisArea` — a restored area `continue`s at the top of the loop and
   never re-enters the body, so a score written after the checkpoint is absent from every
   restored area (`services/agent/internal/discovery/orchestrator.go:1796-1810`).
2. **Recommendations**, after validation and after the id remint
   (`orchestrator.go:2145-2153`).
3. **On-demand "Run validation"** re-scores the **confidence axis and priority only**.
   Severity, `risk_score`, magnitude, weight and the driver stay **frozen**: severity is
   magnitude × weight and a validation run changes neither, and freezing is what keeps a
   stored `severity` an immutable fact about the run that produced it. Priority *must*
   move, because one of its two inputs just did.

   This is the one real change to v1's wiring. `rescoreTargetInsight` currently assigns a
   whole fresh `Scoring` (`services/agent/internal/discovery/validate_single_doc.go:170`),
   which after v2 would blank the severity half. It becomes a merge.

---

## 3. Phases

Each phase is a commit, in order, and each leaves the tree building and green.

### Phase 1 — the engine (nothing calls it)

| File | Change |
|---|---|
| `libs/go-common/scoring/severity.go` | **new** — the magnitude ladder, the band lookups, the clamp, the driver, the engine's severity sentence |
| `libs/go-common/scoring/priority.go` | **new** — trusted/untrusted collapse, the matrix, the tier weights, the sort score, the 1–5 projection |
| `libs/go-common/scoring/config.go` | `SeverityConfig` + `PriorityConfig` on `Config`; extend `DefaultConfig()` |
| `libs/go-common/models/scoring.go` | `SeverityDriver` shape; `SeverityReason`; severity / priority vocabulary constants; rewrite the "declared, never written" block |
| `libs/go-common/scoring/score.go` | `Input` gains `Figures`, `HomeCurrency`, `AreaID`; `Score` writes both axes and priority; `RescoreConfidence` added |

New config:

```go
type SeverityConfig struct {
    AmountBands []MagnitudeBand // ships EMPTY; see §2.1
    ShareBands  []MagnitudeBand // >=20 high, >=5 medium

    HighMagnitude, MediumMagnitude, LowMagnitude float64 // 1.00 / 0.60 / 0.25
    BaselineMagnitude                            float64 // 0.60

    HighAt, MediumAt float64 // 0.66 / 0.33, inclusive
    DefaultWeight    float64 // 1.0
}

// MagnitudeBand is one cutoff: a value at or above At lands in Band.
// Ordered high-to-low; the first match wins. An empty slice bands nothing,
// which is how AmountBands stays honest rather than hardcoding a currency
// threshold (Rule 2).
type MagnitudeBand struct {
    At   float64
    Band string
}

type PriorityConfig struct {
    TierWeights   map[string]float64 // critical 3, investigate 2, high 1, low 0
    SeverityCoeff float64            // 0.15
    ConfidenceCoeff float64          // 0.05
}
```

`scoreSteps` snapping (`confidence.go:12-27`) applies to every number this engine
produces, for the reason it was introduced: `0.60 × 1.0` has to be `0.6` on screen and in
the stored document, and `1.00 - 0.15 - 0.05` has to be `0.80`. The existing
`TestEveryReachableScoreIsExactAtTwoDecimals` is extended to the severity and priority
score sets rather than duplicated.

### Phase 2 — wire it in; the model stops authoring

The scorer already clears before it writes. It now clears two more fields.

| File | Change |
|---|---|
| `services/agent/internal/discovery/scoring_phase.go` | `scoreInsights` clears `Severity` + `RiskScore` too and mirrors the derived band/score back; collect `Figures`, the resolved home currency and `AnalysisArea` into the `Input` |
| `services/agent/internal/discovery/figure_currency.go` | `currencyPlan.forInsight(ins, stepByID) string` — one currency when every cited step's datasource agrees, `""` otherwise |
| `services/agent/internal/discovery/orchestrator.go` | pass the run's `currencyPlan` to `scoreInsights` (it is already built at `:1597`, before the area loop) |
| `services/agent/internal/models/discovery.go` | `Insight.Severity` / `RiskScore` gain `json:"-"`; `risk_score` leaves `UnmarshalJSON`; `Recommendation` gains `Severity`; `Priority` gains `json:"-"`; `priority` leaves `UnmarshalJSON` and **`coercePriority` is deleted** |
| `services/agent/internal/discovery/schema_properties.go` | `Severity` + `RiskScore` leave `insightItemProperties` and `insightRepairItemProperties`; `Priority` leaves `recommendationItemProperties` |
| `services/agent/internal/discovery/insight_schema.go` | the two property descriptions go; `severity` leaves **both** `required` sets (`:278-280`, `:344`) |
| `services/agent/internal/discovery/recommendation_schema.go` | the `Priority` property and its `required` entry go (`:71-76`, `:214`) |
| `services/agent/internal/discovery/repair_prompt.go` | `severity` / `risk_score` leave `repairInsightJSON` |
| `services/agent/internal/discovery/repair.go` | the two carry-over branches leave `mergeRepairedInsight` (`:420-427`) |
| `services/agent/internal/discipline/rules.go` | the three "critical" clauses rewritten (`:183-185`, `:357-359`, `:450-453`) |
| `services/agent/internal/discovery/validate_single_doc.go` | `rescoreTargetInsight` / `rescoreTargetRecommendation` merge instead of replace |
| `services/agent/internal/database/discovery_repo.go` | `DocVerdictUpdate`'s comment: severity is frozen *because it is carried through the merge*, not because it is unwritten |

`json:"-"` on `Severity` / `RiskScore` / `Priority` is the same second defence v1 put on
`Confidence`, and it does real work: a pack's seeds are never overwritten on upgrade and a
project snapshots its area prompts at creation, so **existing deployments keep asking the
model for these fields forever**. Under `json:"-"` a volunteered `"severity": "critical"`
is an unknown field the decoder ignores rather than one that fails the insight and
silently zeroes the area (the issue-#342 failure mode). Removing the ask is token hygiene;
clearing and overwriting is the mechanism.

The `discipline/rules.go` rewrites:

- `:183` — "The word `critical` may appear ONLY as the value of the structured `severity`
  field" becomes a flat prohibition: there is no structured field for the model to put it
  in any more.
- `:357` — the recommendation clause keeps the prohibition and loses "encode urgency in the
  structured `priority` field instead", which is now derived.
- `:450` — the V4 reject rule drops its `severity`-value carve-out.

### Phase 3 — the severity vocabulary sweep, whole

Every site that branches on the literal `critical`, or ranks the severity word, in one
commit. Pass-through sites (the Qdrant payload, the ask-session row, the run-step row,
the trace, the prompt renderings) need nothing — they carry whatever word the document
holds, which is the point of keeping the bson key.

| File | Change |
|---|---|
| `libs/go-common/models/discovery_ledger.go:249-258` | `Rank()`: `high → 4.0`, `critical → 4.0` kept for legacy rows, so a new top-severity finding is never capped below an old one |
| `services/agent/agentserver/agentserver.go:1268-1271` | the completion event counts by **tier**, falling back to the severity word on legacy documents |
| `services/agent/agentserver/agentserver.go:1327-1333` | `topInsightBriefs` sorts by tier, same legacy fallback |
| `libs/go-common/notify/channel.go:64-85` | `Event.InsightsCritical` / `High` / `Medium` keep their wire names and change meaning to tier counts, documented; `InsightBrief` gains `PriorityTier` |
| `libs/go-common/vectorstore/provider.go:66` | `SearchOpts.Severity` doc comment: the three bands, and `critical` as a value only legacy points carry |
| `services/agent/internal/discovery/insight_schema.go` | the severity description is removed with the property (Phase 2) |
| `services/agent/internal/discovery/reflection.go` | `findingMagnitudeChanged` / `trendReason` compare **weight-free magnitude** (§5.3) |
| `libs/go-common/models/discovery_ledger.go` | `LedgerFinding` + `FindingSighting` gain `MagnitudeScore` |
| `ui/dashboard/src/components/ledger/graph.ts:672`, `flowLayout.ts:122` | the two severity-rank tables tie `high` with `critical`, mirroring `Rank()` |

The notification counters are the reason severity and priority are one version. Split
them and this branch reaches `main` reporting "0 critical" on every run until the next
version lands, because `countBySeverity(insights, "critical")` counts a word no insight
writes any more. A temporary word-to-tier mapping would be a workaround with a Rule 3
smell; counting by tier with a legacy fallback is the real thing and is needed anyway.

### Phase 4 — recommendations

| File | Change |
|---|---|
| `libs/go-common/scoring/severity.go` | `InheritSeverity(from []InheritedSeverity) InheritedSeverity` — max by `SeverityScore` over the cited insights, ties to the first in citation order |
| `services/agent/internal/discovery/scoring_phase.go` | `scoreRecommendations` resolves `related_insight_ids` against the run's insights, inherits the severity half, derives priority from the recommendation's **own** confidence band, and writes `Recommendation.Severity` + `Priority` |
| `libs/go-common/models/recommendation.go` | `StandaloneRecommendation.Severity` |
| `services/api/models/discovery.go` | `Recommendation.Severity` (the API mirror) |
| `services/agent/internal/discovery/phase_embed_index.go` | `denormalizeRecommendations` copies `Severity` |

Inherited, not recomputed: a recommendation declares only `ref` and `sum` figures over
insight figures, so computing a magnitude from them would double-count the insight's own.
Max rather than mean: a recommendation that addresses one severe finding and two mild ones
is as important as the severe one. Priority from its **own** verdict, because the verifier
checked whether *this action follows from those findings* and that is the claim a reader is
being told about — the same split v1 made for confidence.

`RelatedInsightIDs` is already validated to resolve to an eligible insight before
persistence (`validateRelatedInsightIDs`), so the lookup cannot miss in a normal run. It
can miss on the on-demand re-score of a stored document, where only the target is in hand;
there the existing severity half is kept rather than blanked (it is frozen on that path
anyway).

### Phase 5 — read path

| File | Change |
|---|---|
| `services/api/database/interfaces.go` | `ListByProject(ctx, projectID string, limit, offset int, sort string)` on both `InsightRepo` and `RecommendationRepo` |
| `services/api/database/insight_repo.go`, `recommendation_repo.go` | the sort translation |
| `services/api/internal/handler/insights.go`, `recommendations.go` | `?sort=` parsed, **`priority` is the default**, unknown value → 400 |
| `services/api/database/init.go:226-244` | one index per collection |
| `services/api/internal/handler/scoring.go` | `/scoring/engines` serves the severity cutoffs, the magnitude bands and the matrix per version |

```go
// priority (default)
bson.D{{"scoring.priority_score", -1}, {"created_at", -1}, {"_id", 1}}
// created_at (today's order, still reachable)
bson.D{{"created_at", -1}, {"_id", 1}}
```

```go
{Keys: bson.D{{"project_id", 1}, {"scoring.priority_score", -1}, {"created_at", -1}, {"_id", 1}}}
```

Two things worth saying out loud about that index:

- The issue specifies `(project_id, scoring.priority_score desc)`. The index above is a
  superset whose prefix is exactly that. The extra keys are not decoration: **a whole
  run's insights share one `created_at`**, because `denormalizeInsights` stamps `now` once
  for the batch (`phase_embed_index.go:88`). Sorting on `(priority_score, created_at)`
  alone therefore leaves large groups genuinely tied, and a tie under `limit`/`offset`
  paging means a document can appear on two pages or none. `_id` is a unique UUID, so the
  three keys are a total order and the sort is fully index-covered rather than a blocking
  in-memory sort.
- A document with no `scoring.priority_score` sorts **last** under a descending sort (a
  missing field compares as null, below any number). That is the right place for a legacy
  row and it is pinned by a test, because it is behaviour nobody would notice breaking.

`?sort=created_at` stays supported. Changing the default without leaving the old order
reachable would break any API client that pages this endpoint.

### Phase 6 — dashboard

New, under `ui/dashboard/src/components/scoring/` beside v1's confidence components:

| File | What it is |
|---|---|
| `severityMeta.ts` | per-band label / gloss / tone for the three bands + the legacy marker. Labels and tone only — the document's own `severity_reason` is rendered verbatim, as v1 established |
| `priorityMeta.ts` | per-tier label / gloss / tone, and the matrix copy ("high severity × trusted evidence") |
| `SeverityBadge.tsx` | band + driver, hover opens the breakdown |
| `PriorityBadge.tsx` | tier, hover opens the matrix cell and the two bands that selected it |
| `SeverityCell.tsx`, `PriorityCell.tsx` | the single entry point each surface uses; branches once on "is there a derived band" and falls back to the legacy word, exactly as `ConfidenceCell` does |
| `LegacySeverityBadge.tsx` | the stored word, marked legacy |
| `TierTally.tsx` | the per-run tally: four tiers plus the `unverifiable` count, computed where the list renders — no new persisted field |

Extended:

| File | Change |
|---|---|
| `ScoringBreakdown.tsx` | a severity section (magnitude → × weight and what set it → final → cutoff crossed, with the checked figure and its verdict) and a priority section (the matrix cell) |
| `lib/api.ts` | `Scoring` gains the severity/priority fields; `ScoringEngine` gains `severity` + `priority`; `Recommendation.severity`; `StandaloneRecommendation.severity` |
| `app/projects/[id]/discoveries/[runId]/page.tsx` | `severityOrder` → tier order; default sort `Priority`; filter chips by tier; the "Critical Findings" KPI counts the critical tier **or** legacy `severity === 'critical'`; `TierTally` on the run header |
| `app/projects/[id]/insights/page.tsx` | same sort/filter/default changes |
| `app/projects/[id]/recommendations/page.tsx` | severity + priority columns |
| `app/projects/[id]/page.tsx` | the three `critical`-literal counts (`:447`, `:787`, `:833`) read the tier with a legacy fallback |
| the three detail pages under `discoveries/[runId]/…` | `SeverityCell` / `PriorityCell` + the full "How this was scored" block |
| `app/projects/[id]/search/page.tsx:115` | the severity filter offers the three bands and keeps `Critical` labelled as legacy — this filter is a server-side Qdrant match on the payload's `severity`, so the option has to name a value points actually carry |

The Qdrant payload is deliberately not touched. It is write-only (nothing reads
`confidence` or `risk_score` back; only `type`, `discovery_id` and `severity` are ever
filtered on), and adding a tier to it would mean a payload-update method on every
vector-store provider — which would silently un-implement the optional interface on
providers that do not grow it.

### Phase 7 — readers, docs, CHANGELOG

| File | Change |
|---|---|
| `services/agent/internal/discovery/questions.go` | the uncertainty digest line carries the band + the stored `severity_reason` instead of nothing, so the question generator sees which findings matter. The `DISCOVERY_QUESTIONS_CONFIDENCE_MAX_PCT` gate is untouched |
| `docs/reference/data-models.md` | the `Scoring` table gains the written severity/priority fields; `severity` and `risk_score` are documented as derived; the recommendation table gains `severity`; the `priority` coercion sentence goes |
| `docs/reference/api.md` | the `/scoring/engines` payload; `?sort=` on both list endpoints, with `priority` as the default |
| `docs/concepts/discovery-lifecycle.md` | the analysis / recommendation response examples drop the three keys; the `INSIGHTS_DATA` allow-list note is corrected |
| `domain-packs/*/prompts/**/analysis_*.md`, `recommendations.md`, `base_context.md` | the `severity` / `risk_score` keys leave the worked examples; the "use KPI targets to calibrate severity" sentence and the "severity values (`high`/`medium`/`low`)" translation carve-out go with them |
| `services/api/internal/handler/seed/*.json` | the same edits in the five embedded seeds, which are the copy that actually ships |
| `CHANGELOG.md` | one entry under `### Changed`, in the house style of v1's |

The executive-summary generator does not live in this repository. What this version owes
it is the shared copy — the stored `severity_reason` and the per-band labels — so that
whichever reader consumes it quotes one wording rather than inventing a third.

---

## 4. Decisions taken here, and the three worth a reviewer's attention

### 4.1 The engine version stays `1` — flagged

`config.go:24-26` says a re-calibration is a version bump, not an edit, because a stored
document has to be explainable with the numbers that actually scored it.

This version **extends engine 1 rather than adding engine 2**, and the argument is that
engine 1 has never reached `main`: v1 is on `feat/grounded-confidence-risk` only, the epic
integrates before anything releases, so no document anywhere in production was written by
"engine 1 = confidence only". Engine 1 is the grounded-scoring calibration as shipped, and
the confidence numbers inside it do not change — so every v1 breakdown stays exact.

The cost is confined to dev stacks that ran a v1 build: those documents have
`engine_version: 1` with no severity half. The dashboard handles them through the same
branch it needs anyway — an absent `severity_band` renders the stored word marked legacy —
so there is no second code path.

**Alternative, if the reviewer prefers it:** bump to `EngineVersion = 2` and keep a frozen
engine-1 entry in `configs()`. That is more literal but costs a permanent calibration entry
describing a shape that only ever existed on a dev stack, plus an API payload and a
dashboard breakdown that both have to express "this version had no severity axis". I
recommend extending; the decision is cheap to reverse before merge and expensive after.

### 4.2 `SeverityDriver` carries two figures

The spec's note on `AmountBands` requires that an unconfigured deployment still **shows**
a grounded `$` as the driver while the band comes from share. With `AmountBands` empty —
which is what v2 ships — that is not an edge case, it is every insight that declares an
amount. One figure field cannot express it, so:

```go
type SeverityDriver struct {
    // Kind is what BANDED the magnitude: "amount" | "share" | "baseline".
    Kind       string
    Quantified bool            // false only for "baseline"

    // Figure is the figure the magnitude was banded from. Nil for "baseline".
    Figure *SeverityFigure
    // Amount is the largest holding home-currency amount on the document, when
    // there is one -- the number a reader is shown beside the band. Equal to
    // Figure when Kind == "amount"; present alongside a share-banded magnitude
    // when no amount band is configured.
    Amount *SeverityFigure
}

type SeverityFigure struct {
    ID, Display string   // the reference, and the figure as the prose renders it
    Value       float64
    Unit        string
    Currency    string
    Verdict     string   // always "holds" today; stored so the UI quotes it rather
                         // than asserting it
}
```

Each field has exactly one meaning, which the alternative ("one driver, and the amount is
dropped when it did not band") does not — that alternative also fails the acceptance
criterion "severity renders as a band with its `$`/`%` driver", because with the shipped
config no insight would ever show a `$`.

This replaces v1's declared `SeverityDriver{FigureID, Display, Value, Unit}`, which nothing
writes and nothing has stored.

### 4.3 `SeverityReason` is stored on the document

Not in the spec's struct, added for the reason `ConfidenceReason` is in it: S11 requires one
wording across the dashboard, the digest and any summary reader, and three independent
phrasings of "why is this medium" is how they drift. A sentence composed in each reader
from `SeverityDriver` would be two implementations of one piece of copy; a stored sentence
is one, and a test can assert the digest and the dashboard quote it rather than compose it.
Cost: one `omitempty` string.

### 4.4 Smaller calls

- **`priority` int mapping**: `critical → 1`, `investigate → 2`, `high → 3`, `low → 4`.
  Tier order preserved inside the documented 1–5 scale; `5` is left unused rather than
  stretched to fit four tiers.
- **Recommendations get no `risk_score`.** They have no such field on any of the four
  persisted copies, and adding one to carry a number nothing renders is exactly what
  Rule 8 forbids.
- **No run-document config pinning.** The spec pins the resolved config at attempt 1 so a
  mid-run change cannot score two halves of a run differently. Weights are uniform in this
  version, so there is nothing a resume could disagree about; the pin belongs to the
  version that makes weights configurable.
- **No backfill.** Forward-only, per D7. Legacy documents keep their stored word, marked
  legacy.

---

## 5. Compatibility

### 5.1 What a reader sees

| Document | `severity` | What renders |
|---|---|---|
| Written before v1 (`scoring == nil`) | model's word, possibly `critical` | the word, marked legacy; no tier; sorts last |
| v1 dev-stack (`scoring` present, no `severity_band`) | model's word | same as above; the confidence band still renders from its own fields |
| v2 and after | derived band | band + driver + tier, both inspectable |

### 5.2 Every consumer keeps decoding

`severity`, `risk_score` and `confidence` keep their bson keys and types. `severity` is a
live Qdrant match filter and sits inside the dedup embedding text; what changes is how it
is populated. The mirror guard
(`services/api/models/insight_mirror_test.go:82`) is the enforcement that every new field
reaches all four persisted copies — a field absent from any one of them is a field BSON
silently drops on the way to a client. It needs extending for `Recommendation.Severity`.

### 5.3 Two things the vocabulary change would break, and the fix

**The ledger would manufacture a trend.** `findingMagnitudeChanged` returns true when
`prior.Severity != cur.Severity`, and `trendReason` reports *"severity moved from critical
to high"* (`reflection.go:921-926`). On the first run after this version lands, every
re-sighted legacy finding would report a worsening that is purely the vocabulary change —
naming a symptom that does not exist.

Fix, per S10: the comparison moves to **weight-free magnitude**. `LedgerFinding` and
`FindingSighting` gain `MagnitudeScore`; the comparison uses it when both sides have one,
and when the prior does not (a legacy row) the severity-word comparison is **skipped** and
only the affected-count delta decides. There is no comparable prior magnitude on that row,
so "no detectable trend" is the honest answer and a fabricated one is not.

**A new top-severity finding would rank below an old one.** `LedgerFinding.Rank()` maps
`critical → 4.0` and `high → 3.0`, and the dashboard's two ledger rank tables do the same.
After this version the top band is `high`, so a new finding would be capped out of the
prompt by a stale `critical`. `high` joins `critical` at the top on all three.

---

## 6. Test strategy

### 6.1 Engine — table tests, where the calibration lives

`libs/go-common/scoring/severity_test.go`, `priority_test.go`.

**Magnitude precedence and the home-currency rule**
- holding amount in home currency + no amount bands + a holding share → banded on share,
  `Kind: "share"`, `Amount` still populated with the `$`.
- the same with amount bands configured → `Kind: "amount"`, `Figure == Amount`.
- amount in a **non-home** currency → excluded; share bands it.
- `HomeCurrency == ""` (datasources disagree, or no currency configured) → every amount
  excluded.
- amount whose verdict is `fails` → excluded. Same for `undecidable`, and for a figure with
  **no verdict entry at all**.
- percent figure whose verdict `fails` → excluded; falls to baseline.
- no qualifying figure → `BaselineMagnitude`, `Quantified: false`, `Kind: "baseline"`,
  `Figure: nil`, and the band is **not** low.
- negative values: `−12.4%` bands as `12.4%`; `−$2.4M` is the largest amount when the
  alternative is `$1M`.
- `affected_count` set to a huge number with no figures → still baseline. The named
  regression guard.
- two amounts of equal magnitude → deterministic pick (first in figure order), asserted.
- a percent figure above 100 (a quotient declared as percent) → high, not an error.

**Severity score and band**
- the clamp: magnitude `1.00` × a weight above 1 stays `1.0`, so `risk_score` honours its
  `[0,1]` contract.
- cutoffs are inclusive at exactly `0.66` and `0.33`.
- `AreaWeight` and `WeightSource{Kind:"default"}` are written, not zero.

**Priority**
- all six matrix cells, both confidence groupings, each asserted by name.
- the acceptance case explicitly: high severity + `unverifiable` → `investigate`, and its
  score above every `high`-tier score. This is AC3 and it gets its own named test.
- `not_confirmed` and `low` collapse to untrusted; `high` and `medium` to trusted.
- monotonicity: for every reachable (tier, severityScore, confidenceScore) triple, the sort
  score never crosses a tier boundary. Generated over the reachable score sets rather than
  hand-listed.
- no severity band → no tier, no score.
- every reachable severity and priority score is exact at two decimals (extends v1's test).

**Inheritance**
- max over cited insights by severity score; the driver comes from the argmax insight.
- ties → first in citation order.
- a citation that resolves to nothing → the remaining insights decide; all of them missing
  → severity half left absent rather than zeroed.

**Confidence-axis re-score**
- `RescoreConfidence` moves confidence and priority and leaves magnitude, severity, weight,
  driver and flags **byte-identical**. Asserted field by field, not by spot check.
- a prior with no severity half → no tier invented.

### 6.2 Seam and persistence

`services/agent/internal/discovery/scoring_phase_test.go`
- `TestTheScorerLeavesSeverityAndRiskScoreAlone` is **deleted** and replaced by
  `TestTheScorerClearsTheModelsSeverityAndRiskScore`: an insight arriving with
  `severity: "critical"`, `risk_score: 0.9` and a stale `scoring` comes out carrying the
  derived band and score and nothing of what it arrived with — including the zero case,
  where no verdict and no figures still produce a written severity.
- idempotence: scoring twice is identical.
- the recommendation path writes `Severity` and `Priority`.

`scoring_seam_test.go` — the four existing seam tests extended: a restored area comes back
scored on **both** axes, and a draft restored area is scored on the attempt that finishes
it.

`libs/go-common/models/scoring_test.go`
- `TestScoringOmitsTheUnwrittenHalfOnTheWire` is rewritten: `risk_flags` is the only half
  still omitted, and the severity/priority fields round-trip through BSON and JSON on all
  four persisted copies.
- the severity band vocabulary is the three values; the tier vocabulary is the four.

`services/api/models/insight_mirror_test.go` — the exhaustive partition picks up
`Recommendation.Severity` automatically; the test fails until the mirror carries it, which
is the point.

`scoring_persist_integration_test.go` (testcontainer Mongo) — a discovery saved and read
back carries the severity and priority halves on the embedded insight, the standalone
insight, the embedded recommendation and the standalone recommendation.

### 6.3 Schema and prompt discipline

- `schema_confidence_rule_test.go` generalises to all three derived fields: no schema asks
  for `severity`, `risk_score`, `confidence` or `priority`, by property name **or** by a
  description that asks the model to rate importance. The existing walk already catches the
  description shape.
- `TestTheInsightSchemaRequiresWhatAnInsightCannotLack` and the repair-schema twin: the
  required sets lose `severity` and keep everything the run-22 measurement put there.
- `TestSchemaPropertyOrderIsDeclaredNotAlphabetical` keeps passing with three properties
  gone.
- a new test asserts the five embedded seeds and the authoring prompt files ask for none of
  the four keys — the seeds are the copy that ships, and they drifted from the prompts
  before.
- a decode test: an insight carrying `"severity": "critical"` and `"risk_score": "0.9"`
  decodes successfully with both fields zero, and the whole area is **not** dropped.
- `coercePriority`'s deletion is covered by the same decode test for `"priority": "P2"`.

### 6.4 Vocabulary sweep

- `Rank()`: a `high` finding and a legacy `critical` finding rank equal, and both above
  `medium`.
- the completion event counts by tier on new documents and by the severity word on legacy
  ones, including a **mixed** run — the case that would have shipped "0 critical".
- `topInsightBriefs` orders critical → investigate → high → low, with a legacy document
  placed by its word.
- the ledger trend: a legacy prior re-sighted under the new vocabulary reports **no**
  severity move; a genuine magnitude move does report one.

### 6.5 Read path

`services/api/internal/handler/insights_test.go`, `recommendations_test.go`
- no `sort` → priority order.
- `?sort=created_at` → today's order.
- `?sort=nonsense` → 400, no silent fallback.
- a legacy document with no `scoring` sorts last under the priority sort.
- paging is stable: two overlapping `offset` windows over a set with many tied priority
  scores and one shared `created_at` return each document exactly once. This is the test
  that would have caught the index-without-a-tiebreak bug.

`services/api/database/integration_test.go` (testcontainer Mongo)
- the new indexes exist after `init`, and `explain` shows the priority sort is served by
  one — a blocking in-memory sort on a project-sized collection is the failure this index
  exists to prevent, and asserting the index exists does not assert it is used.

`handler/scoring_test.go` — the payload carries the severity cutoffs, the magnitude bands
(including `amount_bands: []`, visibly empty rather than absent) and the matrix.

### 6.6 Dashboard (Jest + Testing Library)

`ui/dashboard/src/__tests__/` beside v1's `ConfidenceBadge.test.tsx`
- `SeverityBadge`: band label, driver rendering for amount / share / not-quantified; the
  legacy fallback renders the stored word marked legacy.
- `PriorityBadge`: the four tiers; the hover shows the matrix cell and the two bands.
- `ScoringBreakdown`: the severity column adds up (magnitude × weight = final), names the
  cutoff crossed, and shows the checked figure with its verdict; the cutoff line is
  **omitted, not faked**, when the engine version is unknown to the API.
- `TierTally`: counts the four tiers plus `unverifiable`; a run of legacy insights renders
  the tally without inventing tiers.
- sort/filter: the default sort is Priority on both lists; the Critical chip matches a
  legacy `severity === 'critical'` **or** the critical tier; a mixed list shows both.

Per repo convention the community Jest suite is the gate for the dashboard.

### 6.7 What is deliberately not unit-tested

The Qdrant payload, because nothing reads these fields back from it. The claim is pinned
instead by the existing retrieval tests, which filter only on `type`, `discovery_id` and
`severity`.

---

## 7. Measurements this version owes

Both are in the issue's notes and both are run on a dev stack, not asserted in CI.

1. **Embedding perturbation.** `StandaloneInsight.BuildEmbeddingText()` includes the
   severity word (`libs/go-common/models/insight.go:82-85`), so changing `critical` to
   `high` moves the dedup vector. Dedup marks a duplicate at cosine ≥ 0.95
   (`phase_embed_index.go:318`), so a perturbation that size would change which findings
   are suppressed. Method: take a real run's insights, build the embedding text with the
   old word and the new one, embed both with the project's configured embedder, and report
   mean and max cosine delta against that 0.95 threshold. Reported, not assumed.
2. **Yield.** This is the second and last time fields leave the analysis schema, and run 22
   established that the property set and its required list change what a small model
   emits. Measure insights per area and figures per insight before and after, on the same
   model and the same warehouse, and report it separately from v1's so a change is
   attributable to one of them.

---

## 8. Risks

| Risk | Mitigation |
|---|---|
| **A whole run bands on the baseline** because its insights declare few figures, so severity stops discriminating | The measurement in §7.2 reports the quantified rate. If it is low, that is a finding about the analysis prompts, not a reason to widen the magnitude sources — which is how `affected_count` got used as a magnitude in the first place |
| **Paging instability** on the new default sort | The total-order index and the overlapping-window test in §6.5. Called out because the single shared `created_at` per run makes ties the common case, not the rare one |
| **A mixed-vocabulary run** reports zero of the top tier somewhere | The sweep is one commit, and §6.4 tests the mixed case on every counter rather than the pure-new one |
| **`AmountBands` empty reads as a bug** — a `$2.4M` finding banded medium on a 4% share | The breakdown says so in words: the amount is shown with its verdict and the band names share as its source. An absolute money cutoff is not a constant (Rule 2) |
| **Extending engine 1** rather than bumping | §4.1, with the alternative and the reason the decision is cheap now and expensive after merge |
| **The agent and the API ship as separate images** and can be of different ages | Everything a reader needs is on the document; the API supplies only the cutoff line, which the dashboard omits rather than fakes for a version it does not know. v1's pattern, unchanged |
| **The sweep misses a site** | The `"critical"` literal search over Go and TS is enumerated in Phase 3 and is short; the pass-through sites are listed as such rather than left unexamined |

---

## 9. Alternatives considered and rejected

- **Compute priority at read time.** Desyncs Mongo and the Qdrant payload from what is
  displayed, leaves the `severity` match filter stale, and makes the list unsortable in the
  database — which is the one thing a server-paged list needs.
- **Severity from `affected_count`.** No population denominator exists anywhere in the
  result: `ExplorationStep.RowCount` is rows a query returned and `TableSchema.RowCount` is
  a table's size, neither of which is the cohort. A ratio over them is a number with no
  meaning.
- **Band floors instead of multipliers.** A floor collapses everything it touches onto one
  band and sorts nothing; the continuous score is what orders 20–40 findings *inside* a
  band.
- **Keep `critical` as a fourth severity band and add tiers beside it.** Two vocabularies
  for one idea, and the matrix already calls its top cell `critical`. Every filter and
  counter would then have to decide which of the two it meant.
- **A temporary severity-word → tier mapping, so severity could ship without priority.** A
  workaround with a Rule 3 smell, and it would ship a run that under-reports its own top
  tier until the next version landed.
- **Re-derive severity on the on-demand re-validation path.** A validation run changes
  neither magnitude nor weight, so a re-derivation would either produce the same number or —
  once weights are configurable — quietly rewrite a stored fact about a past run.
- **Backfill historical insights.** Forward-only per D7; re-scoring history is its own
  axis, after the bands have been calibrated against production data.

---

## 10. Acceptance mapping

| Acceptance criterion | Where |
|---|---|
| No model-assigned severity or risk number surfaces; both derived | Phase 2 — cleared and overwritten on every path including the zero case; `json:"-"` as the second defence; §6.3 |
| Severity is a band with its `$`/`%` driver; missing magnitude → area baseline marked "not quantified", never auto-Low | Phase 1 §2.1, Phase 6; §6.1 |
| Priority per-insight via the matrix; high severity + unverifiable → Investigate, surfaced | Phase 1 §2.2; the named test in §6.1 |
| Recommendations inherit severity; priority from their own verdict | Phase 4; §6.1 |
| Priority is the default sort on both insight lists; the per-run tier tally renders | Phases 5 and 6; §6.5, §6.6 |
| Opening a severity label shows magnitude → × weight → final → cutoff, with the checked figure and its verdict; a priority label shows the matrix cell and the two bands | Phase 6 `ScoringBreakdown`; §6.6 |
| Ledger rank, notification tier counts, filter chips and Ask retrieval behave on legacy **and** new documents | Phase 3; §6.4, §5.1 |
| Verified live on a freshly created project | §11 |

---

## 11. Verification

Local, before the PR: `make build`, `make test-go`, `make lint-go`, `make lint-docs`,
`make test-ui`, `make lint-ui`, and the Mongo testcontainer integration targets that cover
the new persistence and index tests. `go vet -tags=integration ./...` as well, because the
integration job does not run on pull requests.

Then **live, on a freshly created project** — not a formality: a project snapshots its area
prompts at creation and the seeds are never overwritten on upgrade, so an existing project
proves nothing about a build that changes what the prompts ask for. A full run on the dev
stack, then check by hand:

- no insight carries a model-authored `severity` or `risk_score`;
- at least one insight banded from an amount, one from a share, and one on the baseline
  marked "not quantified", each with the right driver on screen;
- a high-severity unverifiable insight present and sitting in Investigate near the top of
  the default sort;
- the tier tally on the run header matching the list;
- both breakdowns opening and adding up;
- the completion notification's tier counts matching the run;
- a second run on the same project re-sighting a finding and **not** reporting a severity
  trend it did not have.

---

## 12. Out of scope

Pack risk taxonomies, category weights, per-area overrides and risk flags; per-customer
tuning; the rejected-insight default filter (`#452`); and backfilling historical insights.
Each is a separate issue on the roadmap, and `RiskFlags` stays declared and unwritten here
for the same reason the severity half stayed declared and unwritten in v1.

---

**This is a PLAN for review — no implementation is included. The build follows after
approval.**

Closes #531

— Co-coded with Jale 🤖
