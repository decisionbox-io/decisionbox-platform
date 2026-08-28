package discovery

import (
	"context"
	"unicode/utf8"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Output-token budgeting for the analysis + recommendation phases.
//
// The failure this guards against: a large analysis prompt plus a fixed
// output cap can exceed the model's context window, and the provider
// rejects the whole request with a hard 400 ("maximum context length is
// N tokens"). The area is then lost and the run degrades to Partial.
//
// The fix budgets the requested output against the *measured* input so
// that input + output stays within the window, reusing the same
// llm.Budget arithmetic the /ask path uses (ModelMaxInput − reserved −
// safety-margin). It is deliberately catalog-independent: the caller
// supplies the effective window and output cap resolved from the
// operator override / live auto-detection / catalog / default chain, so
// an uncatalogued customer model is budgeted just like a known one.

// analysisReservedSystemTokens is the flat headroom kept for chat-template
// scaffolding and any per-request overhead the rune/4 estimate of the user
// prompt does not see. The analysis and recommendation calls send an empty
// system prompt, so the whole prompt is already counted as input and this
// stays small.
const analysisReservedSystemTokens = 512

// defaultAnalysisMinOutputTokens is the floor the output budget never drops
// below, so a near-window input still gets a usable (if small) generation
// instead of a zero/negative request. When even the floor does not fit, the
// provider may still 400 and the adaptive context-overflow retry (see
// internal/ai) is the last resort. Env-overridable (Rule 2); mirrors the
// EXPLORATION_MAX_OUTPUT_TOKENS knob.
const defaultAnalysisMinOutputTokens = 8192

// analysisMinOutputTokensEnv overrides defaultAnalysisMinOutputTokens.
const analysisMinOutputTokensEnv = "ANALYSIS_MIN_OUTPUT_TOKENS"

// analysisMinOutputTokens returns the configured output floor (>0), falling
// back to defaultAnalysisMinOutputTokens for an unset/invalid value.
func analysisMinOutputTokens() int {
	if v := goconfig.GetEnvAsInt(analysisMinOutputTokensEnv, defaultAnalysisMinOutputTokens); v > 0 {
		return v
	}
	return defaultAnalysisMinOutputTokens
}

// minPickerBudgetTokens is the small floor the picker budget drops to when the
// window is too small to reserve the full output cap (window ≤ output + system
// + margin). It must never be the 200K default there, or the picker would feed
// a huge input to a tiny-window model and re-trigger the overflow this guards
// against. Kept small so at least a few steps still make it into the prompt;
// the output budget + adaptive retry are the net on a pathologically small
// window.
const minPickerBudgetTokens = 4096

// boundOutputCap clamps an output cap to the model window — output can never
// exceed the context window, so a catalog/default cap larger than a
// (possibly auto-detected, smaller) window must not drive the budget.
func boundOutputCap(effectiveOutputCap, window int) int {
	if window > 0 && effectiveOutputCap > window {
		return window
	}
	return effectiveOutputCap
}

// budgetedMaxOutputTokens returns the max_tokens the caller should request so
// that inputTokens + output stays inside the model's context window, leaving
// the reserved-system headroom and a safety margin free:
//
//	out = clamp(window − input − system − margin, floor, cap)
//
// where cap = min(effectiveOutputCap, window) — output can't exceed the window
// — and the floor is itself capped at cap so a model whose documented output
// limit is below the floor (e.g. Mistral Large at 4096) is never asked for more
// than it allows. The window and output cap come from the caller's resolution
// chain, so this works for catalogued and uncatalogued models alike.
func budgetedMaxOutputTokens(window, inputTokens, effectiveOutputCap, floor int) int {
	outCap := boundOutputCap(effectiveOutputCap, window)
	if floor > outCap {
		floor = outCap
	}
	// Reuse llm.Budget for the window − system − margin arithmetic (the same
	// math /ask uses). ReservedOutput is 0 here because output is exactly what
	// we are solving for. The approximate-counter safety tier is chosen (false)
	// because inputTokens is a rune/4 estimate that under-counts dense JSON.
	avail := gollm.NewBudget(window, 0, analysisReservedSystemTokens, false).Available()
	out := avail - inputTokens
	if out > outCap {
		out = outCap
	}
	if out < floor {
		out = floor
	}
	return out
}

// analysisPickerBudgetTokens couples the analysis step picker's query-results
// budget to the model window: it returns the smaller of the default soft cap
// and the room left for input once the effective output cap, reserved system,
// and safety margin are set aside (Budget.Available with ReservedOutput =
// effectiveOutputCap, bounded by the window). It only ever *lowers* the
// default, so a large-window model keeps the default soft cap while a
// small-window model can no longer be handed an input that alone exceeds its
// window. When the window leaves no room for the reserved output at all, it
// falls to a small floor rather than the 200K default.
func analysisPickerBudgetTokens(defaultBudget, window, effectiveOutputCap int) int {
	outCap := boundOutputCap(effectiveOutputCap, window)
	avail := gollm.NewBudget(window, outCap, analysisReservedSystemTokens, false).Available()
	if avail <= 0 {
		return minPickerBudgetTokens
	}
	if avail < defaultBudget {
		return avail
	}
	return defaultBudget
}

// approxTokens estimates the token count of a fully-assembled prompt with the
// tokenizer-free rune/4 heuristic (gollm.ApproximateCounter), consistent with
// the picker's char/4 sizing and /ask's budget walk. A counter error (only ctx
// cancellation) falls back to a direct rune/4 count so budgeting never blocks
// on cancellation semantics.
func approxTokens(ctx context.Context, prompt string) int {
	n, err := gollm.ApproximateCounter{}.Count(ctx, prompt)
	if err != nil {
		return utf8.RuneCountInString(prompt) / 4
	}
	return n
}
