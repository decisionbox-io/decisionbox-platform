package database

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Unit tests for capSampleData / capSampleValue / capSampleString /
// capSampleBinary. The integration suite (build tag `integration`)
// covers the full Save round-trip against a live Mongo container;
// these tests pin the truncation contract without paying the
// testcontainer startup cost.

func TestCapSampleString_UnderLimitPassesThrough(t *testing.T) {
	in := strings.Repeat("a", schemaCacheSampleValueMaxRunes)
	got := capSampleString(in)
	if got != in {
		t.Errorf("string at the rune limit should pass through, got %v", got)
	}
}

func TestCapSampleString_OverLimitTruncatedAtRuneBoundary(t *testing.T) {
	in := strings.Repeat("a", schemaCacheSampleValueMaxRunes*4)
	got := capSampleString(in)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated value must be valid UTF-8")
	}
	if !strings.Contains(got, "truncated, original") {
		t.Errorf("missing truncation marker in %q", got)
	}
	prefix := strings.Repeat("a", schemaCacheSampleValueMaxRunes)
	if !strings.HasPrefix(got, prefix) {
		t.Errorf("prefix mismatch — first %d runes must be the original head", schemaCacheSampleValueMaxRunes)
	}
	// Marker carries the original rune count.
	if !strings.Contains(got, strconv.Itoa(len(in))) {
		t.Errorf("marker should report original rune count %d, got %q", len(in), got)
	}
}

// TestCapSampleString_NeverSplitsMultibyteRune is the regression for
// the Copilot finding: byte-length truncation can cut a UTF-8 codepoint
// mid-sequence, producing an invalid UTF-8 string that BSON rejects.
// The fix counts runes; this test puts the cap exactly at a rune that
// is multi-byte (Turkish "ı" is 2 bytes) and asserts the result
// validates.
func TestCapSampleString_NeverSplitsMultibyteRune(t *testing.T) {
	// Build an exactly-(N+1)-rune string where every rune is 2 bytes,
	// so byte-length slicing at the boundary would land mid-rune.
	s := strings.Repeat("ı", schemaCacheSampleValueMaxRunes+1)
	got := capSampleString(s)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated UTF-8 must remain valid; got bytes %x", []byte(got))
	}
	if !strings.Contains(got, "truncated, original") {
		t.Error("truncation marker missing")
	}
	if !strings.Contains(got, strconv.Itoa(schemaCacheSampleValueMaxRunes+1)) {
		t.Errorf("original count should report runes (%d), got %q", schemaCacheSampleValueMaxRunes+1, got)
	}
}

// TestCapSampleString_RuneCountVsByteCount also catches the
// byte-vs-rune confusion: a string with many multi-byte runes whose
// byte length exceeds the limit but whose rune count is under it
// should pass through unchanged.
func TestCapSampleString_RuneCountVsByteCount(t *testing.T) {
	// 200 runes × 2 bytes = 400 bytes > 256 byte cap, but only 200 runes.
	in := strings.Repeat("ş", 200)
	got := capSampleString(in)
	if got != in {
		t.Errorf("string under the rune cap should pass through unchanged regardless of byte length")
	}
}

func TestCapSampleBinary_UTF8BytesTreatedAsString(t *testing.T) {
	// Valid UTF-8 bytes should go through capSampleString, not the
	// base64 path. Verify by checking the marker format.
	in := []byte(strings.Repeat("a", schemaCacheSampleValueMaxRunes*3))
	got, ok := capSampleValue(in).(string)
	if !ok {
		t.Fatalf("got %T, want string", got)
	}
	if !strings.Contains(got, "truncated, original") {
		t.Errorf("UTF-8 bytes should use the text marker, got %q", got)
	}
	if strings.Contains(got, "base64") {
		t.Errorf("UTF-8 bytes should NOT take the binary base64 path, got %q", got)
	}
}

