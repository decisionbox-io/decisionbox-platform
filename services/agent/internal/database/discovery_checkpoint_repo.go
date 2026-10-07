// Package database — discovery_checkpoint_repo.go
//
// The durability seam for exploration. A discovery run's cost is concentrated
// in Phase 3: N agentic LLM calls and N warehouse queries. Until this
// collection existed, none of that was written anywhere replayable until the
// Phase 7 tail, so a process that died anywhere earlier lost 100% of it and
// the run had no way back in.
//
// Each completed exploration step lands here as its own small document, and a
// single summary document (step_number 0) lands when exploration ends. A
// resumed run reads the contiguous prefix and replays it instead of paying for
// it again; a run whose summary is present skips exploration entirely.
//
// One document per step, never an embedded array. Embedded []RunStep /
// []ExplorationStep arrays already hit the 16MB BSON limit historically and
// forced the split-log collections (see discovery_log_repo.go) — and a
// checkpoint would hit it exactly on the long runs it matters most for.
//
// Deliberately NOT the same thing as discovery_exploration_steps: that
// collection is the tail-written audit log, keyed by discovery_id, carrying
// full rows and fix history, read by the dashboard. These rows are keyed by
// run_id, carry a bounded row sample, exist only while a run is in flight,
// and are deleted on a terminal-and-not-resumable outcome with a TTL as the
// backstop.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Checkpoint document kinds. The summary rides in the same collection as the
// steps (at step_number 0) so one ordered read answers both "how far did
// exploration get" and "did it finish".
const (
	CheckpointKindStep    = "step"
	CheckpointKindSummary = "exploration_summary"

	// checkpointSummaryStepNumber is the reserved step_number of the summary
	// document. Exploration steps are 1-based, so 0 cannot collide, and it
	// sorts ahead of every step in the ascending read.
	checkpointSummaryStepNumber = 0

	// checkpointTTLIndexName is fixed so a retention change is recognisable
	// as the SAME index with different options (which is what lets
	// EnsureIndexes recreate it) rather than an unrelated second TTL.
	checkpointTTLIndexName = "ttl_discovery_checkpoint"

	// checkpointRetentionEnv configures how long checkpoints survive.
	//
	// An env var rather than a constant because the correct value is a
	// function of DISCOVERY_MAX_DURATION, which is itself an env var: a
	// deployment that raises the run cap (or sets it to 0 to disable it)
	// must raise retention to match, or a long run's early checkpoints
	// expire while it is still running.
	checkpointRetentionEnv = "DISCOVERY_CHECKPOINT_RETENTION"

	// defaultCheckpointRetention comfortably exceeds the default 24h
	// DISCOVERY_MAX_DURATION, leaving an operator a day to notice a failed
	// run and resume it.
	defaultCheckpointRetention = 48 * time.Hour

	// indexOptionsConflictCode is MongoDB's IndexOptionsConflict. A TTL's
	// expireAfterSeconds cannot be mutated in place: CreateOne with a
	// different value on an existing index name returns this rather than
	// updating it.
	indexOptionsConflictCode = 85
)

// DiscoveryCheckpointRepository persists and reads exploration checkpoints.
type DiscoveryCheckpointRepository struct {
	col       *mongo.Collection
	retention time.Duration
}

// NewDiscoveryCheckpointRepository wraps the checkpoint collection. Retention
// is resolved once here from the environment; EnsureIndexes (called at agent
// startup) applies it to the TTL index.
func NewDiscoveryCheckpointRepository(db *DB) *DiscoveryCheckpointRepository {
	return &DiscoveryCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryCheckpoints),
		retention: config.GetEnvAsDuration(checkpointRetentionEnv, defaultCheckpointRetention),
	}
}

// Retention reports the configured checkpoint lifetime. Exposed for the
// startup log line and the integration test that asserts the TTL matches it.
func (r *DiscoveryCheckpointRepository) Retention() time.Duration { return r.retention }

