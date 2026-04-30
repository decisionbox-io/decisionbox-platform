package database

import (
	"strings"
	"testing"
)

// Unit tests for capSampleData / capSampleValue. The integration suite
// (build tag `integration`) covers the full Save round-trip against a
// live Mongo container; these tests pin the truncation contract
// without paying the testcontainer startup cost.

func TestCapSampleValue_StringUnderLimitPassesThrough(t *testing.T) {
	in := strings.Repeat("a", schemaCacheSampleValueMaxChars)
	got := capSampleValue(in)
	if got != in {
		t.Errorf("string at the limit should pass through, got %T %v", got, got)
	}
}

func TestCapSampleValue_StringOverLimitTruncated(t *testing.T) {
	in := strings.Repeat("a", schemaCacheSampleValueMaxChars*4)
	got, ok := capSampleValue(in).(string)
	if !ok {
		t.Fatalf("got %T, want string", got)
	}
	if len(got) <= schemaCacheSampleValueMaxChars {
		t.Errorf("truncated value len = %d, want > %d (prefix + marker)", len(got), schemaCacheSampleValueMaxChars)
	}
	// Marker carries the original length so a downstream reader can
	// see how much was dropped.
	if !strings.Contains(got, "truncated, original") {
		t.Errorf("missing truncation marker in %q", got)
	}
	prefix := strings.Repeat("a", schemaCacheSampleValueMaxChars)
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("prefix mismatch — first %d chars must be the original head", schemaCacheSampleValueMaxChars)
	}
}

func TestCapSampleValue_ByteSliceOverLimitTruncated(t *testing.T) {
	in := []byte(strings.Repeat("z", schemaCacheSampleValueMaxChars*3))
	got, ok := capSampleValue(in).(string)
	if !ok {
		t.Fatalf("byte slice over limit should become string, got %T", got)
	}
	if !strings.Contains(got, "truncated, original") {
		t.Errorf("missing truncation marker in %q", got)
	}
}

func TestCapSampleValue_NonStringPassesThrough(t *testing.T) {
	cases := []interface{}{42, int64(1234567890), 3.14, true, false, nil}
	for _, c := range cases {
		if got := capSampleValue(c); got != c {
			t.Errorf("non-string value %v (%T) modified to %v (%T)", c, c, got, got)
		}
	}
}

func TestCapSampleData_NilInputReturnsNil(t *testing.T) {
	if got := capSampleData(nil); got != nil {
		t.Errorf("capSampleData(nil) = %v, want nil so BSON omitempty drops the field", got)
	}
}

func TestCapSampleData_RowCountCapped(t *testing.T) {
	rows := make([]map[string]interface{}, schemaCacheSampleRowLimit*3)
	for i := range rows {
		rows[i] = map[string]interface{}{"k": i}
	}
	got := capSampleData(rows)
	if len(got) != schemaCacheSampleRowLimit {
		t.Errorf("rows = %d, want %d", len(got), schemaCacheSampleRowLimit)
	}
	// Cap takes the first N rows so the original ordering is preserved.
	for i := 0; i < schemaCacheSampleRowLimit; i++ {
		if got[i]["k"] != i {
			t.Errorf("row %d preserved order, got k=%v", i, got[i]["k"])
		}
	}
}

func TestCapSampleData_ValuesTruncatedPerRow(t *testing.T) {
	huge := strings.Repeat("x", schemaCacheSampleValueMaxChars*5)
	short := "ok"
	rows := []map[string]interface{}{
		{"a": huge, "b": short, "c": 7},
		{"a": "another " + huge},
	}
	got := capSampleData(rows)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	if v, _ := got[0]["a"].(string); !strings.Contains(v, "truncated, original") {
		t.Errorf("row 0 col a should be truncated")
	}
	if got[0]["b"] != short {
		t.Errorf("row 0 col b should pass through unchanged")
	}
	if got[0]["c"] != 7 {
		t.Errorf("row 0 col c (int) should pass through unchanged")
	}
	if v, _ := got[1]["a"].(string); !strings.Contains(v, "truncated, original") {
		t.Errorf("row 1 col a should be truncated")
	}
}

func TestCapSampleData_DoesNotMutateInput(t *testing.T) {
	huge := strings.Repeat("y", schemaCacheSampleValueMaxChars*2)
	rows := []map[string]interface{}{{"a": huge}}
	_ = capSampleData(rows)
	if rows[0]["a"] != huge {
		t.Errorf("input row mutated — capSampleData must return a copy")
	}
}

// TestCapSampleData_BoundsTotalDocSize is a sanity ceiling: even an
// adversarial table (max rows × wide column count × max-length
// strings each) marshals to a manageable byte count well under the
// 16 MB BSON cap. Acts as a regression canary if anyone bumps the
// per-row or per-value limits without budgeting against the doc cap.
func TestCapSampleData_BoundsTotalDocSize(t *testing.T) {
	const adversarialColumns = 1000
	huge := strings.Repeat("Q", schemaCacheSampleValueMaxChars*100)
	row := make(map[string]interface{}, adversarialColumns)
	for i := 0; i < adversarialColumns; i++ {
		row["col"+strings.Repeat("X", 30)+"_"+strings.Repeat("Y", 5)+"_"+string(rune('A'+i%26))] = huge
	}
	rows := []map[string]interface{}{row, row, row, row, row, row}
	got := capSampleData(rows)

	// Crude size estimate — sum of every value's len().
	total := 0
	for _, r := range got {
		for k, v := range r {
			total += len(k)
			if s, ok := v.(string); ok {
				total += len(s)
			}
		}
	}
	const sane = 5 * 1024 * 1024 // 5 MB safety budget; 16 MB BSON cap with margin.
	if total > sane {
		t.Errorf("capped sample data total ≈ %d bytes, want ≤ %d (cap regressed)", total, sane)
	}
}
