package openai

import (
	"context"
	"net/url"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// canonicalOpenAIHosts is the set of hostnames whose tokenizer we
// know matches tiktoken's o200k_base / cl100k_base mapping. A user
// who configures the openai provider with a custom base_url (a
// self-hosted OpenAI-compatible proxy, a model gateway, a third-
// party endpoint) may be talking to a model that tokenizes
// differently — tiktoken's count would over- or under-estimate, and
// flagging the counter as exact would make the budget layer pick
// the narrow 5% safety margin even though it shouldn't.
//
// When the base_url's host isn't on this list, TokenCounter returns
// ApproximateCounter so the budget walk picks the wider 15% margin.
// The list is hostnames only — paths (`/v1`, `/openai/v1`, etc.)
// don't change which tokenizer the upstream uses.
var canonicalOpenAIHosts = map[string]struct{}{
	"api.openai.com": {},
}

// TokenCounter implements gollm.TokenCounterProvider for OpenAI.
// Routing decisions:
//
//  1. Base URL is a known-OpenAI host AND model has an explicit
//     catalog Encoding → tiktoken with that encoding (exact).
//  2. Base URL is a known-OpenAI host AND model has no catalog
//     Encoding (new snapshot, custom deployment name) → tiktoken
//     with FallbackEncoding (still exact for any modern OpenAI
//     surface).
//  3. Base URL is NOT a known-OpenAI host → ApproximateCounter.
//     We can't claim tiktoken matches whatever tokenizer the
//     upstream proxy fronts; the safety-margin layer's 15%
//     approximate-tier margin absorbs the mismatch.
//
// Count() is exact for the raw text input, but chat-completions
// `prompt_tokens` includes per-message overhead (role markers,
// tool_call metadata, priming) — empirically ~3–7 tokens per
// message on gpt-4o-mini. The exact-tier 5% safety margin in
// Budget absorbs the residual; see the
// TestIntegration_TiktokenMatchesOpenAIPromptTokens test.
func (p *OpenAIProvider) TokenCounter(_ context.Context, model string) (gollm.TokenCounter, error) {
	if model == "" {
		model = p.model
	}
	if !isCanonicalOpenAIHost(p.baseURL) {
		// Custom proxy / gateway / self-hosted endpoint — its
		// tokenizer may not match tiktoken at all. Approximate
		// counter + wider safety margin is the safe choice.
		return gollm.ApproximateCounter{}, nil
	}
	encoding := gollm.GetEncoding("openai", model)
	if encoding == "" {
		// Real OpenAI endpoint but the model isn't in our catalog
		// yet (e.g. a freshly released snapshot). o200k_base is the
		// tokenizer for every modern OpenAI surface, so tiktoken
		// is still exact here — the catalog miss is just a
		// staleness gap, not a tokenizer mismatch.
		encoding = FallbackEncoding
	}
	counter, err := gollm.NewTiktokenCounter(encoding)
	if err != nil {
		return nil, err
	}
	return counter, nil
}

// isCanonicalOpenAIHost reports whether baseURL points at an actual
// OpenAI-operated endpoint (so tiktoken's tokenizer is guaranteed
// to match). The empty string (no override → default api.openai.com)
// is treated as canonical too.
func isCanonicalOpenAIHost(baseURL string) bool {
	if baseURL == "" {
		return true
	}
	// Strip scheme so callers can pass either "api.openai.com" or
	// "https://api.openai.com/v1".
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		// Schemeless input ("api.openai.com/v1") — fall through to
		// a prefix scan rather than refusing.
		host = strings.SplitN(baseURL, "/", 2)[0]
	}
	_, ok := canonicalOpenAIHosts[host]
	return ok
}
