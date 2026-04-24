package ollama

import (
	"context"
	"errors"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestChat_RejectsToolsWithSentinel(t *testing.T) {
	p := &OllamaProvider{model: "any"}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Tools: []gollm.ToolDefinition{{Name: "x"}},
	})
	if err == nil {
		t.Fatal("expected error when tools set on a provider without tool support")
	}
	if !errors.Is(err, gollm.ErrToolsNotSupported) {
		t.Errorf("expected ErrToolsNotSupported, got %v", err)
	}
}

func TestChat_RejectsToolResultsInMessageWithSentinel(t *testing.T) {
	p := &OllamaProvider{model: "any"}
	_, err := p.Chat(context.Background(), gollm.ChatRequest{
		Messages: []gollm.Message{
			{Role: "user", ToolResults: []gollm.ToolResult{{CallID: "x", Content: "y"}}},
		},
	})
	if err == nil || !errors.Is(err, gollm.ErrToolsNotSupported) {
		t.Errorf("expected ErrToolsNotSupported, got %v", err)
	}
}
