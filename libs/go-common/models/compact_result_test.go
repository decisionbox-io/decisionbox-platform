package models

import (
	"bytes"
	"encoding/json"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestCompactInlineThreshold_Value(t *testing.T) {
	if CompactInlineThreshold != 20 {
		t.Fatalf("CompactInlineThreshold = %d, want 20 (locked by callers + tests)", CompactInlineThreshold)
	}
}

func TestTopValueCardinality_Value(t *testing.T) {
	if TopValueCardinality != 20 {
		t.Fatalf("TopValueCardinality = %d, want 20 (locked by callers + tests)", TopValueCardinality)
	}
}

func TestHeadTailRowCount_Value(t *testing.T) {
	if HeadTailRowCount != 5 {
		t.Fatalf("HeadTailRowCount = %d, want 5 (locked by callers + tests)", HeadTailRowCount)
	}
}

func TestColumnKind_Constants(t *testing.T) {
	all := []ColumnKind{
		ColumnKindNumber,
		ColumnKindString,
		ColumnKindBoolean,
		ColumnKindTimestamp,
		ColumnKindNull,
		ColumnKindMixed,
	}
	seen := make(map[ColumnKind]bool, len(all))
	for _, k := range all {
		if k == "" {
			t.Errorf("ColumnKind constant is empty")
		}
		if seen[k] {
			t.Errorf("ColumnKind value %q duplicated", k)
		}
		seen[k] = true
	}
}

func TestColumnSummary_BSON_RoundTrip(t *testing.T) {
	min := 1.5
	med := 7.0
	p25 := 3.0
	p75 := 12.0
	max := 100.0
	original := ColumnSummary{
		Name:      "amount",
		Kind:      ColumnKindNumber,
		NullCount: 4,
		Distinct:  17,
		Min:       &min,
		P25:       &p25,
		Median:    &med,
		P75:       &p75,
		Max:       &max,
		MinTime:   "",
		MaxTime:   "",
		Top: []ValueCount{
			{Value: "USD", Count: 5},
			{Value: "EUR", Count: 3},
		},
	}

	data, err := bson.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundtrip ColumnSummary
	if err := bson.Unmarshal(data, &roundtrip); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if roundtrip.Name != original.Name {
		t.Errorf("Name: got %q want %q", roundtrip.Name, original.Name)
	}
	if roundtrip.Kind != original.Kind {
		t.Errorf("Kind: got %q want %q", roundtrip.Kind, original.Kind)
	}
	if roundtrip.NullCount != original.NullCount {
		t.Errorf("NullCount: got %d want %d", roundtrip.NullCount, original.NullCount)
	}
	if roundtrip.Distinct != original.Distinct {
		t.Errorf("Distinct: got %d want %d", roundtrip.Distinct, original.Distinct)
	}
	if roundtrip.Min == nil || *roundtrip.Min != min {
		t.Errorf("Min: got %v want %v", roundtrip.Min, min)
	}
	if roundtrip.Max == nil || *roundtrip.Max != max {
		t.Errorf("Max: got %v want %v", roundtrip.Max, max)
	}
	if roundtrip.Median == nil || *roundtrip.Median != med {
		t.Errorf("Median: got %v want %v", roundtrip.Median, med)
	}
	if len(roundtrip.Top) != 2 {
		t.Fatalf("Top: got %d entries want 2", len(roundtrip.Top))
	}
}

func TestColumnSummary_BSON_NilNumericPointersOmitted(t *testing.T) {
	cs := ColumnSummary{Name: "n", Kind: ColumnKindString, Distinct: 0}
	data, err := bson.Marshal(cs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// "min" / "max" / etc. should not appear in the marshaled doc when nil.
	if bytes.Contains(data, []byte("min")) {
		t.Errorf("nil Min must be omitted from BSON, got: %x", data)
	}
	if bytes.Contains(data, []byte("median")) {
		t.Errorf("nil Median must be omitted from BSON, got: %x", data)
	}
}

func TestColumnSummary_JSON_NilNumericPointersOmitted(t *testing.T) {
	cs := ColumnSummary{Name: "n", Kind: ColumnKindString}
	data, err := json.Marshal(cs)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(data, []byte("min")) {
		t.Errorf("nil Min must be omitted from JSON, got: %s", data)
	}
}
