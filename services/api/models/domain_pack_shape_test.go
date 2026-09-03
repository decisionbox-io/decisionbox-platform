package models

import (
	"encoding/json"
	"strings"
	"testing"

	warehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"go.mongodb.org/mongo-driver/bson"
)

// TestEffectiveShape_AbsentIsEntities pins the default that makes the
// existing corpus readable. Every pack persisted before shape was recorded
// carries no value, and they are all table-shaped; resolving that to "no
// shape" would exclude all of them from any decision that branches on it.
func TestEffectiveShape_AbsentIsEntities(t *testing.T) {
	var pack DomainPack
	if got := pack.EffectiveShape(); got != warehouse.ShapeEntities {
		t.Errorf("an undeclared shape = %q, want %q", got, warehouse.ShapeEntities)
	}
}

// TestEffectiveShape_DeclaredWins covers both spellings, including the one
// that equals the default — a pack that says "entities" must not be
// distinguishable from one that says nothing, or the two would take
// different paths for no reason a reader could see.
func TestEffectiveShape_DeclaredWins(t *testing.T) {
	for _, shape := range []warehouse.SourceShape{warehouse.ShapeEntities, warehouse.ShapeCube} {
		pack := DomainPack{Shape: shape}
		if got := pack.EffectiveShape(); got != shape {
			t.Errorf("declared %q resolved to %q", shape, got)
		}
	}
}

// TestShape_AbsentStaysAbsentInBSON is the writer half of the contract the
// readers depend on. A pack with no shape must persist with the field
// missing, not as an empty string: a query for table-shaped packs has to
// match the whole existing corpus, and it can only be written to match both
// spellings if it knows which ones can occur.
func TestShape_AbsentStaysAbsentInBSON(t *testing.T) {
	raw, err := bson.Marshal(DomainPack{Slug: "gaming"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m bson.M
	if err := bson.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["shape"]; present {
		t.Errorf("a pack that declares no shape persisted shape=%v; it must be absent", m["shape"])
	}
}

// TestShape_RoundTripsThroughBSONAndJSON guards the two round trips a pack
// actually makes: the collection it is stored in, and the portable export
// the import route reads back. A field dropped by either would be silently
// erased by an ordinary edit rather than reported.
func TestShape_RoundTripsThroughBSONAndJSON(t *testing.T) {
	pack := *testDomainPack("analytics", "web")
	pack.Shape = warehouse.ShapeCube

	raw, err := bson.Marshal(pack)
	if err != nil {
		t.Fatalf("bson marshal: %v", err)
	}
	var fromBSON DomainPack
	if err := bson.Unmarshal(raw, &fromBSON); err != nil {
		t.Fatalf("bson unmarshal: %v", err)
	}
	if fromBSON.Shape != warehouse.ShapeCube {
		t.Errorf("bson round trip lost the shape: got %q", fromBSON.Shape)
	}

	encoded, err := json.Marshal(pack)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	var fromJSON DomainPack
	if err := json.Unmarshal(encoded, &fromJSON); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if fromJSON.Shape != warehouse.ShapeCube {
		t.Errorf("json round trip lost the shape: got %q", fromJSON.Shape)
	}
}

// TestValidateDomainPack_RejectsAnUnknownShape covers the typo that would
// otherwise persist and then match nothing wherever shape is compared —
// a feature quietly not working, with no error to connect it to.
func TestValidateDomainPack_RejectsAnUnknownShape(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	pack.Shape = "cubes"
	err := ValidateDomainPack(pack)
	if err == nil {
		t.Fatal("a pack declaring an unknown shape should be rejected")
	}
	// The message has to name the value AND the legal ones, or the author
	// has to go read the source to fix a typo.
	for _, want := range []string{"cubes", string(warehouse.ShapeEntities), string(warehouse.ShapeCube)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestValidateDomainPack_AcceptsEveryKnownShapeAndAbsence pins the other
// side: the check must not reject a pack that says nothing (the whole
// existing corpus) or one that names a real shape.
func TestValidateDomainPack_AcceptsEveryKnownShapeAndAbsence(t *testing.T) {
	for _, shape := range []warehouse.SourceShape{"", warehouse.ShapeEntities, warehouse.ShapeCube} {
		pack := testDomainPack("gaming", "match3")
		pack.Shape = shape
		if err := ValidateDomainPack(pack); err != nil {
			t.Errorf("shape %q should be accepted: %v", shape, err)
		}
	}
}
