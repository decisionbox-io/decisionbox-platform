// Package database — discovery_analysis_draft_repo.go
//
// The durability seam INSIDE an analysis area, one rung finer than
// discovery_analysis_checkpoint_repo.go. Those rows let a resumed run skip the
// areas a previous attempt finished; these let it skip the work a previous
// attempt did inside an area it did NOT finish — the analysis call, the repair
// calls, and the per-insight verifier/refuter calls already paid for.
//
// One document per (run_id, area_id), carrying the models.AnalysisStep as the
// Phase 4 loop body left it just before validation, plus one completion marker
// per insight already submitted to the verifier.
//
// Why a separate collection rather than a `stage` field on the analysis
// checkpoint row. The two rows mean different things to a reader: a completed
// area is restored and SKIPPED, a draft is restored and FINISHED. A reader
// that cannot tell them apart restores a draft as finished, which admits
// insights carrying no validation verdict into the run's result — and
// filterEligibleInsights fails open on a nil Validation, so they reach the
// recommender. A discriminator on the shared row is honoured only by readers
// that know about it, so an older agent resuming the same run applies the
// older meaning to it; a reader cannot misread a collection it never queries.
// The analysis checkpoint collection and its repository are therefore
// untouched by this feature.
//
// What the shared key would have given for free, and what replaces it:
//
//   - The completed row would have REPLACED the draft on the same key. Here
//     the draft is deleted when the area finishes (DeleteByArea), best-effort
//     with the TTL as the backstop — and correctness rests not on that delete
//     but on the loader's rule that a completed row wins over a draft for the
//     same area.
//
// Deliberately NOT discovery_analysis_steps: that is the tail-written audit
// log keyed by discovery_id and read by the dashboard. These rows are keyed by
// (run_id, area_id), exist only while a run is in flight, and are deleted once
// the run reaches a terminal-and-not-resumable outcome.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/config"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// analysisDraftTTLIndexName is fixed for the same reason the other two
// checkpoint TTLs' names are: a retention change has to be recognisable as the
// SAME index with different options, which is what lets EnsureIndexes recreate
// it rather than failing forever.
const analysisDraftTTLIndexName = "ttl_discovery_analysis_draft"

// ErrUnsafeMarkerKey means a document id cannot be used as a BSON field name,
// so no per-item completion marker can be keyed on it.
//
// The ids are normally minted UUIDs, but a model that supplies its own id for
// an insight or a recommendation keeps it — the analysis parser only mints one
// when the field is absent. An id containing a dot would silently become a
// NESTED field rather than a marker: the row would come back carrying a
// zero-valued marker under the prefix, which the budget counts while the
// restore cannot match, so an insight that was already verified ends up
// skipped on the cap instead of restored.
//
// Refusing to write it degrades exactly the way a failed marker write does —
// the document is validated again on a later resume — which is a cost rather
// than a corruption. (Such an id is already a hazard outside this package:
// the embedded id is reused as the Qdrant point id, and Qdrant accepts only
// UUIDs and uint64s.)
var ErrUnsafeMarkerKey = errors.New("document id cannot be a BSON field name, so no completion marker can be keyed on it")

// ErrAmbiguousMarkerKey means two documents of one checkpoint share an id, so
// a per-item marker cannot say which of them it describes.
//
// Reachable for the same reason ErrUnsafeMarkerKey is: a model that supplies
// its own ids can repeat one. The consequence is worse than a lost marker — it
// is a validation BYPASS. One marker would be restored onto both documents, so
// the second would skip the verifier and could enter the recommender carrying
// a `confirmed` verdict earned by an unrelated finding, and the late remint
// would then give the two distinct ids without correcting the verdict either
// of them carries.
//
// So a checkpoint is refused rather than written ambiguously, and the
// documents are validated again on a later resume. (Duplicate ids are already
// a hazard outside this package — the tail inserts each id as a standalone
// `_id` — but that is no reason to add a silent one here.)
var ErrAmbiguousMarkerKey = errors.New("two documents share an id, so a completion marker cannot say which it describes")

