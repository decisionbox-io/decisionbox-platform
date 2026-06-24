package insightsearch

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// fakeVS implements vectorstore.Provider; only Search is exercised.
type fakeVS struct {
	gotVector []float64
	gotOpts   vectorstore.SearchOpts
	results   []vectorstore.SearchResult
	err       error
}

func (f *fakeVS) Search(ctx context.Context, vector []float64, opts vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	f.gotVector = vector
	f.gotOpts = opts
	return f.results, f.err
}
func (f *fakeVS) Upsert(context.Context, []vectorstore.Point) error { return nil }
func (f *fakeVS) FindDuplicates(context.Context, []float64, string, string, string, float64) ([]vectorstore.SearchResult, error) {
	return nil, nil
}
func (f *fakeVS) Delete(context.Context, []string) error             { return nil }
func (f *fakeVS) HealthCheck(context.Context) error                  { return nil }
func (f *fakeVS) EnsureCollection(context.Context, int) error        { return nil }
func (f *fakeVS) SearchSchemaIndex(context.Context, string, []float64, int) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

// fakeEmbedder implements goembedding.Provider.
type fakeEmbedder struct {
	model string
	vec   []float64
	err   error
}

func (e *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	if e.err != nil {
		return nil, e.err
	}
	return [][]float64{e.vec}, nil
}
func (e *fakeEmbedder) Dimensions() int            { return len(e.vec) }
func (e *fakeEmbedder) ModelName() string          { return e.model }
func (e *fakeEmbedder) Validate(context.Context) error { return nil }

// newTestSearcher builds a Searcher with injected fakes + a stub enricher,
// bypassing Mongo.
func newTestSearcher(vs vectorstore.Provider, emb *fakeEmbedder, enrich enrichFunc) *Searcher {
	return &Searcher{projectID: "p1", vs: vs, embedder: emb, enrich: enrich}
}

func TestNew_NilDepsReturnsNil(t *testing.T) {
	emb := &fakeEmbedder{model: "m", vec: []float64{1}}
	vs := &fakeVS{}
	if New("p1", nil, vs, emb) != nil {
		t.Error("nil db should yield nil searcher")
	}
	if New("p1", nil, nil, emb) != nil {
		t.Error("nil vs should yield nil searcher")
	}
}

func TestSearchInsights_MapsAndEnriches(t *testing.T) {
	vs := &fakeVS{results: []vectorstore.SearchResult{
		{ID: "i1", Score: 0.91, Payload: map[string]interface{}{"type": "insight"}},
		{ID: "r1", Score: 0.80, Payload: map[string]interface{}{"type": "recommendation"}},
		{ID: "x9", Score: 0.50, Payload: map[string]interface{}{"type": "insight"}}, // not found in enrich
	}}
	emb := &fakeEmbedder{model: "text-embedding-3-large", vec: []float64{0.1, 0.2}}
	enrich := func(_ context.Context, id, docType string) (ai.InsightHit, bool) {
		switch id {
		case "i1":
			return ai.InsightHit{Name: "Churn spike", Description: "d", Severity: "high", AnalysisArea: "retention", AffectedCount: 42}, true
		case "r1":
			return ai.InsightHit{Name: "Offer winback", Description: "d2", Severity: "medium"}, true
		}
		return ai.InsightHit{}, false
	}
	s := newTestSearcher(vs, emb, enrich)

	hits, err := s.SearchInsights(context.Background(), "why are users leaving", 3)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want 3", len(hits))
	}
	// Score + type carried from the vector result; name/severity from enrich.
	if hits[0].ID != "i1" || hits[0].Type != "insight" || hits[0].Name != "Churn spike" || hits[0].Severity != "high" || hits[0].AffectedCount != 42 || hits[0].Score != 0.91 {
		t.Fatalf("hit[0] not mapped/enriched: %+v", hits[0])
	}
	if hits[1].Type != "recommendation" || hits[1].Name != "Offer winback" {
		t.Fatalf("hit[1] recommendation mismatch: %+v", hits[1])
	}
	// Unenriched hit still returned with id/type/score, empty display fields.
	if hits[2].ID != "x9" || hits[2].Name != "" || hits[2].Score != 0.50 {
		t.Fatalf("hit[2] should degrade gracefully: %+v", hits[2])
	}
	// Project scope + embedding model are passed to the vector store.
	if len(vs.gotOpts.ProjectIDs) != 1 || vs.gotOpts.ProjectIDs[0] != "p1" {
		t.Fatalf("ProjectIDs = %v, want [p1]", vs.gotOpts.ProjectIDs)
	}
	if vs.gotOpts.EmbeddingModel != "text-embedding-3-large" {
		t.Fatalf("EmbeddingModel = %q", vs.gotOpts.EmbeddingModel)
	}
}

func TestSearchInsights_LimitClamped(t *testing.T) {
	vs := &fakeVS{}
	emb := &fakeEmbedder{model: "m", vec: []float64{1}}
	noEnrich := func(context.Context, string, string) (ai.InsightHit, bool) { return ai.InsightHit{}, false }
	s := newTestSearcher(vs, emb, noEnrich)

	for _, tc := range []struct{ in, want int }{{0, defaultLimit}, {-5, defaultLimit}, {3, 3}, {999, maxLimit}} {
		if _, err := s.SearchInsights(context.Background(), "q", tc.in); err != nil {
			t.Fatalf("k=%d err=%v", tc.in, err)
		}
		if vs.gotOpts.Limit != tc.want {
			t.Errorf("k=%d → Limit %d, want %d", tc.in, vs.gotOpts.Limit, tc.want)
		}
	}
}

func TestSearchInsights_EmbedError(t *testing.T) {
	s := newTestSearcher(&fakeVS{}, &fakeEmbedder{err: errors.New("boom")}, nil)
	if _, err := s.SearchInsights(context.Background(), "q", 5); err == nil {
		t.Fatal("expected embed error to propagate")
	}
}
