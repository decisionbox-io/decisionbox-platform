package llm

// Budget describes how many tokens are available for variable-length
// content (history + RAG context) once the fixed costs of a request
// — the model's input cap, the reserved output, the system prompt,
// and a safety margin — have been subtracted.
//
// Callers build a Budget once per Ask turn and then ask Available()
// how many tokens may be spent on history and RAG context. The
// budgeting layer is intentionally separate from the trim loop so
// the same budget can be applied to RAG shrinking and history walk.
type Budget struct {
	// ModelMaxInput is the upstream-published context window. Read
	// from GetMaxInputTokens(provider, model).
	ModelMaxInput int

	// ReservedOutput is the max_tokens value the caller will set on
	// the chat request. The model cannot use those tokens for input,
	// so they come out of the budget up front.
	ReservedOutput int

	// ReservedSystem is the caller's estimated token cost for the
	// system prompt and any fixed scaffolding the budget walk does
	// not see. Set to a small flat number (≤ ~500) so a per-character
	// underestimate cannot blow the budget.
	ReservedSystem int

	// SafetyMargin is the number of tokens kept in reserve to absorb
	// counter inaccuracy. Set wider when the counter is approximate
	// (the model's tokenizer disagrees with our rune-based heuristic)
	// and narrower when the counter is exact (native count API or
	// tiktoken with the model's declared encoding).
	SafetyMargin int
}

// Safety-margin tiers. Tunable by callers via NewBudget; exposed for
// tests that need to assert the right tier was chosen.
const (
	// ExactCounterMarginPct is the % of ModelMaxInput reserved as
	// safety margin when an exact TokenCounter is in use. Small but
	// non-zero — exact counters still drift across SDK versions and
	// chat-template overhead is hard to measure precisely.
	ExactCounterMarginPct = 5

	// ApproxCounterMarginPct is the % reserved when the counter is
	// ApproximateCounter. Wider because rune/4 systematically
	// under-counts code, JSON, and CJK content — exactly the prompt
	// shapes /ask produces.
	ApproxCounterMarginPct = 15
)

// NewBudget builds a Budget for the given (model-input cap, reserved
// output, reserved system) triple, picking the right safety-margin
// tier based on whether the active counter is exact.
//
// Negative inputs are clamped to zero rather than panicking — a
// misconfigured upstream cap (or a future zero default) should yield
// a Budget that surfaces "no room" via Available() == 0, not a
// runtime crash. Callers can test that explicitly.
func NewBudget(modelMaxInput, reservedOutput, reservedSystem int, exact bool) Budget {
	if modelMaxInput < 0 {
		modelMaxInput = 0
	}
	if reservedOutput < 0 {
		reservedOutput = 0
	}
	if reservedSystem < 0 {
		reservedSystem = 0
	}
	pct := ApproxCounterMarginPct
	if exact {
		pct = ExactCounterMarginPct
	}
	margin := modelMaxInput * pct / 100
	return Budget{
		ModelMaxInput:  modelMaxInput,
		ReservedOutput: reservedOutput,
		ReservedSystem: reservedSystem,
		SafetyMargin:   margin,
	}
}

// Available reports how many tokens are left for history + RAG
// context after the fixed costs are subtracted. Returns 0 (never
// negative) when the reserves alone exceed the model cap — the
// caller treats 0 as "context overflow" and returns 413.
func (b Budget) Available() int {
	v := b.ModelMaxInput - b.ReservedOutput - b.ReservedSystem - b.SafetyMargin
	if v < 0 {
		return 0
	}
	return v
}
