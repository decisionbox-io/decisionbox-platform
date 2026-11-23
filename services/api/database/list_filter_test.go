package database

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// TestListFilterApply. The exclusion is the DEFAULT, and the opt-out has to be
// asked for — that asymmetry is the whole point of the type, because for as
// long as these collections have existed Phase 9 has been stamping
// duplicate_of and nothing read it.
func TestListFilterApply(t *testing.T) {
	base := bson.M{"project_id": "p1", "severity": "high"}

	t.Run("the default excludes marked duplicates", func(t *testing.T) {
		got := ListFilter{}.apply(base)
		expr, ok := got["duplicate_of"]
		if !ok {
			t.Fatalf("no duplicate_of clause: %v", got)
		}
		if !reflect.DeepEqual(expr, notDuplicateExpr) {
			t.Errorf("duplicate_of = %v, want %v", expr, notDuplicateExpr)
		}
		// The caller's own clauses survive.
		if got["project_id"] != "p1" || got["severity"] != "high" {
			t.Errorf("the caller's filter was altered: %v", got)
		}
	})

	t.Run("opting in adds nothing", func(t *testing.T) {
		got := ListFilter{IncludeDuplicates: true}.apply(base)
		if _, ok := got["duplicate_of"]; ok {
			t.Errorf("include_duplicates still filtered: %v", got)
		}
		if !reflect.DeepEqual(got, base) {
			t.Errorf("got %v, want the caller's filter unchanged", got)
		}
	})

	t.Run("the caller's map is never mutated", func(t *testing.T) {
		// These filters are built inline at call sites, where a shared
		// mutation would be invisible — and a second call on the same map
		// would then carry a clause the caller did not write.
		mine := bson.M{"project_id": "p1"}
		_ = ListFilter{}.apply(mine)
		if _, leaked := mine["duplicate_of"]; leaked {
			t.Errorf("apply mutated the caller's map: %v", mine)
		}
		if len(mine) != 1 {
			t.Errorf("caller's map has %d keys, want 1", len(mine))
		}
	})

	t.Run("an empty base is a valid filter", func(t *testing.T) {
		got := ListFilter{}.apply(bson.M{})
		if len(got) != 1 {
			t.Errorf("got %v, want just the duplicate_of clause", got)
		}
	})

	t.Run("nil in the expression is what matches a MISSING key", func(t *testing.T) {
		// duplicate_of is `omitempty`, so a document never marked has no such
		// key at all — which is every document written before the field
		// existed. In MongoDB a null inside $in matches both an explicit null
		// and a missing field; matching on "" alone would have hidden every
		// existing deployment's entire corpus.
		in, ok := notDuplicateExpr["$in"].([]any)
		if !ok {
			t.Fatalf("$in is %T, want a slice", notDuplicateExpr["$in"])
		}
		var hasNil, hasEmpty bool
		for _, v := range in {
			if v == nil {
				hasNil = true
			}
			if s, isStr := v.(string); isStr && s == "" {
				hasEmpty = true
			}
		}
		if !hasNil {
			t.Error("no nil in $in — documents with no duplicate_of key would be excluded, i.e. all of them")
		}
		if !hasEmpty {
			t.Error("no \"\" in $in — a document explicitly cleared would be excluded")
		}
	})
}
