package ollama

import (
	"context"

	ollamaapi "github.com/ollama/ollama/api"
)

// ollamaClient abstracts the Ollama API for testing.
// The real implementation is *ollamaapi.Client.
type ollamaClient interface {
	Chat(ctx context.Context, req *ollamaapi.ChatRequest, fn ollamaapi.ChatResponseFunc) error
	List(ctx context.Context) (*ollamaapi.ListResponse, error)
}

// Compile-time check that the real client satisfies the interface.
var _ ollamaClient = (*ollamaapi.Client)(nil)
