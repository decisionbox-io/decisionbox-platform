package openai

import (
	"context"
	"fmt"
	"sync"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/pkoukk/tiktoken-go"
)

// TokenCounter implements gollm.TokenCounterProvider for OpenAI. The
// counter looks up the BPE encoding declared on the catalog entry
// (catalog.go, "Encoding" field) and runs tiktoken-go locally — no
// network round-trip, no API quota consumed.
//
// Count() is exact for the raw text input, but OpenAI's billed
// `prompt_tokens` for a chat-completions request additionally
// includes per-message overhead (role markers, message boundaries,
// tool_call metadata, a small priming budget). Empirically that
// overhead is ~3–7 tokens per message on gpt-4o-mini — small in
// absolute terms but ~25% of a short message. The budget layer's
// 5% safety margin (exact-counter tier) absorbs the residual; the
// tiktoken-vs-real-billing integration test pins the drift at
// ≤20% on a non-trivial prompt.
//
// Unknown models fall back to the o200k_base encoding (FallbackEncoding)
// which matches every modern OpenAI model. If even the fallback
// encoding fails to load, Count returns an error so the caller can
// fall back to gollm.ApproximateCounter.
func (p *OpenAIProvider) TokenCounter(_ context.Context, model string) (gollm.TokenCounter, error) {
	if model == "" {
		model = p.model
	}
	encoding := gollm.GetEncoding("openai", model)
	if encoding == "" {
		// Caller picked a model with no catalog entry. o200k_base
		// matches every modern OpenAI surface and is the safest
		// default; the safety-margin layer in budget absorbs any
		// tail-case drift.
		encoding = FallbackEncoding
	}
	enc, err := getEncoding(encoding)
	if err != nil {
		return nil, fmt.Errorf("openai TokenCounter: load %s: %w", encoding, err)
	}
	return &openaiTokenCounter{enc: enc}, nil
}

// openaiTokenCounter is the per-model exact counter for OpenAI.
type openaiTokenCounter struct {
	enc *tiktoken.Tiktoken
}

// Count returns the exact token count tiktoken assigns to text.
// Honours ctx.Err() before doing any work so a cancelled handler
// doesn't pay the encoding cost.
func (c *openaiTokenCounter) Count(ctx context.Context, text string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if text == "" {
		return 0, nil
	}
	return len(c.enc.Encode(text, nil, nil)), nil
}

// encodingCache caches loaded tiktoken encodings. Loading an encoding
// is non-trivial (pattern compilation + BPE table parse) and the
// library itself does not cache, so the Ask handler — which creates a
// new TokenCounter per request — would re-parse on every turn.
var (
	encodingCacheMu sync.RWMutex
	encodingCache   = make(map[string]*tiktoken.Tiktoken)
)

// getEncoding returns a cached *tiktoken.Tiktoken for the given name,
// loading it on first use. Concurrent first-uses are serialised so
// only one BPE-table parse happens per encoding name.
func getEncoding(name string) (*tiktoken.Tiktoken, error) {
	encodingCacheMu.RLock()
	if enc, ok := encodingCache[name]; ok {
		encodingCacheMu.RUnlock()
		return enc, nil
	}
	encodingCacheMu.RUnlock()

	encodingCacheMu.Lock()
	defer encodingCacheMu.Unlock()
	// Double-check after acquiring the write lock so concurrent
	// loaders don't race past each other.
	if enc, ok := encodingCache[name]; ok {
		return enc, nil
	}
	enc, err := tiktoken.GetEncoding(name)
	if err != nil {
		return nil, err
	}
	encodingCache[name] = enc
	return enc, nil
}
