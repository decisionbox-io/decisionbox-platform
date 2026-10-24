package bedrock

import (
	"context"
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// A safety refusal comes back as a 200 with no content blocks. It must reach
// the caller as gollm.ErrModelRefused, not as an empty reply the caller then
// fails to parse ("unexpected end of JSON input").
func TestAnthropic_RefusalIsAnError(t *testing.T) {
	_, p := capturingClient([]byte(`{"content":[],"stop_reason":"refusal","usage":{"input_tokens":120,"output_tokens":0}}`))
	for _, rf := range []*gollm.ResponseFormat{nil, {Name: "x", Schema: map[string]interface{}{"type": "object"}}} {
		resp, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{Model: p.model,
			Messages: []gollm.Message{{Role: "user", Content: "hi"}}, ResponseFormat: rf})
		if !errors.Is(err, gollm.ErrModelRefused) {
			t.Fatalf("structured=%v: err = %v (resp %+v), want ErrModelRefused", rf != nil, err, resp)
		}
	}
}
