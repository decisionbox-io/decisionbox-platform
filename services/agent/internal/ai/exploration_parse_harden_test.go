package ai

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func actionValue(a *ExplorationAction) string {
	return a.Query + "|" + a.SearchTables + "|" + strings.Join(a.LookupSchema, ",")
}

// TestExtractJSON_ReasoningAndActionCorpus is the issue #341 acceptance corpus:
// every shape a reasoning / open model produces must still select the REAL
// action, not a reasoning fragment.
func TestExtractJSON_ReasoningAndActionCorpus(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantAction string
		wantValue  string // substring that must appear in the selected action
	}{
		{"think_before", "<think>let me reason, consider the set {a, b</think>\n{\"query\":\"SELECT 1\"}", "query_data", "SELECT 1"},
		{"thinking_variant", "<thinking>reasoning here</thinking>{\"query\":\"SELECT 2\"}", "query_data", "SELECT 2"},
		{"action_inside_think", "<think>{\"query\":\"SELECT 3\"}</think>\nOkay, done thinking.", "query_data", "SELECT 3"},
		{"leading_prose", "Here is my next action:\n{\"query\":\"SELECT 4\"}", "query_data", "SELECT 4"},
		{"markdown_fence", "```json\n{\"query\":\"SELECT 5\"}\n```", "query_data", "SELECT 5"},
		{"leading_planning_json", "{\"plan\":\"first inspect users\"}\n{\"query\":\"SELECT 6\"}", "query_data", "SELECT 6"},
		{"trailing_think_fragment", "{\"search_tables\":\"users tables\"}\n{\"thought\":\"maybe reconsider\"}", "search_tables", "users tables"},
		{"lookup_schema_with_noise", "<think>need schemas {unbalanced</think>\n{\"lookup_schema\":[\"ds.t1\",\"ds.t2\"]}", "lookup_schema", "ds.t1"},
		{"tool_envelope_in_prose", "Sure, running it.\n{\"name\":\"query_data\",\"input\":{\"query\":\"SELECT 8\"}}", "query_data", "SELECT 8"},
		{"single_element_array", "[{\"query\":\"SELECT 9\"}]", "query_data", "SELECT 9"},
		{"unbalanced_preamble", "oops here is a stray { brace\n{\"query\":\"SELECT 10\"}", "query_data", "SELECT 10"},
		{"draft_in_think_real_after", "<think>{\"search_tables\":\"draft\"}</think>\n{\"search_tables\":\"real query\"}", "search_tables", "real query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, err := ParseAction(tc.input, nil)
			if err != nil {
				t.Fatalf("ParseAction error: %v (extractJSON=%q)", err, extractJSON(tc.input))
			}
			if action.Action != tc.wantAction {
				t.Errorf("Action = %q, want %q", action.Action, tc.wantAction)
			}
			if !strings.Contains(actionValue(action), tc.wantValue) {
				t.Errorf("selected action %q does not contain %q", actionValue(action), tc.wantValue)
			}
		})
	}
}

// TestExtractJSON_NoActionCorpus: genuine no-action inputs must NOT be coerced
// into a false action — they error so the run re-prompts instead of silently
// "completing".
func TestExtractJSON_NoActionCorpus(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"prose_only", "I have no idea what to do next."},
		{"thinking_only_object", "{\"thinking\":\"hmm, not sure\"}"},
		{"truncated_unclosed_think", "<think>reasoning that never closes and has a {partial fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseAction(tc.input, nil); err == nil {
				t.Errorf("want error for no-action input, got none (extractJSON=%q)", extractJSON(tc.input))
			}
		})
	}
}

func TestJSONHasActionKey_Widened(t *testing.T) {
	truthy := []string{
		`{"query":"SELECT 1"}`,
		`{"done":true}`,
		`{"action":"query_data"}`,
		`{"lookup_schema":["a.b"]}`,
		`{"search_tables":"x"}`,
		`{"name":"query_data","input":{"query":"q"}}`,
		`{"name":"search_tables","input":{"query":"x"}}`,
	}
	for _, s := range truthy {
		if !jsonHasActionKey(s) {
			t.Errorf("jsonHasActionKey(%s) = false, want true", s)
		}
	}
	falsy := []string{
		`{"thinking":"hmm"}`,
		`{"plan":"do X"}`,
		`{"name":"not_a_tool","input":{}}`, // unknown tool name
		`{"name":"query_data"}`,            // envelope without input
		`not json`,
	}
	for _, s := range falsy {
		if jsonHasActionKey(s) {
			t.Errorf("jsonHasActionKey(%s) = true, want false", s)
		}
	}
}

