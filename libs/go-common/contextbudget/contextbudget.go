// Package contextbudget sizes the prior-knowledge a prompt may carry, and
// renders it to fit.
//
// The problem it solves: how much of what a project already knows belongs in a
// prompt is a function of the model's context window, not a constant. A fixed
// number of prior findings is simultaneously a waste of a large window and an
// overflow on a small one — and the overflow is a hard provider 400 that costs
// the whole call.
//
// So the arithmetic here is the same arithmetic the output side already uses
// (llm.Budget: window − reserved output − reserved system − safety margin),
// applied to context composition. It is deliberately catalog-independent: the
// caller supplies the effective window it resolved through its own
// operator-override / live-auto-detection / catalog / default chain, so an
// uncatalogued customer model is budgeted exactly like a known one.
//
// The rendering lives here too, with the arithmetic, because the agent that
// composes the global block and the retrieval plugin that composes the
// per-area section must agree on what a finding costs. Two packages computing
// a cost from two copies of a formatter is how they come to disagree about it.
//
// Everything in this package is pure: no I/O, no globals, no environment
// reads. Operator knobs belong to the caller that owns them.
package contextbudget

import (
	"context"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// Description-width bounds for a rendered finding line.
//
// Constants rather than operator knobs, like MaxFindingTrailEntries and the
// ranking weights next to them in models.LedgerFinding: they are properties of
// a readable prompt line, not of a deployment. Neither bounds the NUMBER of
// findings a prompt may carry — that is what the budget decides, and it is the
// number that was wrong.
//
// MinFindingDescChars is what the fixed renderers truncated at before there was
// a budget, so a tight budget degrades to the old level of detail rather than to
// something new. MaxFindingDescChars is where more text stops buying
// recognition: the line exists so the model can tell it has seen this finding
// before, and the full evidence is a page away.
const (
	MinFindingDescChars = 200
	MaxFindingDescChars = 1200
)

// runesPerToken mirrors the rune/4 heuristic Tokens counts with, used to turn a
// token budget back into a character width for the description.
const runesPerToken = 4

// Tokens estimates the token cost of s with the tokenizer-free rune/4
// heuristic, via llm.ApproximateCounter so this package and the callers'
// own prompt sizing cannot drift onto two different heuristics.
//
// The counter's only error is context cancellation and the context here can
// never be cancelled, so there is nothing to report and nothing to decide.
func Tokens(s string) int {
	n, err := gollm.ApproximateCounter{}.Count(context.Background(), s)
	if err != nil {
		// Unreachable: Count only fails on ctx.Err(), and Background has none.
		// Handled rather than ignored so a future counter that can fail degrades
		// to "costs nothing I can prove" instead of silently returning garbage.
		return 0
	}
	return n
}

// AvailableInput reports how many input tokens are free for composed context
// once the requested output, the caller's fixed scaffolding, and the
// safety margin are set aside. Zero — never negative — when the reserves alone
// exceed the window, which the caller must treat as "carry nothing".
//
// The approximate-counter safety tier is chosen because every size this package
// deals in is a rune/4 estimate, which under-counts dense text.
func AvailableInput(window, reservedOutput, reservedSystem int) int {
	return gollm.NewBudget(window, reservedOutput, reservedSystem, false).Available()
}

// Share returns the given fraction of available, floored at zero. A
// non-positive share yields zero: a caller that does not want to spend
// anything on composed context gets nothing, not everything.
func Share(available int, share float64) int {
	if available <= 0 || share <= 0 {
		return 0
	}
	out := int(float64(available) * share)
	if out < 0 {
		return 0
	}
	return out
}

// SaturatedShare scales a base share by how much genuinely new the last run
// added — ConvergencePoint.MarginalRatio, the share of that run's findings that
// were not already in the ledger.
//
// The direction is the point. A saturated investigation (a low ratio: almost
// everything the run reported was already known) is precisely the one that needs
// to be told MORE about what is known, because not knowing is what produced the
// repetition. A fresh investigation (ratio near 1) has little to be told and
// should spend its window on evidence instead.
//
// have distinguishes "no convergence recorded yet" from "a recorded ratio of
// zero". The first is an early run and gets the base share; the second is a run
// that found nothing new at all and gets the most help this function can give.
// Collapsing them would hand every first run the maximum.
//
// The result is bounded to [base, 2*base], so the knob the operator sets stays
// the floor and no measurement can push composed context past twice it.
func SaturatedShare(base, marginalRatio float64, have bool) float64 {
	if base <= 0 {
		return 0
	}
	if !have {
		return base
	}
	ratio := marginalRatio
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	return base * (2 - ratio)
}

// FindingLine renders one ledger finding as the single prompt line both the
// global block and the retrieval section carry:
//
//   - [area] **Name** (severity, status, seen N×) — key metric. Description…
//
// descChars bounds the description in RUNES, not bytes: a byte slice through
// Turkish or CJK text splits a rune and puts a replacement character in the
// prompt. The status and seen-count clauses are omitted when there is nothing to
// say, and an unset area reads "general" rather than empty brackets.
//
// The returned line carries no trailing newline; callers join.
func FindingLine(f commonmodels.LedgerFinding, descChars int) string {
	area := f.Area
	if area == "" {
		area = "general"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- [%s] **%s** (%s", area, f.Name, f.Severity)
	if f.Status != "" {
		fmt.Fprintf(&b, ", %s", f.Status)
	}
	if f.SeenCount > 1 {
		fmt.Fprintf(&b, ", seen %d×", f.SeenCount)
	}
	b.WriteString(")")
	if f.KeyMetric != "" {
		fmt.Fprintf(&b, " — %s", f.KeyMetric)
	}
	if d := truncateRunes(f.Description, descChars); d != "" {
		fmt.Fprintf(&b, ". %s", d)
	}
	return b.String()
}

// FitFindings selects the prefix of findings that fits budgetTokens, and the
// description width they are rendered at.
//
// findings must already be in the caller's priority order — rank for a global
// block, recency for a re-judgement list — because this takes a PREFIX and
// stops at the first line that does not fit. It does not reorder, and it does
// not skip a long finding to squeeze in a later cheaper one: a budget spent on
// the most important findings the caller could name is the point, and
// cherry-picking by size would quietly reorder by length.
//
// The width is the budget spread over the candidates we actually have, bounded
// to [minDesc, maxDesc]: few findings and a large window render in full, many
// findings and a small window render at the old level of detail and the prefix
// is shorter. One formula, no tiers.
//
// A non-positive budget returns no findings — correct, and the only safe answer
// on a window too small to hold any of them. The returned width is still usable
// so a caller cannot divide by it or render with zero.
func FitFindings(findings []commonmodels.LedgerFinding, budgetTokens, minDesc, maxDesc int) ([]commonmodels.LedgerFinding, int) {
	if minDesc < 0 {
		minDesc = 0
	}
	if maxDesc < minDesc {
		maxDesc = minDesc
	}
	if budgetTokens <= 0 || len(findings) == 0 {
		return nil, minDesc
	}

	descChars := clamp((budgetTokens*runesPerToken)/len(findings), minDesc, maxDesc)

	lines := make([]string, len(findings))
	for i := range findings {
		lines[i] = FindingLine(findings[i], descChars)
	}
	n := FitLines(lines, budgetTokens)
	if n == 0 {
		return nil, descChars
	}
	return findings[:n], descChars
}

// FitLines reports how many of lines fit budgetTokens, taking a PREFIX: it
// stops at the first line that does not fit rather than skipping it to squeeze
// in a later cheaper one, because the caller's order is a priority order and
// cherry-picking by size would quietly reorder by length.
//
// Exported so the prompts that compose their own line formats — a ledger
// finding carries a description, a re-judgement list carries ids and statuses —
// share one selection rule instead of each growing a loop that is nearly the
// same. Zero for a non-positive budget.
func FitLines(lines []string, budgetTokens int) int {
	if budgetTokens <= 0 {
		return 0
	}
	spent := 0
	for i, line := range lines {
		cost := Tokens(line)
		if spent+cost > budgetTokens {
			return i
		}
		spent += cost
	}
	return len(lines)
}

// truncateRunes trims s and cuts it to max runes, marking the cut. A
// non-positive max yields "" — the caller asked for no description.
func truncateRunes(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
