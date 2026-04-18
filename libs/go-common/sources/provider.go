// Package sources defines the interface and registry for retrieving relevant
// text chunks from project knowledge sources (PDFs, DOCX, XLSX, CSV, etc.).
//
// The community platform ships only the interface and a no-op implementation.
// The enterprise plugin registers a real provider via init() that performs
// vector search against indexed source chunks.
//
// Call sites in the agent and API are unconditional: they always invoke
// GetProvider().RetrieveContext(...). Without the enterprise plugin loaded,
// the no-op returns an empty slice and the prompt is unchanged.
package sources

import (
	"context"

	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"go.mongodb.org/mongo-driver/mongo"
)

// Provider retrieves relevant text chunks from project knowledge sources.
type Provider interface {
	// RetrieveContext returns the most semantically relevant chunks for a query,
	// scoped to the given project. Returns an empty slice if no chunks are
	// indexed, no provider is registered, or none pass the score threshold.
	//
	// Implementations MUST scope retrieval to projectID — sources from one
	// project must never appear in another project's results.
	RetrieveContext(ctx context.Context, projectID string, query string, opts RetrieveOpts) ([]Chunk, error)
}

// Chunk is a retrieved fragment of a source document.
type Chunk struct {
	// SourceID is the stable UUID of the parent source document.
	SourceID string
	// SourceName is the original filename, used for citation in prompts.
	SourceName string
	// SourceType is one of "pdf", "docx", "xlsx", "csv", "md", "txt".
	SourceType string
	// Text is the chunk content, already trimmed and ready to inject.
	Text string
	// Score is the similarity score in [0, 1].
	Score float64
	// Position is the chunk index within the source (0-based).
	Position int
	// Metadata carries per-chunk hints such as page number or sheet name.
	// Keys depend on the source type — see parser docs for the full set.
	Metadata map[string]string
}

// RetrieveOpts controls retrieval behavior. Zero values mean "use the
// implementation default".
type RetrieveOpts struct {
	// Limit is the maximum number of chunks to return (top-K).
	Limit int
	// MinScore is the minimum cosine similarity. Chunks below this are dropped.
	MinScore float64
}

// Dependencies bundles infrastructure handles needed by Provider implementations.
// The enterprise plugin's factory consumes this to construct its retriever.
type Dependencies struct {
	// Mongo is the MongoDB database the platform uses (sources collections live here).
	Mongo *mongo.Database
	// Vectorstore is the vector store the platform uses (Qdrant).
	Vectorstore vectorstore.Provider
	// SecretProvider supplies per-project credentials such as embedding-api-key.
	SecretProvider secrets.Provider
}

// ProviderFactory constructs a Provider from runtime dependencies.
// Enterprise plugins register a factory in init(); the API/Agent invokes it
// later via Configure once their own initialization is complete.
type ProviderFactory func(deps Dependencies) (Provider, error)
