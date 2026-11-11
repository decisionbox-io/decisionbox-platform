package discovery

import (
	"context"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
)

// EmbedIndexStore handles Phase 9 database operations.
// This interface allows unit testing without a real MongoDB connection.
type EmbedIndexStore interface {
	InsertInsights(ctx context.Context, insights []*commonmodels.StandaloneInsight) error
	InsertRecommendations(ctx context.Context, recs []*commonmodels.StandaloneRecommendation) error
	UpdateEmbedding(ctx context.Context, collection, id, embeddingText, embeddingModel string) error
	UpdateDuplicate(ctx context.Context, collection, id, duplicateOf string, score float64) error

	// UpdateDocVerdict rewrites one standalone row's verdict and the
	// confidence derived from it, after an on-demand re-validation.
	//
	// It lives beside the two above because they already write these same two
	// collections by id, and it writes all three fields at once for the
	// reason DocVerdictUpdate names: confidence is a function of the verdict,
	// so a row carrying one without the other is internally contradictory.
	// This matters more here than on the discovery document, because these
	// collections are what the insights list page and Ask read directly.
	UpdateDocVerdict(ctx context.Context, collection, id string, u database.DocVerdictUpdate) error

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
