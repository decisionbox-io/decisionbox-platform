package discovery

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Tunable constants for the analysis-area picker. Exported so the
// configuration doc and operator-facing telemetry have a single source
// of truth.
const (
	// AnalysisAreaTopK is the maximum number of vector hits to fetch
	// per area before exact-match boost + budget trimming.
	AnalysisAreaTopK = 24

	// AnalysisAreaMinScore is the cosine-similarity floor below which
	// vector hits are dropped. Tuned empirically — anything lower
	// tends to be off-topic noise that hurts the analysis prompt
	// more than it helps.
	AnalysisAreaMinScore = 0.30

	// ExactMatchFloor is the score we assign to steps promoted via
	// the keyword exact-match boost. Set just above the top of the
	// "below threshold" band so promoted steps survive the min-score
	// gate but rank below clearly-relevant vector hits.
	ExactMatchFloor = 0.55

	// AnalysisQueryResultsBudgetTokens is the soft cap on the
	// rendered {{QUERY_RESULTS}} JSON size, expressed in tokens. The
	// picker drops the lowest-scored steps until the rendered prompt
	// fits under this cap. ~200K tokens fits comfortably under every
	// production model's window with headroom for the rest of the
	// prompt.
	AnalysisQueryResultsBudgetTokens = 200_000

	// charsPerToken is the rough conversion the picker uses to
	// estimate token counts from rendered byte size without calling
	// a tokenizer. Conservative: assumes 4 chars / token, which
	// over-counts for very dense text (mostly fine — we'd rather
	// drop a step we didn't need than blow the budget).
	charsPerToken = 4
)

// PickedStep is one step the picker decided to feed the analysis
// prompt. Source records why it was picked so callers can log /
// surface it in telemetry.
type PickedStep struct {
	Step   models.ExplorationStep
	Score  float64
	Source PickSource
}

// DroppedStep is one step the picker considered and rejected. Reason
// reflects why the step did not make the final cut.
type DroppedStep struct {
	StepNumber int
	Score      float64
	Reason     DropReason
}

// PickSource is the provenance tag the picker attaches to a chosen
// step.
type PickSource string

const (
	PickSourceVector     PickSource = "vector"
	PickSourceExactMatch PickSource = "exact_match"
)

// DropReason describes why a step was excluded from the final pick.
type DropReason string

const (
	DropReasonBelowMinScore DropReason = "below_min_score"
	DropReasonOverBudget    DropReason = "over_budget"
)

// PickResult bundles the picked + dropped lists. Callers stamp
// telemetry with both.
type PickResult struct {
	Picked  []PickedStep
	Dropped []DroppedStep

	// AlsoExamined carries a one-line purpose for each step the smart-overflow
	// trim dropped, so the orchestrator can tell the model what evidence exists
	// but wasn't shown ("Also examined (not shown): …"). Empty unless the smart
	// path trimmed something — so big-window runs, which never trim, produce no
	// breadcrumb and an unchanged prompt.
	AlsoExamined []string
}

// stepRenderer estimates the rendered byte size for a slice of steps.
// The picker uses it to budget-trim. Tests inject a deterministic
// stub; production wiring is a closure over renderCompactedSteps.
type stepRenderer func(steps []models.ExplorationStep) int

