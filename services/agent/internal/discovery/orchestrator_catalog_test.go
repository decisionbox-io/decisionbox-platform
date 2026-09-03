package discovery

import (
	"context"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// catalogStubCache is a schema cache that also answers catalog lookups.
type catalogStubCache struct {
	stubCache
	refs        []string
	catalogErr  error
	catalogGets int
}

func (c *catalogStubCache) FindCatalog(context.Context, string, string, string) ([]string, error) {
	c.catalogGets++
	if c.catalogErr != nil {
		return nil, c.catalogErr
	}
	return c.refs, nil
}

func (c *catalogStubCache) SaveCatalog(context.Context, string, string, string, []gowarehouse.CatalogItem) error {
	return nil
}

// TestDiscoverSchemas_CatalogSourceIsNotAReindexError is the fix for a
// misleading error. A catalog source has no tables, so an empty table cache is
// its normal state — not evidence that indexing never happened. Reporting
// "re-index required" would send the operator to re-run an index that already
// succeeded, and would do it every single time.
func TestDiscoverSchemas_CatalogSourceIsNotAReindexError(t *testing.T) {
	cache := &catalogStubCache{refs: []string{"sessions", "country"}}
	o := &Orchestrator{projectID: "proj-1", schemaCache: cache, warehouseHash: "hash-abc"}

	got, err := o.discoverSchemas(context.Background())
	if err != nil {
		t.Fatalf("discoverSchemas: %v — a source with an indexed catalog and no tables must not be a re-index error", err)
	}
	if got == nil {
		t.Error("want an empty (non-nil) map: the datasource is indexed and has no tables")
	}
	if len(got) != 0 {
		t.Errorf("schemas = %+v, want none", got)
	}
	if cache.catalogGets == 0 {
		t.Error("the catalog was never consulted, so the empty table map was not actually explained")
	}
	// The refs must be RETAINED, not merely counted. The schema provider built
	// later filters catalog hits against exactly this list, so discarding them
	// here lets discovery start and then return nothing from every search —
	// the source is reachable and permanently empty, with no error anywhere.
	if len(o.catalogRefs) != 2 {
		t.Errorf("orchestrator kept %d catalog refs, want 2 — the schema provider filters hits against these", len(o.catalogRefs))
	}
}

// TestDiscoverSchemas_NoCatalogRefsRetainedForATableSource pins that a
// table-shaped source picks none up, so its provider keeps rejecting catalog
// hits it should never see.
func TestDiscoverSchemas_NoCatalogRefsRetainedForATableSource(t *testing.T) {
	cache := &catalogStubCache{}
	cache.hit = fakeSchemas()
	o := &Orchestrator{projectID: "proj-1", schemaCache: cache, warehouseHash: "hash-abc"}

	if _, err := o.discoverSchemas(context.Background()); err != nil {
		t.Fatalf("discoverSchemas: %v", err)
	}
	if len(o.catalogRefs) != 0 {
		t.Errorf("catalogRefs = %v, want none for a source that has tables", o.catalogRefs)
	}
}

// TestDiscoverSchemas_StillErrorsWithNoIndexAtAll is the guard on the guard.
// The re-index error exists because there is no live-warehouse fallback during
// a run: an unindexed project must fail loudly rather than silently exploring
// nothing. Relaxing it for catalog sources must not relax it for everyone.
func TestDiscoverSchemas_StillErrorsWithNoIndexAtAll(t *testing.T) {
	cache := &catalogStubCache{} // no tables, no catalog
	o := &Orchestrator{projectID: "proj-1", schemaCache: cache, warehouseHash: "hash-abc"}

	_, err := o.discoverSchemas(context.Background())
	if err == nil {
		t.Fatal("a datasource with neither tables nor a catalog must still be a hard re-index error")
	}
	if !strings.Contains(err.Error(), "re-index required") {
		t.Errorf("error = %q, want the re-index instruction preserved", err.Error())
	}
}

// TestDiscoverSchemas_CatalogLookupFailureKeepsTheReindexError: if the catalog
// cannot be consulted we do not know whether the datasource is a catalog
// source, and guessing "yes" would silently swallow a genuinely missing index.
func TestDiscoverSchemas_CatalogLookupFailureKeepsTheReindexError(t *testing.T) {
	cache := &catalogStubCache{catalogErr: context.DeadlineExceeded}
	o := &Orchestrator{projectID: "proj-1", schemaCache: cache, warehouseHash: "hash-abc"}

	_, err := o.discoverSchemas(context.Background())
	if err == nil || !strings.Contains(err.Error(), "re-index required") {
		t.Fatalf("error = %v, want the re-index error when the catalog cannot be checked", err)
	}
}

// TestDiscoverSchemas_CacheWithoutCatalogSupportIsUnchanged pins that a cache
// predating catalogs behaves exactly as before.
func TestDiscoverSchemas_CacheWithoutCatalogSupportIsUnchanged(t *testing.T) {
	o := &Orchestrator{projectID: "proj-1", schemaCache: &stubCache{}, warehouseHash: "hash-abc"}

	_, err := o.discoverSchemas(context.Background())
	if err == nil || !strings.Contains(err.Error(), "re-index required") {
		t.Fatalf("error = %v, want the original re-index error for a cache that knows nothing of catalogs", err)
	}
}

// TestCatalogRefsByDatasource covers how the search authority is assembled for
// the schema provider, which filters every catalog hit against it. An empty or
// mis-keyed result here is not an error anywhere — it is a source that returns
// nothing from every search, forever.
func TestCatalogRefsByDatasource(t *testing.T) {
	t.Run("single-datasource run keys the run's own refs", func(t *testing.T) {
		o := &Orchestrator{warehouseID: "ga", catalogRefs: []string{"sessions"}}
		got := o.catalogRefsByDatasource(nil)
		if len(got) != 1 || len(got["ga"]) != 1 || got["ga"][0] != "sessions" {
			t.Errorf("got %v, want the refs keyed under this run's datasource", got)
		}
	})

	t.Run("an unnamed datasource normalises rather than keying on empty", func(t *testing.T) {
		o := &Orchestrator{warehouseID: "", catalogRefs: []string{"sessions"}}
		got := o.catalogRefsByDatasource(nil)
		// Points carry the normalised id, so keying on "" would authorise
		// nothing and silently drop every hit.
		if len(got[normDatasourceID("")]) != 1 {
			t.Errorf("got %v, want the refs under the normalised default id", got)
		}
	})

	t.Run("a multi-datasource run uses every datasource's refs", func(t *testing.T) {
		o := &Orchestrator{warehouseID: "wh", catalogRefs: []string{"ignored"}}
		dc := &datasourceContext{catalogRefs: map[string][]string{
			"ga":  {"sessions"},
			"crm": {"opportunities"},
		}}
		got := o.catalogRefsByDatasource(dc)
		if len(got) != 2 || len(got["ga"]) != 1 || len(got["crm"]) != 1 {
			t.Errorf("got %v, want every datasource's own refs", got)
		}
	})

	t.Run("no catalog anywhere authorises nothing", func(t *testing.T) {
		o := &Orchestrator{warehouseID: "wh"}
		if got := o.catalogRefsByDatasource(&datasourceContext{}); got != nil {
			t.Errorf("got %v, want nil so no catalog hit is trusted", got)
		}
	})
}

// TestDatasourceIsIndexed separates the two states that both present as "no
// tables". Getting it wrong in the permissive direction wires an unindexed
// datasource into the run, where it is described to the model and then answers
// nothing — far harder to diagnose than being told to re-index. Getting it
// wrong the other way rejects a legitimate catalog source outright.
func TestDatasourceIsIndexed(t *testing.T) {
	tables := map[string]models.TableSchema{"sales.orders": {}}

	tests := []struct {
		name    string
		schemas map[string]models.TableSchema
		refs    []string
		want    bool
	}{
		{name: "tables only", schemas: tables, want: true},
		{name: "catalog only, which is a whole legitimate source", refs: []string{"sessions"}, want: true},
		{name: "both", schemas: tables, refs: []string{"sessions"}, want: true},
		{name: "neither means never indexed", want: false},
		{name: "explicitly empty is still neither", schemas: map[string]models.TableSchema{}, refs: []string{}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := datasourceIsIndexed(tt.schemas, tt.refs); got != tt.want {
				t.Errorf("datasourceIsIndexed() = %v, want %v", got, tt.want)
			}
		})
	}
}
