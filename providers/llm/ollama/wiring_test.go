package ollama

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	ollamaapi "github.com/ollama/ollama/api"
)

// TestChat_NumCtxOptIn covers both branches of the per-request
// num_ctx forwarding: when the operator left num_ctx unset (zero),
// the request must NOT carry num_ctx so the server's
// OLLAMA_CONTEXT_LENGTH default applies and a small-VRAM deployment
// is not forced into a catalog-sized KV-cache allocation. When the
// operator set a positive value, that exact value is forwarded.
func TestChat_NumCtxOptIn(t *testing.T) {
	t.Run("unset_omits_num_ctx", func(t *testing.T) {
		mock := &mockOllamaClient{
			chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
		}
		p := newMockOllamaProvider(mock, "gemma4:31b-it-bf16")
		// p.numCtx defaults to zero — operator did not opt in.
		if _, err := p.Chat(context.Background(), gollm.ChatRequest{
			Messages: []gollm.Message{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if _, present := mock.lastChatReq.Options["num_ctx"]; present {
			t.Errorf("num_ctx present (=%v) when operator did not opt in — "+
				"would force a catalog-sized KV-cache allocation",
				mock.lastChatReq.Options["num_ctx"])
		}
	})
	t.Run("operator_value_forwarded", func(t *testing.T) {
		mock := &mockOllamaClient{
			chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
		}
		p := newMockOllamaProvider(mock, "gemma4:31b-it-bf16")
		p.numCtx = 32768
		if _, err := p.Chat(context.Background(), gollm.ChatRequest{
			Messages: []gollm.Message{{Role: "user", Content: "hi"}},
		}); err != nil {
			t.Fatalf("Chat: %v", err)
		}
		got, ok := mock.lastChatReq.Options["num_ctx"]
		if !ok {
			t.Fatal("num_ctx missing from request options")
		}
		gotInt, ok := got.(int)
		if !ok {
			t.Fatalf("num_ctx type = %T, want int", got)
		}
		if gotInt != 32768 {
			t.Errorf("num_ctx = %d, want 32768", gotInt)
		}
	})
}

// TestFactory_NumCtxParsing exercises every branch of the factory's
// num_ctx cfg parsing — missing, empty, valid, negative, non-numeric,
// zero. Negative and unparseable values clamp to zero (no-op) rather
// than erroring so a typo never breaks startup.
func TestFactory_NumCtxParsing(t *testing.T) {
	base := gollm.ProviderConfig{
		"host":  "http://localhost:11434",
		"model": "qwen2.5:7b",
	}
	cases := []struct {
		name    string
		ctxStr  string
		setKey  bool
		wantCtx int
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
				cfg["num_ctx"] = tc.ctxStr
			}
			p, err := gollm.NewProvider("ollama", cfg)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			op, ok := p.(*OllamaProvider)
			if !ok {
				t.Fatalf("type = %T, want *OllamaProvider", p)
			}
			if op.numCtx != tc.wantCtx {
				t.Errorf("numCtx = %d, want %d", op.numCtx, tc.wantCtx)
			}
		})
	}
}

