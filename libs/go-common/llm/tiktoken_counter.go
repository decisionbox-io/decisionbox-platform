package llm

import (
	"context"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// NewTiktokenCounter returns a TokenCounter backed by tiktoken-go
// for the given BPE encoding name. Used by every provider whose
// wire is OpenAI-compatible — direct OpenAI, Azure Foundry (when
// the model entry declares an Encoding), or any future provider
// that fronts an OpenAI-compatible endpoint with a known
// tokenizer.
//
// Count() is exact for the raw text input, but chat-completions
// APIs add per-message overhead (role markers, message boundaries,
// tool_call metadata, a small priming budget) on top of the raw
// content tokens. Empirically that overhead is ~3–7 tokens per
// message on gpt-4o-mini — the Budget layer's exact-tier 5% safety
// margin absorbs the residual.
//
// Encoding-loading is non-trivial (BPE table parse + pattern
// compilation) so the underlying *tiktoken.Tiktoken is cached
// package-wide. Concurrent first-uses are serialised so only one
// parse happens per encoding name.
//
// An unknown encoding returns an error; callers should fall back
// to ApproximateCounter rather than fail the user's request.
func NewTiktokenCounter(encoding string) (TokenCounter, error) {
	enc, err := loadTiktokenEncoding(encoding)
	if err != nil {
		return nil, err
	}
	return &tiktokenCounter{enc: enc}, nil
}

// tiktokenCounter is the package-private implementation. Exposed
// only via NewTiktokenCounter so callers can't accidentally bypass
// the encoding cache.
type tiktokenCounter struct {
	enc *tiktoken.Tiktoken
}

// Count returns the exact token count tiktoken assigns to text.
// Honours ctx.Err() so a cancelled handler doesn't pay the
// encoding cost on dead requests.
func (c *tiktokenCounter) Count(ctx context.Context, text string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if text == "" {
		return 0, nil
	}
	return len(c.enc.Encode(text, nil, nil)), nil
}

var (
	tiktokenCacheMu sync.RWMutex
	tiktokenCache   = make(map[string]*tiktoken.Tiktoken)
)

// loadTiktokenEncoding returns a cached *tiktoken.Tiktoken for the
// named encoding, loading it on first use.
func loadTiktokenEncoding(name string) (*tiktoken.Tiktoken, error) {
	tiktokenCacheMu.RLock()
	if enc, ok := tiktokenCache[name]; ok {
		tiktokenCacheMu.RUnlock()
		return enc, nil
	}
	tiktokenCacheMu.RUnlock()

	tiktokenCacheMu.Lock()
	defer tiktokenCacheMu.Unlock()
	// Double-check after acquiring the write lock so concurrent
	// loaders don't race past each other.
	if enc, ok := tiktokenCache[name]; ok {
		return enc, nil
	}
	enc, err := tiktoken.GetEncoding(name)
	if err != nil {
		return nil, err
	}
	tiktokenCache[name] = enc
	return enc, nil
}
