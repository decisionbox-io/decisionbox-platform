package ai

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// Pre-fix the LLM dialog fields (LLMRequest / LLMResponse / TokensIn /
// TokensOut / DurationMs) on every ExplorationStep emitted by Explore
// were left empty, gutting the row's "Complete LLM dialog (for fine-
// tuning)" purpose. These tests pin the populated shape so a future
// refactor can't silently drop them again.

func TestExploration_Step_PopulatesLLMDialog(t *testing.T) {
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"thinking": "want a count", "query": "SELECT COUNT(*) FROM sessions"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 1234, OutputTokens: 42},
	}
	client, _ := New(provider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})

	const initialPrompt = "Explore the demo dataset for retention patterns"
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: exec,
		MaxSteps: 1, // run exactly one step so we can pin the dialog of step 1
		Dataset:  "test_dataset",
	})

	res, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "p",
		Dataset:       "test_dataset",
		InitialPrompt: initialPrompt,
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	if len(res.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(res.Steps))
	}

	step := res.Steps[0]

	if step.LLMResponse == "" {
		t.Error("LLMResponse must be populated; got empty")
	}
	if !strings.Contains(step.LLMResponse, "SELECT COUNT(*) FROM sessions") {
		t.Errorf("LLMResponse should contain the model's reply verbatim; got %q", step.LLMResponse)
	}

	if step.LLMRequest == "" {
		t.Fatal("LLMRequest must be populated; got empty")
	}
	// The snapshot must include the system prompt and at least the
	// initial user message — these are the new turn's input as the
	// model saw it.
	if !strings.Contains(step.LLMRequest, "[system]") {
		t.Errorf("LLMRequest snapshot should include the system prompt header; got %q", step.LLMRequest[:min(len(step.LLMRequest), 200)])
	}
	if !strings.Contains(step.LLMRequest, "[user]") {
		t.Errorf("LLMRequest snapshot should include the user-message header; got %q", step.LLMRequest[:min(len(step.LLMRequest), 200)])
	}

	if step.TokensIn != 1234 {
		t.Errorf("TokensIn = %d, want 1234", step.TokensIn)
	}
	if step.TokensOut != 42 {
		t.Errorf("TokensOut = %d, want 42", step.TokensOut)
	}
	if step.DurationMs < 0 {
		t.Errorf("DurationMs should be set (>=0); got %d", step.DurationMs)
	}
}

func TestExploration_RejectedCompletion_AlsoCarriesDialog(t *testing.T) {
	// MinSteps rejection branch — also the only path where the rejected
	// completion gets recorded as its own row. That row must keep the
	// dialog so the "rejected premature completion" entry is auditable.
	provider := testutil.NewMockLLMProvider()
	provider.DefaultResponse = &gollm.ChatResponse{
		Content:    `{"done": true, "summary": "early stop"}`,
		Model:      "mock-model",
		StopReason: "end_turn",
		Usage:      gollm.Usage{InputTokens: 10, OutputTokens: 20},
	}
	client, _ := New(provider, "mock-model")
	wh := testutil.NewMockWarehouseProvider("test_dataset")
	exec := queryexec.NewQueryExecutor(queryexec.QueryExecutorOptions{Warehouse: wh, MaxRetries: 1})

	engine := NewExplorationEngine(ExplorationEngineOptions{
		Client:   client,
		Executor: exec,
		MaxSteps: 2,
		MinSteps: 5, // force rejection
		Dataset:  "test_dataset",
	})

	res, err := engine.Explore(context.Background(), ExplorationContext{
		ProjectID:     "p",
		Dataset:       "test_dataset",
		InitialPrompt: "explore",
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	if len(res.Steps) == 0 {
		t.Fatal("expected at least the rejected step to be recorded")
	}
	rejected := res.Steps[0]
	if rejected.Action != "complete_rejected" {
		t.Fatalf("step[0].Action = %q, want complete_rejected", rejected.Action)
	}
	if rejected.LLMResponse == "" || rejected.LLMRequest == "" {
		t.Errorf("rejected-completion row must carry the dialog; got request_empty=%v response_empty=%v",
			rejected.LLMRequest == "", rejected.LLMResponse == "")
	}
}

func TestSnapshotConversation_RendersAllRoles(t *testing.T) {
	out := snapshotConversation("you are a helpful explorer", []gollm.Message{
		{Role: "user", Content: "first user msg"},
		{Role: "assistant", Content: "first assistant reply"},
		{Role: "user", Content: "follow-up"},
	})
	for _, want := range []string{
		"[system]\nyou are a helpful explorer",
		"[user]\nfirst user msg",
		"[assistant]\nfirst assistant reply",
		"[user]\nfollow-up",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot missing %q; got:\n%s", want, out)
		}
	}
}

func TestSnapshotConversation_NoSystemPrompt(t *testing.T) {
	out := snapshotConversation("", []gollm.Message{{Role: "user", Content: "hi"}})
	if strings.Contains(out, "[system]") {
		t.Errorf("empty system prompt should be omitted; got %q", out)
	}
	if !strings.Contains(out, "[user]\nhi") {
		t.Errorf("user message missing; got %q", out)
	}
}
