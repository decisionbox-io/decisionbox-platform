package database

import (
	"context"
	"fmt"
	"time"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DiscoveryRepository manages DiscoveryResult persistence.
type DiscoveryRepository struct {
	collection *mongo.Collection
}

// NewDiscoveryRepository creates a new discovery repository.
func NewDiscoveryRepository(client *DB) *DiscoveryRepository {
	return &DiscoveryRepository{
		collection: client.Collection(CollectionDiscoveries),
	}
}

// Save inserts a discovery result.
func (r *DiscoveryRepository) Save(ctx context.Context, result *models.DiscoveryResult) error {
	result.CreatedAt = time.Now()
	result.UpdatedAt = time.Now()

	applog.WithFields(applog.Fields{
		"project_id": result.ProjectID,
		"insights":   len(result.Insights),
		"steps":      result.TotalSteps,
	}).Debug("Saving discovery result to MongoDB")

	res, err := r.collection.InsertOne(ctx, result)
	if err != nil {
		applog.WithError(err).Error("Failed to save discovery result")
		return fmt.Errorf("failed to save discovery result: %w", err)
	}

	// Populate the ID so downstream consumers (Phase 9) can reference this discovery.
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		result.ID = oid.Hex()
	}

	applog.WithField("project_id", result.ProjectID).Info("Discovery result saved")
	return nil
}

// GetByID retrieves a discovery by its ObjectID hex string. Used by
// the validate-doc agent mode to load the target discovery + its
// embedded insights / recommendations.
func (r *DiscoveryRepository) GetByID(ctx context.Context, idHex string) (*models.DiscoveryResult, error) {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return nil, fmt.Errorf("invalid discovery id %q: %w", idHex, err)
	}
	var result models.DiscoveryResult
	if err := r.collection.FindOne(ctx, bson.M{"_id": oid}).Decode(&result); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("get discovery by id: %w", err)
	}
	return &result, nil
}

// DocVerdictUpdate is what one on-demand re-validation writes back to a
// document: the new verdict, and the grade re-derived from it.
//
// They travel together and in one write, because confidence is now a function
// of the verdict. Writing the verdict alone would leave the document claiming
// a confidence derived from the verdict it no longer has -- not merely stale
// but internally contradictory, which is worse than either staleness alone and
// is what a reader would be shown.
//
// Severity and risk_score are absent from this struct, and the sub-document
// below carries them through unchanged. That is the point rather than an
// omission: severity is magnitude x weight and a validation run changes
// neither, so the scorer re-derives the confidence axis and the priority
// rollup over it and copies the severity half from the stored document. A
// stored severity is an immutable fact about the run that produced it.
type DocVerdictUpdate struct {
	// Validation is the new verdict block. Typed as any for the reason it
	// always was: the agent's InsightValidation is an alias of the shared
	// type and the caller has it in hand already.
	Validation interface{}

	// Scoring is the derived grade, whole: the re-derived confidence axis and
	// priority rollup, and the severity half carried over from the stored
	// document. The sub-document is replaced rather than patched key by key,
	// so the carried-over half has to be present on the value passed here --
	// which is why the scorer merges rather than this layer.
	//
	// Nil writes a null, which is the correct representation of "this
	// document is not scored" -- but no caller should pass one, because a
	// re-validated document always gets a fresh score.
	Scoring *gomodels.Scoring

	// Confidence is the continuous score, duplicated out of Scoring onto the
	// document's own field because that key is a live Qdrant payload field
	// and the sort key on two dashboard pages.
	Confidence float64

	// Severity, RiskScore and Priority are the sub-document's OTHER mirrored
	// fields, and they are here because a re-score can move them.
	//
	// Not on the re-validation of an already-scored document: severity is
	// frozen there, so these arrive equal to what is stored. But two cases do
	// move them, and both would otherwise leave the row internally
	// contradictory -- a `scoring.priority_tier` of critical beside a
	// `priority` of 2, or a derived severity band beside the model's old word
	// -- which is the exact failure the three fields above travel together to
	// prevent.
	//
	//   - A recommendation's priority tier moves whenever its verdict does,
	//     because the tier is the matrix over the verdict and the inherited
	//     severity. The `priority` integer is projected from that tier.
	//   - A document the scorer never reached is scored IN FULL by this path,
	//     so it gains a severity band and a risk score it did not have.
	//
	// Written unconditionally rather than when non-zero. A zero is a real
	// value here -- an unscored recommendation's priority is 0 -- and
	// "write it only when it looks set" is how a field stops being
	// overwritten in exactly the case that needs it.
	Severity string
	// RiskScore mirrors Scoring.SeverityScore. Only meaningful for an
	// insight; a recommendation has no such field, which is why the two
	// renderers below differ.
	RiskScore float64
	// Priority is the 1-5 projection of Scoring.PriorityTier. Only
	// meaningful for a recommendation.
	Priority int
}

