package schema_retrieve

import (
	"testing"

	pb "github.com/qdrant/go-client/qdrant"
)

// roundTrip pushes a blurb through the payload encoding and back, the way an
// index write followed by a search read does.
func roundTrip(t *testing.T, b TableBlurb) TableBlurb {
	t.Helper()
	raw := payloadFromBlurb(b, "p1", "wh1")
	encoded, err := pb.TryValueMap(raw)
	if err != nil {
		t.Fatalf("payload encode: %v", err)
	}
	return blurbFromPayload(encoded)
}

// TestKindRoundTrips_Table is the compatibility guard. Every point written
// before kinds existed carries none, and they are all tables — so a table must
// still round-trip to the empty kind and must still be dataset-qualified on
// read, or existing hits stop matching the schemas map and are dropped.
func TestKindRoundTrips_Table(t *testing.T) {
	got := roundTrip(t, TableBlurb{
		Table:   "sales.orders",
		Dataset: "sales",
		Blurb:   "Orders placed.",
	})
	if got.Kind != "" {
		t.Errorf("Kind = %q, want empty for a table", got.Kind)
	}
	if got.Table != "sales.orders" {
		t.Errorf("Table = %q, want the qualified form rehydrated", got.Table)
	}
}

// TestKindRoundTrips_CatalogItem is the one that matters for a cube source. A
// catalog item's ref is what a query must write verbatim; qualifying it with a
// dataset would store — and return — a name the source does not have, and the
// query built from it would fail.
func TestKindRoundTrips_CatalogItem(t *testing.T) {
	for _, kind := range []string{"dimension", "metric"} {
		t.Run(kind, func(t *testing.T) {
			got := roundTrip(t, TableBlurb{
				Table: "sessionDefaultChannelGroup",
				Kind:  kind,
				Blurb: "The channel group that started the session.",
			})
			if got.Kind != kind {
				t.Errorf("Kind = %q, want %q", got.Kind, kind)
			}
			if got.Table != "sessionDefaultChannelGroup" {
				t.Errorf("Table = %q, want the ref verbatim", got.Table)
			}
		})
	}
}

// TestCatalogItemRefIsNotQualifiedEvenWithADataset covers the trap directly: a
// catalog point that somehow carries a dataset must still return its ref
// unchanged. Prefixing would produce "prop.sessions", which names nothing.
func TestCatalogItemRefIsNotQualifiedEvenWithADataset(t *testing.T) {
	got := roundTrip(t, TableBlurb{
		Table:   "sessions",
		Dataset: "prop",
		Kind:    "metric",
		Blurb:   "Sessions.",
	})
	if got.Table != "sessions" {
		t.Errorf("Table = %q, want %q — a catalog ref must never be dataset-qualified", got.Table, "sessions")
	}
}

// TestTableRefWithADotIsNotDoubleQualified pins the pre-existing behaviour the
// kind branch must not disturb: an already-qualified table ref is left alone.
func TestTableRefWithADotIsNotDoubleQualified(t *testing.T) {
	got := roundTrip(t, TableBlurb{
		Table:   "dataproj.census.variables",
		Dataset: "census",
		Blurb:   "Census variables.",
	})
	if got.Table != "dataproj.census.variables" {
		t.Errorf("Table = %q, want the three-part name untouched", got.Table)
	}
}
