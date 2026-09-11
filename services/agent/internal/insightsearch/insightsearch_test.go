package insightsearch

import (
	"context"
	"errors"
	"fmt"
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
		{ID: "x9", Score: 0.50, Payload: map[string]interface{}{"type": "ledger_finding"}}, // unenrichable — must be dropped
	}}
	emb := &fakeEmbedder{model: "text-embedding-3-large", vec: []float64{0.1, 0.2}}
	enrich := func(_ context.Context, id, docType string) (ai.InsightHit, bool) {
		switch id {
		case "i1":
			return ai.InsightHit{Name: "Churn spike", Description: "d", Severity: "high", AnalysisArea: "retention", AffectedCount: 42, DiscoveryID: "disc-7"}, true
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
	// The unenrichable hit is dropped, not emitted as an empty-titled citation.
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2 (unenrichable dropped)", len(hits))
	}
	// Score + type carried from the vector result; name/severity from enrich.
	if hits[0].ID != "i1" || hits[0].Type != "insight" || hits[0].Name != "Churn spike" || hits[0].Severity != "high" || hits[0].AffectedCount != 42 || hits[0].Score != 0.91 || hits[0].DiscoveryID != "disc-7" {
		t.Fatalf("hit[0] not mapped/enriched: %+v", hits[0])
	}
	if hits[1].Type != "recommendation" || hits[1].Name != "Offer winback" {
		t.Fatalf("hit[1] recommendation mismatch: %+v", hits[1])
	}
	for _, h := range hits {
		if h.ID == "x9" || h.Name == "" {
			t.Fatalf("unenrichable/empty-name hit must be dropped, got %+v", h)
		}
	}
	// Project scope, type filter, and embedding model are passed to the store.
	if len(vs.gotOpts.ProjectIDs) != 1 || vs.gotOpts.ProjectIDs[0] != "p1" {
		t.Fatalf("ProjectIDs = %v, want [p1]", vs.gotOpts.ProjectIDs)
	}
	if len(vs.gotOpts.Types) != 2 || vs.gotOpts.Types[0] != "insight" || vs.gotOpts.Types[1] != "recommendation" {
		t.Fatalf("Types = %v, want [insight recommendation]", vs.gotOpts.Types)
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

	// The requested k is clamped (0/-5 → default, 999 → max), then the STORE is
	// asked for an overfetched slice (clampedK * overfetchFactor, capped at
	// maxLimit) so dropped stale hits don't starve the result.
	for _, tc := range []struct{ in, clampedK int }{{0, defaultLimit}, {-5, defaultLimit}, {3, 3}, {999, maxLimit}} {
		if _, err := s.SearchInsights(context.Background(), "q", tc.in); err != nil {
			t.Fatalf("k=%d err=%v", tc.in, err)
		}
		wantFetch := tc.clampedK * overfetchFactor
		if wantFetch > maxLimit {
			wantFetch = maxLimit
		}
		if vs.gotOpts.Limit != wantFetch {
			t.Errorf("k=%d → store Limit %d, want %d (overfetch)", tc.in, vs.gotOpts.Limit, wantFetch)
		}
	}
}

func TestSearchInsights_TrimsOverfetchToK(t *testing.T) {
	// The store returns more than k (overfetch); after enrichment the result is
	// trimmed back to k so callers still get at most what they asked for.
	results := make([]vectorstore.SearchResult, 0, 6)
	for i := 0; i < 6; i++ {
		results = append(results, vectorstore.SearchResult{ID: fmt.Sprintf("i%d", i), Score: 0.9, Payload: map[string]interface{}{"type": "insight"}})
	}
	vs := &fakeVS{results: results}
	emb := &fakeEmbedder{model: "m", vec: []float64{1}}
	allEnrich := func(_ context.Context, id, _ string) (ai.InsightHit, bool) { return ai.InsightHit{Name: id}, true }
	s := newTestSearcher(vs, emb, allEnrich)

	hits, err := s.SearchInsights(context.Background(), "q", 3)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("overfetched result must be trimmed to k=3, got %d", len(hits))
	}
}

func TestSearchInsights_EmbedError(t *testing.T) {
	s := newTestSearcher(&fakeVS{}, &fakeEmbedder{err: errors.New("boom")}, nil)
	if _, err := s.SearchInsights(context.Background(), "q", 5); err == nil {
		t.Fatal("expected embed error to propagate")
	}
}
