package database

import (
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
)

// How a project-level insight or recommendation list is ordered.
//
// Priority is the default, because it is the order the two numbers on a
// document were always meant to produce: a run surfaces 20-40 findings at
// once, and "which of these should I act on first" is answered by the severity
// band folded with whether the claim held up -- which is exactly what the
// priority tier and its sort score are. Creation order answers a different
// question, and it is the only order this endpoint used to offer.
const (
	// SortPriority orders by the derived priority score, highest first.
	SortPriority = "priority"
	// SortCreatedAt is the original order: newest first. Kept reachable
	// because changing the default without leaving the old order available
	// would break any client that pages this endpoint.
	SortCreatedAt = "created_at"
)

// ListSort resolves a `sort` query value to the Mongo sort it names.
//
// An unknown value is an error rather than a silent fall back to the default.
// A client that asked for an order it did not get is a client whose paging is
// wrong in a way it cannot detect, and the one thing worse than a 400 here is
// a 200 with the rows in a different order than the one requested.
func ListSort(sort string) (bson.D, error) {
	switch sort {
	case "", SortPriority:
		// Three keys, and the last two are not decoration.
		//
		// A whole run's documents share one `created_at`: the agent stamps
		// `now` once for the batch when it denormalises them. And a run's
		// findings routinely share a priority score, because the score is a
		// tier plus two banded refinements. So a sort on the score alone
		// leaves large groups genuinely tied -- and a tie under limit/offset
		// paging means a document can appear on two pages or on none.
		//
		// `_id` is a UUID and unique, so these three are a total order. The
		// index declared for this collection carries the same keys in the same
		// directions, which is what keeps this an index scan rather than a
		// blocking in-memory sort over a project's whole history.
		//
		// A document with no `scoring.priority_score` sorts LAST here: a
		// missing field compares as null, below any number, under a descending
		// sort. That is the right place for a finding nothing graded, and it
		// is pinned by a test because it is behaviour nobody would notice
		// breaking.
		return bson.D{
			{Key: "scoring.priority_score", Value: -1},
			{Key: "created_at", Value: -1},
			{Key: "_id", Value: 1},
		}, nil
	case SortCreatedAt:
		return bson.D{
			{Key: "created_at", Value: -1},
			{Key: "_id", Value: 1},
		}, nil
	default:
		return nil, fmt.Errorf("unknown sort %q: want %q or %q", sort, SortPriority, SortCreatedAt)
	}
}
