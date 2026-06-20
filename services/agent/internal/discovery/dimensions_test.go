package discovery

import (
	"context"
	"errors"
	"testing"
)

// dimProbeEmbedder is a minimal embedder for exercising the dimension
// resolver: it reports a declared size and records how many times it was
// probed via Embed.
type dimProbeEmbedder struct {
	dims       int
	vec        []float64
	embedErr   error
	embedCalls int
}

func (f *dimProbeEmbedder) Dimensions() int { return f.dims }

func (f *dimProbeEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	f.embedCalls++
	if f.embedErr != nil {
		return nil, f.embedErr
	}
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

func TestResolveEmbeddingDimensions(t *testing.T) {
	// A known model / explicit override declares its size — trust it, no probe.
	t.Run("declared dims skip the probe", func(t *testing.T) {
		e := &dimProbeEmbedder{dims: 3072, vec: make([]float64, 999)}
		got, err := resolveEmbeddingDimensions(context.Background(), e)
		if err != nil {
			t.Fatal(err)
		}
		if got != 3072 {
			t.Errorf("dims = %d, want 3072 (declared)", got)
		}
		if e.embedCalls != 0 {
			t.Errorf("expected no probe for a declared size, got %d embed calls", e.embedCalls)
		}
	})

	// An unknown model / gateway alias reports 0 — probe and measure the
	// actual vector (this is the case that used to fail with "dimensions must
	// be positive, got 0").
	t.Run("unknown dims probe and measure", func(t *testing.T) {
		e := &dimProbeEmbedder{dims: 0, vec: make([]float64, 3072)}
		got, err := resolveEmbeddingDimensions(context.Background(), e)
		if err != nil {
			t.Fatal(err)
		}
		if got != 3072 {
			t.Errorf("dims = %d, want 3072 (probed)", got)
		}
		if e.embedCalls != 1 {
			t.Errorf("expected exactly one probe, got %d", e.embedCalls)
		}
	})

	t.Run("probe error surfaces", func(t *testing.T) {
		e := &dimProbeEmbedder{dims: 0, embedErr: errors.New("upstream down")}
		if _, err := resolveEmbeddingDimensions(context.Background(), e); err == nil {
			t.Fatal("expected an error when the probe embedding fails")
		}
	})

	t.Run("empty probe vector is an error not zero", func(t *testing.T) {
		e := &dimProbeEmbedder{dims: 0, vec: nil}
		if _, err := resolveEmbeddingDimensions(context.Background(), e); err == nil {
			t.Fatal("expected an error for an empty probe vector")
		}
	})
}