// AnalysisStepPicker selects which exploration steps feed each
// analysis area's prompt. Pure logic — no IO inside Pick. The vector
// search itself is a function the caller passes in (typically a
// closure over RunStepIndex.Search) so tests can inject canned hits
// without spinning up Qdrant.
type AnalysisStepPicker struct {
	// Search is the vector search function. Returns hits annotated
	// with their step number, score, and lightweight payload.
	Search func(ctx context.Context, areaQuery string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error)

	// EstimateRenderedSize returns the rendered byte size of the
	// compacted JSON for the given step slice. Defaults to a
	// renderer that walks the existing models.ExplorationStep
	// representation; tests inject deterministic stubs.
	EstimateRenderedSize stepRenderer

	// TopK overrides AnalysisAreaTopK when non-zero.
	TopK int

	// MinScore overrides AnalysisAreaMinScore when non-zero.
	MinScore float64

	// BudgetTokens overrides AnalysisQueryResultsBudgetTokens when
	// non-zero. Set to a small value in tests to exercise the
	// trimming branch.
	BudgetTokens int

	// SmartOverflowEnabled turns on the smart budget-trim path (R5/R6): when
	// the picked evidence exceeds the budget, survivors are first re-compacted
	// to a tighter digest to fit more steps, then near-duplicate steps are
	// dropped in preference to unique evidence, and the dropped purposes are
	// surfaced to the model as an "also examined" breadcrumb. It engages ONLY
	// on the over-budget trim path (which big-window models never reach, so
	// they are unaffected). Off → the classic drop-lowest-scored trim, exactly
	// as before. Resolved per project (default on) by the caller.
	SmartOverflowEnabled bool
}

// NewAnalysisStepPicker returns a picker with the canonical
// production wiring: search fn supplied, default constants for
// TopK / MinScore / Budget.
func NewAnalysisStepPicker(search func(ctx context.Context, areaQuery string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error)) *AnalysisStepPicker {
	return &AnalysisStepPicker{
		Search:               search,
		EstimateRenderedSize: defaultRenderedSize,
	}
}

