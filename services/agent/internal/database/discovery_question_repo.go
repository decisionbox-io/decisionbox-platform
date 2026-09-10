package database

import (
	"context"
	"fmt"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DiscoveryQuestionRepository persists the clarifying questions the agent
// generates at the end of a run. The agent is the writer; the enterprise API is
// the reader/updater (it records answers and dismissals). The shared document
// type lives in libs/go-common/models so the two modules cannot drift on its
// BSON tags.
type DiscoveryQuestionRepository struct {
	collection *mongo.Collection
}

// NewDiscoveryQuestionRepository creates the repository.
func NewDiscoveryQuestionRepository(client *DB) *DiscoveryQuestionRepository {
	return &DiscoveryQuestionRepository{
		collection: client.Collection(CollectionDiscoveryQuestions),
	}
}

// EnsureIndexes creates the compound index the dedup read pattern relies on:
// ListForProject filters by project_id (+ a status $in) and sorts by created_at
// desc. Without it, the completion-path dedup query scans + sorts the whole
// collection as it grows. Mirrors the enterprise repository's index so the agent
// (writer) and API (reader) don't depend on each other's startup order.
func (r *DiscoveryQuestionRepository) EnsureIndexes(ctx context.Context) error {
	_, err := r.collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "project_id", Value: 1},
			{Key: "status", Value: 1},
			{Key: "created_at", Value: -1},
		},
	})
	if err != nil {
		return fmt.Errorf("ensure discovery_questions indexes: %w", err)
	}
	return nil
}

// Insert writes a batch of freshly-generated questions. A nil/empty batch is a
// no-op. Best-effort at the call site — the caller logs and continues on error.
func (r *DiscoveryQuestionRepository) Insert(ctx context.Context, questions []commonmodels.DiscoveryQuestion) error {
	if len(questions) == 0 {
		return nil
	}
	docs := make([]interface{}, len(questions))
	for i := range questions {
		docs[i] = questions[i]
	}
	if _, err := r.collection.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("insert discovery questions: %w", err)
	}
	applog.WithField("count", len(questions)).Debug("Inserted discovery clarifying questions")
	return nil
}

// ListForProject returns the project's questions, newest first, optionally
// filtered to the given statuses (pending / answered / dismissed). No statuses
// means "all". Used by the agent to dedup against already-asked / already-
// answered questions before generating a new batch.
func (r *DiscoveryQuestionRepository) ListForProject(ctx context.Context, projectID string, statuses ...string) ([]commonmodels.DiscoveryQuestion, error) {
	filter := bson.M{"project_id": projectID}
	if len(statuses) > 0 {
		filter["status"] = bson.M{"$in": statuses}
	}
	cur, err := r.collection.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("list discovery questions: %w", err)
	}
	defer cur.Close(ctx)
	var out []commonmodels.DiscoveryQuestion
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("decode discovery questions: %w", err)
	}
	return out, nil
}
