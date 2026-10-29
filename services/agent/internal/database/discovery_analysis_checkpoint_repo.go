// Package database — discovery_analysis_checkpoint_repo.go
//
// The durability seam for analysis, one phase on from
// discovery_checkpoint_repo.go. Exploration checkpoints let a resumed run
// skip Phase 3; these rows let it skip the Phase 4 areas a previous attempt
// already finished, instead of re-running every area from the top.
//
// Each COMPLETED analysis area lands here as its own document, keyed by
// (run_id, area_id) — the whole models.AnalysisStep, so the restored area
// carries its insights (with their validation verdicts and evidence-quality
// labels already stamped), its validation results, and its audit prompt and
// response. That is everything the Phase 4 loop body produces which outlives
// the iteration, which is what makes restoring an area a merge rather than a
// reconstruction.
//
// Why a separate collection rather than another `kind` in
// discovery_checkpoints: that collection's unique index is
// (run_id, step_number) and an area has no step number, and both of its
// readers scan every row of a run in step order and end the replayable
// prefix at the first unexpected number. A second row kind inside that scan
// would put those invariants at risk for no gain.
//
// Deliberately NOT discovery_analysis_steps: that is the tail-written audit
// log keyed by discovery_id and read by the dashboard. These rows are keyed
// by run_id, exist only while a run is in flight, and are deleted on a
// terminal-and-not-resumable outcome with a TTL as the backstop.
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

// analysisCheckpointTTLIndexName is fixed for the same reason the
// exploration TTL's name is: a retention change has to be recognisable as
// the SAME index with different options, which is what lets EnsureIndexes
// recreate it rather than failing forever.
const analysisCheckpointTTLIndexName = "ttl_discovery_analysis_checkpoint"

// DiscoveryAnalysisCheckpointRepository persists and reads per-area analysis
// checkpoints.
type DiscoveryAnalysisCheckpointRepository struct {
	col       *mongo.Collection
	retention time.Duration
}

// NewDiscoveryAnalysisCheckpointRepository wraps the analysis-checkpoint
// collection.
//
// Retention comes from the SAME DISCOVERY_CHECKPOINT_RETENTION as the
// exploration checkpoints, because these rows are one feature with them: an
// area checkpoint is only ever usable alongside an exploration prefix the
// run can replay, so two independently settable retentions would mostly be a
// way to misconfigure the pair.
func NewDiscoveryAnalysisCheckpointRepository(db *DB) *DiscoveryAnalysisCheckpointRepository {
	return &DiscoveryAnalysisCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryAnalysisCheckpoints),
		retention: config.GetEnvAsDuration(checkpointRetentionEnv, defaultCheckpointRetention),
	}
}

// Retention reports the configured lifetime. Exposed for the startup log
// line and the integration test that asserts the TTL matches it.
func (r *DiscoveryAnalysisCheckpointRepository) Retention() time.Duration { return r.retention }

// AnalysisAreaCheckpointDoc is the storage shape of one completed area.
//
// models.AnalysisStep is embedded inline so its existing BSON tags stay
// authoritative — the same pattern AnalysisStepDoc and
// ExplorationCheckpointDoc use. Note what is deliberately NOT declared here:
// the area id. AnalysisStep.AreaID already carries `bson:"area_id"` and is
// the key half of the unique index, so an outer field of the same name would
// write the key twice. TestAnalysisAreaCheckpointDocTagsAreDisjoint pins
// that.
type AnalysisAreaCheckpointDoc struct {
	RunID     string `bson:"run_id"`
	ProjectID string `bson:"project_id"`
	// Attempt records which attempt of the run analysed this area. The
	// upsert filter is fenced on it, so an orphaned earlier attempt
	// cannot overwrite a newer one's area.
	Attempt int `bson:"attempt"`
	// ExplorationAttempt is the attempt that wrote the exploration summary
	// this area's analysis was computed over. It is the staleness fence —
	// see LoadAreas.
	ExplorationAttempt int `bson:"exploration_attempt"`
	// InsightsValidated is the area's slice of the run's validation
	// budget. See models.AnalysisAreaCheckpoint.
	InsightsValidated int       `bson:"insights_validated,omitempty"`
	CreatedAt         time.Time `bson:"created_at"`

	models.AnalysisStep `bson:",inline"`
}

// AnalysisAreaCheckpointInput is one completed area to checkpoint.
type AnalysisAreaCheckpointInput struct {
	ProjectID string
	RunID     string
	Attempt   int
	// ExplorationAttempt must be the attempt that wrote this run's
	// exploration summary. A run with no durable summary has no valid
	// value for it and must not checkpoint areas at all — there is no
	// exploration a later attempt could restore them against.
	ExplorationAttempt int
	Step               models.AnalysisStep
	InsightsValidated  int
}

// AnalysisCheckpointSet is what a resumed run gets back: the completed areas
// it may restore, by area id.
type AnalysisCheckpointSet struct {
	// Areas is keyed by area id, not ordered, and that is the shape the
	// question has: the orchestrator builds its area list by ranging over
	// a map of prompts, so "the first unfinished area" is not a positional
	// concept in this codebase.
	Areas map[string]models.AnalysisAreaCheckpoint
	// Attempt is the highest attempt number among the restored areas, so a
	// resumed run can log which attempt's work it is continuing.
	Attempt int
}

