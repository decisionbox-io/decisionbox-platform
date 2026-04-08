package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExtractJSONObject_ValidJSON(t *testing.T) {
	input := []byte(`{"key": "value"}`)
	result := extractJSONObject(input)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if string(result) != `{"key": "value"}` {
		t.Errorf("got %q", string(result))
	}
}

func TestExtractJSONObject_MixedOutput(t *testing.T) {
	input := []byte(`some log line
another log
{"total_cost_usd": 0.45, "llm": {"cost_usd": 0.40}}
more logs after`)

	result := extractJSONObject(input)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if string(result) != `{"total_cost_usd": 0.45, "llm": {"cost_usd": 0.40}}` {
		t.Errorf("got %q", string(result))
	}
}

func TestExtractJSONObject_NestedBraces(t *testing.T) {
	input := []byte(`{"outer": {"inner": {"deep": 1}}}`)
	result := extractJSONObject(input)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if string(result) != `{"outer": {"inner": {"deep": 1}}}` {
		t.Errorf("got %q", string(result))
	}
}

func TestExtractJSONObject_NoJSON(t *testing.T) {
	input := []byte("plain text with no braces at all")
	result := extractJSONObject(input)
	if result != nil {
		t.Errorf("expected nil, got %q", string(result))
	}
}

func TestExtractJSONObject_EmptyInput(t *testing.T) {
	result := extractJSONObject([]byte{})
	if result != nil {
		t.Errorf("expected nil for empty input, got %q", string(result))
	}
}

func TestExtractJSONObject_UnbalancedBraces(t *testing.T) {
	input := []byte(`{"key": "value"`)
	result := extractJSONObject(input)
	if result != nil {
		t.Errorf("expected nil for unbalanced braces, got %q", string(result))
	}
}

func TestExtractJSONObject_OnlyOpenBrace(t *testing.T) {
	result := extractJSONObject([]byte("{"))
	if result != nil {
		t.Errorf("expected nil for single open brace, got %q", string(result))
	}
}

func TestExtractJSONObject_EmptyObject(t *testing.T) {
	result := extractJSONObject([]byte("{}"))
	if result == nil {
		t.Fatal("expected non-nil for empty object")
	}
	if string(result) != "{}" {
		t.Errorf("got %q", string(result))
	}
}

func TestExtractJSONObject_LeadingGarbage(t *testing.T) {
	input := []byte(`2026-03-22T10:00:00Z INFO Starting estimate
{"success": true, "cost": 0.50}`)
	result := extractJSONObject(input)
	if result == nil {
		t.Fatal("expected non-nil")
	}
	if string(result) != `{"success": true, "cost": 0.50}` {
		t.Errorf("got %q", string(result))
	}
}

func TestNewEstimateHandler(t *testing.T) {
	h := NewEstimateHandler(nil)
	if h == nil {
		t.Fatal("NewEstimateHandler returned nil")
	}
}

func TestNewProjectsHandler(t *testing.T) {
	h := NewProjectsHandler(nil, nil)
	if h == nil {
		t.Fatal("NewProjectsHandler returned nil")
	}
}

func TestNewPricingHandler(t *testing.T) {
	h := NewPricingHandler(nil)
	if h == nil {
		t.Fatal("NewPricingHandler returned nil")
	}
}

func TestNewFeedbackHandler(t *testing.T) {
	h := NewFeedbackHandler(nil)
	if h == nil {
		t.Fatal("NewFeedbackHandler returned nil")
	}
}

func TestNewDomainsHandler(t *testing.T) {
	h := NewDomainsHandler(nil)
	if h == nil {
		t.Fatal("NewDomainsHandler returned nil")
	}
}

func TestNewDomainPacksHandler(t *testing.T) {
	h := NewDomainPacksHandler(nil)
	if h == nil {
		t.Fatal("NewDomainPacksHandler returned nil")
	}
}

func TestNewProvidersHandler(t *testing.T) {
	h := NewProvidersHandler()
	if h == nil {
		t.Fatal("NewProvidersHandler returned nil")
	}
}

func TestNewSecretsHandler(t *testing.T) {
	h := NewSecretsHandler(nil, nil)
	if h == nil {
		t.Fatal("NewSecretsHandler returned nil")
	}
}

func TestNewTestConnectionHandler(t *testing.T) {
	h := NewTestConnectionHandler(nil, nil)
	if h == nil {
		t.Fatal("NewTestConnectionHandler returned nil")
	}
}

func TestHealthCheck_ResponseFormat(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	HealthCheck(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", w.Header().Get("Content-Type"))
	}
}
