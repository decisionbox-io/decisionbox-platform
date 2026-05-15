package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- writeError (legacy) ------------------------------------------

func TestWriteError_OmitsCodeAndDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "bad input")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Error != "bad input" {
		t.Errorf("Error = %q, want %q", resp.Error, "bad input")
	}
	if resp.Code != "" || resp.Details != "" {
		t.Errorf("legacy writeError must not set Code/Details; got %+v", resp)
	}
}

// --- writeErrorCode -----------------------------------------------

func TestWriteErrorCode_SetsAllFields(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorCode(rec, http.StatusRequestEntityTooLarge,
		ErrCodeContextOverflow,
		"context too long",
		"model=claude-sonnet window=200000")

	if rec.Code != 413 {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Code != ErrCodeContextOverflow {
		t.Errorf("Code = %q, want %q", resp.Code, ErrCodeContextOverflow)
	}
	if resp.Error != "context too long" {
		t.Errorf("Error = %q, want %q", resp.Error, "context too long")
	}
	if resp.Details != "model=claude-sonnet window=200000" {
		t.Errorf("Details = %q", resp.Details)
	}
}

func TestWriteErrorCode_EmptyDetailsAreOmitted(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErrorCode(rec, http.StatusPreconditionFailed,
		ErrCodeLLMNotConfigured,
		"no llm",
		"")
	// json.Marshal honours `omitempty`, so the wire shape should not
	// include "details": "".
	if got := rec.Body.String(); contains(got, "\"details\"") {
		t.Errorf("body leaked empty Details: %s", got)
	}
}

func TestWriteErrorCode_AllCodesAreStrings(t *testing.T) {
	// Guard against the codes accidentally drifting to ints — the
	// dashboard pattern-matches on strings.
	codes := []string{
		ErrCodeEmbeddingNotConfigured,
		ErrCodeLLMNotConfigured,
		ErrCodeContextOverflow,
		ErrCodeLLMUpstream,
		ErrCodeLLMSynthesisFailed,
	}
	for _, c := range codes {
		if c == "" {
			t.Fatalf("empty error code in registry")
		}
	}
}

// --- writeJSON ----------------------------------------------------

func TestWriteJSON_WrapsDataField(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]string{"a": "b"})
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Data["a"] != "b" {
		t.Fatalf("got %v", resp.Data)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
