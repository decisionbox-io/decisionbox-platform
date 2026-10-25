package handler

import "testing"

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

func TestExtractJSONObject_K8sMixedStdoutStderr(t *testing.T) {
	// K8s pod logs mix stderr (structured log lines with "severity") and stdout (result).
	input := []byte(`{"severity":"INFO","timestamp":"2026-04-08T21:33:10.123Z","caller":"log/compat.go:75","message":"Connected to MongoDB","service":"decisionbox-agent"}
{"severity":"INFO","timestamp":"2026-04-08T21:33:10.456Z","caller":"log/compat.go:75","message":"Secret provider initialized","service":"decisionbox-agent","provider":"aws"}
{"severity":"INFO","timestamp":"2026-04-08T21:33:10.789Z","caller":"log/compat.go:75","message":"Warehouse provider initialized","service":"decisionbox-agent","provider":"bigquery"}
{"success":true,"provider":"bigquery","datasets":["analytics"]}`)

	result := extractJSONObject(input)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if string(result) != `{"success":true,"provider":"bigquery","datasets":["analytics"]}` {
		t.Errorf("got %q", string(result))
	}
}

func TestExtractJSONObject_K8sLogLinesOnly(t *testing.T) {
	// All lines are log lines — no agent result present.
	input := []byte(`{"severity":"INFO","timestamp":"2026-04-08T21:33:10.123Z","message":"Starting"}
{"severity":"ERROR","timestamp":"2026-04-08T21:33:10.456Z","message":"Failed"}`)

	result := extractJSONObject(input)
	if result != nil {
		t.Errorf("expected nil for log-only output, got %q", string(result))
	}
}
