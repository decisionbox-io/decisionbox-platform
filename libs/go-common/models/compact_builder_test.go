package models

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBuildCompactResult_NilInput(t *testing.T) {
	got := BuildCompactResult(nil)
	if got.RowCount != 0 {
		t.Errorf("RowCount: got %d want 0", got.RowCount)
	}
	if len(got.Columns) != 0 {
		t.Errorf("Columns: got %d want 0", len(got.Columns))
	}
	if len(got.HeadRows) != 0 {
		t.Errorf("HeadRows: got %d want 0", len(got.HeadRows))
	}
	if got.AllRows != nil {
		t.Errorf("AllRows: got %v want nil", got.AllRows)
	}
	if got.TailRows != nil {
		t.Errorf("TailRows: got %v want nil", got.TailRows)
	}
}

func TestBuildCompactResult_EmptySlice(t *testing.T) {
	got := BuildCompactResult([]map[string]any{})
	if got.RowCount != 0 {
		t.Errorf("RowCount: got %d want 0", got.RowCount)
	}
	if len(got.HeadRows) != 0 {
		t.Errorf("HeadRows: got %d want 0", len(got.HeadRows))
	}
}

func TestBuildCompactResult_SingleEmptyRow(t *testing.T) {
	got := BuildCompactResult([]map[string]any{{}})
	if got.RowCount != 1 {
		t.Errorf("RowCount: got %d want 1", got.RowCount)
	}
	if len(got.Columns) != 0 {
		t.Errorf("Columns: got %d want 0 (no keys in the row)", len(got.Columns))
	}
	if len(got.HeadRows) != 1 {
		t.Errorf("HeadRows: got %d want 1", len(got.HeadRows))
	}
	if len(got.AllRows) != 1 {
		t.Errorf("AllRows: got %d want 1", len(got.AllRows))
	}
}

func TestBuildCompactResult_RowCountAtThreshold(t *testing.T) {
	rows := numericRows("x", CompactInlineThreshold)
	got := BuildCompactResult(rows)

	if got.RowCount != CompactInlineThreshold {
		t.Errorf("RowCount: got %d want %d", got.RowCount, CompactInlineThreshold)
	}
	if len(got.AllRows) != CompactInlineThreshold {
		t.Errorf("AllRows at the threshold: got %d want %d", len(got.AllRows), CompactInlineThreshold)
	}
	if len(got.HeadRows) != HeadTailRowCount {
		t.Errorf("HeadRows: got %d want %d", len(got.HeadRows), HeadTailRowCount)
	}
	if got.TailRows != nil {
		t.Errorf("TailRows must be nil at the threshold (small enough that head + all suffice), got len=%d", len(got.TailRows))
	}
}

func TestBuildCompactResult_RowCountJustOver(t *testing.T) {
	rows := numericRows("x", CompactInlineThreshold+1)
	got := BuildCompactResult(rows)

	if got.AllRows != nil {
		t.Errorf("AllRows must be omitted above the threshold, got len=%d", len(got.AllRows))
	}
	if len(got.HeadRows) != HeadTailRowCount {
		t.Errorf("HeadRows: got %d want %d", len(got.HeadRows), HeadTailRowCount)
	}
	if len(got.TailRows) != HeadTailRowCount {
		t.Errorf("TailRows: got %d want %d", len(got.TailRows), HeadTailRowCount)
	}
}

func TestBuildCompactResult_RowCount10_TailNil(t *testing.T) {
	// 10 rows = 2 * HeadTailRowCount → head already covers everything,
	// tail would just duplicate, so omit it.
	rows := numericRows("x", 2*HeadTailRowCount)
	got := BuildCompactResult(rows)

	if got.TailRows != nil {
		t.Errorf("TailRows should be nil for 2*HeadTailRowCount rows (head covers it), got len=%d", len(got.TailRows))
	}
}

