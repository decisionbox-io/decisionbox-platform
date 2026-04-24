package discovery

import (
	"context"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
)

// schema_indexer unit tests cover the branches that don't need a live
// Qdrant client. The happy-path end-to-end (warehouse → blurb → embed →
// Qdrant upsert) is covered by the integration_test.go file in this
// package — that test spins up a real Qdrant via testcontainers and
// uses live Bedrock / OpenAI blurb+embed providers.

// --- stub LLM (used by the blurb generator in these tests) ---

type stubLLM struct{ text string }

func (s *stubLLM) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	return &gollm.ChatResponse{Content: s.text, Usage: gollm.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}
func (s *stubLLM) Validate(ctx context.Context) error { return nil }

// --- minimal stub embedder, progress reporter ---

type stubEmbedder struct {
	dim   int
	model string
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range out {
		v := make([]float64, s.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}
func (s *stubEmbedder) Dimensions() int   { return s.dim }
func (s *stubEmbedder) ModelName() string { return s.model }

// --- validation branches ---

func TestBuildIndex_MissingProjectID(t *testing.T) {
	si := SchemaIndexer{}
	_, err := si.BuildIndex(context.Background(), IndexOptions{})
	if err == nil {
		t.Fatal("expected error for empty ProjectID")
	}
}

func TestBuildIndex_MissingDiscovery(t *testing.T) {
	si := SchemaIndexer{
		Blurber:   mustBlurber(t),
		Embedder:  &stubEmbedder{dim: 3, model: "fake"},
		Retriever: nil, // also missing, but Discovery is checked first
	}
	_, err := si.BuildIndex(context.Background(), IndexOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected error for missing Discovery")
	}
}

func TestBuildIndex_MissingBlurber(t *testing.T) {
	si := SchemaIndexer{
		Discovery: &SchemaDiscovery{},
		Embedder:  &stubEmbedder{dim: 3, model: "fake"},
	}
	_, err := si.BuildIndex(context.Background(), IndexOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected error for missing Blurber")
	}
}

func TestBuildIndex_MissingEmbedder(t *testing.T) {
	si := SchemaIndexer{
		Discovery: &SchemaDiscovery{},
		Blurber:   mustBlurber(t),
	}
	_, err := si.BuildIndex(context.Background(), IndexOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected error for missing Embedder")
	}
}

func TestBuildIndex_MissingRetriever(t *testing.T) {
	si := SchemaIndexer{
		Discovery: &SchemaDiscovery{},
		Blurber:   mustBlurber(t),
		Embedder:  &stubEmbedder{dim: 3, model: "fake"},
	}
	_, err := si.BuildIndex(context.Background(), IndexOptions{ProjectID: "p"})
	if err == nil {
		t.Fatal("expected error for missing Retriever")
	}
}

// --- pure helper: indexDot ---

func TestIndexDot(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", -1},
		{"abc", -1},
		{"a.b", 1},
		{".x", 0},
		{"dataset.t", 7},
		{"a.b.c", 1}, // first dot only
	}
	for _, c := range cases {
		if got := indexDot(c.in); got != c.want {
			t.Errorf("indexDot(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// --- helpers ---

func mustBlurber(t *testing.T) *blurb.Generator {
	t.Helper()
	g, err := blurb.New(blurb.Config{LLM: &stubLLM{text: "blurb"}, Model: "gpt-4o", Workers: 1})
	if err != nil {
		t.Fatalf("blurb.New: %v", err)
	}
	return g
}
