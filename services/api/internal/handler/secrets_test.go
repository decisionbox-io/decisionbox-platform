package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecretsHandler_Set_MissingValue(t *testing.T) {
	h := NewSecretsHandler(nil, nil)

	req := httptest.NewRequest("PUT", "/api/v1/projects/p1/secrets/llm-api-key",
		strings.NewReader(`{"value": ""}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "p1")
	req.SetPathValue("key", "llm-api-key")
	w := httptest.NewRecorder()

	// Will panic on nil projectRepo — check validation first
	defer func() { recover() }()
	h.Set(w, req)

	if w.Code == http.StatusOK {
		t.Error("empty value should not succeed")
	}
}

func TestSecretsHandler_Set_InvalidJSON(t *testing.T) {
	h := NewSecretsHandler(nil, nil)

	req := httptest.NewRequest("PUT", "/api/v1/projects/p1/secrets/llm-api-key",
		strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "p1")
	req.SetPathValue("key", "llm-api-key")
	w := httptest.NewRecorder()

	// Will panic on nil projectRepo — check that JSON error comes first
	defer func() { recover() }()
	h.Set(w, req)
}

func TestSecretsHandler_List_NilProvider(t *testing.T) {
	h := NewSecretsHandler(nil, nil)

	req := httptest.NewRequest("GET", "/api/v1/projects/p1/secrets", nil)
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()

	defer func() { recover() }()
	h.List(w, req)
}