// ExplorationCheckpointDoc is the storage shape of one checkpoint row.
//
// models.ExplorationStep and models.CheckpointArgs are both embedded inline so
// their existing BSON tags stay authoritative — exactly the pattern
// ExplorationStepDoc uses. Their tag sets are disjoint.
type ExplorationCheckpointDoc struct {
	RunID      string `bson:"run_id"`
	ProjectID  string `bson:"project_id"`
	StepNumber int    `bson:"step_number"`
	// Attempt records which attempt of the run wrote this row. Purely
	// observability: a resumed run replaces the rows it replays, so a mixed
	// set of attempt values across a prefix is how an operator sees where
	// the previous attempt stopped.
	Attempt   int       `bson:"attempt"`
	Kind      string    `bson:"kind"`
	CreatedAt time.Time `bson:"created_at"`

	models.ExplorationStep `bson:",inline"`
	models.CheckpointArgs  `bson:",inline"`

	// Summary fields — set only when Kind == CheckpointKindSummary.
	Completed     bool   `bson:"completed,omitempty"`
	CompletionMsg string `bson:"completion_msg,omitempty"`
	TotalSteps    int    `bson:"total_steps,omitempty"`
	DurationMs    int64  `bson:"exploration_duration_ms,omitempty"`
}

// CheckpointStepInput is one step to checkpoint.
type CheckpointStepInput struct {
	ProjectID string
	RunID     string
	Attempt   int
	Step      models.ExplorationStep
	// RowSample is the bounded, normalised slice of Step.QueryResult to
	// retain in place of the full result set — built by the caller with
	// verifier.CheckpointSample so the sample the verifier's evidence
	// bundle is rebuilt from on resume is produced by the same code that
	// builds it on the live path.
	//
	// Nil is legitimate (a step that ran no query, or a query that failed).
	RowSample []map[string]interface{}
	Args      models.CheckpointArgs
}

// CheckpointSummaryInput is the end-of-exploration summary.
type CheckpointSummaryInput struct {
	ProjectID string
	RunID     string
	Attempt   int
	Summary   models.ExplorationCheckpointSummary
}

// CheckpointSet is what a resumed run gets back: the contiguous prefix of
// steps it may replay, and the summary when exploration had already finished.
type CheckpointSet struct {
	// Steps is the prefix 1..K in order, with no gaps. See LoadPrefix.
	Steps []models.ExplorationCheckpoint
	// Summary is non-nil only when exploration completed its loop on a
	// previous attempt — the signal that the resumed run may go straight to
	// analysis.
	Summary *models.ExplorationCheckpointSummary
	// Attempt is the highest attempt number seen in the set, so a resumed
	// run can log which attempt it is continuing.
	Attempt int
}

// Len reports how many steps may be replayed.
func (c *CheckpointSet) Len() int {
	if c == nil {
		return 0
	}
	return len(c.Steps)
}

// ExplorationComplete reports whether exploration already finished, in which
// case a resumed run makes zero exploration LLM calls and zero warehouse
// queries.
func (c *CheckpointSet) ExplorationComplete() bool {
	return c != nil && c.Summary != nil
}

// checkpointPayload reduces a step to what a checkpoint keeps.
//
// Pure, and the single owner of the strip invariant, so what is dropped is
// testable without Mongo.
//
// Dropped:
//   - QueryResult is replaced by sample. The full result set is what makes a
//     step unbounded, and the digest (CompactResult) rides alongside for the
//     analysis phase. The sample is what the verifier's evidence bundle
//     consumes — on the live path that bundle is ALREADY a ≤SampleRows
//     sample, so a resumed run's verifier sees the same evidence rather than
//     an empty result it would mark unverifiable.
//   - FixHistory. Each entry carries a full SQL-fix prompt and response: it
//     is audit data, written once at the tail, not something replay reads.
//   - LLMRequest / LLMResponse. Unpopulated by the exploration engine today;
//     stripped so they stay that way here rather than silently becoming the
//     largest field in the document if that changes.
//
// Everything else is kept, and all of it is load-bearing — including Quality,
// which attachSourceQuality derives every insight's evidence label from and
// which is knowable nowhere else. A resumed run without it would relabel
// findings computed over withheld rows as sound.
func checkpointPayload(step models.ExplorationStep, sample []map[string]interface{}) models.ExplorationStep {
	out := step
	out.QueryResult = sample
	out.FixHistory = nil
	out.LLMRequest = ""
	out.LLMResponse = ""
	return out
}

