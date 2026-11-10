// Package database — discovery_recommendation_checkpoint_repo.go
//
// The durability seam for Phases 5 and 5.5, the last unprotected spend in a
// run. Exploration checkpoints let a resumed run skip Phase 3, analysis
// checkpoints let it skip the areas a previous attempt finished, analysis
// drafts let it skip the work inside an area it did not finish — and this row
// lets it skip the recommendation generation call and the per-recommendation
// verifier/refuter calls already paid for.
//
// One document per run_id: run-scoped rather than area-scoped, which is why it
// is neither another row in discovery_analysis_drafts (keyed by area) nor in
// discovery_checkpoints (whose readers scan a run's rows in step order and end
// the replayable prefix at the first unexpected number).
//
// Only a run whose Phase 5 actually banked something gets a row. A generation
// call that failed, or a response that could not be parsed, carries none — the
// same rule that leaves a failed analysis area unmarked, and for the same
// reason: marking it would make a resume restore the failure rather than retry
// it.
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

// recommendationCheckpointTTLIndexName is fixed for the same reason the other
// checkpoint TTLs' names are — see EnsureIndexes.
const recommendationCheckpointTTLIndexName = "ttl_discovery_recommendation_checkpoint"

// DiscoveryRecommendationCheckpointRepository persists and reads the per-run
// recommendation checkpoint.
type DiscoveryRecommendationCheckpointRepository struct {
	col       *mongo.Collection
	retention time.Duration
}

// NewDiscoveryRecommendationCheckpointRepository wraps the collection.
// Retention comes from the same DISCOVERY_CHECKPOINT_RETENTION as its three
// siblings, because they are one feature.
func NewDiscoveryRecommendationCheckpointRepository(db *DB) *DiscoveryRecommendationCheckpointRepository {
	return &DiscoveryRecommendationCheckpointRepository{
		col:       db.Collection(CollectionDiscoveryRecommendationCheckpoints),
		retention: config.GetEnvAsDuration(checkpointRetentionEnv, defaultCheckpointRetention),
	}
}

// Retention reports the configured lifetime.
func (r *DiscoveryRecommendationCheckpointRepository) Retention() time.Duration {
	return r.retention
}

// RecommendationCheckpointDoc is the storage shape of one run's finished
// Phase 5.
//
// models.RecommendationStep is embedded inline so its existing BSON tags stay
// authoritative, the same pattern the other checkpoint docs use. Note what is
// deliberately NOT declared here: the recommendation list. RecommendationStep
// already carries it under `bson:"recommendations"`, and both
// applyRecommendationDropStats and applyUnresolvedFigureDropStats resync that
// field to the surviving slice, so an outer field of the same name would write
// a second list that could disagree with it.
// TestRecommendationCheckpointDocTagsAreDisjoint pins that.
type RecommendationCheckpointDoc struct {
	RunID     string `bson:"run_id"`
	ProjectID string `bson:"project_id"`
	// Attempt records which attempt generated these recommendations. The
	// upsert filter is fenced on it, so an orphaned earlier attempt cannot
	// overwrite a newer one's row.
	Attempt int `bson:"attempt"`
	// ExplorationAttempt is the attempt that wrote the exploration summary the
	// insights behind these recommendations were analysed over — the same
	// staleness fence the analysis rows carry. See Load.
	ExplorationAttempt int `bson:"exploration_attempt"`

	// EligibleInsightIDs is the insight set Phase 5 was generated over, in the
	// ids the run's analysis checkpoints hold for those insights. It is the
	// restore gate; see models.RecommendationCheckpoint.
	EligibleInsightIDs []string `bson:"eligible_insight_ids"`

	// AnalysisGeneration identifies the analysis those insights came from,
	// independently of their ids. See models.RecommendationCheckpoint.
	AnalysisGeneration []string `bson:"analysis_generation,omitempty"`

	// RecVerdicts is one Phase 5.5 completion marker per recommendation
	// already submitted to the verifier, keyed by the recommendation id as
	// THIS document holds it. Written one field at a time by SaveRecVerdict
	// for the reason AnalysisAreaDraftDoc.InsightVerdicts gives: the callers
	// are concurrent.
	RecVerdicts map[string]models.ValidationResult `bson:"rec_verdicts,omitempty"`

	CreatedAt time.Time `bson:"created_at"`

	models.RecommendationStep `bson:",inline"`
}

// RecommendationCheckpointInput is one run's finished Phase 5 to checkpoint.
type RecommendationCheckpointInput struct {
	ProjectID string
	RunID     string
	Attempt   int
	// ExplorationAttempt must be the attempt that wrote this run's
	// exploration summary, for the reason the analysis rows carry it.
	ExplorationAttempt int
	Step               models.RecommendationStep
	EligibleInsightIDs []string
	AnalysisGeneration []string
}

// RecommendationVerdictInput is one per-recommendation completion marker.
type RecommendationVerdictInput struct {
	RunID            string
	Attempt          int
	RecommendationID string
	Verdict          models.ValidationResult
}

