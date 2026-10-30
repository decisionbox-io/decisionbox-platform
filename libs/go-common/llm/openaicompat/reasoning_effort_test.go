package openaicompat

import (
	"encoding/json"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// An explicitly named level travels on the wire.
func TestBuildRequestBodyCarriesAnExplicitEffort(t *testing.T) {
	for _, level := range []string{gollm.ReasoningEffortLow, gollm.ReasoningEffortMedium, gollm.ReasoningEffortHigh} {
		body := BuildRequestBody("m", gollm.ChatRequest{ReasoningEffort: level})
		if body.ReasoningEffort != level {
			t.Errorf("reasoning_effort = %q, want %q", body.ReasoningEffort, level)
		}
	}
}

// Everything else omits the field.
//
// This wire is shared by every OpenAI-compatible backend, and OpenAI rejects
// reasoning_effort outright on its non-reasoning models — so injecting a
// deployment default here would turn a quality setting into a 400 for
// backends that never asked for one. The default is applied one hop later, by
// the provider that actually talks to the model and knows whether it takes an
// effort setting.
func TestBuildRequestBodyOmitsEffortUnlessNamed(t *testing.T) {
	for _, effort := range []string{
		gollm.ReasoningEffortDefault,
		gollm.ReasoningEffortOn,
		gollm.ReasoningEffortOff,
		"minimal",
		"nonsense",
	} {
		body := BuildRequestBody("m", gollm.ChatRequest{ReasoningEffort: effort})
		if body.ReasoningEffort != "" {
			t.Errorf("effort %q put %q on the wire, want it omitted", effort, body.ReasoningEffort)
		}
	}
}

// A request that names no effort must serialise byte-identically to before
// the field existed, so backends that reject unknown keys are unaffected.
func TestRequestWithoutEffortSerialisesWithoutTheField(t *testing.T) {
	body := BuildRequestBody("m", gollm.ChatRequest{
		Messages: []gollm.Message{{Role: "user", Content: "hi"}},
	})
	if body.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want empty", body.ReasoningEffort)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if contains(raw, "reasoning_effort") {
		t.Errorf("reasoning_effort appears in the serialised body: %s", raw)
	}
}