// SaveStep writes (or replaces) one step's checkpoint.
//
// ReplaceOne with upsert against the unique (run_id, step_number) index, so a
// retried write — or a resumed run re-checkpointing a step it replayed —
// replaces the row rather than duplicating it or failing on the index.
func (r *DiscoveryCheckpointRepository) SaveStep(ctx context.Context, in CheckpointStepInput) error {
	if in.RunID == "" {
		return errors.New("checkpoint step: run_id is required")
	}
	if in.Step.Step <= 0 {
		return fmt.Errorf("checkpoint step: step number must be >= 1, got %d", in.Step.Step)
	}
	doc := ExplorationCheckpointDoc{
		RunID:           in.RunID,
		ProjectID:       in.ProjectID,
		StepNumber:      in.Step.Step,
		Attempt:         in.Attempt,
		Kind:            CheckpointKindStep,
		CreatedAt:       time.Now(),
		ExplorationStep: checkpointPayload(in.Step, in.RowSample),
		CheckpointArgs:  in.Args,
	}
	// The attempt bound is a fence against a previous attempt's agent that is
	// somehow still alive. That is reachable: the API's startup sweep marks
	// in-flight runs `failed` after a restart WITHOUT reaping their
	// workloads, so an operator resuming such a run can have two agents on
	// one run id — and both write checkpoints keyed on (run_id, step_number).
	// Without this, the orphan would overwrite the live attempt's steps with
	// its own, and replay would reconstruct a conversation spliced together
	// from two different runs.
	//
	// $lte, not equality: a resumed run legitimately replaces the rows of the
	// prefix it replayed, which were written under a lower attempt.
	filter := bson.M{
		"run_id":      in.RunID,
		"step_number": in.Step.Step,
		"attempt":     bson.M{"$lte": in.Attempt},
	}
	_, err := r.col.ReplaceOne(ctx, filter, doc, options.Replace().SetUpsert(true))
	if err == nil {
		return nil
	}
	// A duplicate key here means precisely one thing: the filter above found
	// nothing, so the upsert tried to insert, and the unique
	// (run_id, step_number) index refused it because a row already exists —
	// under a HIGHER attempt. The write is correctly refused and there is
	// nothing to retry; the newer attempt owns this step.
	if mongo.IsDuplicateKeyError(err) {
		applog.WithFields(applog.Fields{
			"run_id":  in.RunID,
			"step":    in.Step.Step,
			"attempt": in.Attempt,
		}).Info("checkpoint refused: a newer attempt of this run owns this step")
		return nil
	}
	return fmt.Errorf("checkpoint step %d of run %s: %w", in.Step.Step, in.RunID, err)
}

// SaveExplorationSummary records that exploration finished, which is what
// lets a resumed run skip Phase 3 outright.
func (r *DiscoveryCheckpointRepository) SaveExplorationSummary(ctx context.Context, in CheckpointSummaryInput) error {
	if in.RunID == "" {
		return errors.New("checkpoint summary: run_id is required")
	}
	doc := ExplorationCheckpointDoc{
		RunID:         in.RunID,
		ProjectID:     in.ProjectID,
		StepNumber:    checkpointSummaryStepNumber,
		Attempt:       in.Attempt,
		Kind:          CheckpointKindSummary,
		CreatedAt:     time.Now(),
		Completed:     in.Summary.Completed,
		CompletionMsg: in.Summary.CompletionMsg,
		TotalSteps:    in.Summary.TotalSteps,
		DurationMs:    in.Summary.Duration.Milliseconds(),
	}
	// Same attempt fence as SaveStep: an orphaned previous attempt must not
	// declare exploration finished on behalf of the live one.
	filter := bson.M{
		"run_id":      in.RunID,
		"step_number": checkpointSummaryStepNumber,
		"attempt":     bson.M{"$lte": in.Attempt},
	}
	_, err := r.col.ReplaceOne(ctx, filter, doc, options.Replace().SetUpsert(true))
	if err == nil {
		return nil
	}
	if mongo.IsDuplicateKeyError(err) {
		applog.WithFields(applog.Fields{
			"run_id":  in.RunID,
			"attempt": in.Attempt,
		}).Info("exploration summary checkpoint refused: a newer attempt of this run owns it")
		return nil
	}
	return fmt.Errorf("checkpoint exploration summary of run %s: %w", in.RunID, err)
}

