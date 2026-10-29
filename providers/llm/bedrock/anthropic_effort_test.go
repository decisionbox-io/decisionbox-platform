package bedrock

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// A reasoning model gets output_config.effort on the wire.
//
// Verified against Bedrock: the field is accepted and changes what the model
// spends — at `high` the same prompt returned 148 output tokens of which 75
// were thinking, against 53/6 with the field absent. Without it, every layer
// inherits whichever default the model release happens to carry.
func TestAnthropic_EffortOnTheWireForAReasoningModel(t *testing.T) {
	mock, p := capturingClient(nil)
	const model = "us.anthropic.claude-opus-5-5"
	if !gollm.IsReasoningModel(providerName, model) {
		t.Fatalf("catalog does not mark %s as a reasoning model — the premise of this test", model)
	}
	if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model:           model,
		Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
		ReasoningEffort: gollm.ReasoningEffortHigh,
	}); err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, mock.lastBody)
	oc, ok := body["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("no output_config on the request: %#v", body)
	}
	if oc["effort"] != gollm.ReasoningEffortHigh {
		t.Errorf("effort = %v, want high", oc["effort"])
	}
}

// A caller that names no effort still gets the deployment default, so the
// choice is ours and visible rather than the model release's.
func TestAnthropic_EffortDefaultsWhenTheCallerNamesNone(t *testing.T) {
	mock, p := capturingClient(nil)
	if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model:    "us.anthropic.claude-opus-5-5",
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, mock.lastBody)
	oc, ok := body["output_config"].(map[string]interface{})
	if !ok {
		t.Fatalf("no output_config on the request: %#v", body)
	}
	if oc["effort"] != gollm.DefaultReasoningEffort {
		t.Errorf("effort = %v, want the deployment default %q", oc["effort"], gollm.DefaultReasoningEffort)
	}
}

// A model the catalog does not mark as reasoning must see no output_config:
// the field is rejected by models that predate it, so emitting it
// unconditionally would break older deployments to configure newer ones.
func TestAnthropic_NoEffortForANonReasoningModel(t *testing.T) {
	mock, p := capturingClient(nil)
	const model = "anthropic.claude-opus-4-8"
	if gollm.IsReasoningModel(providerName, model) {
		t.Fatalf("catalog marks %s as reasoning — pick a different model for this test", model)
	}
	if _, err := p.chatAnthropic(context.Background(), gollm.ChatRequest{
		Model:           model,
		Messages:        []gollm.Message{{Role: "user", Content: "hi"}},
		ReasoningEffort: gollm.ReasoningEffortHigh,
	}); err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, mock.lastBody)
	if _, present := body["output_config"]; present {
		t.Errorf("output_config sent to a non-reasoning model: %#v", body["output_config"])
	}
}
