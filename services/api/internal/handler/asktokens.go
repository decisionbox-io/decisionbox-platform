package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	apilog "github.com/decisionbox-io/decisionbox/services/api/internal/log"
)

// askMaxOutputTokens is the generation cap Ask reserves on the chat
// request. Was an inline literal in the handler; promoting to a
// named constant lets the budgeting layer subtract the same value
// the caller will send.
const askMaxOutputTokens = 2048

// askReservedSystemTokens is the flat token estimate the budget
// reserves for the system prompt and any fixed scaffolding. Set
// generously so a per-character miscount can't blow the budget — the
// trim loop only sees Available() after this subtraction.
const askReservedSystemTokens = 600

// askMinRAGItems is the floor for dynamic top-K shrinking. The trim
// loop will not drop below this count of insights before returning a
// typed 413; below this, the answer becomes useless and the user is
// better off being told to start a new chat / pick a wider model.
const askMinRAGItems = 1

// verifyExactPromptFits does a single exact-counter check on the
// fully assembled request (system prompt + every history message +
// the new user prompt). The Ask handler does the budget walk with
// gollm.ApproximateCounter for performance — counting every message
// with an exact counter that round-trips to the upstream (Anthropic
// /count_tokens, Vertex :countTokens) would put N+1 calls in the
// hot path. This function is the safety net: ONE exact call right
// before Chat, just to confirm the approximation didn't slip past
// the model's window.
//
// Returns:
//   - (nil, false): provider has no exact counter, the verify call
//     errored, or context cancelled. Caller proceeds with the
//     request — the approximate walk's 15% safety margin is the
//     only safety net, and any provider-side overflow gets typed
//     via classifyLLMError.
//   - (nil, true): exact counter confirmed the prompt fits.
//   - (err, true): exact counter reported overflow. Caller surfaces
//     413 context_overflow with err.Error() as details.
//
// The exact counter is invoked exactly once on a flattened string —
// `Count(text string)` is the only operation the TokenCounter
// interface exposes, so we sum the system prompt + each message's
// content (separator = "\n\n"). The flattened text is a lower bound
// on the upstream-billed tokens (chat-template overhead adds a few
// per message) — the askMaxOutputTokens + askReservedSystemTokens
// reserves cover that gap.
func verifyExactPromptFits(ctx context.Context, llmProvider gollm.Provider, model, systemPrompt string, messages []gollm.Message, modelMaxInput int) (error, bool) {
	if llmProvider == nil {
		return nil, false
	}
	tcp, ok := llmProvider.(gollm.TokenCounterProvider)
	if !ok {
		return nil, false
	}
	counter, err := tcp.TokenCounter(ctx, model)
	if err != nil || counter == nil {
		return nil, false
	}
	// Skip when the provider returned ApproximateCounter (custom
	// OpenAI base_url, Claude/Mistral on Foundry, etc.) — verifying
	// with the same approximation we already used wastes a call.
	if !gollm.IsExact(counter) {
		return nil, false
	}

	// Flatten the full request into one string the counter sees.
	// We don't model role markers / message boundaries here: the
	// exact counter's job is to spot a clear overshoot, not to
	// match the upstream's chat-template billing byte-for-byte.
	full := flattenForExactCount(systemPrompt, messages)
	exactTokens, err := counter.Count(ctx, full)
	if err != nil {
		apilog.WithError(err).Warn("exact prompt verification failed; proceeding with approximate walk's clearance")
		return nil, false
	}

	// Allow the request when the exact count plus the generation
	// reserve still fits the model's input window. The system-prompt
	// reserve already lives in askReservedSystemTokens; we don't
	// double-subtract it here because the flattened text already
	// includes the system prompt.
	if exactTokens+askMaxOutputTokens > modelMaxInput {
		return fmt.Errorf("model=%s window=%d exact_tokens=%d max_output=%d",
			model, modelMaxInput, exactTokens, askMaxOutputTokens), true
	}
	return nil, true
}

