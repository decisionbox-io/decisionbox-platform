package discovery

import (
	"context"
	"fmt"
)

// dimensionProbeText is the throwaway input used to measure an embedding
// model's output size when the provider can't declare it up front.
const dimensionProbeText = "dimension probe"

// dimensionResolver is the slim embedding surface needed to size a vector
// collection: a declared dimension when the provider knows it, plus the
// ability to embed a probe when it doesn't.
type dimensionResolver interface {
	Embed(ctx context.Context, texts []string) ([][]float64, error)
	Dimensions() int
}

// resolveEmbeddingDimensions returns the vector size to provision a collection
// with. It trusts the provider's declared Dimensions() when it is known — a
// catalogued model or an explicit `dimensions` config override, the fast path
// that keeps known models exactly as they were — and otherwise probes: it
// embeds one short string and measures the returned vector. The probe makes
// any provider/model correct, including a DecisionBox AI gateway alias the
// catalog doesn't know (which would otherwise report Dimensions()==0 and fail
// collection creation with "dimensions must be positive, got 0"), without a
// hardcoded size.
func resolveEmbeddingDimensions(ctx context.Context, e dimensionResolver) (int, error) {
	if d := e.Dimensions(); d > 0 {
		return d, nil
	}
	vecs, err := e.Embed(ctx, []string{dimensionProbeText})
	if err != nil {
		return 0, fmt.Errorf("probe embedding to detect dimensions: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("probe embedding returned an empty vector")
	}
	return len(vecs[0]), nil
}