// safeMarkerKey reports whether an id can be a BSON field name. A dot would
// create a nested path, a leading "$" reads as an operator, and a NUL is
// invalid outright.
func safeMarkerKey(id string) bool {
	return id != "" &&
		!strings.Contains(id, ".") &&
		!strings.HasPrefix(id, "$") &&
		!strings.ContainsRune(id, 0)
}

// markerKeysUsable reports whether a checkpoint's documents can carry per-item
// markers at all: every id must be a legal BSON field name and no two may be
// the same. Named rather than inlined because both checkpoint repositories
// apply it, on the way in AND on the way out.
func markerKeysUsable(ids []string) error {
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !safeMarkerKey(id) {
			return fmt.Errorf("id %q: %w", id, ErrUnsafeMarkerKey)
		}
		if seen[id] {
			return fmt.Errorf("id %q: %w", id, ErrAmbiguousMarkerKey)
		}
		seen[id] = true
	}
	return nil
}

// insightIDs is the ids of a step's insights, in order.
func insightIDs(insights []models.Insight) []string {
	out := make([]string, len(insights))
	for i := range insights {
		out[i] = insights[i].ID
	}
	return out
}

// DiscoveryAnalysisDraftRepository persists and reads in-flight analysis
// areas and their per-insight validation markers.
type DiscoveryAnalysisDraftRepository struct {
	col       *mongo.Collection
	retention time.Duration
}

// NewDiscoveryAnalysisDraftRepository wraps the analysis-draft collection.
//
// Retention comes from the SAME DISCOVERY_CHECKPOINT_RETENTION as the other
// two checkpoint collections, because these rows are one feature with them: a
// draft is only ever usable alongside an exploration prefix the run can
// replay, so independently settable retentions would mostly be a way to
// misconfigure the set.
func NewDiscoveryAnalysisDraftRepository(db *DB) *DiscoveryAnalysisDraftRepository {
	return &DiscoveryAnalysisDraftRepository{
		col:       db.Collection(CollectionDiscoveryAnalysisDrafts),
		retention: config.GetEnvAsDuration(checkpointRetentionEnv, defaultCheckpointRetention),
	}
}

// Retention reports the configured lifetime. Exposed for the startup log line
// and the integration test that asserts the TTL matches it.
func (r *DiscoveryAnalysisDraftRepository) Retention() time.Duration { return r.retention }

// AnalysisAreaDraftDoc is the storage shape of one in-flight area.
//
// models.AnalysisStep is embedded inline so its existing BSON tags stay
// authoritative — the same pattern AnalysisAreaCheckpointDoc and
// ExplorationCheckpointDoc use. Note what is deliberately NOT declared here:
// the area id. AnalysisStep.AreaID already carries `bson:"area_id"` and is the
// key half of the unique index, so an outer field of the same name would write
// the key twice. TestAnalysisAreaDraftDocTagsAreDisjoint pins that.
type AnalysisAreaDraftDoc struct {
	RunID     string `bson:"run_id"`
	ProjectID string `bson:"project_id"`
	// Attempt records which attempt of the run drafted this area. The upsert
	// filter is fenced on it, so an orphaned earlier attempt cannot overwrite
	// a newer one's draft.
	Attempt int `bson:"attempt"`
	// ExplorationAttempt is the attempt that wrote the exploration summary
	// this area's analysis was computed over. It is the staleness fence — see
	// LoadDrafts.
	ExplorationAttempt int `bson:"exploration_attempt"`

	// InsightVerdicts is one completion marker per insight already submitted
	// to the verifier, keyed by the insight id as THIS document holds it.
	//
	// Written one field at a time by SaveInsightVerdict rather than by
	// re-writing the document, which is a correctness requirement and not an
	// optimisation: the verdicts are produced by validationPhase.runBounded's
	// workers, and two of them replacing the whole document with their own
	// copy of the map would lose one of the two verdicts. Mongo applies
	// concurrent $sets on distinct paths of one document without losing
	// either.
	InsightVerdicts map[string]models.ValidationResult `bson:"insight_verdicts,omitempty"`

	CreatedAt time.Time `bson:"created_at"`

	// MissingValueFigures records, per insight id, which of its figures the
	// model declared no readable value for — because models.Figure.ValueMissing
	// is `bson:"-"`. Exactly the reason AnalysisAreaCheckpointDoc carries the
	// same field, and the same consequence if it is dropped: usableFigures
	// withholds a value-missing figure from the recommender prompt and
	// buildFigureRefIndex refuses to resolve it, both reading the flag, so a
	// restored figure without it reads as a real zero and ships a fabricated
	// measurement. Keyed by the insight id as STORED, so it is applied before
	// any restore remints ids.
	MissingValueFigures map[string][]string `bson:"missing_value_figures,omitempty"`

	models.AnalysisStep `bson:",inline"`
}