// TestCapSampleBinary_NonUTF8BytesBase64 is the regression for the
// Copilot finding: invalid UTF-8 bytes (e.g. JPEG header, VARBINARY
// payload) cannot be stored as a BSON string without corruption. The
// fix base64-encodes the prefix.
func TestCapSampleBinary_NonUTF8BytesBase64(t *testing.T) {
	// 0xFF 0xFE is invalid UTF-8 (continuation byte without leader).
	bin := []byte{0xFF, 0xFE, 0xFD, 0xFC}
	for i := 0; i < 100; i++ {
		bin = append(bin, byte(i))
	}
	got, ok := capSampleValue(bin).(string)
	if !ok {
		t.Fatalf("got %T, want string", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("base64 output must be valid UTF-8")
	}
	// Under the limit, no truncation marker.
	if strings.Contains(got, "truncated") {
		t.Errorf("under-limit binary should not be truncated: %q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("output must round-trip through base64: %v", err)
	}
	if string(decoded) != string(bin) {
		t.Error("under-limit binary must round-trip exactly through base64")
	}
}

func TestCapSampleBinary_NonUTF8OverLimitTruncated(t *testing.T) {
	bin := make([]byte, schemaCacheSampleBinaryMaxBytes*4)
	for i := range bin {
		bin[i] = byte(0x80 + (i % 64)) // non-UTF-8 high bytes
	}
	got, ok := capSampleValue(bin).(string)
	if !ok {
		t.Fatalf("got %T, want string", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("base64 output must be valid UTF-8 even when truncated")
	}
	if !strings.Contains(got, "truncated binary, original") {
		t.Errorf("missing binary truncation marker in %q", got)
	}
	if !strings.Contains(got, "base64") {
		t.Errorf("binary marker should advertise base64 format, got %q", got)
	}
	if !strings.Contains(got, strconv.Itoa(len(bin))) {
		t.Errorf("marker should report original byte count %d, got %q", len(bin), got)
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
	for i := 0; i < schemaCacheSampleRowLimit; i++ {
		if got[i]["k"] != i {
			t.Errorf("row %d preserved order, got k=%v", i, got[i]["k"])
		}
	}
}

func TestCapSampleData_ValuesTruncatedPerRow(t *testing.T) {
	huge := strings.Repeat("x", schemaCacheSampleValueMaxRunes*5)
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
	huge := strings.Repeat("y", schemaCacheSampleValueMaxRunes*2)
	rows := []map[string]interface{}{{"a": huge}}
	_ = capSampleData(rows)
	if rows[0]["a"] != huge {
		t.Errorf("input row mutated — capSampleData must return a copy")
	}
}

// TestCapSampleData_BoundsTotalDocSize is a sanity ceiling: even an
// adversarial table (max rows × wide column count × max-length
// strings each) marshals to a byte count well under the 16 MB BSON
// cap. Acts as a regression canary if anyone bumps the per-row or
// per-value limits without budgeting against the doc cap.
func TestCapSampleData_BoundsTotalDocSize(t *testing.T) {
	const adversarialColumns = 1000
	huge := strings.Repeat("Q", schemaCacheSampleValueMaxRunes*100)
	row := make(map[string]interface{}, adversarialColumns)
	for i := 0; i < adversarialColumns; i++ {
		// Each column key includes the index so the map actually
		// holds adversarialColumns distinct keys (the prior version
		// of this test only varied by i%26 and so silently collapsed
		// to ~26 entries — found by Copilot review on PR #195).
		key := "col_" + strconv.Itoa(i) + "_" + strings.Repeat("X", 30)
		row[key] = huge
	}
	rows := []map[string]interface{}{row, row, row, row, row, row}
	got := capSampleData(rows)

	// Crude size estimate — sum of every value's len().
	total := 0
	for _, r := range got {
		if len(r) != adversarialColumns {
			t.Fatalf("column count after cap = %d, want %d (keys collapsed)", len(r), adversarialColumns)
		}
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
