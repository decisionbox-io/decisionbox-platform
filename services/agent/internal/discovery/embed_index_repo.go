package discovery

import (
	"context"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// EmbedIndexStore handles Phase 9 database operations.
// This interface allows unit testing without a real MongoDB connection.
type EmbedIndexStore interface {
	InsertInsights(ctx context.Context, insights []*commonmodels.StandaloneInsight) error
	InsertRecommendations(ctx context.Context, recs []*commonmodels.StandaloneRecommendation) error
	UpdateEmbedding(ctx context.Context, collection, id, embeddingText, embeddingModel string) error
	UpdateDuplicate(ctx context.Context, collection, id, duplicateOf string, score float64) error

	// DeleteByDiscovery removes the standalone insight / recommendation rows
	// of one discovery and returns the ids it deleted, insights first then
	// recommendations.
	//
	// The ids come back because they are also the Qdrant point ids: the
	// caller deletes the vectors through vectorstore.Provider.Delete, which
	// needs the list, and the rows have to be read before they are removed.
	// Returning them rather than deleting the points in here keeps this
	// store a Mongo store.
	DeleteByDiscovery(ctx context.Context, discoveryID string) (insightIDs, recIDs []string, err error)
}