// AnalysisAreaDraftInput is one in-flight area to draft.
type AnalysisAreaDraftInput struct {
	ProjectID string
	RunID     string
	Attempt   int
	// ExplorationAttempt must be the attempt that wrote this run's
	// exploration summary. A run with no durable summary has no valid value
	// for it and must not draft areas at all — there is no exploration a later
	// attempt could restore them against.
	ExplorationAttempt int
	Step               models.AnalysisStep
}

// InsightVerdictInput is one per-insight completion marker.
type InsightVerdictInput struct {
	RunID     string
	AreaID    string
	Attempt   int
	InsightID string
	Verdict   models.ValidationResult
}

// AnalysisDraftSet is what a resumed run gets back: the in-flight areas it may
// restore and finish, by area id.
type AnalysisDraftSet struct {
	Drafts map[string]models.AnalysisAreaDraft
	// Attempt is the highest attempt number among the restored drafts, so a
	// resumed run can log which attempt's work it is continuing.
	Attempt int
}

// Len reports how many drafts may be restored. Safe on nil.
func (s *AnalysisDraftSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.Drafts)
}

// SaveDraft writes (or replaces) one in-flight area's draft.
//
// ReplaceOne with upsert against the unique (run_id, area_id) index, so an
// attempt that re-analyses an area a previous attempt drafted replaces its row
// rather than duplicating it or failing on the index.
//
// It deliberately writes no InsightVerdicts: a fresh draft has none, and a
// resumed attempt that RESTORED a draft does not re-save it, so the markers a
// previous attempt accumulated are never overwritten by one. That is what lets
// a third attempt read every marker any attempt wrote under the one set of ids
// the immutable draft holds.
//
// The attempt bound is the same fence SaveStep and SaveArea use, against the
// same reachable state: the API's startup sweep marks in-flight runs `failed`
// WITHOUT reaping their workloads, so an operator resuming such a run can have
// two agents on one run id. $lte rather than equality, because a resumed
// attempt legitimately replaces a draft an earlier attempt wrote.
func (r *DiscoveryAnalysisDraftRepository) SaveDraft(ctx context.Context, in AnalysisAreaDraftInput) error {
	if in.RunID == "" {
		return errors.New("draft analysis area: run_id is required")
	}
	if in.Step.AreaID == "" {
		return errors.New("draft analysis area: area_id is required")
	}
	if in.ExplorationAttempt <= 0 {
		return fmt.Errorf(
			"draft analysis area %s: exploration_attempt must be >= 1, got %d — an area cannot be drafted against an exploration that was never summarised",
			in.Step.AreaID, in.ExplorationAttempt,
		)
	}
	// Refused rather than written ambiguously: a draft whose insights cannot
	// each be named by exactly one marker would restore one verdict onto two
	// findings. The area is analysed again on a later resume instead.
	if err := markerKeysUsable(insightIDs(in.Step.Insights)); err != nil {
		return fmt.Errorf("draft analysis area %s of run %s: %w", in.Step.AreaID, in.RunID, err)
	}
	doc := AnalysisAreaDraftDoc{
		RunID:               in.RunID,
		ProjectID:           in.ProjectID,
		Attempt:             in.Attempt,
		ExplorationAttempt:  in.ExplorationAttempt,
		CreatedAt:           time.Now(),
		MissingValueFigures: missingValueFigures(in.Step.Insights),
		AnalysisStep:        in.Step,
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
	// A duplicate key means the filter matched nothing, so the upsert tried to
	// insert and the unique index refused it — a row already exists under a
	// HIGHER attempt. This attempt has been superseded, which the caller must
	// act on rather than shrug at: the per-insight validation about to follow
	// is the most expensive part of the area.
	if mongo.IsDuplicateKeyError(err) {
		applog.WithFields(applog.Fields{
			"run_id":  in.RunID,
			"area":    in.Step.AreaID,
			"attempt": in.Attempt,
		}).Info("analysis draft refused: a newer attempt of this run owns this area")
		return ErrSupersededAttempt
	}
	return fmt.Errorf("draft analysis area %s of run %s: %w", in.Step.AreaID, in.RunID, err)
}

// SaveInsightVerdict records that one insight of a drafted area has been
// validated, so a resumed run does not pay for its verifier and refuter again.
//
// A single-field $set rather than a document replace, for the reason
// AnalysisAreaDraftDoc.InsightVerdicts gives: the callers are concurrent.
//
// Keyed by the insight id as the DRAFT holds it, which is the id live in
// memory for the whole of the area's in-flight life — a restored draft's
// insights are reminted only when the area is merged into the run's result, so
// every attempt writing a marker for the same draft writes it under the same
// key.
//
// A write that matches nothing is NOT an error and is not reported as one. It
// means the draft row is gone (its area finished, or the TTL reclaimed it) or
// a newer attempt owns it; the cost either way is one re-validated insight on
// a later resume, and a checkpoint must never be able to abort a working run.
// The ownership signal that does matter is raised one level up, by SaveDraft
// and by SaveArea.
func (r *DiscoveryAnalysisDraftRepository) SaveInsightVerdict(ctx context.Context, in InsightVerdictInput) error {
	if in.RunID == "" {
		return errors.New("checkpoint insight verdict: run_id is required")
	}
	if in.AreaID == "" {
		return errors.New("checkpoint insight verdict: area_id is required")
	}
	if in.InsightID == "" {
		return errors.New("checkpoint insight verdict: insight_id is required")
	}
	if !safeMarkerKey(in.InsightID) {
		return fmt.Errorf("checkpoint verdict for insight %q of run %s: %w", in.InsightID, in.RunID, ErrUnsafeMarkerKey)
	}
	res, err := r.col.UpdateOne(ctx,
		bson.M{
			"run_id":  in.RunID,
			"area_id": in.AreaID,
			"attempt": bson.M{"$lte": in.Attempt},
		},
		bson.M{"$set": bson.M{"insight_verdicts." + in.InsightID: in.Verdict}},
	)
	if err != nil {
		return fmt.Errorf("checkpoint verdict for insight %s of run %s: %w", in.InsightID, in.RunID, err)
	}
	if res.MatchedCount == 0 {
		applog.WithFields(applog.Fields{
			"run_id":  in.RunID,
			"area":    in.AreaID,
			"insight": in.InsightID,
		}).Debug("no analysis draft to record this insight verdict against; it would be validated again on a later resume")
	}
	return nil
}

// LoadDrafts returns the in-flight areas a resumed run may restore and finish,
// bounded to the exploration it is itself about to analyse over.
//
// explorationAttempt is the attempt that wrote the exploration summary this
// run trusts, and filtering on it is the same correctness fence the analysis
// checkpoints apply: a draft's insights cite exploration step numbers through
// SourceSteps, and the verifier, the evidence-quality labelling and the
// recommender all resolve them against the run's own steps, so an area drafted
// over a DIFFERENT exploration must not come back.
//
// Zero (or negative) means the run has no durable exploration summary, so no
// row can be valid: returns an empty set without querying.
//
// NOTE the caller's obligation, which this method cannot discharge: a draft is
// superseded by the analysis CHECKPOINT for the same area, and the two live in
// different collections. Whoever loads both must prefer the completed row. See
// Orchestrator.loadAreaDrafts.
func (r *DiscoveryAnalysisDraftRepository) LoadDrafts(ctx context.Context, runID string, explorationAttempt int) (*AnalysisDraftSet, error) {
	if runID == "" {
		return nil, errors.New("load analysis drafts: run_id is required")
	}
	set := &AnalysisDraftSet{Drafts: make(map[string]models.AnalysisAreaDraft)}
	if explorationAttempt <= 0 {
		return set, nil
	}
	cur, err := r.col.Find(ctx, bson.M{"run_id": runID})
	if err != nil {
		return nil, fmt.Errorf("load analysis drafts for run %s: %w", runID, err)
	}
	defer cur.Close(ctx) //nolint:errcheck

	stale := 0
	for cur.Next(ctx) {
		var doc AnalysisAreaDraftDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode analysis draft for run %s: %w", runID, err)
		}
		if doc.ExplorationAttempt != explorationAttempt {
			stale++
			continue
		}
		if doc.AreaID == "" {
			// Nothing writes such a row — SaveDraft refuses it — so this is a
			// hand-edited or corrupted document. Skipped rather than keyed
			// under "", which would look like an area and suppress a real one.
			applog.WithField("run_id", runID).Warn("analysis draft row has no area id; ignoring it")
			continue
		}
		if err := markerKeysUsable(insightIDs(doc.Insights)); err != nil {
			// The writer refuses such a row, so this is defence in depth
			// rather than a reachable state — and it is the one guard whose
			// absence would be a validation bypass rather than a cost, so it
			// does not rest on the writer alone.
			applog.WithFields(applog.Fields{
				"run_id": runID,
				"area":   doc.AreaID,
				"error":  err.Error(),
			}).Warn("ignoring an analysis draft whose insights cannot each be named by one marker; the area will be analysed again")
			continue
		}
		if len(doc.Insights) == 0 {
			// A draft with no insights protects nothing: there is no
			// validation to resume, and restoring it would hand the loop an
			// area with nothing in it instead of letting it analyse the area
			// again. SaveDraft's caller refuses to write one, so this is the
			// same corrupted-row case as above.
			applog.WithFields(applog.Fields{
				"run_id": runID,
				"area":   doc.AreaID,
			}).Warn("analysis draft row carries no insights; ignoring it so the area is analysed again")
			continue
		}
		if doc.Attempt > set.Attempt {
			set.Attempt = doc.Attempt
		}
		// Before anything reads the step: BSON dropped these flags on the way
		// in, and Phase 5 depends on them. See MissingValueFigures.
		applyMissingValueFigures(doc.Insights, doc.MissingValueFigures)
		set.Drafts[doc.AreaID] = models.AnalysisAreaDraft{
			Step:            doc.AnalysisStep,
			InsightVerdicts: markersForInsights(doc.Insights, doc.InsightVerdicts, runID, doc.AreaID),
		}
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("cursor analysis drafts for run %s: %w", runID, err)
	}
	if stale > 0 {
		applog.WithFields(applog.Fields{
			"run_id":              runID,
			"restorable":          len(set.Drafts),
			"ignored":             stale,
			"exploration_attempt": explorationAttempt,
		}).Info("ignored analysis drafts from a superseded exploration; those areas will be analysed again")
	}
	return set, nil
}