// Len reports how many areas may be restored. Safe on nil.
func (s *AnalysisCheckpointSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Areas)
}

// SaveArea writes (or replaces) one completed area's checkpoint.
//
// ReplaceOne with upsert against the unique (run_id, area_id) index, so
// re-analysing an area a previous attempt already persisted replaces its row
// rather than duplicating it or failing on the index. That is the idempotency
// the acceptance criteria ask for, at the only layer that needs it: the
// run's own result is still one clean insert at the tail under a freshly
// minted discovery_id, so there is no re-save for the unique index on
// discovery_recommendation_log.discovery_id to refuse.
//
// The attempt bound is the same fence SaveStep uses, against the same
// reachable state: the API's startup sweep marks in-flight runs `failed`
// WITHOUT reaping their workloads, so an operator resuming such a run can
// have two agents on one run id. $lte rather than equality, because a
// resumed attempt legitimately replaces an area an earlier attempt analysed.
func (r *DiscoveryAnalysisCheckpointRepository) SaveArea(ctx context.Context, in AnalysisAreaCheckpointInput) error {
	if in.RunID == "" {
		return errors.New("checkpoint analysis area: run_id is required")
	}
	if in.Step.AreaID == "" {
		return errors.New("checkpoint analysis area: area_id is required")
	}
	if in.ExplorationAttempt <= 0 {
		return fmt.Errorf(
			"checkpoint analysis area %s: exploration_attempt must be >= 1, got %d — an area cannot be checkpointed against an exploration that was never summarised",
			in.Step.AreaID, in.ExplorationAttempt,
		)
	}
	doc := AnalysisAreaCheckpointDoc{
		RunID:              in.RunID,
		ProjectID:          in.ProjectID,
		Attempt:            in.Attempt,
		ExplorationAttempt: in.ExplorationAttempt,
		InsightsValidated:  in.InsightsValidated,
		CreatedAt:          time.Now(),
		AnalysisStep:       in.Step,
	}
	filter := bson.M{
		"run_id":  in.RunID,
		"area_id": in.Step.AreaID,
		"attempt": bson.M{"$lte": in.Attempt},
	}
	_, err := r.col.ReplaceOne(ctx, filter, doc, options.Replace().SetUpsert(true))
	if err == nil {
		return nil
	}
	// A duplicate key means the filter matched nothing, so the upsert tried
	// to insert and the unique index refused it — a row already exists under
	// a HIGHER attempt. This attempt has been superseded, which the caller
	// must act on rather than shrug at: everything after analysis is
	// expensive and would be spent on a result that is then discarded.
	if mongo.IsDuplicateKeyError(err) {
		applog.WithFields(applog.Fields{
			"run_id":  in.RunID,
			"area":    in.Step.AreaID,
			"attempt": in.Attempt,
		}).Info("analysis area checkpoint refused: a newer attempt of this run owns this area")
		return ErrSupersededAttempt
	}
	return fmt.Errorf("checkpoint analysis area %s of run %s: %w", in.Step.AreaID, in.RunID, err)
}

// LoadAreas returns the completed areas a resumed run may restore, bounded to
// the exploration it is itself about to analyse over.
//
// explorationAttempt is the attempt that wrote the exploration summary this
// run trusts, and filtering on it is the correctness fence of the whole
// feature. A restored area's insights cite exploration step numbers through
// SourceSteps, and the verifier, the evidence-quality labelling and the
// recommender all resolve them against the run's own steps. If this attempt
// had to RE-explore, those citations can name different work — so an area
// analysed over a different exploration must not come back.
//
// That is reachable rather than theoretical. Attempt 1 explores 40 steps,
// summarises, analyses three areas and dies. Its early step rows later TTL
// out, so attempt 2 reads a prefix with a hole, LoadPrefix drops the summary,
// and attempt 2 re-explores to a different 45 steps and writes its own
// summary. Attempt 3 then has a perfectly trustworthy summary — over attempt
// 2's exploration — and attempt 1's three areas must not be restored into it.
// Because attempts only ever increase, the re-explored summary carries a
// higher number and those rows simply stop matching.
//
// Zero (or negative) means the run has no durable exploration summary, so no
// row can be valid: returns an empty set without querying.
func (r *DiscoveryAnalysisCheckpointRepository) LoadAreas(ctx context.Context, runID string, explorationAttempt int) (*AnalysisCheckpointSet, error) {
	if runID == "" {
		return nil, errors.New("load analysis checkpoints: run_id is required")
	}
	set := &AnalysisCheckpointSet{Areas: make(map[string]models.AnalysisAreaCheckpoint)}
	if explorationAttempt <= 0 {
		return set, nil
	}
	// Served off the run_id prefix of the unique (run_id, area_id) index.
	// The exploration_attempt equality rides along as a filter over at most
	// one row per analysis area, so it needs no index of its own.
	cur, err := r.col.Find(ctx, bson.M{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("load analysis checkpoints for run %s: %w", runID, err)
	}
	defer cur.Close(ctx) //nolint:errcheck

	stale := 0
	for cur.Next(ctx) {
		var doc AnalysisAreaCheckpointDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode analysis checkpoint for run %s: %w", runID, err)
		}
		if doc.ExplorationAttempt != explorationAttempt {
			stale++
			continue
		}
		if doc.AreaID == "" {
			// Nothing writes such a row — SaveArea refuses it — so this is
			// a hand-edited or corrupted document rather than a state the
			// code produces. Skipped rather than keyed under "", which
			// would make it look like an area and silently suppress a real
			// one.
			applog.WithField("run_id", runID).Warn("analysis checkpoint row has no area id; ignoring it")
			continue
		}
		if doc.Attempt > set.Attempt {
			set.Attempt = doc.Attempt
		}
		set.Areas[doc.AreaID] = models.AnalysisAreaCheckpoint{
			Step:              doc.AnalysisStep,
			InsightsValidated: doc.InsightsValidated,
		}
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("cursor analysis checkpoints for run %s: %w", runID, err)
	}
	if stale > 0 {
		// Worth a line rather than silence: this is the fence above firing,
		// and it means the run is about to re-pay for areas whose rows are
		// sitting right there.
		applog.WithFields(applog.Fields{
			"run_id":              runID,
			"restorable":          len(set.Areas),
			"ignored":             stale,
			"exploration_attempt": explorationAttempt,
		}).Info("ignored analysis checkpoints from a superseded exploration; those areas will be analysed again")
	}
	return set, nil
}

