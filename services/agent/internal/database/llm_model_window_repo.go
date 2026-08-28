package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// LLMModelWindowRepository persists a model's context window learned at run
// time from a context-overflow 400 (issue #347). The discovery agent budgets
// output against the model window; for an uncatalogued model whose window it
// could only guess, the overflow error reveals the true window, and recording
// it here lets a later run budget correctly before the first call — so the
// overflow is paid at most once per project+model.
//
// This is a system-learned hint, deliberately separate from project.LLM.Config
// so the agent never overwrites the operator's own settings (the operator
// override always wins in the resolution chain). Keyed by
// (project_id, provider, model); one doc per key, upserted.
type LLMModelWindowRepository struct {
	db *DB
}

func NewLLMModelWindowRepository(db *DB) *LLMModelWindowRepository {
	return &LLMModelWindowRepository{db: db}
}

func (r *LLMModelWindowRepository) col() *mongo.Collection {
	return r.db.Collection(CollectionLLMModelWindows)
}

// EnsureIndexes creates the unique compound index on
// (project_id, provider, model) so there is at most one learned-window doc per
// key and the upsert in SaveWindow is race-safe. Idempotent.
func (r *LLMModelWindowRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.col().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "project_id", Value: 1},
			{Key: "provider", Value: 1},
			{Key: "model", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("uniq_project_provider_model"),
	})
	if err != nil {
		return fmt.Errorf("llm model window ensure indexes: %w", err)
	}
	return nil
}

// LLMModelWindowEntry is the on-disk shape.
type LLMModelWindowEntry struct {
	ProjectID   string    `bson:"project_id"`
	Provider    string    `bson:"provider"`
	Model       string    `bson:"model"`
	InputWindow int       `bson:"input_window"`
	Source      string    `bson:"source"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

// SaveWindow upserts the learned context window for (projectID, provider,
// model). A non-positive window is ignored (nothing to learn).
func (r *LLMModelWindowRepository) SaveWindow(ctx context.Context, projectID, provider, model string, window int) error {
	if projectID == "" || provider == "" || model == "" {
		return errors.New("projectID, provider and model are required")
	}
	if window <= 0 {
		return nil
	}
	filter := bson.M{"project_id": projectID, "provider": provider, "model": model}
	update := bson.M{"$set": bson.M{
		"input_window": window,
		"source":       "overflow_calibration",
		"updated_at":   time.Now().UTC(),
	}}
	if _, err := r.col().UpdateOne(ctx, filter, update, options.Update().SetUpsert(true)); err != nil {
		return fmt.Errorf("llm model window save: %w", err)
	}
	return nil
}

// GetWindow returns the persisted context window for (projectID, provider,
// model), or 0 when none is recorded. A miss is not an error — callers fall
// through to live auto-detection / catalog / default.
func (r *LLMModelWindowRepository) GetWindow(ctx context.Context, projectID, provider, model string) (int, error) {
	if projectID == "" || provider == "" || model == "" {
		return 0, errors.New("projectID, provider and model are required")
	}
	var e LLMModelWindowEntry
	err := r.col().FindOne(ctx, bson.M{
		"project_id": projectID,
		"provider":   provider,
		"model":      model,
	}).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("llm model window get: %w", err)
	}
	return e.InputWindow, nil
}
