package ai

import (
	"context"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// TestRunStepWithRetry_NeverEmptyAssistantTurn covers the robustness guard that
// keeps the exploration retry valid on strict providers. A reasoning model can
// return an empty Content (its output went to the reasoning channel); on the
// parse-retry the conversation must NOT carry an empty assistant message —
// Moonshot/Kimi (via LiteLLM) reject that with a hard 400, turning a
// recoverable retry into a dead run. The empty turn should instead carry the
// reasoning text so the retry has context and stays non-empty everywhere.
func TestRunStepWithRetry_NeverEmptyAssistantTurn(t *testing.T) {
	engine, provider := buildTestEngine(t, ExplorationEngineOptions{MaxSteps: 3}, nil)
	provider.ResponseQueue = []*gollm.ChatResponse{
		// attempt 0: reasoning-only — empty Content, non-empty Reasoning.
		{Content: "", Reasoning: "let me consider the schema before acting", Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}},
		// attempt 1: a valid action after the reformat nudge.
		{Content: `{"query":"SELECT 1 FROM test_dataset.users"}`, Usage: gollm.Usage{InputTokens: 2, OutputTokens: 2}},
	}

	conv := NewConversation(ConversationOptions{SystemPrompt: "sys", MaxMessages: 100})
	conv.AddUserMessage("explore")

	action, _, _, err := engine.runStepWithRetry(context.Background(), conv, 1)
	if err != nil {
		t.Fatalf("runStepWithRetry: %v", err)
	}
	if action == nil || action.Query == "" {
		t.Fatalf("expected the retry to recover a query action, got %+v", action)
	}

	// No assistant turn may be empty (the whole point — Moonshot rejects it).
	sawReasoningTurn := false
	for i, m := range conv.GetMessages() {
		if m.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(m.Content) == "" {
			t.Fatalf("message %d is an empty assistant turn — would 400 on strict providers (Moonshot/Kimi)", i)
		}
		if strings.Contains(m.Content, "consider the schema") {
			sawReasoningTurn = true
		}
	}
	if !sawReasoningTurn {
		t.Errorf("the empty-content turn should have carried the reasoning text into the conversation")
	}
}