func TestFindBalancedJSONObjects_SkipsUnbalancedPrefix(t *testing.T) {
	// A stray unbalanced '{' before a valid object must not abort the scan.
	got := findBalancedJSONObjects("prefix { never closes ... {\"query\":\"ok\"}")
	if len(got) != 1 || !strings.Contains(got[0], `"query":"ok"`) {
		t.Fatalf("got %v, want the trailing balanced object", got)
	}
	// A single trailing unbalanced object still yields nothing.
	if out := findBalancedJSONObjects("text { unbalanced only"); len(out) != 0 {
		t.Errorf("unbalanced-only should yield no objects, got %v", out)
	}
}

func TestStripReasoningBlocks(t *testing.T) {
	if got := stripReasoningBlocks("a<think>x</think>b"); strings.Contains(got, "x") || !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("matched pair not stripped cleanly: %q", got)
	}
	if got := stripReasoningBlocks("keep<think>drop to end"); strings.Contains(got, "drop") || !strings.Contains(got, "keep") {
		t.Errorf("dangling block not stripped to end: %q", got)
	}
	if got := stripReasoningBlocks("A<THINK>x</THINK>B<thinking>y</thinking>C"); strings.Contains(got, "x") || strings.Contains(got, "y") {
		t.Errorf("case-insensitive / multiple blocks not stripped: %q", got)
	}
}

// --- structured output + token cap on the exploration call (issue #341) ---

func driveOneStep(t *testing.T, engine *ExplorationEngine, providerName string) {
	t.Helper()
	if providerName != "" {
		engine.client.SetProvenance("p", "r", providerName)
	}
	conv := NewConversation(ConversationOptions{SystemPrompt: "sys", MaxMessages: 100})
	conv.AddUserMessage("explore")
	if _, _, _, err := engine.runStepWithRetry(context.Background(), conv, 1); err != nil {
		t.Fatalf("runStepWithRetry: %v", err)
	}
}

func TestExploration_TokenCapFromCatalog(t *testing.T) {
	const pn = "test-explore-maxtok"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
		gollm.ProviderMeta{ID: pn, Name: "explore maxtok",
			Models: []gollm.ModelEntry{{ID: "mock-model", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 12321}}})

	engine, provider := buildTestEngine(t, ExplorationEngineOptions{MaxSteps: 3}, []string{`{"query":"SELECT 1 FROM test_dataset.users"}`})
	driveOneStep(t, engine, pn)
	if got := provider.Calls[0].Request.MaxTokens; got != 12321 {
		t.Errorf("MaxTokens = %d, want 12321 (catalog cap, not the old hard-coded 4096)", got)
	}
}

func TestExploration_StructuredOutputGating(t *testing.T) {
	const pn = "test-explore-structured"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
		gollm.ProviderMeta{ID: pn, Name: "explore structured", SupportsStructuredOutput: true,
			Models: []gollm.ModelEntry{{ID: "mock-model", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 8000}}})

	// Supported provider → ResponseFormat attached with the action schema.
	engine, provider := buildTestEngine(t, ExplorationEngineOptions{MaxSteps: 3}, []string{`{"query":"SELECT 1 FROM test_dataset.users"}`})
	driveOneStep(t, engine, pn)
	if rf := provider.Calls[0].Request.ResponseFormat; rf == nil {
		t.Error("structured-output provider must carry ResponseFormat on the exploration call")
	} else if rf.Name != "exploration_action" {
		t.Errorf("ResponseFormat.Name = %q, want exploration_action", rf.Name)
	}

	// Unsupported provider (no provenance) → no format.
	engine2, provider2 := buildTestEngine(t, ExplorationEngineOptions{MaxSteps: 3}, []string{`{"query":"SELECT 1 FROM test_dataset.users"}`})
	driveOneStep(t, engine2, "")
	if provider2.Calls[0].Request.ResponseFormat != nil {
		t.Error("provider without structured-output support must not carry ResponseFormat")
	}
}
