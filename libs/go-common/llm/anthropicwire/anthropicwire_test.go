package anthropicwire

import (
	"encoding/json"
	"reflect"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestSystemField(t *testing.T) {
	if got := SystemField("sys", false); got != "sys" {
		t.Errorf("caching off: got %#v, want the plain string", got)
	}
	got, ok := SystemField("sys", true).([]map[string]interface{})
	if !ok || len(got) != 1 {
		t.Fatalf("caching on: got %#v, want one text block", SystemField("sys", true))
	}
	if got[0]["type"] != "text" || got[0]["text"] != "sys" || !reflect.DeepEqual(got[0]["cache_control"], map[string]interface{}{"type": "ephemeral"}) {
		t.Errorf("block = %#v", got[0])
	}
}

func TestCacheConversationTail(t *testing.T) {
	if CacheConversationTail(1) {
		t.Error("a single-message request has no prefix to reuse; it must not pay the cache-write premium")
	}
	if !CacheConversationTail(2) {
		t.Error("a multi-turn request re-sends its prefix next call; it must be cached")
	}
}

func TestMarkLastMessage(t *testing.T) {
	msgs := []map[string]interface{}{
		{"role": "user", "content": "first"},
		{"role": "assistant", "content": "reply"},
		{"role": "user", "content": "second"},
	}
	MarkLastMessage(msgs)
	if msgs[0]["content"] != "first" || msgs[1]["content"] != "reply" {
		t.Error("only the last message may carry the breakpoint")
	}
	blocks, ok := msgs[2]["content"].([]map[string]interface{})
	if !ok || len(blocks) != 1 || blocks[0]["text"] != "second" || blocks[0]["cache_control"] == nil {
		t.Fatalf("last message = %#v", msgs[2])
	}

	withBlocks := []map[string]interface{}{{"role": "user", "content": []map[string]interface{}{
		{"type": "tool_result", "tool_use_id": "a", "content": "x"},
		{"type": "text", "text": "y"},
	}}}
	MarkLastMessage(withBlocks)
	bl := withBlocks[0]["content"].([]map[string]interface{})
	if bl[0]["cache_control"] != nil || bl[1]["cache_control"] == nil {
		t.Errorf("breakpoint must sit on the final block only: %#v", bl)
	}

	empty := []map[string]interface{}{{"role": "user", "content": ""}}
	MarkLastMessage(empty)
	if empty[0]["content"] != "" {
		t.Error("empty content must stay empty")
	}
	MarkLastMessage(nil) // must not panic
}

func TestToolChoice(t *testing.T) {
	cases := []struct {
		choice          string
		disableParallel bool
		want            map[string]interface{}
	}{
		{"", false, nil},
		{"auto", false, nil},
		{"auto", true, map[string]interface{}{"type": "auto", "disable_parallel_tool_use": true}},
		{"any", false, map[string]interface{}{"type": "any"}},
		{"required", true, map[string]interface{}{"type": "any", "disable_parallel_tool_use": true}},
		{"none", true, map[string]interface{}{"type": "none"}},
		{"emit", false, map[string]interface{}{"type": "tool", "name": "emit"}},
		{"emit", true, map[string]interface{}{"type": "tool", "name": "emit", "disable_parallel_tool_use": true}},
	}
	for _, c := range cases {
		if got := ToolChoice(c.choice, c.disableParallel); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ToolChoice(%q, %v) = %#v, want %#v", c.choice, c.disableParallel, got, c.want)
		}
	}
}

// Anthropic's input_tokens counts only the uncached tail of the prompt; the
// neutral Usage reports the total so window and budget arithmetic is the
// same with or without caching.
func TestUsageToLLM(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(`{"input_tokens":50,"output_tokens":20,"cache_creation_input_tokens":300,"cache_read_input_tokens":4000}`), &u); err != nil {
		t.Fatal(err)
	}
	want := gollm.Usage{InputTokens: 4350, OutputTokens: 20, CacheReadTokens: 4000, CacheWriteTokens: 300}
	if got := u.ToLLM(); got != want {
		t.Errorf("ToLLM() = %+v, want %+v", got, want)
	}
}
