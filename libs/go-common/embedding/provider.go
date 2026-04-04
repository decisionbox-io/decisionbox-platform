package embedding

import "context"

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