// SaveDraft writes (or replaces) the run's recommendation checkpoint.
//
// It deliberately writes no RecVerdicts, for the reason
// DiscoveryAnalysisDraftRepository.SaveDraft gives: a resumed attempt that
// RESTORED this row does not re-save it, so the markers a previous attempt
// accumulated are never overwritten by one.
//
// The attempt fence is the same as its siblings'. A duplicate key means a row
// already exists under a higher attempt, which the caller must act on rather
// than shrug at: Phase 5.5 and the whole persistence tail follow.
func (r *DiscoveryRecommendationCheckpointRepository) SaveDraft(ctx context.Context, in RecommendationCheckpointInput) error {
	if in.RunID == "" {
		return errors.New("checkpoint recommendations: run_id is required")
	}
	if in.ExplorationAttempt <= 0 {
		return fmt.Errorf(
			"checkpoint recommendations of run %s: exploration_attempt must be >= 1, got %d — recommendations cannot be checkpointed against an exploration that was never summarised",
			in.RunID, in.ExplorationAttempt,
		)
	}
	// Same rule the analysis drafts apply, and for the same reason: a row
	// whose recommendations cannot each be named by exactly one marker would
	// restore one verdict onto two of them.
	if err := markerKeysUsable(recommendationIDs(in.Step.Recommendations)); err != nil {
		return fmt.Errorf("checkpoint recommendations of run %s: %w", in.RunID, err)
	}
	doc := RecommendationCheckpointDoc{
		RunID:              in.RunID,
		ProjectID:          in.ProjectID,
		Attempt:            in.Attempt,
		ExplorationAttempt: in.ExplorationAttempt,
		EligibleInsightIDs: in.EligibleInsightIDs,
		AnalysisGeneration: in.AnalysisGeneration,
		CreatedAt:          time.Now(),
		RecommendationStep: in.Step,
	}
	filter := bson.M{
		"run_id":  in.RunID,
		"attempt": bson.M{"$lte": in.Attempt},
	}
	_, err := r.col.ReplaceOne(ctx, filter, doc, options.Replace().SetUpsert(true))
	if err == nil {
		return nil
	}
	if mongo.IsDuplicateKeyError(err) {
		applog.WithFields(applog.Fields{
			"run_id":  in.RunID,
			"attempt": in.Attempt,
		}).Info("recommendation checkpoint refused: a newer attempt of this run owns it")
		return ErrSupersededAttempt
	}
	return fmt.Errorf("checkpoint recommendations of run %s: %w", in.RunID, err)
}

// SaveRecVerdict records that one recommendation has been validated.
//
// A single-field $set, concurrent-safe, keyed by the recommendation id as the
// checkpoint holds it — which is the id live in memory for the whole of
// Phase 5.5, because a restored checkpoint's recommendations are reminted only
// after that phase returns.
//
// A write that matches nothing is not an error, for the reason
// SaveInsightVerdict gives.
// The bool reports whether the update actually APPLIED, on the same contract as
// SaveInsightVerdict's: MatchedCount 0 with a nil error is ordinary, and only a
// caller deciding whether a cooperative stop is free needs to know the
// difference.
func (r *DiscoveryRecommendationCheckpointRepository) SaveRecVerdict(ctx context.Context, in RecommendationVerdictInput) (bool, error) {
	if in.RunID == "" {
		return false, errors.New("checkpoint recommendation verdict: run_id is required")
	}
	if in.RecommendationID == "" {
		return false, errors.New("checkpoint recommendation verdict: recommendation_id is required")
	}
	if !safeMarkerKey(in.RecommendationID) {
		return false, fmt.Errorf("checkpoint verdict for recommendation %q of run %s: %w", in.RecommendationID, in.RunID, ErrUnsafeMarkerKey)
	}
	res, err := r.col.UpdateOne(ctx,
		bson.M{
			"run_id":  in.RunID,
			"attempt": bson.M{"$lte": in.Attempt},
		},
		bson.M{"$set": bson.M{"rec_verdicts." + in.RecommendationID: in.Verdict}},
	)
	if err != nil {
		return false, fmt.Errorf("checkpoint verdict for recommendation %s of run %s: %w", in.RecommendationID, in.RunID, err)
	}
	if res.MatchedCount == 0 {
		applog.WithFields(applog.Fields{
			"run_id":         in.RunID,
			"recommendation": in.RecommendationID,
		}).Debug("no recommendation checkpoint to record this verdict against; it would be validated again on a later resume")
		return false, nil
	}
	return true, nil
}

