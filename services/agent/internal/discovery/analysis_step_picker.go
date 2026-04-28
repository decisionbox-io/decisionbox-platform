package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

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
	for len(pickedList) > 1 {
		stepsForEstimate := stepsFromPicked(pickedList)
		size := estimate(stepsForEstimate)
		tokens := size / charsPerToken
		if tokens <= budgetTokens {
			break
		}
		// Drop the lowest-scored step (last after sort).
		victim := pickedList[len(pickedList)-1]
		applog.WithFields(applog.Fields{
			"area":           area.ID,
			"step":           victim.Step.Step,
			"score":          victim.Score,
			"size_chars":     size,
			"tokens_estimate": tokens,
			"budget_tokens":   budgetTokens,
		}).Debug("analysis_step_picker: dropping step over budget")
		dropped = append(dropped, DroppedStep{
			StepNumber: victim.Step.Step,
			Score:      victim.Score,
			Reason:     DropReasonOverBudget,
		})
		pickedList = pickedList[:len(pickedList)-1]
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
		Picked:  pickedList,
		Dropped: dropped,
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
