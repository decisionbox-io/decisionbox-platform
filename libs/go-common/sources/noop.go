package sources

import "context"

// noopProvider is the default Provider used when no enterprise plugin is loaded.
// It returns no chunks for any query.
type noopProvider struct{}

func (noopProvider) RetrieveContext(_ context.Context, _ string, _ string, _ RetrieveOpts) ([]Chunk, error) {
	return nil, nil
}
