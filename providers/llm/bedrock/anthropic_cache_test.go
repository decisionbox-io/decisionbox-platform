package bedrock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func decodeBody(t *testing.T, raw []byte) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return body
}

func multiTurn() []gollm.Message {
	return []gollm.Message{
		{Role: "user", Content: "step 1"},
		{Role: "assistant", Content: "action 1"},
		{Role: "user", Content: "result 1"},
	}
}

func TestAnthropic_PromptCacheBreakpoints(t *testing.T) {
	mock, p := capturingClient(nil)
	p.promptCache = true
	if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{Model: p.model, SystemPrompt: "sys", Messages: multiTurn()}); err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, mock.lastBody)
	sys, ok := body["system"].([]interface{})
	if !ok || len(sys) != 1 || sys[0].(map[string]interface{})["cache_control"] == nil {
		t.Errorf("system should be one block with a cache breakpoint: %#v", body["system"])
	}
	msgs := body["messages"].([]interface{})
	last := msgs[len(msgs)-1].(map[string]interface{})
	blocks, ok := last["content"].([]interface{})
	if !ok || blocks[len(blocks)-1].(map[string]interface{})["cache_control"] == nil {
		t.Errorf("last message should end in a cache breakpoint: %#v", last)
	}
	if first := msgs[0].(map[string]interface{}); first["content"] != "step 1" {
		t.Errorf("earlier messages must be unchanged: %#v", first)
	}
}

// A single-message request has no prefix to reuse, so only the system
// prompt (shared across calls) is marked — the message must not pay the
// cache-write premium.
func TestAnthropic_PromptCacheSingleMessageMarksSystemOnly(t *testing.T) {
	mock, p := capturingClient(nil)
	p.promptCache = true
	if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{Model: p.model, SystemPrompt: "sys",
		Messages: []gollm.Message{{Role: "user", Content: "only"}}}); err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, mock.lastBody)
	if _, ok := body["system"].([]interface{}); !ok {
		t.Errorf("system should carry the breakpoint: %#v", body["system"])
	}
	if c := body["messages"].([]interface{})[0].(map[string]interface{})["content"]; c != "only" {
		t.Errorf("single message must stay a plain string, got %#v", c)
	}
}

func TestAnthropic_PromptCacheOffKeepsHistoricalShape(t *testing.T) {
	mock, p := capturingClient(nil)
	p.promptCache = false
	if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{Model: p.model, SystemPrompt: "sys", Messages: multiTurn()}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mock.lastBody), "cache_control") {
		t.Errorf("caching off must send no breakpoints: %s", mock.lastBody)
	}
	if body := decodeBody(t, mock.lastBody); body["system"] != "sys" {
		t.Errorf("system should be the plain string: %#v", body["system"])
	}
}

// Claude Sonnet 5.5 rejects a forced tool_choice with a 400; the structured
// output request is relaxed to auto + an instruction. Haiku 5.5 accepts a
// forced choice and keeps it.
func TestAnthropic_ForcedToolChoiceRelaxedOnlyForRejectingModels(t *testing.T) {
	rf := &gollm.ResponseFormat{Name: "verdict", Schema: map[string]interface{}{"type": "object"}}
	cases := []struct {
		model    string
		wantType string
	}{
		{"global.anthropic.claude-sonnet-5-5", "auto"},
		{"us.anthropic.claude-opus-5-5", "auto"},
		{"global.anthropic.claude-haiku-5-5", "tool"},
		{"us.anthropic.claude-opus-4-8", "tool"},
	}
	for _, c := range cases {
		mock, p := capturingClient(nil)
		if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{Model: c.model, SystemPrompt: "sys",
			Messages: []gollm.Message{{Role: "user", Content: "x"}}, ResponseFormat: rf}); err != nil {
			t.Fatalf("%s: %v", c.model, err)
		}
		body := decodeBody(t, mock.lastBody)
		tc := body["tool_choice"].(map[string]interface{})
		if tc["type"] != c.wantType || tc["disable_parallel_tool_use"] != true {
			t.Errorf("%s: tool_choice = %#v, want type %q with parallel use disabled", c.model, tc, c.wantType)
		}
		if c.wantType == "auto" && !strings.Contains(string(mock.lastBody), `\"verdict\" tool`) {
			t.Errorf("%s: relaxed request should name the tool in the system prompt: %s", c.model, mock.lastBody)
		}
	}
}

