package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
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
// (project_id, provider_id, provider, model); one doc per key, upserted.
//
// provider_id is the project's LLM provider-ENTRY id (per-layer models,
// #407). Without it, two entries pointing at different deployments that name
// the same model alias — two LiteLLM endpoints both serving "qwen3-32b", say —
// would share one learned window and contaminate each other. It is stored
// empty for the reserved default entry, so every window already learned stays
// readable with no migration.
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
// (project_id, provider_id, provider, model) so there is at most one
// learned-window doc per key and the upsert in SaveWindow is race-safe.
// Idempotent.
//
// The earlier index omitted provider_id, and its uniqueness constraint would
// now reject the second of two provider entries learning a window for the
// same model alias — so it is dropped first. Dropping it loses no data: it
// was a constraint, not a store.
func (r *LLMModelWindowRepository) EnsureIndexes(ctx context.Context) error {
	// Ignored deliberately: the index is absent on a fresh deployment and on
	// a namespace that does not exist yet, and a genuine problem surfaces from
	// the create below — which is the operation that has to succeed.
	_, _ = r.col().Indexes().DropOne(ctx, legacyModelWindowIndex)
	_, err := r.col().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "project_id", Value: 1},
			{Key: "provider_id", Value: 1},
			{Key: "provider", Value: 1},
			{Key: "model", Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName("uniq_project_entry_provider_model"),
	})
	if err != nil {
		return fmt.Errorf("llm model window ensure indexes: %w", err)
	}
	return nil
}

// legacyModelWindowIndex is the pre-#407 index name, dropped by EnsureIndexes.
const legacyModelWindowIndex = "uniq_project_provider_model"

// windowProviderKey renders a provider-entry id for the calibration key.
//
// The reserved default entry is stored EMPTY (the field is omitempty, so
// absent), which is exactly what every pre-#407 document looks like — so a
// single-provider project keeps reading the window it already learned.
func windowProviderKey(providerID string) string {
	if providerID == llmlayer.DefaultProviderID {
		return ""
	}
	return providerID
}

// LLMModelWindowEntry is the on-disk shape.
type LLMModelWindowEntry struct {
	ProjectID string `bson:"project_id"`
	// ProviderID is the project's LLM provider-entry id, empty for the
	// reserved default entry so pre-#407 documents match.
	ProviderID  string    `bson:"provider_id,omitempty"`
	Provider    string    `bson:"provider"`
	Model       string    `bson:"model"`
	InputWindow int       `bson:"input_window"`
	Source      string    `bson:"source"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

// SaveWindow upserts the learned context window for (projectID, providerID,
// provider, model). A non-positive window is ignored (nothing to learn).
func (r *LLMModelWindowRepository) SaveWindow(ctx context.Context, projectID, providerID, provider, model string, window int) error {
	if projectID == "" || provider == "" || model == "" {
		return errors.New("projectID, provider and model are required")
	}
	if window <= 0 {
		return nil
	}
	key := windowProviderKey(providerID)
	filter := bson.M{"project_id": projectID, "provider": provider, "model": model}
	if key == "" {
		// The default entry's documents have no provider_id at all — both the
		// ones written here and every one written before #407.
		filter["provider_id"] = bson.M{"$in": bson.A{nil, ""}}
	} else {
		filter["provider_id"] = key
	}
	update := bson.M{"$set": bson.M{
		"input_window": window,
		"source":       "overflow_calibration",
		"updated_at":   time.Now().UTC(),
	}}
	if key != "" {
		update["$set"].(bson.M)["provider_id"] = key
	}
	if _, err := r.col().UpdateOne(ctx, filter, update, options.Update().SetUpsert(true)); err != nil {
		return fmt.Errorf("llm model window save: %w", err)
	}
	return nil
}

// GetWindow returns the persisted context window for (projectID, providerID,
// provider, model), or 0 when none is recorded. A miss is not an error —
// callers fall through to live auto-detection / catalog / default.
func (r *LLMModelWindowRepository) GetWindow(ctx context.Context, projectID, providerID, provider, model string) (int, error) {
	if projectID == "" || provider == "" || model == "" {
		return 0, errors.New("projectID, provider and model are required")
	}
	filter := bson.M{
		"project_id": projectID,
		"provider":   provider,
		"model":      model,
	}
	if key := windowProviderKey(providerID); key == "" {
		filter["provider_id"] = bson.M{"$in": bson.A{nil, ""}}
	} else {
		filter["provider_id"] = key
	}
	var e LLMModelWindowEntry
	err := r.col().FindOne(ctx, filter).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("llm model window get: %w", err)
	}
	return e.InputWindow, nil
}
