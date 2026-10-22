// Package anthropicwire holds request/response pieces shared by every
// provider that speaks the Anthropic Messages wire (claude, bedrock,
// vertex-ai, azure-foundry), so prompt caching and usage accounting
// behave identically whichever cloud serves Claude.
package anthropicwire

import gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"

// CacheControl returns a fresh breakpoint marker, the value the Messages
// API accepts as `cache_control` on a system or content block. "ephemeral"
// is the only type the API defines. Fresh per call so callers may attach
// it to several blocks without aliasing.
func CacheControl() map[string]interface{} {
	return map[string]interface{}{"type": "ephemeral"}
}

// SystemField returns the value for the request's "system" field. With
// caching off it is the plain prompt string (the historical wire shape).
// With caching on it is a single text block carrying a cache breakpoint,
// so every call that shares the system prompt — each exploration step,
// each verifier round — reads it from the cache.
func SystemField(prompt string, cache bool) interface{} {
	if !cache {
		return prompt
	}
	return []map[string]interface{}{{"type": "text", "text": prompt, "cache_control": CacheControl()}}
}

// CacheConversationTail reports whether the final message of a request
// should carry a cache breakpoint. Only multi-turn requests re-send their
// prefix on the next call; marking a single-message request would pay the
// cache-write premium for a prefix nothing reads back.
func CacheConversationTail(messageCount int) bool {
	return messageCount > 1
}

// MarkLastMessage puts a cache breakpoint on the final content block of
// the last message in a map-shaped messages array, converting a plain
// string content into a one-block list. The next call of a multi-turn
// loop re-sends this whole prefix and reads it from the cache. No-op for
// an empty array or empty content.
func MarkLastMessage(messages []map[string]interface{}) {
	if len(messages) == 0 {
		return
	}
	last := messages[len(messages)-1]
	switch c := last["content"].(type) {
	case string:
		if c != "" {
			last["content"] = []map[string]interface{}{{"type": "text", "text": c, "cache_control": CacheControl()}}
		}
	case []map[string]interface{}:
		if len(c) > 0 {
			c[len(c)-1]["cache_control"] = CacheControl()
		}
	}
}

// Usage mirrors the Messages API usage object, including the prompt-cache
// counters. Anthropic's input_tokens counts only the uncached tail of the
// prompt.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ToLLM converts to the provider-neutral Usage, whose InputTokens is the
// total prompt (uncached + cache write + cache read) with the cache split
// carried alongside.
func (u Usage) ToLLM() gollm.Usage {
	return gollm.Usage{
		InputTokens:      u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

// ToolChoice maps the wire-neutral ChatRequest.ToolChoice to the Messages
// API tool_choice object. "" / "auto" map to nil (the API default) unless
// disableParallel is set, in which case an explicit auto choice carries
// the flag — that is the shape a relaxed structured-output request needs
// (see gollm.RelaxForcedToolChoice) so the model still returns a single
// object. disableParallel is ignored for "none".
func ToolChoice(choice string, disableParallel bool) map[string]interface{} {
	var tc map[string]interface{}
	switch choice {
	case "", "auto":
		if !disableParallel {
			return nil
		}
		tc = map[string]interface{}{"type": "auto"}
	case "any", "required":
		tc = map[string]interface{}{"type": "any"}
	case "none":
		return map[string]interface{}{"type": "none"}
	default:
		tc = map[string]interface{}{"type": "tool", "name": choice}
	}
	if disableParallel {
		tc["disable_parallel_tool_use"] = true
	}
	return tc
}
