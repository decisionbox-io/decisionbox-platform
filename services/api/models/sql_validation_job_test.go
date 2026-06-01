package models

import (
	"encoding/json"
	"testing"
)

func TestSQLValidationJob_IsTerminal(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{ValidationJobStatusPending, false},
		{ValidationJobStatusRunning, false},
		{ValidationJobStatusCompleted, true},
		{ValidationJobStatusFailed, true},
		{ValidationJobStatusCancelled, true},
		{"", false},
		{"bogus", false},
	}
	for _, c := range cases {
		j := &SQLValidationJob{Status: c.status}
		if got := j.IsTerminal(); got != c.want {
			t.Errorf("IsTerminal(status=%q) = %v, want %v", c.status, got, c.want)
		}
	}
}

// The per-statement verdict must round-trip through JSON with the
// wire-shape the issue specifies: {sql, ok, error}, with error omitted
// when empty so a passing statement carries no error key.
func TestSQLValidationResult_JSONRoundTrip(t *testing.T) {
	in := SQLValidationResult{SQL: "SELECT 1", OK: true}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"sql":"SELECT 1","ok":true}` {
		t.Errorf("marshal ok result = %s, want error key omitted", got)
	}

	failing := SQLValidationResult{SQL: "SELEC 1", OK: false, Error: "syntax error at or near \"SELEC\""}
	b, err = json.Marshal(failing)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back SQLValidationResult
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != failing {
		t.Errorf("round-trip = %+v, want %+v", back, failing)
	}
}

// The job model must marshal an empty statement batch as an empty array
// (not null) so callers polling the result can rely on a stable shape,
// and results stay omitted while the job has not completed.
func TestSQLValidationJob_JSONShape(t *testing.T) {
	j := SQLValidationJob{
		ID:         "job-1",
		ProjectID:  "507f1f77bcf86cd799439011",
		Statements: []string{},
		Status:     ValidationJobStatusPending,
	}
	b, err := json.Marshal(j)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["results"]; ok {
		t.Errorf("results should be omitted while empty, got %s", b)
	}
	if string(m["statements"]) != "[]" {
		t.Errorf("statements = %s, want []", m["statements"])
	}
}
