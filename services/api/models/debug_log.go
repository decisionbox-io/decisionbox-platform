package models

import "time"

// DebugLogEntry is the projection of a single `discovery_debug_logs`
// document that the API exposes. The underlying document has more fields
// (raw query result rows, analysis input/output blobs, full Claude system
// prompts); those stay in Mongo and aren't returned here.
//
// What IS returned was chosen for debugging utility:
//   - Claude calls: model + token counts + response so you can see what the
//     agent decided at each step (the response typically contains the next
//     action — SQL to run, analysis verdict, etc.).
//   - SQL executions: the query, its purpose, row count, fix attempts, and
//     any error — enough to reproduce locally.
//
// `llm_prompt` and `llm_system_prompt` are intentionally withheld:
// they're huge (10KB+ each, mostly static boilerplate) and would balloon
// the poll response without adding much debugging value.
type DebugLogEntry struct {
	ID             string    `json:"id"`
	DiscoveryRunID string    `json:"discovery_run_id"`
	CreatedAt      time.Time `json:"created_at"`
	LogType        string    `json:"log_type"`
	Component      string    `json:"component"`
	Operation      string    `json:"operation"`
	Phase          string    `json:"phase,omitempty"`
	Step           int       `json:"step,omitempty"`
	DurationMs     int64     `json:"duration_ms,omitempty"`
	Success        bool      `json:"success"`

	// SQL-related (present for execute_query)
	SQLQuery     string `json:"sql_query,omitempty"`
	QueryPurpose string `json:"query_purpose,omitempty"`
	RowCount     int    `json:"row_count,omitempty"`
	FixAttempts  int    `json:"fix_attempts,omitempty"`
	QueryError   string `json:"query_error,omitempty"`

	// Claude-related (present for create_message)
	LLMModel        string `json:"llm_model,omitempty"`
	LLMResponse     string `json:"llm_response,omitempty"`
	LLMInputTokens  int    `json:"llm_input_tokens,omitempty"`
	LLMOutputTokens int    `json:"llm_output_tokens,omitempty"`

	ErrorMessage string `json:"error_message,omitempty"`
}
