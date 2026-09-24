package discovery

import (
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// The gate is the safety property of this file: with the variable unset, an
// operator's run must do none of this work. Asserted rather than assumed
// because every emitter reads it and a default of "on" would put a per-row log
// line into every production discovery.
func TestTraceEnabled_OffUnlessExplicitlyTrue(t *testing.T) {
	for _, v := range []string{"", " ", "0", "false", "no", "off", "yes", "TRACE", "2x"} {
		if traceEnabled(v) {
			t.Errorf("traceEnabled(%q) = true, want false", v)
		}
	}
	for _, v := range []string{"1", "true", "TRUE", "True", " true "} {
		if !traceEnabled(v) {
			t.Errorf("traceEnabled(%q) = false, want true", v)
		}
	}
}

// digestRowsShown is the one piece of arithmetic in the trace, and the number it
// produces is the one a reader uses to decide whether a population claim could
// have been grounded at all. An inline digest shows every row; a windowed one
// shows both ends and nothing between.
func TestDigestRowsShown(t *testing.T) {
	row := func(i int) map[string]any { return map[string]any{"i": i} }
	cases := []struct {
		name string
		in   *gomodels.CompactResult
		want int
	}{
		{"nil digest shows nothing", nil, 0},
		{
			"inline digest shows every row",
			&gomodels.CompactResult{RowCount: 3, AllRows: []map[string]any{row(1), row(2), row(3)}},
			3,
		},
		{
			"windowed digest shows head plus tail only",
			&gomodels.CompactResult{
				RowCount: 150,
				HeadRows: []map[string]any{row(1), row(2), row(3), row(4), row(5)},
				TailRows: []map[string]any{row(146), row(147), row(148), row(149), row(150)},
			},
			10,
		},
		{
			"head-only digest counts the head",
			&gomodels.CompactResult{RowCount: 7, HeadRows: []map[string]any{row(1), row(2)}},
			2,
		},
		{
			"inline wins when both are present",
			&gomodels.CompactResult{
				RowCount: 2,
				AllRows:  []map[string]any{row(1), row(2)},
				HeadRows: []map[string]any{row(1), row(2)},
			},
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := digestRowsShown(tc.in); got != tc.want {
				t.Errorf("digestRowsShown() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestOneLineAndClip(t *testing.T) {
	if got := oneLine("select *\n  from  t\nwhere x = 1"); got != "select * from t where x = 1" {
		t.Errorf("oneLine() = %q", got)
	}
	if got := clip("abcdef", 3); got != "abc…" {
		t.Errorf("clip() = %q, want %q", got, "abc…")
	}
	if got := clip("abc", 3); got != "abc" {
		t.Errorf("clip() should not mark an unclipped string, got %q", got)
	}
}
