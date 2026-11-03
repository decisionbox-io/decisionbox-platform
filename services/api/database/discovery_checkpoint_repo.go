package database

import (
	"context"
	"errors"
	"fmt"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DiscoveryCheckpointRepository is the API's read / purge view of the
// exploration checkpoints the agent writes (see the agent's
// discovery_checkpoint_repo.go for the shape and the reasoning).
//
// Deliberately narrow. The API answers exactly two questions about
// checkpoints — "is there anything to resume this run from" and "throw them
// away, this run is over for good" — so it has no business decoding the
// steps, and keeping the replay shape out of this module means there is one
// definition of it rather than two that can drift.
//
// It spans BOTH checkpoint collections: the exploration rows and the
// per-area analysis rows the agent writes alongside them. Only the purge
// needs the second one — resumability is still decided entirely by the
// exploration prefix, and a run that died in analysis has a complete one —
// and it lives here rather than in a repository of its own so a caller that
// ends a run cannot throw away half the feature's rows.
type DiscoveryCheckpointRepository struct {
	col *mongo.Collection
	// analysisCol holds one row per completed analysis area. Purge-only
	// from the API side; the agent owns reading and writing it.
	analysisCol *mongo.Collection
}

func NewDiscoveryCheckpointRepository(db *DB) *DiscoveryCheckpointRepository {
	return &DiscoveryCheckpointRepository{
		col:         db.Collection(gomongo.CollectionDiscoveryCheckpoints),
		analysisCol: db.Collection(gomongo.CollectionDiscoveryAnalysisCheckpoints),
	}
}

// ResumeState reports how many exploration steps a run can replay and
// whether its exploration already finished.
//
// prefixLen is the length of the REPLAYABLE prefix 1..K, not the row count,
// and it stops for the same two reasons the agent's loader stops. A hole
// means an earlier checkpoint write failed, and the steps after it cannot be
// replayed without misnumbering every one of them. A step written by an
// OLDER attempt than the one before it is a stale tail left when a filled
// gap stranded a previous attempt's answers. Counting either would let the
// API promise a resume it cannot deliver.
//
// explorationComplete true means the resumed run will skip Phase 3 outright,
// so it is resumable even when prefixLen is 0 — which is possible when the
// summary landed and the step rows have since been pruned.
func (r *DiscoveryCheckpointRepository) ResumeState(ctx context.Context, runID string) (prefixLen int, explorationComplete bool, err error) {
	if runID == "" {
		return 0, false, errors.New("checkpoint resume state: run_id is required")
	}
	cur, findErr := r.col.Find(ctx,
		bson.M{"run_id": runID},
		options.Find().
			SetSort(bson.D{{Key: "step_number", Value: 1}}).
			SetProjection(bson.M{"step_number": 1, "kind": 1, "total_steps": 1, "attempt": 1}),
	)
	if findErr != nil {
		return 0, false, fmt.Errorf("read checkpoints for run %s: %w", runID, findErr)
	}
	defer cur.Close(ctx) //nolint:errcheck

	expected := 1
	summaryTotalSteps := 0
	// The newest attempt that has contributed to the prefix so far, and the
	// attempt that wrote the summary.
	prefixAttempt := 0
	summaryAttempt := 0
	ended := false
	for cur.Next(ctx) {
		var doc struct {
			StepNumber int    `bson:"step_number"`
			Kind       string `bson:"kind"`
			TotalSteps int    `bson:"total_steps"`
			Attempt    int    `bson:"attempt"`
		}
		if decodeErr := cur.Decode(&doc); decodeErr != nil {
			return 0, false, fmt.Errorf("decode checkpoint for run %s: %w", runID, decodeErr)
		}
		// step_number 0 is the reserved exploration-summary row.
		if doc.StepNumber == 0 {
			explorationComplete = true
			summaryTotalSteps = doc.TotalSteps
			summaryAttempt = doc.Attempt
			continue
		}
		if ended {
			// Past the end of the prefix. Keep reading only so the summary
			// row is still observed — it sorts first today, but relying on
			// that would make this answer depend on the sort order.
			continue
		}
		if doc.StepNumber != expected || doc.Attempt < prefixAttempt {
			ended = true
			continue
		}
		prefixAttempt = doc.Attempt
		prefixLen++
		expected++
	}
	if curErr := cur.Err(); curErr != nil {
		return 0, false, fmt.Errorf("cursor checkpoints for run %s: %w", runID, curErr)
	}

	// The same rule the agent's loader applies, and it has to be the same or
	// the API would promise a resume that behaves differently: the summary is
	// only trustworthy when the replayable prefix is non-empty AND covers
	// every step it claims. A hole leaves a short prefix; a failed write on
	// the LAST step leaves a prefix that looks clean but is one short of the
	// claim; a stale tail leaves one cut off at the splice.
	//
	// The empty prefix is the case this used to get wrong, in both
	// directions. A comment here claimed it was "the 'rows pruned, summary
	// survived' state that is resumable on the summary alone" — it is not.
	// Skipping to analysis means analysing the replayed steps, and there are
	// none: the picker finds nothing for every area, so the operator pays
	// for the analysis phase and gets an empty discovery. The `>` comparison
	// happened to reject the realistic shape of it (a summary claiming 40
	// steps with no rows) while admitting the degenerate one (a summary
	// claiming 0), so the stated intent and the behaviour disagreed and
	// neither was right.
	if explorationComplete && (prefixLen == 0 || summaryTotalSteps > prefixLen) {
		explorationComplete = false
	}
	// And it must come from an attempt no older than the prefix. The counts
	// can line up over a prefix the summary knows nothing about — a later
	// attempt filling the gap that broke it, then dying before writing a
	// summary of its own.
	if explorationComplete && summaryAttempt < prefixAttempt {
		explorationComplete = false
	}
	return prefixLen, explorationComplete, nil
}

// DeleteByRun removes a run's checkpoints — exploration rows AND the
// per-area analysis rows — and reports the combined count. Called on
// cancellation and on discard, both of which are terminal and explicitly not
// resumable.
//
// Both collections, from one call, because they are one feature: an area
// checkpoint is only ever usable alongside an exploration prefix the run can
// replay, so leaving the area rows behind would leak rows for a run that can
// never read them again. The analysis rows go FIRST — they are the ones
// nothing else would reclaim early, since the exploration delete is what the
// callers log and act on, so a failure there is reported with the rows that
// matter most already gone.
func (r *DiscoveryCheckpointRepository) DeleteByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("delete checkpoints: run_id is required")
	}
	var deleted int64
	if r.analysisCol != nil {
		res, err := r.analysisCol.DeleteMany(ctx, bson.M{"run_id": runID})
		if err != nil {
			return 0, fmt.Errorf("delete analysis checkpoints for run %s: %w", runID, err)
		}
		deleted += res.DeletedCount
	}
	res, err := r.col.DeleteMany(ctx, bson.M{"run_id": runID})
	if err != nil {
		return deleted, fmt.Errorf("delete checkpoints for run %s: %w", runID, err)
	}
	return deleted + res.DeletedCount, nil
}