// insightFields and recommendationFields render the update as the $set keys
// for one embedded array element.
//
// Two renderers rather than one, because the two documents genuinely mirror
// different fields: an insight carries `risk_score` and no `priority`, a
// recommendation the reverse. One renderer writing both would add a
// `priority` of 0 to every insight and a `risk_score` to every
// recommendation -- fields neither struct declares, which BSON would store
// anyway and no decoder would ever read back.
func (u DocVerdictUpdate) insightFields(prefix string) bson.M {
	return bson.M{
		prefix + ".validation": u.Validation,
		prefix + ".scoring":    u.Scoring,
		prefix + ".confidence": u.Confidence,
		prefix + ".severity":   u.Severity,
		prefix + ".risk_score": u.RiskScore,
		"updated_at":           time.Now(),
	}
}

func (u DocVerdictUpdate) recommendationFields(prefix string) bson.M {
	return bson.M{
		prefix + ".validation": u.Validation,
		prefix + ".scoring":    u.Scoring,
		prefix + ".confidence": u.Confidence,
		prefix + ".severity":   u.Severity,
		prefix + ".priority":   u.Priority,
		"updated_at":           time.Now(),
	}
}

// UpdateInsightValidation writes the new verdict and the confidence derived
// from it onto the matching embedded insight via positional array-filter,
// leaving the other insights untouched. The element filter scopes the write so
// a concurrent discovery save doesn't clobber sibling docs.
//
// The main filter includes `insights.id` so a missing or wrong
// insightID surfaces as MatchedCount=0 — without that predicate the
// UpdateOne still matches the discovery doc, updates `updated_at`,
// touches no array element, and returns nil (silent no-op on bad
// IDs).
func (r *DiscoveryRepository) UpdateInsightValidation(ctx context.Context, discoveryIDHex, insightID string, u DocVerdictUpdate) error {
	oid, err := primitive.ObjectIDFromHex(discoveryIDHex)
	if err != nil {
		return fmt.Errorf("invalid discovery id %q: %w", discoveryIDHex, err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"el.id": insightID}},
	})
	res, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": oid, "insights.id": insightID},
		bson.M{"$set": u.insightFields("insights.$[el]")},
		opts,
	)
	if err != nil {
		return fmt.Errorf("update insight validation: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("discovery %s or insight %s not found", discoveryIDHex, insightID)
	}
	return nil
}

// UpdateRecommendationValidation is the recommendation twin of
// UpdateInsightValidation. Same positional array-filter contract +
// the same `recommendations.id` predicate on the main filter so a
// missing recID returns an error instead of silently no-op'ing the
// array filter while updating `updated_at`.
func (r *DiscoveryRepository) UpdateRecommendationValidation(ctx context.Context, discoveryIDHex, recID string, u DocVerdictUpdate) error {
	oid, err := primitive.ObjectIDFromHex(discoveryIDHex)
	if err != nil {
		return fmt.Errorf("invalid discovery id %q: %w", discoveryIDHex, err)
	}
	opts := options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []interface{}{bson.M{"el.id": recID}},
	})
	res, err := r.collection.UpdateOne(ctx,
		bson.M{"_id": oid, "recommendations.id": recID},
		bson.M{"$set": u.recommendationFields("recommendations.$[el]")},
		opts,
	)
	if err != nil {
		return fmt.Errorf("update recommendation validation: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("discovery %s or recommendation %s not found", discoveryIDHex, recID)
	}
	return nil
}

// GetLatest retrieves the most recent discovery for a project.
func (r *DiscoveryRepository) GetLatest(ctx context.Context, projectID string) (*models.DiscoveryResult, error) {
	filter := bson.M{"project_id": projectID}
	opts := options.FindOne().SetSort(bson.D{{Key: "discovery_date", Value: -1}})

	var result models.DiscoveryResult
	err := r.collection.FindOne(ctx, filter, opts).Decode(&result)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get latest discovery: %w", err)
	}
	return &result, nil
}

