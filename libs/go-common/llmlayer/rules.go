package llmlayer

import (
	"errors"
	"fmt"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// ErrReasoningModelNotSupported is returned by CheckLayerModel for a layer
// that cannot use a reasoning-class model.
//
// Reasoning-class models stay rejected for table descriptions regardless of
// whether the provider surfaces their chain of thought separately: a
// 2,000-table warehouse runs 2,000 parallel small calls, and the per-call
// wall clock dominates total indexing time. Reasoning models burn hundreds of
// hidden tokens per call before producing the visible answer, which both
// blows the throughput budget and — on Converse / Chat Completions, where the
// thinking channel is not exposed as text — returns an empty description
// within a small output budget.
var ErrReasoningModelNotSupported = errors.New("reasoning-class models are too slow for this layer — pick a fast non-reasoning model (Haiku-class, gpt-4o-mini, gemini-2.5-flash, qwen2.5)")

// reasoningClassPatterns lists the model-id substrings whose output is routed
// through a thinking channel that Converse / Chat Completions do not expose
// as text. Catch-all patterns on purpose: rejecting a non-reasoning model by
// accident is better than letting a reasoning model silently emit empty
// descriptions.
//
// Moved here from the agent's blurb package when the rule became layer-keyed
// data; the API, the agent and the connection test now read one copy.
var reasoningClassPatterns = []string{
	"deepseek-r1",
	"r1-", // DeepSeek R1 variants (r1-distill, r1-lite, ...)
	"o1-",
	"o3",
	"o4-mini",
	// GPT-5 family — reasoning by default. Burns max_completion_tokens
	// on hidden reasoning before producing visible content, so a 512-token
	// description budget yields content="". Matches "gpt-5", "gpt-5-mini",
	// "gpt-5-nano", and dated snapshots (gpt-5-2025-08-07).
	"gpt-5",
	"extended-thinking",
}

// IsReasoningClassModel reports whether a model's user-visible text is routed
// through a thinking channel the OpenAI / Bedrock Converse / Anthropic
// Messages APIs do not expose. Matching is case-insensitive and substring-
// based, because these families share no catalog flag.
func IsReasoningClassModel(model string) bool {
	lc := strings.ToLower(model)
	for _, p := range reasoningClassPatterns {
		if strings.Contains(lc, p) {
			return true
		}
	}
	return false
}

// refusesReasoningModels is the layer-keyed pre-flight rule set.
//
// There is exactly ONE rule, and the restraint is deliberate. The other case
// people reach for — "Opus 5.5 refuses pack generation" — is a RUNTIME
// refusal (gollm.ErrModelRefused), already handled end to end: it is not
// knowable before the call, differs by prompt, and must not become a
// pre-flight denylist that stops a model the next release would accept.
var refusesReasoningModels = map[string]bool{
	LayerTableDescriptions: true,
}

// CheckLayerModel applies a layer's pre-flight rule to a (provider, model)
// choice. Returns nil for a layer with no rule, and for a model the rule
// accepts.
//
// Two independent reasoning signals for the one layer that has a rule: the
// substring patterns above (families with no shared catalog flag — OpenAI
// o-series, GPT-5, extended-thinking Claude) and the registry's catalog flag
// (Ollama models declared Reasoning: gemma4 / gemma3 / qwen3 / deepseek-r1).
// A catalog reasoning model that thinks only ADAPTIVELY
// (gollm.IsAdaptiveThinkingModel — Claude Haiku 5.5, Sonnet 5.5) answers a
// short prompt without thinking and is accepted.
//
// The same rule runs in the API's project-save handler, the dashboard's model
// picker (via Descriptor.RefusesReasoningModels) and the per-layer connection
// test, so a refused model fails when it is configured rather than half an
// hour into a run.
func CheckLayerModel(layer, provider, model string) error {
	if !refusesReasoningModels[layer] {
		return nil
	}
	if IsReasoningClassModel(model) ||
		(gollm.IsReasoningModel(provider, model) && !gollm.IsAdaptiveThinkingModel(provider, model)) {
		return fmt.Errorf("%s: %w (model=%q)", layer, ErrReasoningModelNotSupported, model)
	}
	return nil
}
