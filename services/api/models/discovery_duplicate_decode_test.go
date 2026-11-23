package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// The agent WRITES a discovery document and the API READS it, through two
// separately declared structs in two modules that cannot import each other. A
// field the agent writes and this struct does not declare is dropped silently
// on the way out: it reaches MongoDB and never reaches a screen, and nothing
// fails anywhere.
//
// That is how the duplicate markers were nearly shipped inert. Phase 9 mirrored
// them onto the embedded copy, three dashboard pages were taught to hide a
// restatement, and the API decoded the document into structs that did not carry
// the fields — so every page would have received nothing and shown every
// restatement, with all the tests passing.
//
// Narrow on purpose: this pins the two fields that bug was about rather than
// policing every field of both structs, which cannot be done from here without
// making one service depend on the other.
func TestDiscoveryDuplicateMarkersSurviveDecoding(t *testing.T) {
	t.Run("insight", func(t *testing.T) {
		raw, err := bson.Marshal(bson.M{
			"id": "ins-1", "name": "A restatement", "analysis_area": "pricing",
			"duplicate_of": "ins-canonical", "similarity_score": 0.93,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got Insight
		if err := bson.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.DuplicateOf != "ins-canonical" {
			t.Errorf("duplicate_of = %q, want ins-canonical — the API would drop the mark and the dashboard would show the restatement", got.DuplicateOf)
		}
		if got.SimilarityScore != 0.93 {
			t.Errorf("similarity_score = %v, want 0.93 — the reveal control shows this, so a reader can disagree with the suppression", got.SimilarityScore)
		}
	})

	t.Run("recommendation", func(t *testing.T) {
		raw, err := bson.Marshal(bson.M{
			"id": "rec-1", "title": "Do the thing again",
			"duplicate_of": "rec-canonical", "similarity_score": 0.9,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got Recommendation
		if err := bson.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.DuplicateOf != "rec-canonical" || got.SimilarityScore != 0.9 {
			t.Errorf("recommendation markers lost: %+v", got)
		}
	})

	t.Run("an unmarked document decodes as not-a-duplicate", func(t *testing.T) {
		// The fields are omitempty, so every document written before the mark
		// existed has no such key. Absence has to read as "not a restatement",
		// never as a zero that something treats as meaningful.
		raw, _ := bson.Marshal(bson.M{"id": "ins-2", "name": "An original"})
		var got Insight
		if err := bson.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.DuplicateOf != "" || got.SimilarityScore != 0 {
			t.Errorf("an unmarked insight decoded as %+v", got)
		}
	})
}
