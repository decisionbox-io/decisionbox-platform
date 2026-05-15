package handler

import (
	"encoding/json"
	"net/http"
)

// APIResponse is the standard response wrapper.
//
// Error is the human-readable message kept for backwards compat with
// every caller that does `throw new Error(json.error)`. Code is a
// machine-readable identifier so the dashboard can branch on
// recoverable conditions (e.g. show "context overflow — start a new
// chat" instead of the generic "Sorry, I could not answer this
// question"). Details, when populated, carries provider-specific
// context the dashboard may surface in a "what happened" expander.
type APIResponse struct {
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Code    string      `json:"code,omitempty"`
	Details string      `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Data: data})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{Error: msg})
}

// writeErrorCode writes a structured error with both a
// human-readable message and a machine-readable code. Dashboards
// branch on Code; the message is what falls out of
// `new Error(json.error)` paths that don't know about Code.
//
// Details is optional — keep it empty for conditions where the
// upstream message would leak provider internals or secrets. The
// caller is responsible for sanitisation (see
// gollm.SanitizeErrorBody for upstream LLM bodies).
func writeErrorCode(w http.ResponseWriter, status int, code, msg, details string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIResponse{
		Error:   msg,
		Code:    code,
		Details: details,
	})
}

func decodeJSON(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// Machine-readable error codes returned by Ask and any other
// endpoint that needs the dashboard to branch on condition. Strings,
// not iota — they cross the network and the dashboard pattern-matches
// against them directly.
const (
	// ErrCodeEmbeddingNotConfigured: project has no embedding
	// provider configured. 412 — fix by configuring an embedding
	// provider in project settings.
	ErrCodeEmbeddingNotConfigured = "embedding_not_configured"

	// ErrCodeLLMNotConfigured: project has no LLM provider
	// configured, or the configured provider failed to instantiate.
	// 412 — fix by configuring an LLM provider in project settings.
	ErrCodeLLMNotConfigured = "llm_not_configured"

	// ErrCodeContextOverflow: even after trimming history and
	// shrinking RAG context, the request would exceed the model's
	// input window. 413 — fix by starting a new chat or picking a
	// model with a wider context window.
	ErrCodeContextOverflow = "context_overflow"

	// ErrCodeLLMUpstream: the LLM provider returned a 4xx that is
	// not specifically a context-overflow (rate-limit, content
	// filter, billing). 502 — fix typically requires action on the
	// upstream provider's side.
	ErrCodeLLMUpstream = "llm_upstream"

	// ErrCodeLLMSynthesisFailed: catch-all for unexpected provider
	// failures (5xx, transport error). 500 — see API logs for the
	// upstream error and try again.
	ErrCodeLLMSynthesisFailed = "llm_synthesis_failed"
)
