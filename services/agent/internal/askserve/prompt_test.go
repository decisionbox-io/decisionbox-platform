package askserve

import (
	"strings"
	"testing"
)

func TestPrompt_PermitsClarifyWhenAToolAsksForConfirmation(t *testing.T) {
	// A write tool can need the user to agree to something before it does the
	// work. Every place that describes clarify used to say it was only for a
	// question that cannot be turned into a query — so the model would not use
	// it, and a tool that asks for confirmation had no way to put the question.
	// The native-tools prompt is what a provider with real tool-calling gets,
	// which is every deployment that has one — so it is the one that matters.
	tools := buildSystemPromptForTools(&ProjectRuntime{}, turnRouting{}, Config{}, false, true, nil)
	if !strings.Contains(tools, "use clarify to ask; do not substitute a different tool") {
		t.Error("the write-tool section does not say how to put a question to the user")
	}
	if !strings.Contains(tools, "asked you to confirm something with the user first") {
		t.Error("the tools grounding rule still forbids clarify outright")
	}
	// And the JSON-action prompt, for providers without tool-calling.
	p := buildSystemPrompt(&ProjectRuntime{}, turnRouting{}, Config{}, false, nil)
	if !strings.Contains(p, "asks you to put something to the user before continuing") {
		t.Error("the clarify action does not mention confirming on a tool's behalf")
	}
	if !strings.Contains(p, "asked you to confirm something with the user first") {
		t.Error("the grounding rule still forbids clarify outright")
	}
	if !strings.Contains(groundingNudge, "asked you to confirm something with the user first") {
		t.Error("the grounding nudge still forbids clarify outright")
	}
	// The allowance is conditional, not an invitation: the default remains that
	// an answerable question gets answered.
	if !strings.Contains(p, "too ambiguous to answer") {
		t.Error("clarify should still be described as the ambiguity terminal first")
	}
}
