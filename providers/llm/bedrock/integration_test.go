//go:build integration

package bedrock

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// Default Anthropic model on Bedrock; override with
// INTEGRATION_TEST_BEDROCK_MODEL. Note: the user-provided configuration
// uses "global.anthropic.claude-opus-4-6-v1" in us-east-1.
func bedrockModel() string {
	if m := os.Getenv("INTEGRATION_TEST_BEDROCK_MODEL"); m != "" {
		return m
	}
	return "global.anthropic.claude-opus-4-6-v1"
}

// Default OpenAI-compat model on Bedrock; override with
// INTEGRATION_TEST_BEDROCK_OPENAICOMPAT_MODEL. Defaults to the Qwen
// entry in the shipped catalog.
func bedrockOpenAICompatModel() string {
	if m := os.Getenv("INTEGRATION_TEST_BEDROCK_OPENAICOMPAT_MODEL"); m != "" {
		return m
	}
	return "qwen.qwen3-next-80b-a3b"
}

func skipIfNoRegion(t *testing.T) string {
	t.Helper()
	region := os.Getenv("INTEGRATION_TEST_BEDROCK_REGION")
	if region == "" {
		t.Skip("INTEGRATION_TEST_BEDROCK_REGION not set (also requires AWS credentials)")
	}
	return region
}

func maybeSkipOnRateLimit(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "ThrottlingException") || strings.Contains(err.Error(), "429") {
		t.Skipf("Rate limited (auth works, quota exceeded): %v", err)
	}
}

// --- Anthropic wire ---

func TestIntegration_Anthropic_BasicChat(t *testing.T) {
	region := skipIfNoRegion(t)
	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  bedrockModel(),
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Say hello in one word."}},
		MaxTokens: 10,
	})
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response content should not be empty")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("should report input tokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("should report output tokens")
	}
	t.Logf("Bedrock Anthropic: %q (model=%s, tokens: in=%d out=%d)",
		resp.Content, resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

func TestIntegration_Anthropic_SystemPrompt(t *testing.T) {
	region := skipIfNoRegion(t)
	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  bedrockModel(),
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		SystemPrompt: "You are a calculator. Only respond with numbers.",
		Messages:     []gollm.Message{{Role: "user", Content: "What is 2+2?"}},
		MaxTokens:    10,
	})
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response should not be empty")
	}
	t.Logf("Bedrock system prompt: %q", resp.Content)
}

// --- OpenAI-compat wire (Qwen / DeepSeek / Mistral / Llama on Bedrock) ---

func TestIntegration_OpenAICompat_BasicChat(t *testing.T) {
	region := skipIfNoRegion(t)
	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  bedrockOpenAICompatModel(),
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Reply with a single word."}},
		MaxTokens: 32,
	})
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if resp.Content == "" {
		t.Error("response should not be empty")
	}
	if resp.Usage.InputTokens == 0 {
		t.Error("should report input tokens")
	}
	if resp.Usage.OutputTokens == 0 {
		t.Error("should report output tokens")
	}
	t.Logf("Bedrock OpenAICompat: %q (model=%s, tokens: in=%d out=%d)",
		resp.Content, resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}

// --- Error paths ---

func TestIntegration_InvalidModel(t *testing.T) {
	region := skipIfNoRegion(t)
	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  "anthropic.nonexistent-model-v1:0",
		// Force Anthropic wire since this made-up model is not catalogued;
		// otherwise dispatch rejects before reaching AWS. The point of this
		// test is the AWS-side error, not the catalog error.
		"wire_override": "anthropic",
	})
	if err != nil {
		t.Fatalf("Provider creation should succeed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for invalid model")
	}
	t.Logf("Invalid model error: %v", err)
}

func TestIntegration_UncataloguedModelActionable(t *testing.T) {
	// No region needed — dispatch fails before any AWS call.
	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": "us-east-1",
		"model":  "vendor.future-unknown-model",
	})
	if err != nil {
		t.Fatalf("Provider creation should succeed: %v", err)
	}

	_, err = provider.Chat(context.Background(), gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"wire_override", "vendor.future-unknown-model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestIntegration_ContextCancellation(t *testing.T) {
	region := skipIfNoRegion(t)

	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  bedrockModel(),
	})
	if err != nil {
		t.Fatalf("Provider creation failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "hello"}},
		MaxTokens: 5,
	})
	if err == nil {
		t.Fatal("should return error for cancelled context")
	}
	t.Logf("Cancelled context error: %v", err)
}

// --- upstream rejection carries its status (#41) ---

// An over-long prompt is the upstream rejecting the REQUEST, and a caller
// has to be able to tell that from the backend being broken: one is worth
// reporting to whoever sent it, the other is worth retrying. Both used to
// arrive as an indistinguishable error, which is how a managed gateway
// answered 502 to a request Bedrock had called invalid.
//
// Run against a real model, with a prompt past its context window.
func TestIntegration_Anthropic_OverlongPromptIsAClientFault(t *testing.T) {
	region := skipIfNoRegion(t)

	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  bedrockModel(),
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Comfortably past any current Claude window. "word " is one token or
	// so, so this is on the order of a million tokens.
	huge := strings.Repeat("word ", 1_000_000)

	_, err = provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: huge}},
		MaxTokens: 16,
	})
	if err == nil {
		t.Fatal("a prompt past the context window should have been rejected")
	}
	// Not maybeSkipOnRateLimit: a 429 here would be a different outcome,
	// and the assertion below distinguishes them on purpose.
	status := gollm.UpstreamStatus(err)
	t.Logf("status=%d client_fault=%v err=%v", status, gollm.UpstreamClientFault(err), err)

	if status == 0 {
		t.Fatalf("the upstream status did not survive to the caller: %v", err)
	}
	if status == 429 {
		t.Skipf("rate limited rather than rejected (status 429); auth works, quota exceeded: %v", err)
	}
	if status < 400 || status > 499 {
		t.Errorf("status = %d, want a 4xx for a request the upstream rejected", status)
	}
	if !gollm.UpstreamClientFault(err) {
		t.Errorf("status %d did not read as a client fault", status)
	}
}

// The counterpart: a well-formed request must not be reported as the
// caller's fault, or everything becomes a 400 and nothing is ever retried.
func TestIntegration_Anthropic_HealthyCallIsNotAClientFault(t *testing.T) {
	region := skipIfNoRegion(t)

	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"region": region,
		"model":  bedrockModel(),
	})
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err = provider.Chat(ctx, gollm.ChatRequest{
		Messages:  []gollm.Message{{Role: "user", Content: "Say hello in one word."}},
		MaxTokens: 32,
	})
	maybeSkipOnRateLimit(t, err)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if gollm.UpstreamClientFault(err) {
		t.Error("a successful call must not read as a client fault")
	}
}
