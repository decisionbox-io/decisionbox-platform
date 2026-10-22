package askserve

import (
	"strings"
	"testing"
)

// The JSON-text action's free-text key is "rationale". Claude Opus 5.5 and
// Sonnet 5.5 refuse (stop_reason "refusal", no content) a conversation
// that asks for a JSON field named "thinking".

func TestParseTurnAction_AcceptsRationaleKey(t *testing.T) {
	act, err := parseTurnAction(`{"rationale":"count first","query":"SELECT 1"}`)
	if err != nil {
		t.Fatalf("parseTurnAction: %v", err)
	}
	if act.Kind != actQuery || act.Thinking != "count first" {
		t.Errorf("kind = %v, thinking = %q", act.Kind, act.Thinking)
	}
}

func TestPrompt_NeverAsksForThinkingKey(t *testing.T) {
	single := &ProjectRuntime{Datasources: []DatasourceInfo{{ID: "wh_a"}}, PrimaryID: "wh_a"}
	pinned, _ := single.resolveTurnRouting("")
	multiRT := &ProjectRuntime{Datasources: []DatasourceInfo{{ID: "wh_a"}, {ID: "wh_b"}}, PrimaryID: "wh_a"}
	multi, _ := multiRT.resolveTurnRouting("")
	for name, p := range map[string]string{
		"single": buildSystemPrompt(single, pinned, Config{}, true, nil),
		"multi":  buildSystemPrompt(multiRT, multi, Config{}, true, nil),
	} {
		if strings.Contains(p, `"thinking"`) {
			t.Errorf("%s text prompt asks for a \"thinking\" key", name)
		}
		if !strings.Contains(p, `"rationale"`) {
			t.Errorf("%s text prompt should show the \"rationale\" key", name)
		}
	}
}
