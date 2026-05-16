package embedding

import "context"

// ProjectConfig holds per-project embedding configuration.
// Stored in the project document in MongoDB.
// Shared between API and Agent services.
//
// Credentials are NOT persisted in this struct — they live in the
// secret provider under the "embedding-credentials" key. The agent
// resolves them via the shared resolveCredential helper (dashboard
// secret wins, env var EMBEDDING_API_KEY as fallback) and injects them
// into the per-provider ProviderConfig as "credentials_json".
//
// Provider-specific non-credential fields (auth_method, region,
// project_id, location, role_arn, …) flow through a parallel
// map[string]string config stored alongside this struct in the project
// document.
type ProjectConfig struct {
	Provider string `bson:"provider,omitempty" json:"provider,omitempty"`
	Model    string `bson:"model,omitempty" json:"model,omitempty"`
}

// RemoteModel is one row returned by a provider's live ListModels
// endpoint. Kept separate from the catalog-backed ModelInfo so the
// dashboard can distinguish models the shipped build knows about from
// ones it learned at runtime (e.g. a user's custom Ollama tag).
type RemoteModel struct {
	ID          string
	DisplayName string
	// Dimensions is 0 when the provider's list endpoint doesn't carry
	// that field (OpenAI's /v1/models for example) — the dashboard
	// falls back to the catalog Dimensions for known model IDs.
	Dimensions int
	// Lifecycle is the free-form status string the provider returns
	// ("active", "deprecated", ...). Empty when the provider doesn't
	// expose one.
	Lifecycle string
}

// ModelLister is an optional capability interface: embedding providers
// that can enumerate the user's available models implement it. Matches
// the llm.ModelLister pattern so the UI phase-of-credentials → load-
// models works the same way for both.
//
// Implementations must be read-only and must not consume paid quota —
// use the provider's list endpoint (/v1/models for OpenAI-compat,
// ListFoundationModels for Bedrock, etc.). A failing list call must
// never block project creation: the handler falls back to the shipped
// catalog.
type ModelLister interface {
	ListModels(ctx context.Context) ([]RemoteModel, error)
}

// Provider abstracts text embedding operations.
// Implement this interface to add support for a new embedding provider
// (e.g., OpenAI, Ollama, Vertex AI, Bedrock).
//
// Selection via project-level configuration (embedding.provider field).
type Provider interface {
	// Embed generates vector embeddings for the given texts.
	// Returns one vector per input text, each with Dimensions() elements.
	Embed(ctx context.Context, texts []string) ([][]float64, error)

	// Dimensions returns the vector dimensionality for this model.
	Dimensions() int

	// ModelName returns the model identifier (e.g., "text-embedding-3-small").
	// Stored alongside vectors for migration tracking.
	ModelName() string

	// Validate checks that the provider credentials and configuration are valid.
	// Uses a lightweight API call (e.g., embed a single word) to verify access.
	Validate(ctx context.Context) error
}