// LoadPrefix returns the CONTIGUOUS prefix 1..K of a run's checkpointed
// steps, stopping at the first gap, plus the summary when one was written.
//
// Stopping at a gap is the honest answer, not a convenience. A hole means an
// earlier write failed (checkpoint failures are logged and swallowed so they
// can never abort a run). Replaying a conversation with a hole in it would
// misnumber every later step relative to what the model was told, and the
// insights cite step numbers — so the run would attribute evidence to the
// wrong queries. Resuming at the hole costs the steps after it and keeps
// every citation true.
func (r *DiscoveryCheckpointRepository) LoadPrefix(ctx context.Context, runID string) (*CheckpointSet, error) {
	if runID == "" {
		return nil, errors.New("load checkpoints: run_id is required")
	}
	cur, err := r.col.Find(ctx,
		bson.M{"run_id": runID},
		options.Find().SetSort(bson.D{{Key: "step_number", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("load checkpoints for run %s: %w", runID, err)
	}
	defer cur.Close(ctx) //nolint:errcheck

	set := &CheckpointSet{}
	expected := 1
	gapAt := 0
	for cur.Next(ctx) {
		var doc ExplorationCheckpointDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode checkpoint for run %s: %w", runID, err)
		}
		if doc.Attempt > set.Attempt {
			set.Attempt = doc.Attempt
		}
		if doc.Kind == CheckpointKindSummary || doc.StepNumber == checkpointSummaryStepNumber {
			set.Summary = &models.ExplorationCheckpointSummary{
				Completed:     doc.Completed,
				CompletionMsg: doc.CompletionMsg,
				TotalSteps:    doc.TotalSteps,
				Duration:      time.Duration(doc.DurationMs) * time.Millisecond,
			}
			continue
		}
		if gapAt > 0 {
			// Past a gap: this row is unreachable for replay. Keep reading
			// only so Attempt and the summary are still observed.
			continue
		}
		if doc.StepNumber != expected {
			gapAt = expected
			continue
		}
		set.Steps = append(set.Steps, models.ExplorationCheckpoint{
			Step: doc.ExplorationStep,
			Args: doc.CheckpointArgs,
		})
		expected++
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("cursor checkpoints for run %s: %w", runID, err)
	}

	if gapAt > 0 {
		applog.WithFields(applog.Fields{
			"run_id":          runID,
			"replayable":      len(set.Steps),
			"first_missing":   gapAt,
			"summary_present": set.Summary != nil,
		}).Warn("checkpoint prefix has a gap; resume will re-explore from the first missing step")
	}

	// The summary may only be trusted when the replayable prefix covers every
	// step it claims. Otherwise it promises "exploration finished" over a step
	// set that cannot be replayed in full, and the resumed run skips straight
	// to analysis over incomplete evidence — silently dropping paid-for work
	// instead of re-exploring it.
	//
	// This covers BOTH shapes a failed checkpoint write takes, and the second
	// is the one a gap check alone misses: if the write that failed was for
	// the LAST step, there is no later row to leave a hole, so the prefix
	// looks clean at 39 steps while the summary says 40. The prefix is the
	// stronger fact either way — it is what replay can actually produce.
	if set.Summary != nil && set.Summary.TotalSteps > len(set.Steps) {
		applog.WithFields(applog.Fields{
			"run_id":              runID,
			"replayable":          len(set.Steps),
			"summary_total_steps": set.Summary.TotalSteps,
		}).Warn("exploration summary claims more steps than the replayable prefix holds; resume will continue exploring rather than skip to analysis")
		set.Summary = nil
	}
	return set, nil
}

// DeleteByRun removes every checkpoint of a run and reports how many rows
// went. Called when a run reaches an outcome it cannot be resumed from —
// success, or cancellation.
func (r *DiscoveryCheckpointRepository) DeleteByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("delete checkpoints: run_id is required")
	}
	res, err := r.col.DeleteMany(ctx, bson.M{"run_id": runID})
	if err != nil {
		return 0, fmt.Errorf("delete checkpoints for run %s: %w", runID, err)
	}
	return res.DeletedCount, nil
}

