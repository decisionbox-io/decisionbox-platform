// Package database — run_step_repo.go (API side, read-only).
//
// Read-only counterpart of the agent's RunStepRepository. The dashboard
// polls GET /api/v1/runs/{id}/steps with an opaque cursor (the last
// row's `id`) to stream live run progress without re-reading the whole
// tail every time. The agent owns the writers; this repository only
// reads.
//
// Cursor design — why ObjectID, not timestamp: MongoDB datetimes are
// ms-precision, so two writes inside the same ms collide. A
// `timestamp > since` cursor would permanently drop any later row that
// shares the previous poll's last timestamp. ObjectIDs increment per
// writer process (timestamp + counter), so `_id > since_id` is
// collision-free for our single-agent-per-run writer. The dashboard
// treats `id` as opaque.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrInvalidCursor is returned when ListByRun receives a sinceID that
// is not a valid ObjectID hex string. Handlers should map this to a
// 400, not a 500 — it's caller-supplied input.
var ErrInvalidCursor = errors.New("invalid run-step cursor")

// RunStepRepository reads per-step rows for a discovery run.
type RunStepRepository struct {
	col *mongo.Collection
}

// NewRunStepRepository wraps the discovery_run_steps collection. The
// collection name lives in libs/go-common/mongodb so agent (writer) and
// api (reader) stay in lockstep on rename.
func NewRunStepRepository(db *DB) *RunStepRepository {
	return &RunStepRepository{col: db.Collection(gomongo.CollectionDiscoveryRunSteps)}
}

// RunStepDoc is the wire shape returned by ListByRun. IDHex is the
// doc's ObjectID rendered as a hex string — the dashboard treats it as
// an opaque cursor and feeds the last one back as `since` on the next
// poll. The embedded models.RunStep carries the per-step data.
//
// Attempt is which attempt of the run wrote the row; the agent stamps it on
// insert. Zero means the row predates the field (or its writer did not know
// its attempt), which ListByRun reads as attempt 1.
type RunStepDoc struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	IDHex     string             `bson:"-" json:"id"`
	RunID     string             `bson:"run_id" json:"run_id"`
	ProjectID string             `bson:"project_id,omitempty" json:"project_id,omitempty"`
	Attempt   int                `bson:"attempt,omitempty" json:"attempt,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`

	models.RunStep `bson:",inline" json:",inline"`
}

// ListByRun returns the steps for a run, ordered by `_id` ascending.
// sinceID is the last id the caller saw — empty means "head of
// stream". Returns ErrInvalidCursor if sinceID is non-empty and not a
// valid ObjectID hex string.
//
// attempt scopes the result to one attempt of the run, and is why a resume
// does not corrupt the live feed. Two things go wrong without it. The
// obvious one is that a superseded agent keeps writing for up to one
// in-flight LLM call after it has been replaced, so its rows show up in the
// resumed run's feed and are never cleaned up. The subtler one is that the
// `_id > since_id` cursor assumes a single writer process per run (see the
// agent's run_step_repo.go header): an ObjectID is 4 bytes of seconds, then
// 5 bytes that are random PER PROCESS, so with two attempts alive the
// ordering within any one second is decided by that random prefix. If the
// superseded writer's prefix sorts above the live one's, a cursor that
// advances onto a superseded row will never return the live attempt's rows
// from that same second — they compare less than the cursor and are dropped
// for good. Filtering to one attempt puts a single writer back in the
// stream and restores the cursor's precondition.
//
// Pass the run's current attempt (DiscoveryRun.Attempt). Zero or one also
// matches rows with no attempt field, which is every row written before the
// field existed — otherwise historical runs would render an empty feed.
//
// Not worth its own index: the (run_id, _id) index still serves the equality
// prefix and the _id range, leaving attempt as a cheap post-filter over the
// tens-to-hundreds of rows one run produces.
func (r *RunStepRepository) ListByRun(ctx context.Context, runID, sinceID string, limit, attempt int) ([]RunStepDoc, error) {
	filter := bson.M{"run_id": runID}
	if attempt <= 1 {
		filter["attempt"] = bson.M{"$in": []any{1, nil}}
	} else {
		filter["attempt"] = attempt
	}
	if sinceID != "" {
		oid, err := primitive.ObjectIDFromHex(sinceID)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidCursor, sinceID)
		}
		filter["_id"] = bson.M{"$gt": oid}
	}
	opts := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}})
	if limit > 0 {
		opts = opts.SetLimit(int64(limit))
	}
	cur, err := r.col.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list run steps: %w", err)
	}
	defer cur.Close(ctx)

	out := make([]RunStepDoc, 0)
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode run steps: %w", err)
	}
	for i := range out {
		out[i].IDHex = out[i].ID.Hex()
	}
	return out, nil
}
