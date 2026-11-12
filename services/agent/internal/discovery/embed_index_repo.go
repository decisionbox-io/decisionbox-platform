package discovery

import (
	"context"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// IndexedDoc is what the duplicate check needs to know about a document already
// in the index.
type IndexedDoc struct {
	// DuplicateOf is what this document was itself folded into, or "" when it
	// is canonical. An earlier run's duplicate stays indexed in the vector
	// store, so a later document can match it and would otherwise be folded
	// into a row that is itself hidden, and that it may not resemble at all.
	DuplicateOf string
	// EmbeddingText is the text the document's vector was built from. Compared
	// because two documents with the SAME text are indistinguishable to the
	// search by construction, so anything the text leaves out has to decide
	// between them.
	EmbeddingText string
	// AffectedCount and Metrics are what BuildEmbeddingText leaves out. Two
	// insights with identical prose and different magnitudes embed identically
	// and score 1.0, and suppressing the second hides a different claim — the
	// magnitude having moved is the thing the prompt asks the model to report.
	AffectedCount int
	Metrics       map[string]interface{}
}

// EmbedIndexStore handles Phase 9 database operations.
// This interface allows unit testing without a real MongoDB connection.
type EmbedIndexStore interface {
	InsertInsights(ctx context.Context, insights []*commonmodels.StandaloneInsight) error
	InsertRecommendations(ctx context.Context, recs []*commonmodels.StandaloneRecommendation) error
	UpdateEmbedding(ctx context.Context, collection, id, embeddingText, embeddingModel string) error
	UpdateDuplicate(ctx context.Context, collection, id, duplicateOf string, score float64) error

	// IndexedDoc reads what the duplicate check needs to know about a document
	// a vector search matched, and whether that document exists at all.
	//
	// exists is separate from the fields because "" has to mean one thing. A
	// canonical document and a vector-store point whose row has since been
	// deleted both have no DuplicateOf, and folding into the second hides the
	// new document behind an original that cannot be displayed.
	IndexedDoc(ctx context.Context, collection, id string) (doc IndexedDoc, exists bool, err error)

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