// DeleteByRun removes every analysis checkpoint of a run and reports how many
// rows went. Called when a run reaches an outcome it cannot be resumed from.
func (r *DiscoveryAnalysisCheckpointRepository) DeleteByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("delete analysis checkpoints: run_id is required")
	}
	res, err := r.col.DeleteMany(ctx, bson.M{"run_id": runID})
	if err != nil {
		return 0, fmt.Errorf("delete analysis checkpoints for run %s: %w", runID, err)
	}
	return res.DeletedCount, nil
}

// TouchByRun re-anchors the retention clock on every analysis checkpoint of a
// run, and reports how many rows moved.
//
// A resumed run READS these rows without rewriting them, so their created_at
// would otherwise stay at whatever the attempt that wrote them stamped, and
// an operator resuming near DISCOVERY_CHECKPOINT_RETENTION would watch them
// expire while the attempt was still working.
//
// It sets created_at and nothing else, which is load-bearing rather than
// incidental: touching attempt would hand a restored area this attempt's
// number, and touching exploration_attempt would defeat the staleness fence
// outright.
func (r *DiscoveryAnalysisCheckpointRepository) TouchByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("touch analysis checkpoints: run_id is required")
	}
	res, err := r.col.UpdateMany(ctx,
		bson.M{"run_id": runID},
		bson.M{"$set": bson.M{"created_at": time.Now()}},
	)
	if err != nil {
		return 0, fmt.Errorf("touch analysis checkpoints for run %s: %w", runID, err)
	}
	return res.ModifiedCount, nil
}

// EnsureIndexes creates the unique (run_id, area_id) index and the retention
// TTL.
//
// The TTL needs the same conflict dance the exploration checkpoints need:
// expireAfterSeconds cannot be mutated in place, so CreateOne against an
// existing index of the same name with a different expiry returns
// IndexOptionsConflict. Without handling it, changing
// DISCOVERY_CHECKPOINT_RETENTION would fail agent startup forever instead of
// taking effect here — i.e. the env var would be a knob for one of the two
// collections it governs.
func (r *DiscoveryAnalysisCheckpointRepository) EnsureIndexes(ctx context.Context) error {
	if _, err := r.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "run_id", Value: 1},
			{Key: "area_id", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("uq_run_area"),
	}); err != nil {
		return fmt.Errorf("ensure analysis checkpoint (run_id, area_id) index: %w", err)
	}

	ttl := mongo.IndexModel{
		Keys: bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().
			SetName(analysisCheckpointTTLIndexName).
			SetExpireAfterSeconds(int32(r.retention.Seconds())),
	}
	_, err := r.col.Indexes().CreateOne(ctx, ttl)
	if err == nil {
		return nil
	}
	if !isIndexOptionsConflict(err) {
		return fmt.Errorf("ensure analysis checkpoint TTL index: %w", err)
	}

	applog.WithFields(applog.Fields{
		"index":     analysisCheckpointTTLIndexName,
		"retention": r.retention.String(),
	}).Info("analysis checkpoint retention changed; recreating the TTL index")
	if _, err := r.col.Indexes().DropOne(ctx, analysisCheckpointTTLIndexName); err != nil {
		return fmt.Errorf("drop analysis checkpoint TTL index before recreating it at %s: %w", r.retention, err)
	}
	if _, err := r.col.Indexes().CreateOne(ctx, ttl); err != nil {
		return fmt.Errorf("recreate analysis checkpoint TTL index at %s: %w", r.retention, err)
	}
	return nil
}