// flattenForExactCount concatenates the system prompt + every
// message's content into a single string the exact counter can
// consume. Uses "\n\n" as the separator — close enough to what
// chat-template formatters produce that any small overcount lives
// in the askMaxOutputTokens reserve, not in user-visible behavior.
func flattenForExactCount(systemPrompt string, messages []gollm.Message) string {
	parts := make([]string, 0, len(messages)+1)
	if systemPrompt != "" {
		parts = append(parts, systemPrompt)
	}
	for _, m := range messages {
		if m.Content != "" {
			parts = append(parts, m.Content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// trimMessagesByTokens walks the session's stored Q/A pairs newest →
// oldest and keeps only the suffix whose cumulative token cost fits
// budget. Returns the kept pairs flattened into the [user, assistant,
// user, assistant, …] sequence the chat API expects.
//
// Invariants enforced here:
//   - A user message is never separated from its assistant reply (we
//     trim by pair, never by individual message), so the model never
//     sees a dangling answer.
//   - The most recent N pairs are kept; ancient pairs are dropped
//     first. This matches user expectation that recent context
//     matters more.
//   - A counter error stops the walk and returns whatever fit so far,
//     plus the error. Callers can decide whether to surface it (the
//     Ask handler degrades gracefully — partial history + the current
//     question is still useful).
//
// Counting both Question and Answer separately matches what the
// provider will see (chat APIs send two messages per turn). The
// budget value should already be the post-RAG, post-question
// remainder — see Budget.Available() and the caller.
func trimMessagesByTokens(ctx context.Context, msgs []commonmodels.AskSessionMessage, counter gollm.TokenCounter, budget int) ([]gollm.Message, error) {
	if len(msgs) == 0 || budget <= 0 {
		return nil, nil
	}
	if counter == nil {
		counter = gollm.ApproximateCounter{}
	}

	// Build in reverse so we can stop as soon as the next-older pair
	// won't fit. Each kept pair contributes (qTokens + aTokens) to the
	// running total.
	out := make([]keptPair, 0, len(msgs))
	used := 0

	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]

		qTokens, err := counter.Count(ctx, m.Question)
		if err != nil {
			return flattenKept(out), err
		}
		aTokens, err := counter.Count(ctx, m.Answer)
		if err != nil {
			return flattenKept(out), err
		}
		pairTokens := qTokens + aTokens
		if used+pairTokens > budget {
			break
		}
		out = append(out, keptPair{
			user:      gollm.Message{Role: "user", Content: m.Question},
			assistant: gollm.Message{Role: "assistant", Content: m.Answer},
		})
		used += pairTokens
	}

	return flattenKept(out), nil
}

// keptPair holds one user/assistant turn that survived the trim
// walk. Internal — the public output is a flattened []gollm.Message.
type keptPair struct {
	user, assistant gollm.Message
}

// fitRAGContext assembles the [i] Name: Description (score) citation
// block from insights, dropping the lowest-scoring entry whenever
// the assembled context exceeds available. Returns:
//   - the final context string,
//   - its token count,
//   - the number of insights it kept (callers gate the 413 path on
//     this rather than len(insights), so they don't have to track
//     the shrink state separately), and
//   - the last counter error (if any — caller treats it as
//     informational).
//
// Search results arrive sorted by similarity (best first), so
// dropping the tail is also dropping the worst match. Stops when:
//   - the context fits, or
//   - only askMinRAGItems insights remain. The caller decides
//     whether to return a typed 413 from there.
func fitRAGContext(ctx context.Context, counter gollm.TokenCounter, insights []searchResultItem, available int) (string, int, int, error) {
	if counter == nil {
		counter = gollm.ApproximateCounter{}
	}
	working := insights
	var lastErr error
	for {
		text := formatRAGContext(working)
		tokens, err := counter.Count(ctx, text)
		if err != nil {
			lastErr = err
			// Couldn't count — assume it fits and return what we have
			// rather than spinning forever.
			return text, tokens, len(working), lastErr
		}
		if tokens <= available || len(working) <= askMinRAGItems {
			return text, tokens, len(working), lastErr
		}
		working = working[:len(working)-1]
	}
}

// formatRAGContext renders the [i] Name: Description (score) lines
// the LLM cites against. Centralised so fitRAGContext and the Ask
// handler share a single formatter — no drift between what gets
// counted and what gets sent.
func formatRAGContext(insights []searchResultItem) string {
	if len(insights) == 0 {
		return ""
	}
	parts := make([]string, 0, len(insights))
	for i, s := range insights {
		parts = append(parts, fmt.Sprintf("[%d] %s: %s (score: %.2f)", i+1, s.Name, s.Description, s.Score))
	}
	return strings.Join(parts, "\n")
}

// classifyLLMError maps an upstream LLM error into an HTTP status +
// typed error code so the dashboard can branch on condition. The
// classification is deliberately small: context overflow (413) is the
// only condition the user can act on themselves; everything else is
// a 5xx the user surfaces to support.
//
// Context-overflow detection sniffs known upstream phrases —
// Anthropic ("prompt is too long"), OpenAI ("context length"), and
// Bedrock ("input too long"). Adding a new provider may require a
// new substring; the worst case is we miss the classification and
// surface 502 instead of 413, which is annoying but not broken.
func classifyLLMError(err error) (status int, code, msg, details string) {
	if err == nil {
		return http.StatusOK, "", "", ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, ErrCodeLLMUpstream,
			"LLM request timed out", err.Error()
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "prompt is too long"),
		strings.Contains(low, "context length"),
		strings.Contains(low, "context_length_exceeded"),
		strings.Contains(low, "maximum context length"),
		strings.Contains(low, "input too long"),
		strings.Contains(low, "too many tokens"):
		return http.StatusRequestEntityTooLarge, ErrCodeContextOverflow,
			"this conversation has grown past the model's context window",
			gollm.SanitizeErrorBody([]byte(err.Error()), 300)
	case strings.Contains(low, "rate limit"),
		strings.Contains(low, "rate_limit"),
		strings.Contains(low, "quota"),
		strings.Contains(low, "throttl"):
		return http.StatusBadGateway, ErrCodeLLMUpstream,
			"LLM provider is rate-limiting", gollm.SanitizeErrorBody([]byte(err.Error()), 300)
	case strings.Contains(low, "content_filter"),
		strings.Contains(low, "content filter"),
		strings.Contains(low, "safety"):
		return http.StatusBadGateway, ErrCodeLLMUpstream,
			"LLM provider blocked this content", gollm.SanitizeErrorBody([]byte(err.Error()), 300)
	}
	return http.StatusInternalServerError, ErrCodeLLMSynthesisFailed,
		"LLM synthesis failed", gollm.SanitizeErrorBody([]byte(err.Error()), 300)
}

// flattenKept reverses the newest-first kept slice into the
// oldest-first [user, assistant, user, assistant, …] sequence the
// chat API consumes.
func flattenKept(pairs []keptPair) []gollm.Message {
	if len(pairs) == 0 {
		return nil
	}
	out := make([]gollm.Message, 0, 2*len(pairs))
	for i := len(pairs) - 1; i >= 0; i-- {
		out = append(out, pairs[i].user, pairs[i].assistant)
	}
	return out
}
