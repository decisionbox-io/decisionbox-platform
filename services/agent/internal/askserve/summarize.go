package askserve

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// QuerySummary is the bounded, summary-only representation of a query result.
// It is what gets persisted as the tool event's Output and rendered in the
// transcript — never the full table. RowCount is the true number of rows the
// warehouse returned; Preview shows at most PreviewRows of them; Truncated is
// set when the preview omits rows. No raw result set is ever stored.
type QuerySummary struct {
	SQL       string                   `json:"sql" bson:"sql"`
	Purpose   string                   `json:"purpose,omitempty" bson:"purpose,omitempty"`
	RowCount  int                      `json:"row_count" bson:"row_count"`
	Truncated bool                     `json:"truncated" bson:"truncated"`
	Columns   []string                 `json:"columns" bson:"columns"`
	Preview   []map[string]interface{} `json:"preview" bson:"preview"`
	Note      string                   `json:"note,omitempty" bson:"note,omitempty"`
	Fixed     bool                     `json:"fixed,omitempty" bson:"fixed,omitempty"`
	// ExecutionTimeMs is wall-clock for the query execution (incl. any repair).
	ExecutionTimeMs int64 `json:"execution_time_ms,omitempty" bson:"execution_time_ms,omitempty"`
}

// summarizeResult turns an executor result into a bounded QuerySummary,
// applying the fetch cap (memory bound) and the preview cap (token bound).
// purpose is the model-supplied purpose for the query; cfg supplies the caps.
func summarizeResult(res *queryexec.ExecuteResult, purpose string, cfg Config) QuerySummary {
	total := res.RowCount
	rows := res.Data
	// Defensive memory re-cap: never carry more than the fetch cap forward,
	// even though the executor returned the full set.
	overFetchCap := false
	if cfg.MaxFetchRows > 0 && len(rows) > cfg.MaxFetchRows {
		rows = rows[:cfg.MaxFetchRows]
		overFetchCap = true
	}

	previewN := cfg.PreviewRows
	if previewN > len(rows) {
		previewN = len(rows)
	}
	preview := rows[:previewN]

	sum := QuerySummary{
		SQL:             res.FinalQuery,
		Purpose:         purpose,
		RowCount:        total,
		Columns:         columnsOf(rows),
		Preview:         preview,
		Fixed:           res.Fixed,
		ExecutionTimeMs: res.ExecutionTimeMs,
	}
	sum.Truncated = total > len(preview)

	switch {
	case overFetchCap:
		sum.Note = fmt.Sprintf(
			"Result has %d rows; only the first %d were retained and the first %d are previewed. "+
				"Use aggregate SQL (COUNT/SUM/GROUP BY) for exact figures over the full set.",
			total, cfg.MaxFetchRows, len(preview),
		)
	case sum.Truncated:
		sum.Note = fmt.Sprintf("Showing the first %d of %d rows.", len(preview), total)
	}
	return sum
}

// observation renders the summary as the user-message text fed back to the
// model after a query. Compact JSON preview keeps tokens bounded; the note
// nudges the model toward aggregate SQL for exact totals.
func (s QuerySummary) observation() string {
	var b strings.Builder
	if s.Fixed {
		b.WriteString("Query executed successfully (auto-repaired).\n")
	} else {
		b.WriteString("Query executed successfully.\n")
	}
	fmt.Fprintf(&b, "Rows returned: %d\n", s.RowCount)
	if len(s.Columns) > 0 {
		fmt.Fprintf(&b, "Columns: %s\n", strings.Join(s.Columns, ", "))
	}
	b.WriteString("Preview (JSON):\n")
	if js, err := json.MarshalIndent(s.Preview, "", "  "); err == nil {
		b.Write(js)
		b.WriteByte('\n')
	}
	if s.Note != "" {
		b.WriteString(s.Note)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// columnsOf returns the sorted union of keys across the given rows, so a
// ragged result (rows with differing key sets) still reports every column
// deterministically.
func columnsOf(rows []map[string]interface{}) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			seen[k] = struct{}{}
		}
	}
	cols := make([]string, 0, len(seen))
	for k := range seen {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	return cols
}
