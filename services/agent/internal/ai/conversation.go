package ai

import (
	"fmt"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"time"

	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// Conversation manages the conversation state with Claude
type Conversation struct {
	messages     []gollm.Message
	systemPrompt string
	startTime    time.Time
	maxMessages  int // Limit conversation history to prevent token overflow
}

// ConversationOptions configure conversation behavior
type ConversationOptions struct {
	SystemPrompt string
	MaxMessages  int // 0 = unlimited
}

// NewConversation creates a new conversation
func NewConversation(opts ConversationOptions) *Conversation {
	maxMessages := opts.MaxMessages
	if maxMessages == 0 {
		maxMessages = 100 // Default limit
	}

	logger.WithFields(logger.Fields{
		"max_messages":  maxMessages,
		"has_system":    opts.SystemPrompt != "",
		"system_length": len(opts.SystemPrompt),
	}).Debug("New conversation created")

	return &Conversation{
		messages:     make([]gollm.Message, 0),
		systemPrompt: opts.SystemPrompt,
		startTime:    time.Now(),
		maxMessages:  maxMessages,
	}
}

// AddUserMessage adds a user message to the conversation
func (c *Conversation) AddUserMessage(content string) {
	c.messages = append(c.messages, gollm.Message{
		Role:    "user",
		Content: content,
	})

	logger.WithFields(logger.Fields{
		"role":           "user",
		"content_length": len(content),
		"total_messages": len(c.messages),
	}).Debug("User message added to conversation")

	c.trimIfNeeded()
}

// AddAssistantMessage adds an assistant message to the conversation
func (c *Conversation) AddAssistantMessage(content string) {
	c.messages = append(c.messages, gollm.Message{
		Role:    "assistant",
		Content: content,
	})

	logger.WithFields(logger.Fields{
		"role":           "assistant",
		"content_length": len(content),
		"total_messages": len(c.messages),
	}).Debug("Assistant message added to conversation")

	c.trimIfNeeded()
}

// AddMessage adds a message to the conversation
func (c *Conversation) AddMessage(role, content string) error {
	if role != "user" && role != "assistant" {
		return fmt.Errorf("invalid role: %s (must be 'user' or 'assistant')", role)
	}

	if role == "user" {
		c.AddUserMessage(content)
	} else {
		c.AddAssistantMessage(content)
	}

	return nil
}

// GetMessages returns all messages in the conversation
func (c *Conversation) GetMessages() []gollm.Message {
	return c.messages
}

// GetSystemPrompt returns the system prompt
func (c *Conversation) GetSystemPrompt() string {
	return c.systemPrompt
}

// SetSystemPrompt updates the system prompt
func (c *Conversation) SetSystemPrompt(prompt string) {
	c.systemPrompt = prompt
	logger.WithField("length", len(prompt)).Debug("System prompt updated")
}

// MessageCount returns the number of messages in the conversation
func (c *Conversation) MessageCount() int {
	return len(c.messages)
}

// GetLastMessage returns the last message in the conversation
func (c *Conversation) GetLastMessage() *gollm.Message {
	if len(c.messages) == 0 {
		return nil
	}
	return &c.messages[len(c.messages)-1]
}

// GetLastAssistantMessage returns the last assistant message
func (c *Conversation) GetLastAssistantMessage() *gollm.Message {
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Role == "assistant" {
			return &c.messages[i]
		}
	}
	return nil
}

// GetDuration returns how long this conversation has been active
func (c *Conversation) GetDuration() time.Duration {
	return time.Since(c.startTime)
}

// Clear clears all messages (but keeps system prompt)
func (c *Conversation) Clear() {
	c.messages = make([]gollm.Message, 0)
	logger.Debug("Conversation messages cleared")
}

// Clone creates a copy of the conversation
func (c *Conversation) Clone() *Conversation {
	clone := &Conversation{
		messages:     make([]gollm.Message, len(c.messages)),
		systemPrompt: c.systemPrompt,
		startTime:    c.startTime,
		maxMessages:  c.maxMessages,
	}

	copy(clone.messages, c.messages)

	return clone
}

// trimIfNeeded trims old messages if we exceed max limit
func (c *Conversation) trimIfNeeded() {
	if c.maxMessages <= 0 {
		return // No limit
	}

	if len(c.messages) > c.maxMessages {
		// Keep most recent messages
		trimCount := len(c.messages) - c.maxMessages
		c.messages = c.messages[trimCount:]

		logger.WithFields(logger.Fields{
			"trimmed":      trimCount,
			"remaining":    len(c.messages),
			"max_messages": c.maxMessages,
		}).Warn("Conversation trimmed to stay within max message limit")
	}
}

// GetSummary returns a summary of the conversation for logging
func (c *Conversation) GetSummary() map[string]interface{} {
	userCount := 0
	assistantCount := 0
	totalChars := 0

	for _, msg := range c.messages {
		if msg.Role == "user" {
			userCount++
		} else {
			assistantCount++
		}
		totalChars += len(msg.Content)
	}

	return map[string]interface{}{
		"total_messages":     len(c.messages),
		"user_messages":      userCount,
		"assistant_messages": assistantCount,
		"total_characters":   totalChars,
		"duration_seconds":   time.Since(c.startTime).Seconds(),
		"has_system_prompt":  c.systemPrompt != "",
	}
}

// ExportToJSON returns conversation as JSON-compatible structure
func (c *Conversation) ExportToJSON() map[string]interface{} {
	return map[string]interface{}{
		"system_prompt": c.systemPrompt,
		"messages":      c.messages,
		"start_time":    c.startTime,
		"duration":      c.GetDuration().String(),
		"message_count": len(c.messages),
	}
}
