package askserve

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

func rows(n int) []map[string]interface{} {
	out := make([]map[string]interface{}, n)
	for i := range out {
		out[i] = map[string]interface{}{"id": i, "name": "row"}
	}
	return out
}

func TestSummarize_NoTruncation(t *testing.T) {
	cfg := Config{MaxFetchRows: 1000, PreviewRows: 50}
	res := &queryexec.ExecuteResult{Data: rows(10), RowCount: 10, FinalQuery: "SELECT *"}
	s := summarizeResult(res, "p", cfg)
	if s.Truncated {
		t.Fatal("should not be truncated")
	}
	if s.RowCount != 10 || len(s.Preview) != 10 {
		t.Fatalf("rowCount=%d preview=%d", s.RowCount, len(s.Preview))
	}
	if s.Note != "" {
		t.Fatalf("unexpected note: %q", s.Note)
	}
	if len(s.Columns) != 2 || s.Columns[0] != "id" || s.Columns[1] != "name" {
		t.Fatalf("columns = %v (want sorted [id name])", s.Columns)
	}
}

func TestSummarize_PreviewCapTruncates(t *testing.T) {
	cfg := Config{MaxFetchRows: 1000, PreviewRows: 50}
	res := &queryexec.ExecuteResult{Data: rows(200), RowCount: 200, FinalQuery: "SELECT *"}
	s := summarizeResult(res, "p", cfg)
	if !s.Truncated {
		t.Fatal("should be truncated (preview omits rows)")
	}
	if len(s.Preview) != 50 {
		t.Fatalf("preview = %d, want 50", len(s.Preview))
	}
	if s.RowCount != 200 {
		t.Fatalf("rowCount = %d, want true total 200", s.RowCount)
	}
	if !strings.Contains(s.Note, "200") {
		t.Fatalf("note should mention total: %q", s.Note)
	}
}

func TestSummarize_FetchCapBound(t *testing.T) {
	cfg := Config{MaxFetchRows: 100, PreviewRows: 50}
	res := &queryexec.ExecuteResult{Data: rows(5000), RowCount: 5000, FinalQuery: "SELECT *"}
	s := summarizeResult(res, "p", cfg)
	if len(s.Preview) != 50 {
		t.Fatalf("preview = %d, want 50", len(s.Preview))
	}
	if !s.Truncated {
		t.Fatal("should be truncated")
	}
	if !strings.Contains(s.Note, "aggregate") {
		t.Fatalf("over-fetch-cap note should nudge toward aggregates: %q", s.Note)
	}
}

func TestSummarize_EmptyResult(t *testing.T) {
	cfg := Config{MaxFetchRows: 1000, PreviewRows: 50}
	res := &queryexec.ExecuteResult{Data: nil, RowCount: 0, FinalQuery: "SELECT * WHERE 1=0"}
	s := summarizeResult(res, "p", cfg)
	if s.Truncated || s.RowCount != 0 || len(s.Preview) != 0 {
		t.Fatalf("empty result mishandled: %+v", s)
	}
}

func TestSummarize_RaggedColumns(t *testing.T) {
	cfg := Config{MaxFetchRows: 1000, PreviewRows: 50}
	data := []map[string]interface{}{{"a": 1}, {"b": 2, "a": 3}}
	res := &queryexec.ExecuteResult{Data: data, RowCount: 2, FinalQuery: "SELECT *"}
	s := summarizeResult(res, "p", cfg)
	if len(s.Columns) != 2 || s.Columns[0] != "a" || s.Columns[1] != "b" {
		t.Fatalf("columns = %v, want union [a b]", s.Columns)
	}
}

func TestSummarize_ObservationMentionsRowsAndNote(t *testing.T) {
	cfg := Config{MaxFetchRows: 1000, PreviewRows: 2}
	res := &queryexec.ExecuteResult{Data: rows(10), RowCount: 10, FinalQuery: "SELECT *", Fixed: true}
	obs := summarizeResult(res, "p", cfg).observation()
	if !strings.Contains(obs, "Rows returned: 10") {
		t.Fatalf("observation missing row count: %q", obs)
	}
	if !strings.Contains(obs, "repaired") {
		t.Fatalf("observation should note auto-repair: %q", obs)
	}
}
