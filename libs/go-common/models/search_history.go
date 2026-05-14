package models

import "time"

// SearchHistory records a user's search or ask query.
// Stored in the "search_history" collection with a 90-day TTL.
type SearchHistory struct {
	ID           string   `bson:"_id" json:"id"`
	UserID       string   `bson:"user_id" json:"user_id"`
	ProjectID    string   `bson:"project_id" json:"project_id"`
	Query        string   `bson:"query" json:"query"`
	Type         string   `bson:"type" json:"type"` // "search" or "ask"
	ResultsCount int      `bson:"results_count" json:"results_count"`
	TopResultIDs []string `bson:"top_result_ids,omitempty" json:"top_result_ids,omitempty"`
	TopResultScore float64 `bson:"top_result_score,omitempty" json:"top_result_score,omitempty"`

	// For "ask" type only
	AnswerSummary string   `bson:"answer_summary,omitempty" json:"answer_summary,omitempty"`
	SourceIDs     []string `bson:"source_ids,omitempty" json:"source_ids,omitempty"`
	LLMModel      string   `bson:"llm_model,omitempty" json:"llm_model,omitempty"`
	// omitempty so legacy rows and search-type entries (which never
	// make an LLM call) render absent rather than 0.
	InputTokens  int `bson:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens int `bson:"output_tokens,omitempty" json:"output_tokens,omitempty"`

	CreatedAt time.Time `bson:"created_at" json:"created_at"`
}
