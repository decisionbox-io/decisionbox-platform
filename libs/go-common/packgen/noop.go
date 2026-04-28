package packgen

import "context"

// noopProvider is the default Provider used until a plugin registers a
// factory and Configure activates it. Every method returns
// ErrNotConfigured so handlers can surface a clear "feature
// unavailable" response rather than silently no-op'ing (which would
// leave the project stuck in pack_generation state).
type noopProvider struct{}

func (noopProvider) Generate(_ context.Context, _ GenerateRequest) (*GenerateResult, error) {
	return nil, ErrNotConfigured
}

func (noopProvider) RegenerateSection(_ context.Context, _ RegenerateSectionRequest) (*RegenerateSectionResult, error) {
	return nil, ErrNotConfigured
}
