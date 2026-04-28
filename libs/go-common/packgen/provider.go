// Package packgen defines the interface and registry for generating
// DecisionBox domain packs from a customer's knowledge sources and
// warehouse schema.
//
// The package ships an interface, a registry, and a no-op default
// implementation. A plugin registers a real provider via init() with a
// blank import; until that happens GetProvider() returns the no-op,
// whose methods return ErrNotConfigured.
//
// Call sites in the agent and API are unconditional: they always invoke
// GetProvider().Generate(...) or GetProvider().RegenerateSection(...).
// Handlers translate ErrNotConfigured into a "feature unavailable"
// response.
package packgen

import (
	"context"
	"errors"

	"github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/decisionbox-io/decisionbox/libs/go-common/secrets"
	"github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"go.mongodb.org/mongo-driver/mongo"
)

// EmbeddingFactory constructs an embedding provider for a per-project
// embedding configuration. Pack generation uses this to embed the
// business summary / source chunks before querying the project's
// schema-blurb Qdrant collection so the schema slice fed into synth
// is semantically related to the customer's actual business, not
// just the largest tables in their warehouse.
//
// Mirrors the EmbeddingFactory shape used by the sources worker so
// callers configure the same factory for both call sites.
type EmbeddingFactory func(provider string, cfg embedding.ProviderConfig) (embedding.Provider, error)

// ErrNotConfigured is returned by every method of the no-op Provider.
// Handlers MUST translate this into a "feature unavailable" response
// (HTTP 404 in the API, fatal exit in the agent).
var ErrNotConfigured = errors.New("packgen: provider not configured")

// Provider generates and regenerates DecisionBox domain packs for a project.
//
// Implementations may run their work synchronously (the agent process in
// --mode=pack-gen) or asynchronously (an API handler delegating to the
// agent via the runner abstraction). Callers can distinguish via
// GenerateResult.Async.
//
// Implementations MUST scope all work to GenerateRequest.ProjectID. A
// generation triggered for one project must never read or write data
// belonging to another project.
type Provider interface {
	// Generate runs a full pack generation for the given project. The
	// implementation retrieves project knowledge sources and a warehouse
	// schema slice, calls the project's configured LLM with the pack-gen
	// prompt, validates the output, persists the resulting pack to
	// MongoDB, and updates project lifecycle state.
	//
	// Implementations may execute synchronously or by spawning a worker;
	// see GenerateResult.Async.
	Generate(ctx context.Context, req GenerateRequest) (*GenerateResult, error)

	// RegenerateSection synchronously re-emits a single section of an
	// existing pack using the user's feedback. Section regeneration is a
	// single LLM call and never spawns a worker.
	RegenerateSection(ctx context.Context, req RegenerateSectionRequest) (*RegenerateSectionResult, error)
}

// GenerateRequest carries inputs for a full pack generation.
type GenerateRequest struct {
	// ProjectID is the project that owns the warehouse and knowledge
	// sources used to seed the pack. Required.
	ProjectID string

	// RunID is the identifier of the pack-generation run. Mirrors the
	// discovery run-ID format and is used for status updates and logs.
	// When empty, implementations may generate one.
	RunID string

	// PackName is the human-readable name of the new pack ("Acme Gaming").
	// Required.
	PackName string

	// PackSlug is the unique slug of the new pack ("acme-gaming"). Must
	// match ^[a-z][a-z0-9-]*$. Required.
	PackSlug string

	// Description is an optional one-paragraph user-supplied summary of
	// the customer's domain. Empty string means no extra hint.
	Description string
}

// GenerateResult is the outcome of a successful Generate call.
//
// When Async is true, only RunID is populated; the dashboard polls the
// project state for completion. When Async is false, the work is done:
// PackSlug names the persisted pack and Attempts records how many LLM
// round-trips it took to converge on a valid result (1..3).
type GenerateResult struct {
	// RunID identifies the run carrying out the generation. Always populated.
	RunID string

	// Async is true when Generate returned before the work was complete.
	Async bool

	// PackSlug is the slug of the generated pack. Populated only when
	// Async is false.
	PackSlug string

	// Attempts is the number of LLM round-trips taken (1..3). Populated
	// only when Async is false.
	Attempts int
}

// RegenerateSectionRequest carries inputs for a section-level regeneration.
type RegenerateSectionRequest struct {
	// ProjectID is the project that owns the pack being regenerated. Required.
	ProjectID string

	// PackSlug identifies the existing pack to mutate. Required.
	PackSlug string

	// Section is the dotted path of the field being regenerated, e.g.
	// "categories", "analysis_areas.base", "prompts.exploration",
	// "profile_schema.base". Required.
	Section string

	// Feedback is the user-supplied free-text guidance steering the
	// regeneration ("more retention focus, less monetization"). Required.
	Feedback string
}

// RegenerateSectionResult is the outcome of a successful section regeneration.
type RegenerateSectionResult struct {
	// PackSlug is the slug of the updated pack.
	PackSlug string

	// Section mirrors the input — the section that was rewritten.
	Section string

	// Attempts is the number of LLM round-trips taken (1..3).
	Attempts int
}

// Dependencies bundles the infrastructure handles needed by a Provider.
// The factory passed to RegisterFactory consumes this to construct its
// orchestrator.
//
// Both the API and Agent build a Dependencies value at startup and call
// Configure once. Dependencies is intentionally minimal — per-project
// LLM and warehouse providers are looked up at request time using the
// project document plus SecretProvider, mirroring how the discovery
// agent already resolves them.
type Dependencies struct {
	// Mongo is the MongoDB database where projects and domain_packs live.
	Mongo *mongo.Database

	// Vectorstore is the vector store the platform uses (Qdrant). The
	// generator queries the same per-project collections that
	// schema_indexer and the sources retriever populate.
	Vectorstore vectorstore.Provider

	// SecretProvider supplies per-project credentials (LLM API key,
	// embedding API key, warehouse credentials).
	SecretProvider secrets.Provider

	// Sources is the project knowledge-sources retriever (URLs, files,
	// free text). Pack-gen uses the SAME store and retrieval path that
	// exploration and /ask use — see sources.Provider.RetrieveContext.
	Sources sources.Provider

	// EmbeddingFactory builds a per-project embedding client (matching
	// the project's embedding.provider / embedding.model). Used to
	// embed the project's business summary so the orchestrator can
	// query the per-project schema-blurb Qdrant collection for tables
	// semantically related to the business. Optional — when nil the
	// orchestrator falls back to a row-count + system-table-prefix
	// exclusion heuristic.
	EmbeddingFactory EmbeddingFactory
}

// ProviderFactory constructs a Provider from runtime dependencies.
// Plugins register a factory in init(); the API and Agent invoke
// Configure once their own initialization is complete.
type ProviderFactory func(deps Dependencies) (Provider, error)