func TestBuildCompactResult_AllNumeric_IntFloat(t *testing.T) {
	rows := []map[string]any{
		{"x": 1},
		{"x": 2.5},
		{"x": int64(3)},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")

	if col.Kind != ColumnKindNumber {
		t.Errorf("Kind: got %q want number", col.Kind)
	}
	if col.Min == nil || *col.Min != 1 {
		t.Errorf("Min: got %v want 1", col.Min)
	}
	if col.Max == nil || *col.Max != 3 {
		t.Errorf("Max: got %v want 3", col.Max)
	}
}

func TestBuildCompactResult_AllNumeric_VariousIntWidths(t *testing.T) {
	rows := []map[string]any{
		{"x": int8(1)},
		{"x": int16(2)},
		{"x": int32(3)},
		{"x": int64(4)},
		{"x": uint8(5)},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")
	if col.Kind != ColumnKindNumber {
		t.Errorf("Kind: got %q want number (mixed integer widths)", col.Kind)
	}
	if col.Min == nil || *col.Min != 1 || col.Max == nil || *col.Max != 5 {
		t.Errorf("Min/Max: got %v/%v want 1/5", col.Min, col.Max)
	}
}

func TestBuildCompactResult_AllString(t *testing.T) {
	rows := []map[string]any{
		{"s": "a"}, {"s": "b"}, {"s": "a"}, {"s": "c"},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "s")
	if col.Kind != ColumnKindString {
		t.Errorf("Kind: got %q want string", col.Kind)
	}
	if col.Distinct != 3 {
		t.Errorf("Distinct: got %d want 3", col.Distinct)
	}
}

func TestBuildCompactResult_AllBoolean(t *testing.T) {
	rows := []map[string]any{
		{"b": true}, {"b": false}, {"b": true},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "b")
	if col.Kind != ColumnKindBoolean {
		t.Errorf("Kind: got %q want boolean", col.Kind)
	}
	if col.Distinct != 2 {
		t.Errorf("Distinct: got %d want 2", col.Distinct)
	}
	if len(col.Top) != 2 {
		t.Errorf("Top: got %d want 2", len(col.Top))
	}
	// "true" appears twice, "false" once → true must come first.
	if col.Top[0].Value != true || col.Top[0].Count != 2 {
		t.Errorf("Top[0]: got %+v want {true, 2}", col.Top[0])
	}
}

func TestBuildCompactResult_AllTimestamp_TimeTime(t *testing.T) {
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	rows := []map[string]any{
		{"ts": t2}, {"ts": t1}, {"ts": t2},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "ts")
	if col.Kind != ColumnKindTimestamp {
		t.Errorf("Kind: got %q want timestamp", col.Kind)
	}
	if col.MinTime != t1.Format(time.RFC3339) {
		t.Errorf("MinTime: got %q want %q", col.MinTime, t1.Format(time.RFC3339))
	}
	if col.MaxTime != t2.Format(time.RFC3339) {
		t.Errorf("MaxTime: got %q want %q", col.MaxTime, t2.Format(time.RFC3339))
	}
}

func TestBuildCompactResult_AllNil(t *testing.T) {
	rows := []map[string]any{
		{"x": nil}, {"x": nil}, {"x": nil},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")
	if col.Kind != ColumnKindNull {
		t.Errorf("Kind: got %q want null", col.Kind)
	}
	if col.NullCount != 3 {
		t.Errorf("NullCount: got %d want 3", col.NullCount)
	}
}

func TestBuildCompactResult_MixedTypes(t *testing.T) {
	rows := []map[string]any{
		{"x": 1}, {"x": "hello"},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")
	if col.Kind != ColumnKindMixed {
		t.Errorf("Kind: got %q want mixed", col.Kind)
	}
	if col.Min != nil || col.Max != nil || col.Median != nil {
		t.Errorf("mixed columns must carry no numeric statistics")
	}
}

func TestBuildCompactResult_TimestampStringValues(t *testing.T) {
	// RFC3339 strings stay strings — only time.Time is promoted to timestamp.
	rows := []map[string]any{
		{"ts": "2024-01-01T00:00:00Z"},
		{"ts": "2024-06-01T12:00:00Z"},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "ts")
	if col.Kind != ColumnKindString {
		t.Errorf("RFC3339-shaped strings must NOT auto-promote to timestamp; got kind %q", col.Kind)
	}
}

func TestBuildCompactResult_NumericPercentiles_Even(t *testing.T) {
	rows := []map[string]any{
		{"x": 1.0}, {"x": 2.0}, {"x": 3.0}, {"x": 4.0},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")

	expectFloat(t, "Min", col.Min, 1.0)
	expectFloat(t, "P25", col.P25, 1.75)
	expectFloat(t, "Median", col.Median, 2.5)
	expectFloat(t, "P75", col.P75, 3.25)
	expectFloat(t, "Max", col.Max, 4.0)
}

func TestBuildCompactResult_NumericPercentiles_Odd(t *testing.T) {
	rows := []map[string]any{
		{"x": 10.0}, {"x": 20.0}, {"x": 30.0}, {"x": 40.0}, {"x": 50.0},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")

	expectFloat(t, "Min", col.Min, 10.0)
	expectFloat(t, "P25", col.P25, 20.0)
	expectFloat(t, "Median", col.Median, 30.0)
	expectFloat(t, "P75", col.P75, 40.0)
	expectFloat(t, "Max", col.Max, 50.0)
}

func TestBuildCompactResult_NumericPercentiles_Single(t *testing.T) {
	rows := []map[string]any{{"x": 42.0}}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")
	expectFloat(t, "Min", col.Min, 42.0)
	expectFloat(t, "P25", col.P25, 42.0)
	expectFloat(t, "Median", col.Median, 42.0)
	expectFloat(t, "P75", col.P75, 42.0)
	expectFloat(t, "Max", col.Max, 42.0)
}

func TestBuildCompactResult_NumericWithNils(t *testing.T) {
	rows := []map[string]any{
		{"x": 1.0}, {"x": nil}, {"x": 3.0}, {"x": nil}, {"x": 5.0},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")
	if col.Kind != ColumnKindNumber {
		t.Errorf("Kind: got %q want number", col.Kind)
	}
	if col.NullCount != 2 {
		t.Errorf("NullCount: got %d want 2", col.NullCount)
	}
	expectFloat(t, "Min", col.Min, 1.0)
	expectFloat(t, "Max", col.Max, 5.0)
}

func TestBuildCompactResult_StringLowCardinality_Top3(t *testing.T) {
	rows := []map[string]any{
		{"s": "a"}, {"s": "b"}, {"s": "a"}, {"s": "c"}, {"s": "a"}, {"s": "b"},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "s")
	if col.Distinct != 3 {
		t.Errorf("Distinct: got %d want 3", col.Distinct)
	}
	if len(col.Top) != 3 {
		t.Fatalf("Top: got %d want 3", len(col.Top))
	}
	if col.Top[0].Value != "a" || col.Top[0].Count != 3 {
		t.Errorf("Top[0]: got %+v want {a, 3}", col.Top[0])
	}
	if col.Top[1].Value != "b" || col.Top[1].Count != 2 {
		t.Errorf("Top[1]: got %+v want {b, 2}", col.Top[1])
	}
	if col.Top[2].Value != "c" || col.Top[2].Count != 1 {
		t.Errorf("Top[2]: got %+v want {c, 1}", col.Top[2])
	}
}

func TestBuildCompactResult_StringLowCardinality_TieOrder(t *testing.T) {
	// Three values, all freq=1: ties must break in lexical order.
	rows := []map[string]any{{"s": "c"}, {"s": "a"}, {"s": "b"}}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "s")
	if len(col.Top) != 3 {
		t.Fatalf("Top: got %d want 3", len(col.Top))
	}
	if col.Top[0].Value != "a" || col.Top[1].Value != "b" || col.Top[2].Value != "c" {
		t.Errorf("Tie-broken order: got [%v %v %v] want [a b c]",
			col.Top[0].Value, col.Top[1].Value, col.Top[2].Value)
	}
}

func TestBuildCompactResult_StringHighCardinality_NoTop(t *testing.T) {
	rows := make([]map[string]any, 0, TopValueCardinality+1)
	for i := 0; i < TopValueCardinality+1; i++ {
		rows = append(rows, map[string]any{"id": "user_" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "id")
	if col.Distinct < TopValueCardinality+1 {
		t.Errorf("Distinct: got %d want >= %d", col.Distinct, TopValueCardinality+1)
	}
	if len(col.Top) != 0 {
		t.Errorf("Top must be empty when distinct exceeds TopValueCardinality (%d), got %d entries",
			TopValueCardinality, len(col.Top))
	}
}

func TestBuildCompactResult_StringWithNils(t *testing.T) {
	rows := []map[string]any{
		{"s": "a"}, {"s": nil}, {"s": "a"}, {"s": nil},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "s")
	if col.NullCount != 2 {
		t.Errorf("NullCount: got %d want 2", col.NullCount)
	}
	if col.Distinct != 1 {
		t.Errorf("Distinct: got %d want 1 (nils don't count toward Distinct)", col.Distinct)
	}
}

func TestBuildCompactResult_HeadFirstFiveRows(t *testing.T) {
	rows := numericRows("x", 50)
	got := BuildCompactResult(rows)
	for i, head := range got.HeadRows {
		want := i
		if v, _ := toFloat64(head["x"]); int(v) != want {
			t.Errorf("HeadRows[%d][x]: got %v want %d", i, head["x"], want)
		}
	}
}

func TestBuildCompactResult_TailLastFiveRows(t *testing.T) {
	rows := numericRows("x", 50)
	got := BuildCompactResult(rows)
	for i, tail := range got.TailRows {
		want := 50 - HeadTailRowCount + i
		if v, _ := toFloat64(tail["x"]); int(v) != want {
			t.Errorf("TailRows[%d][x]: got %v want %d", i, tail["x"], want)
		}
	}
}

func TestBuildCompactResult_Deterministic(t *testing.T) {
	rows := []map[string]any{
		{"a": 1, "b": "x"}, {"a": 2, "b": "y"}, {"a": 3, "b": "x"},
	}
	first := BuildCompactResult(rows)
	second := BuildCompactResult(rows)

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal first: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("Marshal second: %v", err)
	}
	if !reflect.DeepEqual(first.Columns, second.Columns) {
		t.Errorf("Columns differ across runs:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func TestBuildCompactResult_NaN_Inf_Excluded(t *testing.T) {
	rows := []map[string]any{
		{"x": 1.0},
		{"x": math.NaN()},
		{"x": math.Inf(1)},
		{"x": math.Inf(-1)},
		{"x": 5.0},
	}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")

	if col.Kind != ColumnKindNumber {
		t.Errorf("Kind: got %q want number", col.Kind)
	}
	expectFloat(t, "Min", col.Min, 1.0)
	expectFloat(t, "Max", col.Max, 5.0)
	if col.NullCount != 3 {
		t.Errorf("NullCount: got %d want 3 (NaN, +Inf, -Inf must be counted as null)", col.NullCount)
	}
}

func TestBuildCompactResult_HeadAtSmallSize(t *testing.T) {
	rows := numericRows("x", 3)
	got := BuildCompactResult(rows)
	if len(got.HeadRows) != 3 {
		t.Errorf("HeadRows: got %d want 3 (full result fits in head)", len(got.HeadRows))
	}
	if got.TailRows != nil {
		t.Errorf("TailRows must be nil for 3-row result")
	}
	if len(got.AllRows) != 3 {
		t.Errorf("AllRows: got %d want 3", len(got.AllRows))
	}
}

func TestBuildCompactResult_RowsAreCloned(t *testing.T) {
	rows := []map[string]any{{"x": 1}, {"x": 2}}
	got := BuildCompactResult(rows)
	if len(got.AllRows) != 2 {
		t.Fatalf("AllRows: got %d want 2", len(got.AllRows))
	}
	rows[0]["x"] = 999
	if got.AllRows[0]["x"] == 999 {
		t.Errorf("AllRows must not share storage with the input slice — caller mutation leaked through")
	}
}

func TestBuildCompactResult_ColumnOrderStableAcrossInputs(t *testing.T) {
	// A column missing from row 1 but present in row 0 must still be
	// surfaced; the union is the column set.
	rows := []map[string]any{
		{"a": 1, "b": 2},
		{"a": 3},
	}
	got := BuildCompactResult(rows)
	names := make([]string, 0, len(got.Columns))
	for _, c := range got.Columns {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("Column order: got %v want [a b] (sorted within first row)", names)
	}
}

func TestBuildCompactResult_BooleanOnlyTrue(t *testing.T) {
	rows := []map[string]any{{"b": true}, {"b": true}}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "b")
	if col.Kind != ColumnKindBoolean {
		t.Errorf("Kind: got %q want boolean", col.Kind)
	}
	if col.Distinct != 1 {
		t.Errorf("Distinct: got %d want 1 (only true seen)", col.Distinct)
	}
	if len(col.Top) != 1 || col.Top[0].Value != true || col.Top[0].Count != 2 {
		t.Errorf("Top: got %+v want [{true, 2}]", col.Top)
	}
}

func TestBuildCompactResult_BooleanTieFalseFirst(t *testing.T) {
	// Equal counts: lexical "false" < "true" tie-break must put false first.
	rows := []map[string]any{{"b": true}, {"b": false}}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "b")
	if len(col.Top) != 2 {
		t.Fatalf("Top: got %d want 2", len(col.Top))
	}
	if col.Top[0].Value != false || col.Top[1].Value != true {
		t.Errorf("tie-break: got [%v %v] want [false true]", col.Top[0].Value, col.Top[1].Value)
	}
}

func TestKindOf_UnsupportedTypePromotesToMixed(t *testing.T) {
	// A column whose only value is a slice — not in the supported set —
	// must surface as Mixed, not panic.
	rows := []map[string]any{{"x": []int{1, 2, 3}}}
	got := BuildCompactResult(rows)
	col := findColumn(t, got, "x")
	if col.Kind != ColumnKindMixed {
		t.Errorf("Kind: got %q want mixed (unsupported type)", col.Kind)
	}
}

func TestToFloat64_NonNumericReturnsFalse(t *testing.T) {
	cases := []any{"42", true, time.Time{}, []byte{1}, nil, struct{}{}}
	for _, v := range cases {
		if _, ok := toFloat64(v); ok {
			t.Errorf("toFloat64(%T) must return ok=false", v)
		}
	}
}

func TestToFloat64_AllNumericWidthsRoundTrip(t *testing.T) {
	cases := []any{
		int(3), int8(3), int16(3), int32(3), int64(3),
		uint(3), uint8(3), uint16(3), uint32(3), uint64(3),
		float32(3.0), float64(3.0),
	}
	for _, v := range cases {
		f, ok := toFloat64(v)
		if !ok || f != 3.0 {
			t.Errorf("toFloat64(%T = %v) = (%v, %v); want (3, true)", v, v, f, ok)
		}
	}
}

func TestPercentile_Single(t *testing.T) {
	got := percentile([]float64{42}, 0.5)
	if got != 42 {
		t.Errorf("percentile single: got %v want 42", got)
	}
}

func TestPercentile_ExactPosition(t *testing.T) {
	// Sorted slice with q*(n-1) landing exactly on an integer index;
	// hits the low==high branch without interpolation.
	sorted := []float64{0, 10, 20, 30, 40}
	if got := percentile(sorted, 0.5); got != 20 {
		t.Errorf("percentile exact: got %v want 20", got)
	}
}

func TestSummarizeTimestamp_MixedNonTimeIsCountedAsNull(t *testing.T) {
	// A column inferred as Timestamp can't have non-time.Time values
	// in its non-nil set (inferKind would have flagged Mixed). To
	// exercise the defensive nil branch in summarizeTimestamp we
	// call it directly.
	cs := ColumnSummary{Kind: ColumnKindTimestamp}
	t1 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	summarizeTimestamp(&cs, []any{t1, "not-a-time"})
	if cs.NullCount != 1 {
		t.Errorf("NullCount: got %d want 1 (defensive non-time path)", cs.NullCount)
	}
	if cs.MinTime == "" || cs.MaxTime == "" {
		t.Errorf("MinTime/MaxTime should be populated from the one valid time, got %q/%q", cs.MinTime, cs.MaxTime)
	}
}

func TestSummarizeBoolean_AllNonBoolIsNullCount(t *testing.T) {
	// Defensive branch: kindOf would never produce ColumnKindBoolean
	// for non-bool values, but exercise the `!ok` path directly.
	cs := ColumnSummary{Kind: ColumnKindBoolean}
	summarizeBoolean(&cs, []any{"true", 1})
	if cs.NullCount != 2 {
		t.Errorf("NullCount: got %d want 2", cs.NullCount)
	}
	if cs.Distinct != 0 || len(cs.Top) != 0 {
		t.Errorf("no booleans seen → Distinct/Top should remain zero, got %d/%d", cs.Distinct, len(cs.Top))
	}
}

func TestSummarizeString_NilColumnLeavesTopEmpty(t *testing.T) {
	cs := ColumnSummary{}
	summarizeString(&cs, []any{nil, nil})
	if cs.Distinct != 0 || len(cs.Top) != 0 {
		t.Errorf("all-nil string column: got distinct=%d top=%d", cs.Distinct, len(cs.Top))
	}
	if cs.NullCount != 2 {
		t.Errorf("NullCount: got %d want 2", cs.NullCount)
	}
}

func TestSummarizeNumeric_AllInfsLeaveStatsNil(t *testing.T) {
	cs := ColumnSummary{}
	summarizeNumeric(&cs, []any{math.Inf(1), math.Inf(-1), math.NaN()})
	if cs.Min != nil || cs.Max != nil || cs.Median != nil {
		t.Errorf("no finite values → all stats must be nil, got Min=%v Max=%v", cs.Min, cs.Max)
	}
	if cs.NullCount != 3 {
		t.Errorf("NullCount: got %d want 3", cs.NullCount)
	}
}

func TestInferKind_AllNilReturnsNull(t *testing.T) {
	// Defensive path — summarizeColumn already filters nils before
	// calling inferKind, but exercise the empty-slice branch directly.
	if got := inferKind(nil); got != ColumnKindNull {
		t.Errorf("inferKind(nil) = %q want null", got)
	}
}

// --- helpers ---

func numericRows(col string, n int) []map[string]any {
	rows := make([]map[string]any, n)
	for i := 0; i < n; i++ {
		rows[i] = map[string]any{col: i}
	}
	return rows
}

func findColumn(t *testing.T, got CompactResult, name string) ColumnSummary {
	t.Helper()
	for _, c := range got.Columns {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("column %q not found in summary; got %v", name, got.Columns)
	return ColumnSummary{}
}

func expectFloat(t *testing.T, label string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s: got nil want %v", label, want)
		return
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s: got %v want %v", label, *got, want)
	}
}