// Pick selects steps for one analysis area.
//
// Pipeline:
//  1. Vector search the run-scoped collection for the area query.
//  2. Promote any step whose Query / QueryPurpose / Analysis text
//     contains a verbatim area keyword (case-insensitive substring),
//     with score = max(existing, ExactMatchFloor).
//  3. Apply the min-score floor; record dropped steps.
//  4. Sort by score desc, step asc on ties.
//  5. Estimate rendered size; drop the lowest-scoring step until the
//     rendered output fits under BudgetTokens.
//
// The function never silently drops a step — every excluded step is
// recorded in PickResult.Dropped with a reason. Callers log the
// dropped list to telemetry.
func (p *AnalysisStepPicker) Pick(ctx context.Context, area AnalysisArea, allSteps []models.ExplorationStep) (*PickResult, error) {
	if p.Search == nil {
		return nil, errors.New("analysis_step_picker: Search is required")
	}
	topK := p.TopK
	if topK <= 0 {
		topK = AnalysisAreaTopK
	}
	minScore := p.MinScore
	if minScore <= 0 {
		minScore = AnalysisAreaMinScore
	}
	budgetTokens := p.BudgetTokens
	if budgetTokens <= 0 {
		budgetTokens = AnalysisQueryResultsBudgetTokens
	}

	// 1. Vector hits.
	areaQuery := buildAreaQueryText(area)
	applog.WithFields(applog.Fields{
		"area":         area.ID,
		"area_keywords": len(area.Keywords),
		"top_k":        topK,
		"min_score":    minScore,
		"budget_tokens": budgetTokens,
		"total_steps":  len(allSteps),
	}).Debug("analysis_step_picker: starting pick for area")

	hits, err := p.Search(ctx, areaQuery, RunStepIndexSearchOpts{TopK: topK, MinScore: 0}) // we'll filter ourselves so we can record dropped
	if err != nil {
		return nil, fmt.Errorf("analysis_step_picker: vector search: %w", err)
	}

	stepByNumber := make(map[int]models.ExplorationStep, len(allSteps))
	for _, s := range allSteps {
		stepByNumber[s.Step] = s
	}

	picked := make(map[int]PickedStep, len(hits))
	dropped := make([]DroppedStep, 0)

	for _, h := range hits {
		step, ok := stepByNumber[h.Step]
		if !ok {
			// Index has a step we didn't get from the orchestrator.
			// Treat as a phantom — log nothing, skip silently.
			continue
		}
		if h.Score < minScore {
			dropped = append(dropped, DroppedStep{
				StepNumber: h.Step,
				Score:      h.Score,
				Reason:     DropReasonBelowMinScore,
			})
			continue
		}
		picked[h.Step] = PickedStep{
			Step:   step,
			Score:  h.Score,
			Source: PickSourceVector,
		}
	}

	// 2. Exact-match boost. Promote steps whose textual content
	// contains any area keyword verbatim. This is the belt-and-
	// braces guard against vector ranking missing a step that was
	// explicitly written for the area's keyword.
	if len(area.Keywords) > 0 {
		for _, step := range allSteps {
			haystack := strings.ToLower(step.Query + " " + step.QueryPurpose + " " + step.Thinking)
			matched := false
			for _, kw := range area.Keywords {
				kw = strings.ToLower(strings.TrimSpace(kw))
				if kw == "" {
					continue
				}
				if strings.Contains(haystack, kw) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}

			existing, present := picked[step.Step]
			if present {
				// Already in the picked set — only bump score if
				// existing is below the floor (which can happen when
				// an exact match also got a low vector score). Never
				// demote.
				if existing.Score < ExactMatchFloor {
					existing.Score = ExactMatchFloor
				}
				picked[step.Step] = existing
				continue
			}
			picked[step.Step] = PickedStep{
				Step:   step,
				Score:  ExactMatchFloor,
				Source: PickSourceExactMatch,
			}
		}
	}

	// 3. Sort: score desc, step asc on ties (deterministic).
	pickedList := make([]PickedStep, 0, len(picked))
	for _, ps := range picked {
		pickedList = append(pickedList, ps)
	}
	sort.SliceStable(pickedList, func(i, j int) bool {
		if pickedList[i].Score != pickedList[j].Score {
			return pickedList[i].Score > pickedList[j].Score
		}
		return pickedList[i].Step.Step < pickedList[j].Step.Step
	})

	// 4. Budget trimming.
	estimate := p.EstimateRenderedSize
	if estimate == nil {
		estimate = defaultRenderedSize
	}
	preTrimCount := len(pickedList)
	var alsoExamined []string
	recompacted := false
	for len(pickedList) > 1 {
		stepsForEstimate := stepsFromPicked(pickedList)
		size := estimate(stepsForEstimate)
		tokens := size / charsPerToken
		if tokens <= budgetTokens {
			break
		}

		// R6 (smart overflow): before dropping any step, try once to shrink
		// per-step detail — re-compact survivors to a tighter head/tail digest
		// so more steps fit. If that alone brings us under budget, no step is
		// dropped. Only steps that still carry their raw rows can be rebuilt;
		// the digest can only shrink, never grow, so this never inflates.
		if p.SmartOverflowEnabled && !recompacted {
			recompacted = true
			if recompactSurvivorsTighter(pickedList) {
				continue
			}
		}

		// Choose the victim. The classic path drops the lowest-scored step
		// (last after sort). The smart path prefers dropping a near-duplicate of
		// a higher-scored survivor (redundant evidence) so unique evidence
		// survives; it falls back to lowest-scored when nothing is redundant.
		victimIdx := len(pickedList) - 1
		if p.SmartOverflowEnabled {
			if idx, ok := lowestScoredDuplicateIdx(pickedList); ok {
				victimIdx = idx
			}
		}
		victim := pickedList[victimIdx]
		applog.WithFields(applog.Fields{
			"area":            area.ID,
			"step":            victim.Step.Step,
			"score":           victim.Score,
			"size_chars":      size,
			"tokens_estimate": tokens,
			"budget_tokens":   budgetTokens,
			"smart_overflow":  p.SmartOverflowEnabled,
		}).Debug("analysis_step_picker: dropping step over budget")
		dropped = append(dropped, DroppedStep{
			StepNumber: victim.Step.Step,
			Score:      victim.Score,
			Reason:     DropReasonOverBudget,
		})
		if p.SmartOverflowEnabled {
			if bc := breadcrumbForStep(victim.Step); bc != "" {
				alsoExamined = append(alsoExamined, bc)
			}
		}
		pickedList = append(pickedList[:victimIdx], pickedList[victimIdx+1:]...)
	}

	finalSize := estimate(stepsFromPicked(pickedList))
	applog.WithFields(applog.Fields{
		"area":             area.ID,
		"vector_hits":      len(hits),
		"picked":           len(pickedList),
		"dropped":          len(dropped),
		"trimmed_for_budget": preTrimCount - len(pickedList),
		"rendered_chars":   finalSize,
		"rendered_tokens":  finalSize / charsPerToken,
	}).Info("analysis_step_picker: pick result")
	return &PickResult{
		Picked:       pickedList,
		Dropped:      dropped,
		AlsoExamined: alsoExamined,
	}, nil
}

