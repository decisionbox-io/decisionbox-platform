package askserve

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// QuerySummary is the bounded, summary-only representation of a query result.
// It is what gets persisted as the tool event's Output and rendered in the
// transcript — never the full table. RowCount is the true number of rows the
// warehouse returned; Preview shows at most PreviewRows of them; Truncated is
// set when the preview omits rows. No raw result set is ever stored.
type QuerySummary struct {
	// Step is the per-turn query step id (e.g. "q2") the model can reference as a
	// chart's source_step_id. It is stamped on successful queries only and is
	// unique within a turn — unlike ToolEvent.Round, which is not (native mode can
	// run several queries in one round).
	Step      string                   `json:"step,omitempty" bson:"step,omitempty"`
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
	// Quality is what the SOURCE said about this result's fidelity — rows it
	// withheld, values it sampled, a tail it collapsed. Distinct from
	// Truncated, which is our own preview cap: Truncated says the model was
	// shown part of what came back, Quality says what came back was not the
	// whole answer, and no amount of paging fixes the second.
	//
	// Persisted with the tool event, so a turn answered from a degraded result
	// is identifiable afterwards rather than only in the moment.
	Quality []gowarehouse.QualityCaveat `json:"quality,omitempty" bson:"quality,omitempty"`
	// Scoped says whether this result was verified as restricted to the rows
	// the turn observed on ANOTHER datasource. It is set only on a query made
	// after a different datasource was queried in the same turn; nil — the
	// normal case, and every single-datasource turn — means the question never
	// arose, so an existing turn's persisted summary is unchanged.
	//
	// True requires a positive answer from a join-key report. False is not an
	// accusation: it covers an undeclared hop, a report that does not list the
	// declared field, and a report that could not be reached. ScopeNote says
	// which, and is what the model is shown.
	//
	// It deliberately does not affect chartability, unlike Quality. These rows
	// are a faithful result of the query that ran; what is unverified is what
	// the filter values MEAN across two sources. Withholding the chart would
	// also stop charts working on the second hop of every existing SQL turn,
	// which is the behaviour this was required not to change.
	Scoped    *bool  `json:"scoped,omitempty" bson:"scoped,omitempty"`
	ScopeNote string `json:"scope_note,omitempty" bson:"scope_note,omitempty"`
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
	// Carried, never summarised: a caveat's own words name what was degraded
	// and by how much, and the source is the only thing that knows.
	sum.Quality = res.Quality

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
	// Lead with the step id so the model can reference it as a chart's
	// source_step_id (e.g. `Query q2 — executed successfully.`).
	step := ""
	if s.Step != "" {
		step = " " + s.Step + " —"
	}
	if s.Fixed {
		fmt.Fprintf(&b, "Query%s executed successfully (auto-repaired).\n", step)
	} else {
		fmt.Fprintf(&b, "Query%s executed successfully.\n", step)
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
	// Before the source's own caveats, so that when a result carries both, the
	// stronger statement — the source saying these rows are not the answer —
	// is the one left in the tail position the loop reserves for corrections.
	// With no caveats to follow it this note holds that position itself.
	if s.ScopeNote != "" {
		b.WriteString("\n" + s.ScopeNote + "\n")
	}
	// Last, and as an instruction. A model that has already read the rows and
	// the preview needs to be told what they are NOT before it starts
	// computing over them — and the same wording discovery uses, because the
	// same caveat on the same data must not read as two different severities
	// depending on which path asked.
	b.WriteString(gowarehouse.CaveatInstruction(s.Quality))
	return strings.TrimRight(b.String(), "\n")
}

// chartable reports whether a chart of this result would be true.
//
// Two ways it would not be. Truncated means the preview omits rows, so a chart
// — which must be an exact projection of the preview — cannot show what was
// returned. A source-reported caveat means what was RETURNED is not the whole
// population, which no re-query fixes and which a chart has nowhere to say.
func (s QuerySummary) chartable() bool {
	return !s.Truncated && len(s.Quality) == 0
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