// markersForInsights narrows a draft's markers to the insights it actually
// carries.
//
// A marker that names nothing in the draft can be neither restored nor
// skipped, so counting it would charge the run's validation cap for work no
// document can claim — and at the cap that turns an insight the previous
// attempt verified into one skipped for budget. The same rule
// restoredRecommendationBudget applies on the recommendation side, here so the
// budget arithmetic is sound regardless of how a key reached the document.
//
// Nothing the writer accepts can produce such a marker; this is the guard that
// keeps that true rather than assumed.
func markersForInsights(insights []models.Insight, markers map[string]models.ValidationResult, runID, areaID string) map[string]models.ValidationResult {
	if len(markers) == 0 {
		return nil
	}
	carried := make(map[string]bool, len(insights))
	for i := range insights {
		carried[insights[i].ID] = true
	}
	out := make(map[string]models.ValidationResult, len(markers))
	for id, vr := range markers {
		if carried[id] {
			out[id] = vr
			continue
		}
		applog.WithFields(applog.Fields{
			"run_id":  runID,
			"area":    areaID,
			"insight": id,
		}).Warn("ignoring an analysis draft marker that names no insight in the draft; that insight will be validated again")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DeleteByArea removes one area's draft, because the area has finished and its
// analysis checkpoint now describes the same work with its validation done.
//
// Best-effort by contract: the loader prefers the completed row over a draft
// for the same area, so a draft that outlives its area is ignored rather than
// restored, and the TTL reclaims it. Deleting it promptly keeps the collection
// to the rows that can still be used.
func (r *DiscoveryAnalysisDraftRepository) DeleteByArea(ctx context.Context, runID, areaID string) error {
	if runID == "" {
		return errors.New("delete analysis draft: run_id is required")
	}
	if areaID == "" {
		return errors.New("delete analysis draft: area_id is required")
	}
	if _, err := r.col.DeleteOne(ctx, bson.M{"run_id": runID, "area_id": areaID}); err != nil {
		return fmt.Errorf("delete analysis draft %s of run %s: %w", areaID, runID, err)
	}
	return nil
}

// DeleteByRun removes every analysis draft of a run and reports how many rows
// went. Called when a run reaches an outcome it cannot be resumed from.
func (r *DiscoveryAnalysisDraftRepository) DeleteByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("delete analysis drafts: run_id is required")
	}
	res, err := r.col.DeleteMany(ctx, bson.M{"run_id": runID})
	if err != nil {
		return 0, fmt.Errorf("delete analysis drafts for run %s: %w", runID, err)
	}
	return res.DeletedCount, nil
}

