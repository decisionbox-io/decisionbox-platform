package database

import "go.mongodb.org/mongo-driver/bson"

// ListFilter narrows a list or count query over the denormalised insight and
// recommendation collections.
//
// It exists so the default — exclude what has already been marked a duplicate —
// is what every caller gets without asking, and the exception has to be asked
// for explicitly at the call site. Phase 9 has been detecting near-duplicates
// and stamping duplicate_of for as long as the collections have existed, and
// nothing read the flag: a document identified as a restatement was marked and
// then displayed next to the row it restates, and counted alongside it.
type ListFilter struct {
	// IncludeDuplicates returns the marked duplicates as well.
	//
	// The flag is an audit path, not a preference. Suppressing a document on a
	// similarity score is a judgement that can be wrong, so the rows stay
	// reachable — with the score that hid them — rather than disappearing.
	IncludeDuplicates bool
}

// notDuplicateExpr matches a document that has NOT been marked a duplicate.
//
// `duplicate_of` is declared `bson:"duplicate_of,omitempty"`, so a document
// that was never marked has NO SUCH KEY — which is every document written
// before the field existed, and most documents written since. In MongoDB a
// null in $in matches both an explicit null and a missing field, which is
// exactly the set wanted here; matching on "" alone would hide the entire
// existing corpus, and $ne:"" would do the same.
var notDuplicateExpr = bson.M{"$in": []any{nil, ""}}

// apply returns base with the duplicate exclusion added, unless the caller
// opted out. The caller's map is never mutated: these filters are built inline
// at call sites and a shared mutation would be invisible there.
func (f ListFilter) apply(base bson.M) bson.M {
	if f.IncludeDuplicates {
		return base
	}
	out := make(bson.M, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out["duplicate_of"] = notDuplicateExpr
	return out
}
