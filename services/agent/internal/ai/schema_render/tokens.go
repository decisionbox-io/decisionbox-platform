package schema_render

import (
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// TokenCounter estimates how many tokens a string consumes in the target
// LLM's tokenizer. Returning a conservative over-estimate is safer than
// under-estimating — the budget already includes a 20-30% margin (§5.1),
// but nothing stops a CharCounter from being used on an OpenAI model where
// the real encoding packs more aggressively.
type TokenCounter interface {
	// CountTokens returns the estimated token count for s.
	// Non-negative; empty string → 0.
	CountTokens(s string) int
}

// CharCounter is the fallback estimator used when we don't know the target
// tokenizer (Ollama local models, self-hosted finetunes, etc). Approximates
// at charsPerToken, then applies a 1.25× safety factor so the caller's
// budget is still respected in the worst case.
//
// Default ratio matches OpenAI's "≈4 characters per token" rule of thumb
// for English text; most non-English text tokenizes slightly denser, which
// is why the 1.25× multiplier is baked in.
type CharCounter struct {
	// CharsPerToken is the naive chars-per-token divisor. 0 falls back to
	// the OpenAI/Anthropic average of 4.
	CharsPerToken float64
	// SafetyFactor multiplies the naive estimate to account for non-English
	// density, non-ASCII glyphs, and tokenizer variance. 0 falls back to
	// 1.25 (aligns with the 20-30% budget margin stated in §5.1).
	SafetyFactor float64
}

// CountTokens implements TokenCounter.
func (c CharCounter) CountTokens(s string) int {
	if s == "" {
		return 0
	}
	div := c.CharsPerToken
	if div <= 0 {
		div = 4
	}
	factor := c.SafetyFactor
	if factor <= 0 {
		factor = 1.25
	}
	// Character count in bytes — close enough, slightly over-counts for
	// multibyte glyphs which is the safer direction.
	n := float64(len(s)) / div * factor
	return int(n + 0.5) // round half up
}

// TiktokenCounter wraps pkoukk/tiktoken-go for OpenAI/Anthropic-class
// models. Safe for the spike's winning combos (Qwen on Bedrock uses a
// compatible-enough SentencePiece variant; cl100k_base over-estimates by
// ~10-15% there, which is on the safe side of the budget).
//
// Construct with NewTiktokenCounter so initialisation errors surface
// upfront instead of turning every CountTokens into an error-returning API.
type TiktokenCounter struct {
	tk *tiktoken.Tiktoken
}

// NewTiktokenCounter loads the tiktoken encoding for the target model.
// Passes "cl100k_base" for most OpenAI/Anthropic models; anything else
// falls back to CharCounter via the returned error.
func NewTiktokenCounter(encoding string) (*TiktokenCounter, error) {
	if encoding == "" {
		encoding = "cl100k_base"
	}
	tk, err := tiktokenOnce(encoding)
	if err != nil {
		return nil, err
	}
	return &TiktokenCounter{tk: tk}, nil
}

// CountTokens implements TokenCounter.
func (t *TiktokenCounter) CountTokens(s string) int {
	if s == "" {
		return 0
	}
	return len(t.tk.Encode(s, nil, nil))
}

// tiktokenOnce caches encoder instances — tiktoken-go's GetEncoding is slow
// on first call (downloads BPE vocab from disk / embedded), so doing it
// once per process keeps catalog rendering cheap.
var (
	tiktokenCacheMu sync.Mutex
	tiktokenCache   = map[string]*tiktoken.Tiktoken{}
)

func tiktokenOnce(encoding string) (*tiktoken.Tiktoken, error) {
	tiktokenCacheMu.Lock()
	defer tiktokenCacheMu.Unlock()
	if tk, ok := tiktokenCache[encoding]; ok {
		return tk, nil
	}
	tk, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, err
	}
	tiktokenCache[encoding] = tk
	return tk, nil
}
