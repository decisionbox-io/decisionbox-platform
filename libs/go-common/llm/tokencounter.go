package llm

import (
	"context"
	"strings"
	"unicode/utf8"
)

// TokenCounter returns the token count for a piece of text using a
// specific tokenizer. Implementations may be exact (call the
// provider's native count endpoint) or approximate (a character/byte
// heuristic). Callers must not assume bounded latency — exact counters
// can make a network round-trip, so callers in hot paths should pass
// a context with a deadline.
type TokenCounter interface {
	// Count returns the number of tokens text would consume when
	// submitted to the model the counter was constructed for. The
	// context governs cancellation for counters that make network
	// calls; counters that work locally must still honour ctx.Err().
	//
	// An error means the count could not be obtained. Callers should
	// fall back to ApproximateCounter rather than fail the request.
	Count(ctx context.Context, text string) (int, error)
}

// TokenCounterProvider is an optional capability interface. A
// Provider that can construct a TokenCounter for a given model
// implements it. Callers obtain the counter via type assertion:
//
//	var counter llm.TokenCounter = llm.ApproximateCounter{}
//	if tcp, ok := provider.(llm.TokenCounterProvider); ok {
//	    if c, err := tcp.TokenCounter(ctx, modelID); err == nil {
//	        counter = c
//	    }
//	}
//
// Providers without a native count endpoint should not implement this
// interface — callers fall back to ApproximateCounter automatically.
type TokenCounterProvider interface {
	// TokenCounter returns a counter sized for the given model. It
	// may fail if the model id is unknown to the provider (callers
	// fall back to ApproximateCounter on error rather than failing
	// the user's request).
	TokenCounter(ctx context.Context, model string) (TokenCounter, error)
}

// ApproximateCounter is a tokenizer-free fallback that returns
// runes/4. It is intentionally pessimistic for non-English content —
// CJK / emoji / heavily punctuated text tokenizes denser than 4-char
// average, so the counter tends to *under*-count. The budgeting
// layer compensates by attaching a wider safety margin (see Budget).
//
// Use this when the active provider does not implement
// TokenCounterProvider, or when the provider's native counter
// returned an error.
type ApproximateCounter struct{}

// Count returns runes/4 rounded up. Treats empty or whitespace-only
// input as zero tokens — a prompt that boils down to "" after
// trimming has no semantic content the model will be billed for, and
// returning 1 here would falsely report budget pressure on a
// no-op turn. Honours ctx.Err() so callers in a cancellation chain
// don't get a stale count.
func (ApproximateCounter) Count(ctx context.Context, text string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	runes := utf8.RuneCountInString(text)
	// Ceiling division so a 1-rune prompt still costs 1 token rather
	// than rounding down to zero.
	return (runes + approxRunesPerToken - 1) / approxRunesPerToken, nil
}

// approxRunesPerToken is the rough ratio used by ApproximateCounter.
// Picked to match OpenAI's "~4 characters per token" rule of thumb
// for English; non-English content typically tokenizes denser, which
// the safety-margin layer in Budget absorbs.
const approxRunesPerToken = 4

// IsExact reports whether the counter is the package-default
// ApproximateCounter (i.e. not exact). Returns true only when c is
// non-nil and not the zero ApproximateCounter — callers use this to
// decide which Budget safety-margin tier to apply.
func IsExact(c TokenCounter) bool {
	if c == nil {
		return false
	}
	_, isApprox := c.(ApproximateCounter)
	return !isApprox
}
