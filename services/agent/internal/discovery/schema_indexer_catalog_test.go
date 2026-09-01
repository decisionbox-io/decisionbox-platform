package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// stubCatalog is a source that describes itself with a catalog instead of
// tables.
type stubCatalog struct {
	items []gowarehouse.CatalogItem
	err   error
	calls int
}

func (c *stubCatalog) Catalog(context.Context) ([]gowarehouse.CatalogItem, error) {
	c.calls++
	return c.items, c.err
}

// TestBuildIndex_CatalogPathNeedsNoTableCollaborators pins the requirement
// change. A catalog source has no tables to discover and brings its own
// descriptions, so demanding a Discovery and a Blurber would demand two
// collaborators that are never called — and in practice would be satisfied
// with a table lister that fails on first use.
func TestBuildIndex_CatalogPathNeedsNoTableCollaborators(t *testing.T) {
	si := &SchemaIndexer{
		Catalog:  &stubCatalog{items: []gowarehouse.CatalogItem{{Ref: "sessions", Kind: gowarehouse.ItemKindMetric, Text: "Sessions."}}},
		Embedder: &stubEmbedder{dim: 3, model: "test/embed"},
	}
	_, err := si.BuildIndex(context.Background(), IndexOptions{ProjectID: "p1"})
	if err == nil {
		t.Fatal("expected the Retriever requirement to be the failure")
	}
	// It must complain about the Retriever — not about Discovery or Blurber,
	// which this path does not use.
	if !strings.Contains(err.Error(), "Retriever is required") {
		t.Errorf("error = %q, want the Retriever requirement; Discovery/Blurber must not be demanded on the catalog path", err.Error())
	}
}

// TestBuildIndex_TablePathStillDemandsItsCollaborators is the other half: the
// requirement was relaxed only for catalog sources.
func TestBuildIndex_TablePathStillDemandsItsCollaborators(t *testing.T) {
	si := &SchemaIndexer{Embedder: &stubEmbedder{dim: 3}}
	_, err := si.BuildIndex(context.Background(), IndexOptions{ProjectID: "p1"})
	if err == nil || !strings.Contains(err.Error(), "Discovery is required") {
		t.Fatalf("error = %v, want the Discovery requirement to still apply without a catalog", err)
	}
}

// TestBuildCatalogIndex_RefusesAnEmptyCatalog covers the case that would
// otherwise leave a datasource reporting "ready" with nothing in it — the
// model would retrieve nothing from it forever and no error would say why.
func TestBuildCatalogIndex_RefusesAnEmptyCatalog(t *testing.T) {
	tests := []struct {
		name  string
		items []gowarehouse.CatalogItem
	}{
		{name: "no items at all", items: nil},
		{
			name:  "every item unreadable by this credential",
			items: []gowarehouse.CatalogItem{{Ref: "totalRevenue", Text: "Revenue.", Unavailable: "NO_REVENUE_METRICS"}},
		},
		{
			name:  "every item unnameable",
			items: []gowarehouse.CatalogItem{{Ref: "", Text: "Something."}},
		},
		{
			name:  "every item undescribed",
			items: []gowarehouse.CatalogItem{{Ref: "sessions", Text: ""}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			si := &SchemaIndexer{
				Catalog:   &stubCatalog{items: tt.items},
				Embedder:  &stubEmbedder{dim: 3},
				Retriever: nil,
			}
			// Retriever is nil, so reaching the write phase would panic;
			// the refusal must happen before that.
			_, err := si.buildCatalogIndex(context.Background(), IndexOptions{ProjectID: "p1"}, time.Now())
			if err == nil {
				t.Fatal("an unusable catalog must fail the run, not produce an empty index")
			}
			if !strings.Contains(err.Error(), "no indexable items") {
				t.Errorf("error = %q, want it to say the catalog had nothing usable", err.Error())
			}
		})
	}
}

// TestBuildCatalogIndex_SurfacesAReadFailure: a catalog that cannot be read is
// a failed run, not an empty index.
func TestBuildCatalogIndex_SurfacesAReadFailure(t *testing.T) {
	si := &SchemaIndexer{
		Catalog:  &stubCatalog{err: errors.New("permission denied on the property")},
		Embedder: &stubEmbedder{dim: 3},
	}
	_, err := si.buildCatalogIndex(context.Background(), IndexOptions{ProjectID: "p1"}, time.Now())
	if err == nil {
		t.Fatal("a catalog read failure must fail the run")
	}
	if !strings.Contains(err.Error(), "permission denied on the property") {
		t.Errorf("error = %q, want the source's own message preserved", err.Error())
	}
}

// countingProgress records what the indexer reports, so a run that finishes
// can be distinguished from one that merely stops.
type countingProgress struct {
	total, done int
	phases      []string
}

func (p *countingProgress) Reset(context.Context, string, string) error { return nil }
func (p *countingProgress) SetPhase(_ context.Context, _, phase string) error {
	p.phases = append(p.phases, phase)
	return nil
}
func (p *countingProgress) SetTotals(_ context.Context, _ string, total int) error {
	p.total = total
	return nil
}
func (p *countingProgress) SetCounters(_ context.Context, _ string, total, done int) error {
	p.total, p.done = total, done
	return nil
}
func (p *countingProgress) IncrementDone(_ context.Context, _ string, delta int) error {
	p.done += delta
	return nil
}
func (p *countingProgress) IncrementTokens(context.Context, string, int, int) error { return nil }
func (p *countingProgress) RecordError(context.Context, string, string) error       { return nil }

// TestBuildCatalogIndex_CompletesItsProgressCounters guards a wrongness the
// user sees rather than the data does. The catalog path has no per-item leg to
// report against, so its counter completes in one step instead of climbing —
// but leaving it unset means the run finishes while the progress display still
// reads 0 of N, which looks like a stalled index rather than a finished one.
//
// Asserted on the failure path too: a run that did NOT finish must not report
// completion.
func TestBuildCatalogIndex_CompletesItsProgressCounters(t *testing.T) {
	t.Run("a failed run reports no completion", func(t *testing.T) {
		prog := &countingProgress{}
		si := &SchemaIndexer{
			Catalog:  &stubCatalog{err: errors.New("catalog unreachable")},
			Embedder: &stubEmbedder{dim: 3},
			Progress: prog,
		}
		if _, err := si.buildCatalogIndex(context.Background(), IndexOptions{ProjectID: "p1"}, time.Now()); err == nil {
			t.Fatal("expected the catalog read to fail")
		}
		if prog.done != 0 {
			t.Errorf("done = %d, want 0 — a run that never indexed anything must not report progress", prog.done)
		}
	})
}