// TouchByRun re-anchors the retention clock on every analysis draft of a run,
// and reports how many rows moved.
//
// A resumed run READS these rows without rewriting them, so their created_at
// would otherwise stay at whatever the attempt that wrote them stamped, and an
// operator resuming near DISCOVERY_CHECKPOINT_RETENTION would watch them
// expire while the attempt was still working.
//
// It sets created_at and nothing else, which is load-bearing: touching attempt
// would hand a restored draft this attempt's number, and touching
// exploration_attempt would defeat the staleness fence outright.
func (r *DiscoveryAnalysisDraftRepository) TouchByRun(ctx context.Context, runID string) (int64, error) {
	if runID == "" {
		return 0, errors.New("touch analysis drafts: run_id is required")
	}
	res, err := r.col.UpdateMany(ctx,
		bson.M{"run_id": runID},
		bson.M{"$set": bson.M{"created_at": time.Now()}},
	)
	if err != nil {
		return 0, fmt.Errorf("touch analysis drafts for run %s: %w", runID, err)
	}
	return res.ModifiedCount, nil
}

// EnsureIndexes creates the unique (run_id, area_id) index and the retention
// TTL.
//
// The TTL needs the same conflict dance the other checkpoint collections need:
// expireAfterSeconds cannot be mutated in place, so CreateOne against an
// existing index of the same name with a different expiry returns
// IndexOptionsConflict. Without handling it, changing
// DISCOVERY_CHECKPOINT_RETENTION would fail agent startup forever instead of
// taking effect here.
func (r *DiscoveryAnalysisDraftRepository) EnsureIndexes(ctx context.Context) error {
	if _, err := r.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "run_id", Value: 1},
			{Key: "area_id", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("uq_run_area"),
	}); err != nil {
		return fmt.Errorf("ensure analysis draft (run_id, area_id) index: %w", err)
	}

	ttl := mongo.IndexModel{
		Keys: bson.D{{Key: "created_at", Value: 1}},
		Options: options.Index().
			SetName(analysisDraftTTLIndexName).
			SetExpireAfterSeconds(int32(r.retention.Seconds())),
	}
	_, err := r.col.Indexes().CreateOne(ctx, ttl)
	if err == nil {
		return nil
	}
	if !isIndexOptionsConflict(err) {
		return fmt.Errorf("ensure analysis draft TTL index: %w", err)
	}

	applog.WithFields(applog.Fields{
		"index":     analysisDraftTTLIndexName,
		"retention": r.retention.String(),
	}).Info("analysis draft retention changed; recreating the TTL index")
	if _, err := r.col.Indexes().DropOne(ctx, analysisDraftTTLIndexName); err != nil {
		return fmt.Errorf("drop analysis draft TTL index before recreating it at %s: %w", r.retention, err)
	}
	if _, err := r.col.Indexes().CreateOne(ctx, ttl); err != nil {
		return fmt.Errorf("recreate analysis draft TTL index at %s: %w", r.retention, err)
	}
	return nil
}
