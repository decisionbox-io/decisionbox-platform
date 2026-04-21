package embedding

import "context"

// ProjectConfig holds per-project embedding configuration.
// Stored in the project document in MongoDB.
// Shared between API and Agent services.
type ProjectConfig struct {
	Provider string `bson:"provider,omitempty" json:"provider,omitempty"`
	Model    string `bson:"model,omitempty" json:"model,omitempty"`

	// Credentials is the BYOK API key the project owner supplied via
	// the UI. Persisted so the shape is BYOK-ready end-to-end, but
	// ignored by the factory at runtime when an EMBEDDING_PROVIDER_API_KEY
	// env override is present (DecisionBox Cloud injects the override
	// today — paid plans will opt into BYOK by flipping
	// byok_embedding_enabled, at which point the override is withheld
	// and this field wins).
	Credentials string `bson:"credentials,omitempty" json:"credentials,omitempty"`
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
