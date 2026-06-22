package openai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestInferOpenAIWire_LLMFamilies(t *testing.T) {
	tests := []string{
		// GPT-3.5 / 4 / 4o / 4.1 / 5 — catalogued and not.
		"gpt-3.5-turbo",
		"gpt-3.5-turbo-1106",
		"gpt-4",
		"gpt-4-turbo",
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-4o-2024-08-06",
		"gpt-4.1",
		"gpt-4.1-mini",
		"gpt-4.1-nano",
		"gpt-4.1-2025-04-14",
		"gpt-5",
		"gpt-5-mini",
		"gpt-5-nano",
		"gpt-5-2025-09-01",
		// Reasoning families — dispatchable as analysis LLM; blurb has
		// its own rejection layer for this class.
		"o1",
		"o1-preview",
		"o1-mini",
		"o3",
		"o3-mini",
		"o3-pro-2025-06-10",
		"o4-mini",
		"o4-mini-2025-04-16",
		// ChatGPT-branded model ID.
		"chatgpt-4o-latest",
		// Case-insensitive on the prefix.
		"GPT-4o",
	}
	for _, m := range tests {
		t.Run(m, func(t *testing.T) {
			if got := inferOpenAIWire(m); got != gollm.WireOpenAICompat {
				t.Errorf("inferOpenAIWire(%q) = %q, want %q", m, got, gollm.WireOpenAICompat)
			}
		})
	}
}

// Non-LLM /v1/models entries must be dropped from the LLM picker so the
// user can't accidentally pick text-embedding-3-large as their analysis
// LLM and fail at the first Chat call.
func TestInferOpenAIWire_NonLLMFamiliesAreUnknown(t *testing.T) {
	tests := []string{
		"text-embedding-3-large",
		"text-embedding-3-small",
		"text-embedding-ada-002",
		"text-davinci-003", // legacy completion
		"tts-1",
		"tts-1-hd",
		"whisper-1",
		"dall-e-2",
		"dall-e-3",
		"gpt-image-1",
		"davinci-002",
		"babbage-002",
		"omni-moderation-latest",
		"computer-use-preview",
	}
	for _, m := range tests {
		t.Run(m, func(t *testing.T) {
			if got := inferOpenAIWire(m); got != gollm.WireUnknown {
				t.Errorf("inferOpenAIWire(%q) = %q, want %q", m, got, gollm.WireUnknown)
			}
		})
	}
}

func TestInferOpenAIWire_Unknown(t *testing.T) {
	tests := []string{
		"",
		"not-an-openai-model",
		"claude-3-5-sonnet",
		"gemini-2.5-flash",
	}
	for _, m := range tests {
		t.Run(m, func(t *testing.T) {
			if got := inferOpenAIWire(m); got != gollm.WireUnknown {
				t.Errorf("inferOpenAIWire(%q) = %q, want %q", m, got, gollm.WireUnknown)
			}
		})
	}
}