// ListRunIDsWithCheckpoints returns the distinct run ids that still have
// checkpoints. The boot-time orphan sweep adds them to its live set so it
// does not drop a resumable run's per-run Qdrant collection. Bounded by the
// TTL.
func (r *DiscoveryCheckpointRepository) ListRunIDsWithCheckpoints(ctx context.Context) ([]string, error) {
	vals, err := r.col.Distinct(ctx, "run_id", bson.M{})
	if err != nil {
		return nil, fmt.Errorf("list run ids with checkpoints: %w", err)
	}
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// EnsureIndexes creates the unique ordering index and the retention TTL.
//
// The TTL needs the conflict dance: expireAfterSeconds cannot be mutated in
// place, so CreateOne against an existing index of the same name with a
// different expiry returns IndexOptionsConflict (85). Without handling it,
// changing DISCOVERY_CHECKPOINT_RETENTION would fail agent startup forever
// instead of taking effect — i.e. the env var would not actually be a knob.
// Dropping and recreating is safe: the index is pure retention machinery, and
// rebuilding it costs one scan of a collection that only ever holds in-flight
// runs.
func (r *DiscoveryCheckpointRepository) EnsureIndexes(ctx context.Context) error {
	if _, err := r.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "run_id", Value: 1},
			{Key: "step_number", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("uq_run_step"),
	}); err != nil {
		return fmt.Errorf("ensure checkpoint (run_id, step_number) index: %w", err)
	}

	ttl := mongo.IndexModel{
		Keys: bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().
			SetName(checkpointTTLIndexName).
			SetExpireAfterSeconds(int32(r.retention.Seconds())),
	}
	_, err := r.col.Indexes().CreateOne(ctx, ttl)
	if err == nil {
		return nil
	}
	if !isIndexOptionsConflict(err) {
		return fmt.Errorf("ensure checkpoint TTL index: %w", err)
	}

	applog.WithFields(applog.Fields{
		"index":     checkpointTTLIndexName,
		"retention": r.retention.String(),
	}).Info("checkpoint retention changed; recreating the TTL index")
	if _, err := r.col.Indexes().DropOne(ctx, checkpointTTLIndexName); err != nil {
		return fmt.Errorf("drop checkpoint TTL index before recreating it at %s: %w", r.retention, err)
	}
	if _, err := r.col.Indexes().CreateOne(ctx, ttl); err != nil {
		return fmt.Errorf("recreate checkpoint TTL index at %s: %w", r.retention, err)
	}
	return nil
}

// isIndexOptionsConflict reports whether err is MongoDB's "same index name,
// different options" refusal — the one error EnsureIndexes recovers from by
// recreating the index.
func isIndexOptionsConflict(err error) bool {
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		return ce.Code == indexOptionsConflictCode || ce.Name == "IndexOptionsConflict"
	}
	var we mongo.WriteException
	if errors.As(err, &we) {
		for _, e := range we.WriteErrors {
			if e.Code == indexOptionsConflictCode {
				return true
			}
		}
	}
	return false
}