func TestAnthropic_UsageReportsTotalPromptAndCacheSplit(t *testing.T) {
	_, p := capturingClient([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":40,"output_tokens":9,"cache_creation_input_tokens":200,"cache_read_input_tokens":5000}}`))
	resp, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{Model: p.model, Messages: []gollm.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := gollm.Usage{InputTokens: 5240, OutputTokens: 9, CacheReadTokens: 5000, CacheWriteTokens: 200}
	if resp.Usage != want {
		t.Errorf("usage = %+v, want %+v", resp.Usage, want)
	}
}

func TestCatalog_ClaudeOpus48AndClaude5Line(t *testing.T) {
	cases := []struct {
		id        string
		in, out   float64
		reasoning bool
		rejects   bool
	}{
		{"anthropic.claude-opus-4-8", 5, 25, false, false},
		{"us.anthropic.claude-opus-4-8", 5, 25, false, false},
		{"global.anthropic.claude-opus-5-5", 4, 20, true, true},
		{"global.anthropic.claude-sonnet-5-5", 2, 10, true, true},
		{"global.anthropic.claude-haiku-5-5", 0.10, 0.50, false, false},
		{"global.anthropic.claude-sonnet-5", 2, 10, false, false},
		{"global.anthropic.claude-opus-5", 5, 25, false, false},
	}
	for _, c := range cases {
		if got := gollm.GetMaxInputTokens(providerName, c.id); got != 1000000 {
			t.Errorf("%s: window = %d, want 1000000", c.id, got)
		}
		if got := gollm.GetMaxOutputTokens(providerName, c.id); got != 128000 {
			t.Errorf("%s: max output = %d, want 128000", c.id, got)
		}
		meta, _ := gollm.GetProviderMeta(providerName)
		pr, ok := meta.PricingFor(c.id)
		if !ok || pr.InputPerMillion != c.in || pr.OutputPerMillion != c.out {
			t.Errorf("%s: pricing = %+v (ok=%v), want %v/%v", c.id, pr, ok, c.in, c.out)
		}
		if got := gollm.IsReasoningModel(providerName, c.id); got != c.reasoning {
			t.Errorf("%s: reasoning = %v, want %v", c.id, got, c.reasoning)
		}
		if got := gollm.RejectsForcedToolChoice(providerName, c.id); got != c.rejects {
			t.Errorf("%s: rejects forced tool choice = %v, want %v", c.id, got, c.rejects)
		}
	}
}

func TestCatalog_ClaudeCachePrices(t *testing.T) {
	meta, _ := gollm.GetProviderMeta(providerName)
	cases := map[string][2]float64{ // cache read, cache write per 1M
		"us.anthropic.claude-opus-4-8":       {0.50, 6.25},
		"global.anthropic.claude-sonnet-5-5": {0.10, 2.50},
		"global.anthropic.claude-opus-5-5":   {0.20, 5.00},
		"global.anthropic.claude-haiku-5-5":  {0.01, 0.125},
		"us.anthropic.claude-sonnet-4-6":     {0.30, 3.75},
	}
	for id, want := range cases {
		pr, ok := meta.PricingFor(id)
		if !ok || !approx(pr.CacheReadPerMillion, want[0]) || !approx(pr.CacheWritePerMillion, want[1]) {
			t.Errorf("%s: cache read/write = %v/%v (ok=%v), want %v/%v", id, pr.CacheReadPerMillion, pr.CacheWritePerMillion, ok, want[0], want[1])
		}
	}
	// Haiku 5.5 bills prompts over 100K tokens at 5x.
	if pr, _ := meta.PricingFor("global.anthropic.claude-haiku-5-5"); pr.LongContextThreshold != 100000 || pr.LongContextMultiplier != 5 {
		t.Errorf("haiku 5.5 long-prompt tier = %d / %v, want 100000 / 5", pr.LongContextThreshold, pr.LongContextMultiplier)
	}
	// Non-Claude models have no prompt-cache prices.
	if pr, _ := meta.PricingFor("qwen.qwen3-next-80b-a3b"); pr.CacheReadPerMillion != 0 || pr.CacheWritePerMillion != 0 {
		t.Errorf("qwen should carry no cache prices: %+v", pr)
	}
}

func approx(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