// buildAreaQueryText is what we feed the embedder for an analysis
// area. Stable shape so a re-run with the same area + keywords
// produces the same query embedding.
func buildAreaQueryText(area AnalysisArea) string {
	var b strings.Builder
	b.WriteString(area.Name)
	if area.Description != "" {
		b.WriteString(" — ")
		b.WriteString(area.Description)
	}
	if len(area.Keywords) > 0 {
		b.WriteString(". Keywords: ")
		b.WriteString(strings.Join(area.Keywords, ", "))
	}
	return b.String()
}

func stepsFromPicked(picked []PickedStep) []models.ExplorationStep {
	out := make([]models.ExplorationStep, len(picked))
	for i, p := range picked {
		out[i] = p.Step
	}
	return out
}

// defaultRenderedSize is the fallback estimator. The orchestrator
// wires the real renderer; tests using AnalysisStepPicker directly
// without a renderer fall back here. Returns 0 so an unset renderer
// never accidentally trims.
func defaultRenderedSize(_ []models.ExplorationStep) int { return 0 }

// Smart-overflow tunables (R5/R6). These only bite on the over-budget trim
// path, so a big-window model that fits never sees them.
const (
	// smartOverflowHeadTailRows / smartOverflowInlineThreshold are the tighter
	// digest limits survivors are re-compacted to when the picked evidence
	// overflows the budget. They are strictly smaller than the exploration-time
	// defaults (5 / 20), so a re-compact can only shrink a step, never grow it.
	smartOverflowHeadTailRows    = 2
	smartOverflowInlineThreshold = 6

	// maxAlsoExaminedBreadcrumbs caps how many dropped-step purposes are echoed
	// back to the model, so the breadcrumb itself never re-bloats the prompt it
	// exists to shrink.
	maxAlsoExaminedBreadcrumbs = 12

	// maxBreadcrumbLen caps a single breadcrumb line's length.
	maxBreadcrumbLen = 140
)

// recompactSurvivorsTighter rebuilds each survivor's CompactResult digest at a
// tighter head/tail so the rendered evidence shrinks and more steps fit. It
// only rebuilds steps that still carry their raw rows (QueryResult) — steps
// restored from persistence or schema-only actions are left untouched. Because
// PickedStep.Step is a value copy, replacing its CompactResult pointer does NOT
// mutate the shared exploration step, so the original 5+5 digest (persisted and
// reused by other areas) is preserved. Returns true when at least one step was
// re-compacted (so the caller re-estimates before deciding to drop).
func recompactSurvivorsTighter(picked []PickedStep) bool {
	limits := gomodels.CompactLimits{
		HeadTailRowCount:       smartOverflowHeadTailRows,
		CompactInlineThreshold: smartOverflowInlineThreshold,
	}
	changed := false
	for i := range picked {
		rows := picked[i].Step.QueryResult
		if len(rows) == 0 {
			continue
		}
		tight := gomodels.BuildCompactResultWithLimits(rows, limits)
		picked[i].Step.CompactResult = &tight
		changed = true
	}
	return changed
}

