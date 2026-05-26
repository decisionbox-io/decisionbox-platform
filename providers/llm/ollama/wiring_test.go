package ollama

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
)

// TestChat_NumCtxFromCatalog asserts that every Chat() request carries
// num_ctx from the registered catalog row. Without this the request
// relies on the server's OLLAMA_CONTEXT_LENGTH default, which silently
// truncates any prompt larger than 2-4 k tokens depending on version.
func TestChat_NumCtxFromCatalog(t *testing.T) {
	cases := []struct {
		model      string
		wantNumCtx int
	}{
		{"gemma4:31b-it-bf16", ctx256K},
		{"qwen2.5:7b", ctx128K},
		{"phi4", ctx16K},
		{"gemma2:9b", ctx8K},
		{"some-unknown:tag", ctx128K}, // falls back to DefaultMaxInputTokens
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			mock := &mockOllamaClient{
				chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
			}
			p := newMockOllamaProvider(mock, tc.model)
			if _, err := p.Chat(context.Background(), gollm.ChatRequest{
				Messages: []gollm.Message{{Role: "user", Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			got, ok := mock.lastChatReq.Options["num_ctx"]
			if !ok {
				t.Fatalf("num_ctx not set in request options")
			}
			gotInt, ok := got.(int)
			if !ok {
				t.Fatalf("num_ctx type = %T, want int", got)
			}
			if gotInt != tc.wantNumCtx {
				t.Errorf("num_ctx = %d, want %d", gotInt, tc.wantNumCtx)
			}
		})
	}
}

// TestChat_NumCtxCap covers the three branches of the cap interaction:
// cap below catalog wins, cap at-or-above catalog leaves catalog
// untouched, cap of zero behaves like unset.
func TestChat_NumCtxCap(t *testing.T) {
	const model = "gemma4:31b-it-bf16" // catalog: 256k
	cases := []struct {
		name string
		cap  int
		want int
	}{
		{"cap_below_catalog_clamps", 32768, 32768},
		{"cap_equal_catalog_no_change", ctx256K, ctx256K},
		{"cap_above_catalog_no_change", ctx256K * 2, ctx256K},
		{"cap_zero_no_change", 0, ctx256K},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockOllamaClient{
				chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
			}
			p := newMockOllamaProvider(mock, model)
			p.numCtxCap = tc.cap
			if _, err := p.Chat(context.Background(), gollm.ChatRequest{
				Messages: []gollm.Message{{Role: "user", Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			got, _ := mock.lastChatReq.Options["num_ctx"].(int)
			if got != tc.want {
				t.Errorf("num_ctx = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestChat_TruncateFalse asserts every request pins Truncate=false.
// truncate=true would let the server silently drop messages when the
// prompt exceeds num_ctx, masking budgeting bugs at the call-site.
func TestChat_TruncateFalse(t *testing.T) {
	mock := &mockOllamaClient{
		chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
	}
	p := newMockOllamaProvider(mock, "qwen2.5:7b")
	if _, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if mock.lastChatReq.Truncate == nil {
		t.Fatal("Truncate is nil, want explicit *false")
	}
	if *mock.lastChatReq.Truncate != false {
		t.Errorf("Truncate = %v, want false", *mock.lastChatReq.Truncate)
	}
}

// TestChat_ReasoningEffortMapping exercises every documented value of
// gollm.ReasoningEffort and asserts the resulting Think field on the
// outgoing request. Default and unknown values both omit Think.
func TestChat_ReasoningEffortMapping(t *testing.T) {
	type want struct {
		thinkSet bool
		value    any
	}
	cases := []struct {
		name   string
		effort string
		want   want
	}{
		{"default_omits", gollm.ReasoningEffortDefault, want{thinkSet: false}},
		{"off_false", gollm.ReasoningEffortOff, want{thinkSet: true, value: false}},
		{"on_true", gollm.ReasoningEffortOn, want{thinkSet: true, value: true}},
		{"low_string", gollm.ReasoningEffortLow, want{thinkSet: true, value: "low"}},
		{"medium_string", gollm.ReasoningEffortMedium, want{thinkSet: true, value: "medium"}},
		{"high_string", gollm.ReasoningEffortHigh, want{thinkSet: true, value: "high"}},
		{"unknown_omits", "bogus", want{thinkSet: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockOllamaClient{
				chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
			}
			p := newMockOllamaProvider(mock, "qwen2.5:7b")
			if _, err := p.Chat(context.Background(), gollm.ChatRequest{
				ReasoningEffort: tc.effort,
				Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
			}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			think := mock.lastChatReq.Think
			if tc.want.thinkSet {
				if think == nil {
					t.Fatal("Think is nil, want set")
				}
				if think.Value != tc.want.value {
					t.Errorf("Think.Value = %v (%T), want %v (%T)",
						think.Value, think.Value, tc.want.value, tc.want.value)
				}
			} else if think != nil {
				t.Errorf("Think = %+v, want nil", think)
			}
		})
	}
}

// TestChat_ReasoningRoundTrip asserts the model's hidden chain-of-
// thought (Message.Thinking) is surfaced on ChatResponse.Reasoning.
// Critically, it also asserts that an empty Content stays empty —
// callers expecting JSON must not see Reasoning fed in as a fallback.
func TestChat_ReasoningRoundTrip(t *testing.T) {
	t.Run("both_content_and_reasoning", func(t *testing.T) {
		mock := &mockOllamaClient{
			chatResp: ollamaapi.ChatResponse{
				Done:       true,
				DoneReason: "stop",
				Message: ollamaapi.Message{
					Role:     "assistant",
					Content:  "final answer",
					Thinking: "step 1 ... step 2 ... step 3",
				},
			},
		}
		p := newMockOllamaProvider(mock, "qwen2.5:7b")
		resp, err := p.Chat(context.Background(), gollm.ChatRequest{
			Messages: []gollm.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Content != "final answer" {
			t.Errorf("Content = %q", resp.Content)
		}
		if resp.Reasoning != "step 1 ... step 2 ... step 3" {
			t.Errorf("Reasoning = %q", resp.Reasoning)
		}
	})

	t.Run("empty_content_with_reasoning_stays_empty", func(t *testing.T) {
		mock := &mockOllamaClient{
			chatResp: ollamaapi.ChatResponse{
				Done:       true,
				DoneReason: "length",
				Message: ollamaapi.Message{
					Role:     "assistant",
					Content:  "",
					Thinking: "I was still thinking when the budget hit",
				},
			},
		}
		p := newMockOllamaProvider(mock, "qwen2.5:7b")
		resp, err := p.Chat(context.Background(), gollm.ChatRequest{
			Messages: []gollm.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Content != "" {
			t.Errorf("Content = %q, want empty (Reasoning must not be a fallback)", resp.Content)
		}
		if resp.Reasoning == "" {
			t.Errorf("Reasoning is empty, want round-tripped from Message.Thinking")
		}
	})

	t.Run("no_reasoning_returns_empty", func(t *testing.T) {
		mock := &mockOllamaClient{
			chatResp: ollamaapi.ChatResponse{
				Done:       true,
				DoneReason: "stop",
				Message: ollamaapi.Message{
					Role:    "assistant",
					Content: "no reasoning model, plain answer",
				},
			},
		}
		p := newMockOllamaProvider(mock, "qwen2.5:7b")
		resp, err := p.Chat(context.Background(), gollm.ChatRequest{
			Messages: []gollm.Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Reasoning != "" {
			t.Errorf("Reasoning = %q, want empty", resp.Reasoning)
		}
	})
}

// TestFactory_NumCtxCapParsing exercises every branch of the factory's
// num_ctx_cap parsing: missing key, empty string, valid int, negative,
// non-numeric. Negative or unparseable values fall back to zero (no
// cap), never error.
func TestFactory_NumCtxCapParsing(t *testing.T) {
	base := gollm.ProviderConfig{
		"host":  "http://localhost:11434",
		"model": "qwen2.5:7b",
	}
	cases := []struct {
		name    string
		capStr  string
		setKey  bool
		wantCap int
	}{
		{"missing_key", "", false, 0},
		{"empty_value", "", true, 0},
		{"valid_int", "32768", true, 32768},
		{"negative_clamped_zero", "-5", true, 0},
		{"non_numeric_fallback_zero", "lots", true, 0},
		{"zero_value", "0", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gollm.ProviderConfig{}
			for k, v := range base {
				cfg[k] = v
			}
			if tc.setKey {
				cfg["num_ctx_cap"] = tc.capStr
			}
			p, err := gollm.NewProvider("ollama", cfg)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			op, ok := p.(*OllamaProvider)
			if !ok {
				t.Fatalf("type = %T, want *OllamaProvider", p)
			}
			if op.numCtxCap != tc.wantCap {
				t.Errorf("numCtxCap = %d, want %d", op.numCtxCap, tc.wantCap)
			}
		})
	}
}

// TestFactory_NumCtxCapConfigField asserts the registered ProviderMeta
// surfaces num_ctx_cap as a config field so the dashboard renders an
// input for it. Catches an accidental removal that would silently hide
// the operator override from the UI.
func TestFactory_NumCtxCapConfigField(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("ollama provider not registered")
	}
	for _, f := range meta.ConfigFields {
		if f.Key == "num_ctx_cap" {
			if f.Required {
				t.Error("num_ctx_cap is Required, want optional (zero = use catalog)")
			}
			if f.Label == "" {
				t.Error("num_ctx_cap has no Label — dashboard would render without a header")
			}
			return
		}
	}
	t.Error("num_ctx_cap missing from ConfigFields")
}
