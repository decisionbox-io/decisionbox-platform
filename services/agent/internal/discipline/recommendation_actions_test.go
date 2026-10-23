package discipline

import (
	"strings"
	"testing"
)

// What these pin, from a live run on the default Bedrock model:
//
// Eight recommendations shipped with an empty `actions` array, their steps
// written as a numbered list inside `description` instead. Two runs earlier the
// same model filled `actions` twenty-seven times.
//
// The prompt had told it to do that. The structured-markdown rule listed "the
// steps or actions" among the things to write as a list in `description`, and no
// rule anywhere asked for the `actions` array at all. The schema asks, and on
// this wire the schema cannot make it so: a response format becomes a forced
// tool's input schema, validated best-effort, not a grammar that makes the
// omission unrepresentable. So the rules were the only thing asking, and they
// asked for the opposite.
//
// Measured against the model before and after, on the same captured prompt:
// without the rule below, 0 of 8 recommendations carried actions; with it, 8 of
// 8 twice over, 29 and 32 actions, and more figure references than before rather
// than fewer.

// markdownRuleBlock returns the structured-markdown rule's own text.
func markdownRuleBlock(t *testing.T) string {
	t.Helper()
	const head = "M. STRUCTURED MARKDOWN DESCRIPTION"
	rules := RecommendationsRules()
	i := strings.Index(rules, head)
	if i < 0 {
		t.Fatal("the structured-markdown rule is gone; this test reads it to check what it " +
			"tells the model to put in the description")
	}
	rest := rules[i+len(head):]
	// To the next rule heading, or the end.
	if j := strings.Index(rest, "\n\n"); j >= 0 {
		for j >= 0 && j+2 < len(rest) && rest[j+2] == ' ' {
			k := strings.Index(rest[j+2:], "\n\n")
			if k < 0 {
				break
			}
			j += 2 + k
		}
		rest = rest[:j]
	}
	return rest
}

func TestTheRulesAskForTheActionsArray(t *testing.T) {
	rules := RecommendationsRules()

	if !strings.Contains(rules, "`actions` is an array") {
		t.Error("no rule tells the model that `actions` is an array it must emit; the schema " +
			"asks, and on a wire where the schema is advisory the rules are the only ask")
	}
	if !strings.Contains(rules, "at least one") {
		t.Error("the rule does not say every recommendation carries at least one step")
	}
}

func TestTheMarkdownRuleDoesNotPutTheStepsInTheDescription(t *testing.T) {
	// The description rule governs one field. Naming another field's content
	// among the things to write there is how `actions` came to be empty while
	// its content shipped as prose.
	block := markdownRuleBlock(t)

	for _, forbidden := range []string{"actions", "the steps"} {
		if strings.Contains(block, forbidden) {
			t.Errorf("the structured-markdown rule still offers %q as description content, "+
				"which competes with the rule that asks for the array:\n%s", forbidden, block)
		}
	}
}
