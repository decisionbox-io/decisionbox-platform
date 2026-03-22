package ai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestNewConversation(t *testing.T) {
	c := NewConversation(ConversationOptions{
		SystemPrompt: "You are a test assistant",
		MaxMessages:  50,
	})

	if c.GetSystemPrompt() != "You are a test assistant" {
		t.Error("system prompt not set")
	}
	if c.MessageCount() != 0 {
		t.Errorf("message count = %d, want 0", c.MessageCount())
	}
}

func TestAddMessages(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 100})

	c.AddUserMessage("hello")
	c.AddAssistantMessage("hi there")

	if c.MessageCount() != 2 {
		t.Fatalf("count = %d, want 2", c.MessageCount())
	}

	msgs := c.GetMessages()
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("msg[0] = %v, want user/hello", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Errorf("msg[1] = %v, want assistant/hi there", msgs[1])
	}
}

func TestAddMessageValidation(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 100})

	if err := c.AddMessage("invalid", "test"); err == nil {
		t.Error("should reject invalid role")
	}
	if err := c.AddMessage("user", "test"); err != nil {
		t.Errorf("should accept user role: %v", err)
	}
	if err := c.AddMessage("assistant", "test"); err != nil {
		t.Errorf("should accept assistant role: %v", err)
	}
}

func TestGetLastMessage(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 100})

	if c.GetLastMessage() != nil {
		t.Error("should return nil for empty conversation")
	}

	c.AddUserMessage("first")
	c.AddAssistantMessage("second")

	last := c.GetLastMessage()
	if last == nil || last.Content != "second" {
		t.Errorf("last message = %v, want 'second'", last)
	}
}

func TestGetLastAssistantMessage(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 100})

	c.AddUserMessage("q1")
	c.AddAssistantMessage("a1")
	c.AddUserMessage("q2")

	last := c.GetLastAssistantMessage()
	if last == nil || last.Content != "a1" {
		t.Errorf("last assistant = %v, want 'a1'", last)
	}
}

func TestTrimming(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 5})

	for i := 0; i < 10; i++ {
		c.AddUserMessage("msg")
	}

	if c.MessageCount() != 5 {
		t.Errorf("count = %d, want 5 (should trim)", c.MessageCount())
	}
}

func TestClone(t *testing.T) {
	c := NewConversation(ConversationOptions{
		SystemPrompt: "test",
		MaxMessages:  100,
	})
	c.AddUserMessage("hello")

	clone := c.Clone()
	clone.AddUserMessage("extra")

	if c.MessageCount() != 1 {
		t.Error("original should not be affected by clone changes")
	}
	if clone.MessageCount() != 2 {
		t.Error("clone should have the extra message")
	}
}

func TestClear(t *testing.T) {
	c := NewConversation(ConversationOptions{
		SystemPrompt: "test",
		MaxMessages:  100,
	})
	c.AddUserMessage("hello")
	c.Clear()

	if c.MessageCount() != 0 {
		t.Error("should be empty after clear")
	}
	if c.GetSystemPrompt() != "test" {
		t.Error("system prompt should survive clear")
	}
}

func TestSetSystemPrompt(t *testing.T) {
	c := NewConversation(ConversationOptions{SystemPrompt: "original"})
	c.SetSystemPrompt("updated")

	if c.GetSystemPrompt() != "updated" {
		t.Errorf("system prompt = %q, want %q", c.GetSystemPrompt(), "updated")
	}
}

func TestGetSummary(t *testing.T) {
	c := NewConversation(ConversationOptions{SystemPrompt: "test", MaxMessages: 100})
	c.AddUserMessage("hello")
	c.AddAssistantMessage("hi")

	summary := c.GetSummary()
	if summary["total_messages"].(int) != 2 {
		t.Error("summary should show 2 messages")
	}
	if summary["user_messages"].(int) != 1 {
		t.Error("summary should show 1 user message")
	}
	if summary["has_system_prompt"].(bool) != true {
		t.Error("summary should show system prompt present")
	}
}

func TestExportToJSON(t *testing.T) {
	c := NewConversation(ConversationOptions{SystemPrompt: "test", MaxMessages: 100})
	c.AddUserMessage("hello")

	export := c.ExportToJSON()
	if export["system_prompt"] != "test" {
		t.Error("export should contain system prompt")
	}
	msgs := export["messages"].([]gollm.Message)
	if len(msgs) != 1 {
		t.Error("export should contain messages")
	}
}

func TestGetDuration(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 100})
	duration := c.GetDuration()
	if duration < 0 {
		t.Error("duration should be non-negative")
	}
}

func TestGetLastAssistantMessage_NoAssistant(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 100})
	c.AddUserMessage("question")

	last := c.GetLastAssistantMessage()
	if last != nil {
		t.Error("should return nil when no assistant messages exist")
	}
}

func TestConversation_DefaultMaxMessages(t *testing.T) {
	c := NewConversation(ConversationOptions{})
	if c.maxMessages != 100 {
		t.Errorf("maxMessages = %d, want 100 (default)", c.maxMessages)
	}
}

func TestConversation_TrimPreservesOrder(t *testing.T) {
	c := NewConversation(ConversationOptions{MaxMessages: 3})

	c.AddUserMessage("first")
	c.AddAssistantMessage("response1")
	c.AddUserMessage("second")
	c.AddAssistantMessage("response2")

	// After trimming to 3, should keep the most recent 3
	if c.MessageCount() != 3 {
		t.Errorf("count = %d, want 3", c.MessageCount())
	}

	msgs := c.GetMessages()
	// Oldest message ("first") should be trimmed
	if msgs[0].Content == "first" {
		t.Error("oldest message should have been trimmed")
	}
}
