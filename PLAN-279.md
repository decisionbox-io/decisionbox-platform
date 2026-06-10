# PLAN-279 — Discovery validation steps show empty label (`Validated "": …`)

Issue: #279 · Phase: **plan** (this PR is a draft; no implementation yet)

## Problem

In the discovery run log, every validation step renders an empty label:

```
Validated "": skipped_budget_cap (claimed: 1107, verified: 0)
```

The quotes should hold the insight name / recommendation title so the user can
see *what* was validated.

## Root cause (verified against the tree)

`StatusReporter.AddValidationStep` builds the message from its `insightName`
argument (`services/agent/internal/discovery/status.go:304-306`). The
orchestrator passes `vr.ClaimedMetric` as that argument
(`services/agent/internal/discovery/orchestrator.go:867`):

```go
o.statusReporter.AddValidationStep(ctx, vr.ClaimedMetric, vr.Status, vr.ClaimedCount, vr.VerifiedCount, vr.InputTokens, vr.OutputTokens)
```

But `ValidationResult.ClaimedMetric` (declared `models/discovery.go:386`) is
**never assigned anywhere**. A repo-wide grep confirms exactly one reader
(orchestrator.go:867) and **zero writers** — so `vr.ClaimedMetric == ""` for
every step, for every status (not just `skipped_budget_cap`).

The `ValidationResult` structs are built without it at both sites:
- Insights — `validation_phase.go:63-69` (inside `validateInsights`)
- Recommendations — `validation_phase.go:152-156` (inside `validateRecommendations`)

## Fix (minimal, surgical)

Set `ClaimedMetric` in each struct literal, so the value is present on the `vr`
**before** the budget-cap early `continue` — this makes the label appear on the
skipped path as well as the validated path, with no extra code.

1. **Insights** — `validation_phase.go:63-69`, add to the literal:
   ```go
   vr := models.ValidationResult{
       InsightID:     ins.ID,
       AnalysisArea:  areaID,
       ClaimedCount:  ins.AffectedCount,
       ClaimedMetric: ins.Name,        // ← display label (Insight.Name, discovery.go:80)
       ValidatedAt:   time.Now(),
       DocKind:       valmodels.DocInsight,
   }
   ```

2. **Recommendations** — `validation_phase.go:152-156`, add to the literal:
   ```go
   vr := models.ValidationResult{
       InsightID:     rec.ID,
       ClaimedMetric: rec.Title,       // ← display label (Recommendation.Title, discovery.go:109)
       ValidatedAt:   time.Now(),
       DocKind:       valmodels.DocRecommendation,
   }
   ```

That is the whole behavioural change. Both `ins.Name` and `rec.Title` are plain
string fields already on the structs being iterated; no new lookups, no new
dependencies.

## Deliberately out of scope (Rule 8 — no over-engineering)

- **Renaming `ClaimedMetric` → `Label`/`DocName`.** The issue floats this as an
  optional follow-up. I'm **not** doing it here: the field carries persisted
  tags (`bson:"claimed_metric"`, `json:"claimed_metric"`), so a rename touches
  stored documents and any API/JSON consumer — disproportionate risk for a
  display-label bug fix. Leave as a noted follow-up if the team wants it.
- **Recommendation count display.** Recommendations never set `vr.ClaimedCount`,
  so `AddValidationStep`'s `claimed > 0` branch is skipped and rec steps render
  `Validated "Title": status` without the `(claimed/verified)` suffix. That is
  pre-existing and *not* what #279 reports (the empty label is). Out of scope;
  flag only.

## Testing

Extend `services/agent/internal/discovery/validation_phase_test.go` (it already
exists). The clean seam is the return value of `validateInsights` /
`validateRecommendations` (`[]models.ValidationResult`):

- **Insights, validated path:** input an `Insight{Name: "Foo", AffectedCount: N}`
  under the run cap; assert the returned `vr.ClaimedMetric == "Foo"`.
- **Insights, budget-cap-skipped path:** drive `runValidated >= MaxInsightsPerRun`
  so the `continue` branch fires; assert the skipped `vr` still has
  `ClaimedMetric == "Foo"` and `Status == skipped_budget_cap` — locks
  acceptance criterion 3 (no regression on the skipped path).
- **Recommendations:** input `Recommendation{Title: "Bar"}`; assert
  `vr.ClaimedMetric == "Bar"` (validated and skipped paths as above).

These assert the field is populated regardless of validator/warehouse behaviour,
so they don't need live LLM/warehouse calls. If the existing tests already
construct the phase with fakes, reuse that harness; otherwise add a thin
table-test using the same fakes already present in the file.

Optionally, a `status_test.go` assertion that `AddValidationStep("Foo", …)`
renders `Validated "Foo": …` — but `status.go` already does this correctly, so
the regression risk lives in the phase, where the tests above sit.

## Acceptance-criteria mapping

| Criterion | Covered by |
|---|---|
| Validation steps show insight name / rec title | Insight `ClaimedMetric: ins.Name`, Rec `ClaimedMetric: rec.Title` |
| Both insight and recommendation steps non-empty | Both struct literals set; both tested |
| No regression on budget-cap-skipped path | Set in literal *before* the `continue`; skipped-path test asserts it |

## Verification before opening the build PR

`make build`, `make test-go`, `make lint-go` (after
`export PATH=$PATH:$(go env GOPATH)/bin`). No UI changes. The run-log rendering
is string-built server-side, so a focused Go test is sufficient; no
testcontainer needed for this fix.

## Build-step checklist (for the implementation phase)

1. Apply the two struct-literal edits above.
2. Add the `validation_phase_test.go` cases.
3. `make build && make test-go && make lint-go` green.
4. Delete this `PLAN-279.md` in the final commit.
5. `gh pr ready`, then Codex review loop + Copilot pass + final report.
