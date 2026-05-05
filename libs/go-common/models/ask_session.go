package models

import "time"

// AskSession represents a multi-turn conversation in the "Ask Insights" feature.
// Stored in the "ask_sessions" collection.
type AskSession struct {
	ID        string              `bson:"_id" json:"id"`
	ProjectID string              `bson:"project_id" json:"project_id"`
	UserID    string              `bson:"user_id" json:"user_id"`
	Title     string              `bson:"title" json:"title"` // first question, used as display title
	Messages     []AskSessionMessage `bson:"messages" json:"messages"`
	MessageCount int                 `bson:"message_count" json:"message_count"`
	CreatedAt    time.Time           `bson:"created_at" json:"created_at"`
	UpdatedAt    time.Time           `bson:"updated_at" json:"updated_at"`
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
	TokensUsed int                `bson:"tokens_used" json:"tokens_used"`
	CreatedAt  time.Time          `bson:"created_at" json:"created_at"`
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
