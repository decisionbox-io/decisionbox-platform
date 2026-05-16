//go:build integration

package openai

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Real-OpenAI integration coverage for the tiktoken-go counter. The
// counter is local (no network), but the only meaningful claim is
// "my count matches what OpenAI bills" — verifiable only by sending
// a Chat request and comparing tiktoken's prediction against the
// `prompt_tokens` OpenAI returns.

func openaiAPIKey(t *testing.T) string {
	t.Helper()
	k := os.Getenv("INTEGRATION_TEST_OPENAI_API_KEY")
	if k == "" {
		t.Skip("INTEGRATION_TEST_OPENAI_API_KEY not set")
	}
	return k
}

func openaiModel() string {
	if m := os.Getenv("INTEGRATION_TEST_OPENAI_MODEL"); m != "" {
		return m
	}
	return "gpt-4o-mini"
}

// TestIntegration_TiktokenMatchesOpenAIPromptTokens sends a known
// prompt to OpenAI, reads the actual prompt_tokens from usage, and
// asserts that our tiktoken-go counter's local prediction is within
// a tight tolerance.
//
// Tolerance: OpenAI's chat-completions billing includes a small
// per-message and per-response overhead (role tokens, formatting)
// that our raw text counter doesn't model. We allow ±20% to absorb
// that without papering over a true counter drift. The 5% exact-
// tier safety margin in Budget swallows the residual.
func TestIntegration_TiktokenMatchesOpenAIPromptTokens(t *testing.T) {
	apiKey := openaiAPIKey(t)
	model := openaiModel()

	provider, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": apiKey,
		"model":   model,
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	tcp, ok := provider.(gollm.TokenCounterProvider)
	if !ok {
		t.Fatal("openai provider should implement TokenCounterProvider")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	counter, err := tcp.TokenCounter(ctx, model)
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}
	if !gollm.IsExact(counter) {
		t.Fatal("openai tiktoken counter must register as exact")
	}

	// Multi-paragraph prompt big enough that the per-message
	// chat-template overhead (role markers, priming tokens — ~7
	// tokens on gpt-4o-mini) becomes proportionally negligible.
	// Without this padding the test sits at the tolerance boundary.
	prompt := "Summarize the following sentence in one word: " +
		"The quick brown fox jumps over the lazy dog. " +
		"Reply with only the single summary word.\n\n" +
		"This sentence is a pangram — it contains every letter of " +
		"the English alphabet at least once. It has been used for " +
		"more than a century to test typewriters, computer keyboards, " +
		"and font display systems. Please ignore the pangram property " +
		"and respond with your one-word summary of the sentence's " +
		"literal action: a fox jumps over a dog."

	predicted, err := counter.Count(ctx, prompt)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if predicted <= 0 {
		t.Fatalf("counter predicted %d tokens; expected > 0", predicted)
	}

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Model: model,
		Messages: []gollm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   16,
		Temperature: 0,
	})
	if err != nil {
		// Skip on transient/quota rather than failing — counter
		// behaviour didn't break in this scenario.
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "rate limit") || strings.Contains(low, "429") || strings.Contains(low, "quota") {
			t.Skipf("OpenAI rate-limited; skipping: %v", err)
		}
		t.Fatalf("Chat: %v", err)
	}
	actual := resp.Usage.InputTokens
	if actual <= 0 {
		t.Fatalf("OpenAI returned input_tokens=%d; cannot compare", actual)
	}

	// Ratio: predicted / actual. We expect close to 1.0 with small
	// deviation for chat-template overhead.
	delta := math.Abs(float64(predicted-actual)) / float64(actual)
	t.Logf("model=%s predicted=%d actual=%d delta=%.1f%%",
		model, predicted, actual, delta*100)
	if delta > 0.20 {
		t.Errorf("tiktoken count drifted >20%% from OpenAI billing: predicted=%d actual=%d (%.1f%%)",
			predicted, actual, delta*100)
	}
}

// TestIntegration_TiktokenCorrectlyOrdersIncreasingPromptSizes
// builds three progressively longer prompts and asserts the
// tiktoken counts increase monotonically. Guards against a counter
// regression where e.g. the encoding cache returns the wrong table
// and short prompts count higher than long ones.
func TestIntegration_TiktokenCorrectlyOrdersIncreasingPromptSizes(t *testing.T) {
	apiKey := openaiAPIKey(t)
	model := openaiModel()

	provider, err := gollm.NewProvider("openai", gollm.ProviderConfig{
		"credentials_json": apiKey,
		"model":   model,
	})
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	counter, err := provider.(gollm.TokenCounterProvider).TokenCounter(context.Background(), model)
	if err != nil {
		t.Fatalf("TokenCounter: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	short, _ := counter.Count(ctx, "hello")
	medium, _ := counter.Count(ctx, "hello world how are you today")
	long, _ := counter.Count(ctx, "hello world how are you today and what is the weather like where you live this fine afternoon")
	if !(short < medium && medium < long) {
		t.Fatalf("counts not monotonically increasing: short=%d medium=%d long=%d",
			short, medium, long)
	}
}