// lowestScoredDuplicateIdx returns the index of the lowest-scored step that is a
// near-duplicate of a higher-scored survivor (same cluster key appearing
// earlier in the score-desc list), preferring to drop redundant evidence over
// unique evidence. picked is sorted score-desc, so a later index with a cluster
// key already seen earlier is the redundant, lower-scored copy. Returns
// (0,false) when no step is redundant — the caller then falls back to dropping
// the lowest-scored step (classic behaviour).
func lowestScoredDuplicateIdx(picked []PickedStep) (int, bool) {
	seen := make(map[string]struct{}, len(picked))
	dupIdx := -1
	for i := range picked {
		key := stepClusterKey(picked[i].Step)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			// A duplicate of an earlier (higher-scored) step. Track the latest
			// (lowest-scored) such duplicate.
			dupIdx = i
			continue
		}
		seen[key] = struct{}{}
	}
	if dupIdx < 0 {
		return 0, false
	}
	return dupIdx, true
}

// stepClusterKey is the near-duplicate signature of a step: its normalized
// query purpose plus the sorted set of tables its query reads. Two steps with
// the same key examine the same tables for the same stated purpose, so keeping
// the higher-scored one loses no distinct evidence. Empty when the step has
// neither a purpose nor a table signature (never clustered).
func stepClusterKey(s models.ExplorationStep) string {
	purpose := normalizeWhitespace(strings.ToLower(s.QueryPurpose))
	tables := queryTableSignature(s.Query)
	if purpose == "" && tables == "" {
		return ""
	}
	return purpose + "\x00" + tables
}

// queryTableRefRe extracts the identifier following FROM / JOIN in a SQL
// statement — a cheap table-set signature without a full parser. Handles
// dotted (schema.table) and quoted identifiers.
var queryTableRefRe = regexp.MustCompile(`(?i)\b(?:from|join)\s+[` + "`" + `"\[]?([a-zA-Z0-9_.$-]+)`)

// queryTableSignature returns the sorted, de-duplicated set of table references
// in a query, joined by commas. Empty for a blank query or one with no
// FROM/JOIN (e.g. a scalar SELECT). Lowercased for case-insensitive matching.
func queryTableSignature(query string) string {
	if strings.TrimSpace(query) == "" {
		return ""
	}
	matches := queryTableRefRe.FindAllStringSubmatch(query, -1)
	if len(matches) == 0 {
		return ""
	}
	set := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		t := strings.ToLower(strings.Trim(m[1], `"`+"`"+`[]`))
		if t != "" {
			set[t] = struct{}{}
		}
	}
	if len(set) == 0 {
		return ""
	}
	tables := make([]string, 0, len(set))
	for t := range set {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	return strings.Join(tables, ",")
}

// normalizeWhitespace lowercases nothing (callers decide case) but collapses
// every run of whitespace to a single space and trims the ends, so trivial
// formatting differences don't split a cluster.
func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// breadcrumbForStep renders the one-line "also examined" note for a dropped
// step: its query purpose, or a truncated query when the purpose is blank.
// Capped so the breadcrumb never re-bloats the prompt.
func breadcrumbForStep(s models.ExplorationStep) string {
	text := normalizeWhitespace(s.QueryPurpose)
	if text == "" {
		text = normalizeWhitespace(s.Query)
	}
	if text == "" {
		return ""
	}
	if len(text) > maxBreadcrumbLen {
		text = text[:maxBreadcrumbLen] + "…"
	}
	return text
}

// formatAlsoExamined renders the breadcrumb appended to an analysis prompt when
// smart-overflow trimming dropped steps. It de-duplicates purposes (dropped
// near-duplicates often share one), caps the count, and returns "" when there
// is nothing to show — so a run that fit without trimming has an unchanged
// prompt.
func formatAlsoExamined(purposes []string) string {
	if len(purposes) == 0 {
		return ""
	}
	seen := make(map[string]struct{}, len(purposes))
	uniq := make([]string, 0, len(purposes))
	for _, p := range purposes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key := strings.ToLower(p)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		uniq = append(uniq, p)
		if len(uniq) >= maxAlsoExaminedBreadcrumbs {
			break
		}
	}
	if len(uniq) == 0 {
		return ""
	}
	return "\n\nAlso examined during exploration but omitted here for length " +
		"(the underlying data exists — cite it via source_steps if a finding needs it): " +
		strings.Join(uniq, "; ") + "."
}