// TestFactory_NumCtxConfigField asserts the registered ProviderMeta
// surfaces num_ctx as a config field so the dashboard renders an
// input for it. Catches an accidental removal that would silently
// hide the operator override from the UI.
func TestFactory_NumCtxConfigField(t *testing.T) {
	meta, ok := gollm.GetProviderMeta("ollama")
	if !ok {
		t.Fatal("ollama provider not registered")
	}
	for _, f := range meta.ConfigFields {
		if f.Key == "num_ctx" {
			if f.Required {
				t.Error("num_ctx is Required, want optional (zero = use server default)")
			}
			if f.Label == "" {
				t.Error("num_ctx has no Label — dashboard would render without a header")
			}
			return
		}
	}
	t.Error("num_ctx missing from ConfigFields")
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

// TestChat_ReasoningEffortMapping_ReasoningModel exercises every
// documented value of gollm.ReasoningEffort against a reasoning-
// capable model (gemma4:31b-it-bf16, catalog row Reasoning:true) and
// asserts the resulting Think field on the outgoing request.
func TestChat_ReasoningEffortMapping_ReasoningModel(t *testing.T) {
	const reasoningModel = "gemma4:31b-it-bf16"
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
			p := newMockOllamaProvider(mock, reasoningModel)
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

// TestChat_ReasoningEffortMapping_NonReasoningModel guards the
// non-reasoning gate. Ollama returns HTTP 400
// "<model> does not support thinking" for Think.Bool()==true on
// non-reasoning models — including the effort-string values
// "low"/"medium"/"high". The provider must omit Think for those
// values when the catalog row has Reasoning:false. "off" still
// serialises because think=false is harmless on every model.
func TestChat_ReasoningEffortMapping_NonReasoningModel(t *testing.T) {
	const nonReasoningModel = "qwen2.5:7b"
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
		{"off_false_still_serialises", gollm.ReasoningEffortOff, want{thinkSet: true, value: false}},
		{"on_omits_on_non_reasoning", gollm.ReasoningEffortOn, want{thinkSet: false}},
		{"low_omits_on_non_reasoning", gollm.ReasoningEffortLow, want{thinkSet: false}},
		{"medium_omits_on_non_reasoning", gollm.ReasoningEffortMedium, want{thinkSet: false}},
		{"high_omits_on_non_reasoning", gollm.ReasoningEffortHigh, want{thinkSet: false}},
		{"unknown_omits", "bogus", want{thinkSet: false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockOllamaClient{
				chatResp: ollamaapi.ChatResponse{Done: true, DoneReason: "stop"},
			}
			p := newMockOllamaProvider(mock, nonReasoningModel)
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
				t.Errorf("Think = %+v, want nil (non-reasoning model would 400)", think)
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

// TestCatalogReasoningRowsMarked guards that every catalog row whose
// MaxOutputTokens was raised to the reasoning-friendly cap is also
// flagged Reasoning:true. A future edit that bumps the cap without
// the flag would let "on"/"low"/"medium"/"high" reach a non-reasoning
// model and surface as a Chat-time 400. Conversely, this also pins
// the rows that must NOT be flagged (the gpt-oss row is a deliberate
// hold-out until its thinking capability is verified end-to-end).
func TestCatalogReasoningRowsMarked(t *testing.T) {
	wantReasoning := map[string]bool{
		// Confirmed reasoning families.
		"gemma4":       true,
		"gemma4:31b":   true,
		"gemma3":       true,
		"deepseek-r1":  true,
		"qwen3":        true,
		// Hold-outs — capability not yet verified end-to-end.
		"gpt-oss":      false,
		"qwen3-coder":  false,
		// Sanity checks on definitively non-reasoning rows.
		"qwen2.5":      false,
		"llama3.1":     false,
		"phi4":         false,
		"mistral":      false,
	}
	gotByID := map[string]bool{}
	for _, e := range buildOllamaCatalog() {
		gotByID[e.ID] = e.Reasoning
	}
	for id, want := range wantReasoning {
		got, ok := gotByID[id]
		if !ok {
			t.Errorf("catalog row %q missing", id)
			continue
		}
		if got != want {
			t.Errorf("catalog row %q Reasoning = %v, want %v", id, got, want)
		}
	}
}

// TestIsReasoningModel_RegistryLookup exercises gollm.IsReasoningModel
// through the registered Ollama catalog so a registry refactor that
// breaks alias resolution doesn't silently let "on" reach
// non-reasoning models. Coverage includes every documented size on
// the Ollama library for each reasoning family — a user-pulled tag
// that's missing from the alias list would fall through to
// Reasoning:false and silently strip Think for ReasoningEffort=on /
// low / medium / high.
func TestIsReasoningModel_RegistryLookup(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Canonical IDs.
		{"gemma4:31b", true},
		{"gemma4", true},
		{"gemma3", true},
		{"deepseek-r1", true},
		{"qwen3", true},
		{"qwen2.5", false},
		// Gemma 4 small-tier aliases.
		{"gemma4:e2b", true},
		{"gemma4:e4b", true},
		// Gemma 4 26B/31B aliases incl. quants.
		{"gemma4:31b-it-bf16", true},
		{"gemma4:31b-it-q4_K_M", true},
		{"gemma4:26b-it-q8_0", true},
		// Gemma 3 every size on the Ollama library.
		{"gemma3:1b", true},
		{"gemma3:4b", true},
		{"gemma3:12b", true},
		{"gemma3:27b", true},
		// DeepSeek R1 every size on the Ollama library.
		{"deepseek-r1:1.5b", true},
		{"deepseek-r1:7b", true},
		{"deepseek-r1:8b", true},
		{"deepseek-r1:14b", true},
		{"deepseek-r1:32b", true},
		{"deepseek-r1:70b", true},
		{"deepseek-r1:671b", true},
		// Qwen 3 every size on the Ollama library, including the MoE.
		{"qwen3:0.6b", true},
		{"qwen3:1.7b", true},
		{"qwen3:4b", true},
		{"qwen3:8b", true},
		{"qwen3:14b", true},
		{"qwen3:30b-a3b", true},
		{"qwen3:32b", true},
		{"qwen3:235b", true},
		{"qwen3:235b-a22b", true},
		// Unknown models / non-reasoning rows / unmarked rows.
		{"totally-unknown:1b", false},
		{"qwen3-coder:30b", false},
		{"gpt-oss:20b", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := gollm.IsReasoningModel("ollama", tc.model); got != tc.want {
				t.Errorf("IsReasoningModel(ollama, %q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}
