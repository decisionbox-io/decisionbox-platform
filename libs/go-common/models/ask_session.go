package models

import (
	"strings"
	"time"
)

// AskSession represents a multi-turn conversation in the "Ask Insights" feature.
// Stored in the "ask_sessions" collection.
type AskSession struct {
	ID           string              `bson:"_id" json:"id"`
	ProjectID    string              `bson:"project_id" json:"project_id"`
	UserID       string              `bson:"user_id" json:"user_id"`
	Title        string              `bson:"title" json:"title"` // first question, used as display title
	Messages     []AskSessionMessage `bson:"messages" json:"messages"`
	MessageCount int                 `bson:"message_count" json:"message_count"`
	// SeedContext grounds the whole conversation in one insight or
	// recommendation the user launched the chat from ("Ask about this").
	// It is resolved and stored once at session creation and re-applied to
	// every turn (first and follow-ups) so the conversation stays on-topic.
	// nil for a generic, unseeded chat. omitempty keeps legacy rows clean.
	SeedContext *AskSessionSeed `bson:"seed_context,omitempty" json:"seed_context,omitempty"`
	CreatedAt   time.Time       `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time       `bson:"updated_at" json:"updated_at"`
}

// AskSessionSeed is the insight / recommendation a seeded Ask conversation is
// anchored to. Type + ID identify the entity; Label + Text are the resolved,
// bounded values hydrated server-side at session creation so no re-fetch is
// needed on later turns (and the client cannot spoof the grounding text).
type AskSessionSeed struct {
	Type  string `bson:"type" json:"type"` // "insight" | "recommendation"
	ID    string `bson:"id" json:"id"`
	Label string `bson:"label,omitempty" json:"label,omitempty"` // resolved name / title
	Text  string `bson:"text,omitempty" json:"text,omitempty"`   // hydrated, length-bounded
}

// PromptBlock renders the FOCUS suffix that anchors a seeded Ask conversation
// on its insight / recommendation, for appending to the system prompt. It
// returns a leading-space-prefixed sentence when there is something to ground
// on, or "" (including for a nil seed). Shared by the classic RAG paths in both
// the community and enterprise handlers so the wording stays in one place; the
// ask-serve data-query path renders its own FOCUS block from its wire type.
func (s *AskSessionSeed) PromptBlock() string {
	if s == nil {
		return ""
	}
	kind := s.Type
	if kind != "insight" && kind != "recommendation" {
		kind = "item"
	}
	label := strings.TrimSpace(s.Label)
	text := strings.TrimSpace(s.Text)
	if label == "" && text == "" {
		return ""
	}
	b := " The user opened this conversation from a specific " + kind +
		" and their questions are about it — keep your answers anchored to it."
	if label != "" {
		b += " The " + kind + " is titled: " + label + "."
	}
	if text != "" {
		b += " Details: " + text
	}
	return b
}

// AskSessionMessage is a single Q&A turn within a conversation.
//
// Sources carries the insights / recommendations / knowledge chunks that
// grounded the answer (citations). ToolEvents carries the agentic
// transcript of any tool calls + their outputs the model produced before
// emitting the final answer; the two fields are parallel — Sources keeps
// citations, ToolEvents keeps tool-use replay. A plain RAG-only message
// has empty ToolEvents; an agentic message has both.
type AskSessionMessage struct {
	Question   string             `bson:"question" json:"question"`
	Answer     string             `bson:"answer" json:"answer"`
	Sources    []AskSessionSource `bson:"sources" json:"sources"`
	ToolEvents []ToolEvent        `bson:"tool_events,omitempty" json:"tool_events,omitempty"`
	Model      string             `bson:"model" json:"model"`
	// omitempty so legacy rows render as absent rather than 0 —
	// distinguishes "unknown" from "zero spent."
	InputTokens  int       `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int       `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`
	CreatedAt    time.Time `bson:"created_at" json:"created_at"`
}

// AskSessionSource is a reference to an insight or recommendation used as context.
type AskSessionSource struct {
	ID           string  `bson:"id" json:"id"`
	Type         string  `bson:"type" json:"type"` // "insight" or "recommendation"
	Name         string  `bson:"name" json:"name"`
	Score        float64 `bson:"score" json:"score"`
	Severity     string  `bson:"severity,omitempty" json:"severity,omitempty"`
	AnalysisArea string  `bson:"analysis_area,omitempty" json:"analysis_area,omitempty"`
	Description  string  `bson:"description,omitempty" json:"description,omitempty"`
	DiscoveryID  string  `bson:"discovery_id" json:"discovery_id"`
}

// ToolEvent is one entry in the agentic-Ask transcript: a tool call the
// model emitted, its outcome, and (when the tool was a mutation tool)
// the proposal it produced. The shape is intentionally generic so any
// plugin's tool can persist without owning a model.
type ToolEvent struct {
	// Round is the 1-based loop iteration in which the tool was invoked.
	Round int `bson:"round" json:"round"`
	// Name is the tool identifier (e.g. "list_tables", "propose_note").
	Name string `bson:"name" json:"name"`
	// Args is the tool's input as the model emitted it. Stored verbatim
	// so the dashboard can replay the call without re-derivation.
	Args map[string]any `bson:"args,omitempty" json:"args,omitempty"`
	// Output is the tool's return payload. For propose_* tools this is
	// the rendered card body; for read-only tools this is the JSON the
	// model received back.
	Output any `bson:"output,omitempty" json:"output,omitempty"`
	// Error is the user-facing error message when the tool failed.
	// Empty on success.
	Error string `bson:"error,omitempty" json:"error,omitempty"`
	// ProposalID points at the ask_proposals row this tool produced.
	// Empty for read-only tools or tools that didn't propose.
	ProposalID string `bson:"proposal_id,omitempty" json:"proposal_id,omitempty"`
	// LatencyMS is wall-clock time for the tool execution; surfaced in
	// the UI replay for diagnosing slow tools.
	LatencyMS int64 `bson:"latency_ms,omitempty" json:"latency_ms,omitempty"`
}
