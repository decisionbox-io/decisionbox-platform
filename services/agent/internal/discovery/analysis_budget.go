package discovery

import (
	"context"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
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
// window is too small to reserve even the intended analysis output (window ≤
// reserve + system + margin). It must never be the 200K default there, or the
// picker would feed a huge input to a tiny-window model and re-trigger the
// overflow this guards against. Kept small so at least a few steps still make it
// into the prompt; the output budget + adaptive retry are the net on a
// pathologically small window.
const minPickerBudgetTokens = 4096

// analysisOutputReserveTokens is the output headroom the picker reserves when
// sizing the query-results budget. It is the *intended* analysis generation
// size, NOT the model's hard output cap: reserving the full cap (which on some
// models — e.g. an Ollama row whose output default equals the context window —
// leaves zero input room) would needlessly drop most evidence. The precise
// max_tokens is still recomputed against the measured input after the prompt is
// assembled, so the actual generation is never smaller than the window allows.
const analysisOutputReserveTokens = 16384

// logOutputCapTruncation reports the one case that is otherwise invisible: a
// structured-output response that failed to parse AND consumed its entire
// max_tokens budget. That combination is almost always truncation mid-JSON
// rather than a model that emitted malformed output, and the two need different
// fixes — raise the cap vs. repair the prompt. Without this the phase just
// degrades to a no-op with a generic "unusable response" error, which is how
// issue #403 stayed hidden: the ledger still recorded its non-LLM outputs, so it
// looked like the phase had run.
func logOutputCapTruncation(phase, envKey string, attempt, maxTokens, tokensOut int) {
	if maxTokens <= 0 || tokensOut < maxTokens {
		return
	}
	applog.WithFields(applog.Fields{
		"phase":      phase,
		"attempt":    attempt,
		"max_tokens": maxTokens,
		"tokens_out": tokensOut,
		"env":        envKey,
	}).Warn("Response did not parse and used the entire output budget — it was almost certainly truncated. Raise " + envKey + ", or use a model with a larger output cap.")
}

// phaseOutputCap resolves the output ceiling for a bounded, structured-output
// discovery phase (reflection, clarifying questions).
//
// Unset env → the model's own cap, which is exactly what the analysis and
// recommendation paths pass (see orchestrator.go). Those two scale with
// whatever model the deployment runs. The newer phases instead layered a small
// fixed default on top, and since the budget takes the minimum, that default
// became the binding constraint: on a project large enough to need more, the
// response was truncated mid-JSON, failed to parse, and the phase degraded to
// a no-op — silently, because its other outputs still persisted (issue #403).
//
// Set env → an explicit operator override: clamped to [clampMin, 32000] and
// never above what the model itself allows.
//
// fallback applies only when the model cap is unknown (<= 0). The budgeter must
// never be handed a zero cap: boundOutputCap would keep it at zero and collapse
// max_tokens — and the floor with it — to nothing.
func phaseOutputCap(envKey string, modelOutputCap, clampMin, fallback int) int {
	if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			capped := clampInt(v, clampMin, 32000)
			if modelOutputCap > 0 && capped > modelOutputCap {
				capped = modelOutputCap
			}
			return capped
		}
	}
	if modelOutputCap > 0 {
		return modelOutputCap
	}
	return fallback
}

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
// and the room left for input once the *intended* analysis output, reserved
// system, and safety margin are set aside (Budget.Available). It reserves the
// intended output (analysisOutputReserveTokens), not the model's hard output cap
// — reserving the full cap would starve the input on models whose cap approaches
// the window. It only ever *lowers* the default, so a large-window model keeps
// the default soft cap while a small-window model can no longer be handed an
// input that alone exceeds its window. When the window leaves no room even for
// the intended output, it falls to a small floor rather than the 200K default.
func analysisPickerBudgetTokens(defaultBudget, window, effectiveOutputCap int) int {
	reserve := analysisOutputReserveTokens
	if effectiveOutputCap > 0 && effectiveOutputCap < reserve {
		reserve = effectiveOutputCap
	}
	reserve = boundOutputCap(reserve, window)
	avail := gollm.NewBudget(window, reserve, analysisReservedSystemTokens, false).Available()
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
