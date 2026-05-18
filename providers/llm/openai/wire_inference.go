package openai

import (
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// inferOpenAIWire is the FamilyInferrer wired into ProviderMeta as the
// third resolution tier (after the catalog and wire_override) for any
// model ID that comes back from OpenAI's /v1/models endpoint but isn't
// in our shipped catalog yet. Without this, freshly-released OpenAI
// models (and nano/mini variants we haven't catalogued like
// gpt-4.1-nano) come back from live-list with Wire="" and are hidden
// from the LLM picker as "unsupported wire".
//
// Every OpenAI-served LLM uses the OpenAI Chat Completions wire — both
// the chat families (gpt-*, chatgpt-*) and the reasoning models
// (o1*, o3*, o4*). The reasoning families have wire-level quirks
// (max_completion_tokens, no temperature, hidden thinking tokens) but
// those are dispatch-layer concerns, not wire-format ones. They still
// belong on the LLM picker — blurb generation is the only path that
// rejects them, via blurb.IsReasoningClassModel.
//
// Non-LLM endpoints under the same API key (embeddings, TTS, Whisper,
// DALL-E, gpt-image-*) also come back from /v1/models. We classify
// them as WireUnknown so the LLM combobox filter drops them. Embedding
// providers have their own /v1/providers/embedding/* surface; mixing
// them into the LLM list would let a user pick text-embedding-3-large
// as an analysis LLM and fail at first Chat call.
func inferOpenAIWire(model string) gollm.Wire {
	id := strings.ToLower(model)

	// Non-LLM families served by OpenAI under /v1/models — drop them
	// from the LLM picker. Embeddings have their own provider; the
	// rest aren't supported as agent LLMs at all.
	for _, prefix := range openAINonLLMPrefixes {
		if strings.HasPrefix(id, prefix) {
			return gollm.WireUnknown
		}
	}

	// LLM families. Order-independent — these prefixes don't overlap.
	for _, prefix := range openAILLMPrefixes {
		if strings.HasPrefix(id, prefix) {
			return gollm.WireOpenAICompat
		}
	}
	return gollm.WireUnknown
}

// openAILLMPrefixes are the ID prefixes for OpenAI families the agent
// can dispatch as chat models via the openai-compat wire. Reasoning
// models (o1*/o3*/o4*) are included — they're dispatchable as analysis
// LLMs; the blurb path has its own rejection layer.
var openAILLMPrefixes = []string{
	"gpt-",     // gpt-3.5, gpt-4, gpt-4o, gpt-4.1, gpt-5 (incl. -nano, -mini, dated suffixes)
	"chatgpt-", // chatgpt-4o-latest
	"o1",       // o1, o1-preview, o1-mini
	"o3",       // o3, o3-mini, o3-pro (dated suffixes too)
	"o4-",      // o4-mini
}

// openAINonLLMPrefixes are the ID prefixes for OpenAI offerings that
// /v1/models returns but the agent does NOT dispatch as LLMs. They
// must be hidden from the LLM picker — leaving them in lets users
// accidentally pick text-embedding-3-large as their analysis LLM.
var openAINonLLMPrefixes = []string{
	"text-embedding-", // embeddings (separate provider)
	"text-",           // legacy text-* completion models (deprecated)
	"tts-",            // TTS models
	"whisper-",        // speech-to-text
	"dall-e-",         // image generation (v2/v3)
	"gpt-image-",      // image generation (gpt-image-1)
	"davinci-",        // legacy fine-tune base / embeddings
	"babbage-",        // legacy fine-tune base
	"omni-moderation", // moderation
	"computer-use-",   // computer-use models (separate API)
}