// Load returns the run's recommendation checkpoint, or nil when there is none
// to restore.
//
// explorationAttempt is the same staleness fence the analysis rows apply: a
// recommendation's evidence bundle is the token-budgeted union of its related
// insights' source steps, so a row generated over a DIFFERENT exploration must
// not come back. Zero (or negative) means the run has no durable exploration
// summary, so no row can be valid: returns nil without querying.
//
// Nil-and-no-error is the ordinary answer for a run that never reached Phase 5
// or whose Phase 5 banked nothing, so callers must not treat it as a failure.
func (r *DiscoveryRecommendationCheckpointRepository) Load(ctx context.Context, runID string, explorationAttempt int) (*models.RecommendationCheckpoint, error) {
	if runID == "" {
		return nil, errors.New("load recommendation checkpoint: run_id is required")
	}
	if explorationAttempt <= 0 {
		return nil, nil
	}
	var doc RecommendationCheckpointDoc
	err := r.col.FindOne(ctx, bson.M{"run_id": runID}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load recommendation checkpoint for run %s: %w", runID, err)
	}
	if doc.ExplorationAttempt != explorationAttempt {
		applog.WithFields(applog.Fields{
			"run_id":              runID,
			"row_exploration":     doc.ExplorationAttempt,
			"exploration_attempt": explorationAttempt,
		}).Info("ignored a recommendation checkpoint from a superseded exploration; recommendations will be generated again")
		return nil, nil
	}
	if err := markerKeysUsable(recommendationIDs(doc.Recommendations)); err != nil {
		// Defence in depth, like its counterpart in LoadDrafts: the writer
		// refuses such a row, and the cost of trusting that alone would be a
		// validation bypass rather than a lost saving.
		applog.WithFields(applog.Fields{
			"run_id": runID,
			"error":  err.Error(),
		}).Warn("ignoring a recommendation checkpoint whose recommendations cannot each be named by one marker; they will be generated again")
		return nil, nil
	}
	return &models.RecommendationCheckpoint{
		Step:               doc.RecommendationStep,
		EligibleInsightIDs: doc.EligibleInsightIDs,
		AnalysisGeneration: doc.AnalysisGeneration,
		RecVerdicts:        doc.RecVerdicts,
	}, nil
}

// recommendationIDs is the ids of a step's recommendations, in order.
func recommendationIDs(recs []models.Recommendation) []string {
	out := make([]string, len(recs))
	for i := range recs {
		out[i] = recs[i].ID
	}
	return out
}

// DeleteByRun removes the run's recommendation checkpoint and reports how many
// rows went. Called when a run reaches an outcome it cannot be resumed from.
func (r *DiscoveryRecommendationCheckpointRepository) DeleteByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("delete recommendation checkpoint: run_id is required")
	}
	res, err := r.col.DeleteMany(ctx, bson.M{"run_id": runID})
	if err != nil {
		return 0, fmt.Errorf("delete recommendation checkpoint for run %s: %w", runID, err)
	}
	return res.DeletedCount, nil
}

// TouchByRun re-anchors the retention clock, for the reason its siblings do.
// It sets created_at and nothing else.
func (r *DiscoveryRecommendationCheckpointRepository) TouchByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("touch recommendation checkpoint: run_id is required")
	}
	res, err := r.col.UpdateMany(ctx,
		bson.M{"run_id": runID},
		bson.M{"$set": bson.M{"created_at": time.Now()}},
	)
	if err != nil {
		return 0, fmt.Errorf("touch recommendation checkpoint for run %s: %w", runID, err)
	}
	return res.ModifiedCount, nil
}

// EnsureIndexes creates the unique run_id index and the retention TTL, with
// the same drop-and-recreate dance on IndexOptionsConflict its siblings need:
// expireAfterSeconds cannot be mutated in place, so without it a changed
// DISCOVERY_CHECKPOINT_RETENTION would fail agent startup forever instead of
// taking effect.
func (r *DiscoveryRecommendationCheckpointRepository) EnsureIndexes(ctx context.Context) error {
	if _, err := r.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "run_id", Value: 1}},
		Options: options.Index().SetUnique(true).SetName("uq_run"),
	}); err != nil {
		return fmt.Errorf("ensure recommendation checkpoint run_id index: %w", err)
	}

	ttl := mongo.IndexModel{
		Keys: bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().
			SetName(recommendationCheckpointTTLIndexName).
			SetExpireAfterSeconds(int32(r.retention.Seconds())),
	}
	_, err := r.col.Indexes().CreateOne(ctx, ttl)
	if err == nil {
		return nil
	}
	if !isIndexOptionsConflict(err) {
		return fmt.Errorf("ensure recommendation checkpoint TTL index: %w", err)
	}

	applog.WithFields(applog.Fields{
		"index":     recommendationCheckpointTTLIndexName,
		"retention": r.retention.String(),
	}).Info("recommendation checkpoint retention changed; recreating the TTL index")
	if _, err := r.col.Indexes().DropOne(ctx, recommendationCheckpointTTLIndexName); err != nil {
		return fmt.Errorf("drop recommendation checkpoint TTL index before recreating it at %s: %w", r.retention, err)
	}
	if _, err := r.col.Indexes().CreateOne(ctx, ttl); err != nil {
		return fmt.Errorf("recreate recommendation checkpoint TTL index at %s: %w", r.retention, err)
	}
	return nil
}
