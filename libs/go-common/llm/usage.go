package llm

// UsageAccumulator sums input/output token counts across multiple LLM
// calls that share a single home document — e.g. validation retries on
// one insight, exec-summary section calls on one summary, blurb calls
// across one schema-index build.
//
// Callers are single-goroutined per home document, so the accumulator
// is intentionally NOT thread-safe. Use one accumulator per logical
// operation.
type UsageAccumulator struct {
	input  int
	output int
}

// Add records one LLM call's token usage. Negative values are clamped
// to zero so a misreporting provider can't drive the running total
// below the calls it succeeded on.
func (u *UsageAccumulator) Add(input, output int) {
	if input > 0 {
		u.input += input
	}
	if output > 0 {
		u.output += output
	}
}

// Totals returns the accumulated (input, output) pair.
func (u *UsageAccumulator) Totals() (int, int) {
	return u.input, u.output
}
