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

	// DuplicateOf reports what a document was folded into, or "" when it is
	// canonical. Needed because an earlier run's duplicate stays indexed in the
	// vector store: a later document can match it and would otherwise be folded
	// into a row that is itself hidden, and that it may not resemble at all.
	DuplicateOf(ctx context.Context, collection, id string) (string, error)

	// PointIDsByDiscovery lists the ids of one discovery's standalone
	// insight / recommendation rows, insights first then recommendations,
	// WITHOUT deleting anything.
	//
	// Separate from the delete because the row ids are also the Qdrant point
	// ids: once the rows are gone the points are unaddressable, so the
	// caller lists, deletes the vectors, and only then deletes the rows.
	// Listing here rather than deleting the points keeps this a Mongo store.
	PointIDsByDiscovery(ctx context.Context, discoveryID string) (insightIDs, recIDs []string, err error)

	// DeleteByDiscovery removes the standalone insight / recommendation rows
	// of one discovery. Call it only once the matching vectors are gone.
	DeleteByDiscovery(ctx context.Context, discoveryID string) error
}
