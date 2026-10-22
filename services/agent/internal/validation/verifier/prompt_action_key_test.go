package verifier

import (
	"strings"
	"testing"
)

// TestPrompts_NeverMentionThinkingKey: Claude Opus 5.5 refuses (stop_reason
// "refusal", no content) a conversation whose prompt shows a JSON field
// named "thinking" — even as a counter-example — so neither validation
// prompt may contain one.
func TestPrompts_NeverMentionThinkingKey(t *testing.T) {
	for _, name := range []string{"prompts/verifier.md", "prompts/refuter.md"} {
		data, err := promptFS.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if strings.Contains(string(data), `"thinking"`) {
			t.Errorf("%s shows a \"thinking\" JSON key", name)
		}
	}
}
