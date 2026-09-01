package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestCatalogRefsFor covers the decision every caller shares: is anything
// known about this datasource's catalog?
//
// The three "no" cases must be indistinguishable to the caller — a cache that
// cannot answer, one that fails answering, and a source that genuinely has no
// catalog all mean "treat it as having none". Any of them resolving to
// "assume it has some" would let a failed lookup vouch for stale vector
// points that nothing has authorised.
func TestCatalogRefsFor(t *testing.T) {
	ctx := context.Background()

	t.Run("a cache that does not support catalogs knows nothing", func(t *testing.T) {
		refs, err := CatalogRefsFor(ctx, &stubCache{}, "p", "ds", "h")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if refs != nil {
			t.Errorf("refs = %v, want nil", refs)
		}
	})

	t.Run("a failed lookup returns nothing AND reports why", func(t *testing.T) {
		boom := errors.New("mongo unreachable")
		refs, err := CatalogRefsFor(ctx, &catalogStubCache{catalogErr: boom}, "p", "ds", "h")
		if len(refs) != 0 {
			t.Errorf("refs = %v, want none on a failed lookup", refs)
		}
		// The error must surface: a caller that cannot tell a transient
		// failure from an empty catalog would log nothing and silently drop a
		// datasource's entire searchable surface.
		if !errors.Is(err, boom) {
			t.Errorf("err = %v, want the underlying failure so the caller can say which datasource lost its catalog", err)
		}
	})

	t.Run("a source with a catalog returns it", func(t *testing.T) {
		refs, err := CatalogRefsFor(ctx, &catalogStubCache{refs: []string{"sessions"}}, "p", "ds", "h")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(refs) != 1 || refs[0] != "sessions" {
			t.Errorf("refs = %v, want the catalog", refs)
		}
	})

	t.Run("a source with no catalog returns none", func(t *testing.T) {
		refs, err := CatalogRefsFor(ctx, &catalogStubCache{}, "p", "ds", "h")
		if err != nil || len(refs) != 0 {
			t.Errorf("refs = %v, err = %v; want neither", refs, err)
		}
	})
}

// TestSearchAuthority pins that both halves are present. Omitting the tables
// drops every warehouse hit; omitting the catalog items drops every hit from a
// source that has no tables — which is the whole of what such a source offers.
func TestSearchAuthority(t *testing.T) {
	schemas := map[string]models.TableSchema{
		"sales.orders": {TableName: "sales.orders"},
	}
	got := SearchAuthority(schemas, []string{"sessions", "country"})

	for _, want := range []string{"sales.orders", "sessions", "country"} {
		if !got[want] {
			t.Errorf("%q is not in the search authority; its hits would be dropped", want)
		}
	}
	if got["never.indexed"] {
		t.Error("an unknown ref must not be authorised")
	}
	if len(got) != 3 {
		t.Errorf("authority has %d entries, want 3", len(got))
	}
}

// TestSearchAuthority_EitherHalfMayBeEmpty: a table-only source and a
// catalog-only source are both legitimate, and each must authorise what it
// actually has.
func TestSearchAuthority_EitherHalfMayBeEmpty(t *testing.T) {
	tablesOnly := SearchAuthority(map[string]models.TableSchema{"a.b": {}}, nil)
	if !tablesOnly["a.b"] || len(tablesOnly) != 1 {
		t.Errorf("tables-only authority = %v", tablesOnly)
	}

	catalogOnly := SearchAuthority(nil, []string{"sessions"})
	if !catalogOnly["sessions"] || len(catalogOnly) != 1 {
		t.Errorf("catalog-only authority = %v", catalogOnly)
	}

	neither := SearchAuthority(nil, nil)
	if len(neither) != 0 {
		t.Errorf("empty authority = %v, want nothing authorised", neither)
	}
}