// ListRecent returns the last N discoveries for a project (lightweight — only summary fields).
func (r *DiscoveryRepository) ListRecent(ctx context.Context, projectID string, limit int) ([]*models.DiscoveryResult, error) {
	if limit <= 0 {
		limit = 5
	}
	applog.WithFields(applog.Fields{
		"project_id": projectID,
		"limit":      limit,
	}).Debug("Fetching recent discoveries for context")
	filter := bson.M{"project_id": projectID}
	opts := options.Find().
		SetSort(bson.D{{Key: "discovery_date", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"project_id":      1,
			"discovery_date":  1,
			"run_type":        1,
			"areas_requested": 1,
			"insights":        1,
			"recommendations": 1,
			"summary":         1,
			// Projected because the caller filters this list by it: a
			// resumed run must not be told not to re-tread its OWN partial
			// result. Omitting it here would silently make that filter
			// match nothing.
			"run_id": 1,
		})

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("list recent discoveries: %w", err)
	}
	defer cursor.Close(ctx)

	results := make([]*models.DiscoveryResult, 0)
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode recent discoveries: %w", err)
	}
	return results, nil
}

// DiscoveryRef is one discovery of a run together with the attempt that
// produced it. The attempt is what makes retirement safe: see
// models.DiscoveryResult.Attempt.
type DiscoveryRef struct {
	ID string
	// Attempt is normalised on read — a document written before the field
	// existed has no attempt and reads as 1.
	Attempt int
}

// ListByRun returns every discovery produced by one run, newest first, each
// with the attempt that produced it.
//
// A run produces exactly one result on the happy path. It produces more only
// when it was resumed after a previous attempt had already saved a partial
// result — which is precisely the case the caller needs to find, so it can
// retire the superseded documents once the new attempt has landed. The
// attempt comes back with each id because "superseded" means a LOWER attempt,
// and nothing else: the caller cannot infer it from the ordering, which is by
// discovery_date and so depends on two agents' clocks.
//
// Documents written before run_id existed carry none, so they never match —
// the right answer for a historical result.
func (r *DiscoveryRepository) ListByRun(ctx context.Context, runID string) ([]DiscoveryRef, error) {
	if runID == "" {
		return nil, nil
	}
	cursor, err := r.collection.Find(ctx,
		bson.M{"run_id": runID},
		options.Find().
			SetProjection(bson.M{"_id": 1, "attempt": 1}).
			SetSort(bson.D{{Key: "discovery_date", Value: -1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list discoveries for run %s: %w", runID, err)
	}
	defer cursor.Close(ctx) //nolint:errcheck

	refs := make([]DiscoveryRef, 0, 2)
	for cursor.Next(ctx) {
		var doc struct {
			ID      primitive.ObjectID `bson:"_id"`
			Attempt int                `bson:"attempt"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode discovery id for run %s: %w", runID, err)
		}
		// Normalised here rather than at every caller: a document from
		// before the field existed can only be the first attempt.
		attempt := doc.Attempt
		if attempt <= 0 {
			attempt = 1
		}
		refs = append(refs, DiscoveryRef{ID: doc.ID.Hex(), Attempt: attempt})
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor discoveries for run %s: %w", runID, err)
	}
	return refs, nil
}

// DeleteByID removes one discovery document. Used to retire the partial
// result a superseded attempt of a resumed run left behind.
func (r *DiscoveryRepository) DeleteByID(ctx context.Context, idHex string) error {
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		return fmt.Errorf("invalid discovery ID %q: %w", idHex, err)
	}
	if _, err := r.collection.DeleteOne(ctx, bson.M{"_id": oid}); err != nil {
		return fmt.Errorf("delete discovery %s: %w", idHex, err)
	}
	return nil
}

// EnsureIndexes creates necessary indexes.
func (r *DiscoveryRepository) EnsureIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "project_id", Value: 1},
				{Key: "discovery_date", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "created_at", Value: -1},
			},
		},
		// Resolves "which discoveries did this run produce" — the retire
		// step of a resumed run, and the filter that keeps a run's own
		// partial result out of its previous-discovery context.
		{
			Keys: bson.D{
				{Key: "run_id", Value: 1},
			},
		},
	}

	_, err := r.collection.Indexes().CreateMany(ctx, indexes)
	return err
}
