package discovery

import (
	"context"

	"github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// SchemaCache is the optional cache interface consumed by SchemaIndexer.
// The concrete implementation is database.SchemaCacheRepository; a nil
// cache disables the feature entirely (always-discover behaviour, which
// is what the existing tests exercise).
type SchemaCache interface {
	Find(ctx context.Context, projectID, warehouseID, warehouseHash string) (map[string]models.TableSchema, error)
	Save(ctx context.Context, projectID, warehouseID, warehouseHash string, schemas map[string]models.TableSchema) error
}

// CatalogCache is optionally implemented by a SchemaCache that can also
// persist the refs a catalog source offers.
//
// Separate from SchemaCache rather than added to it so every existing
// implementation — including the fakes in tests — keeps compiling and keeps
// working unchanged. A cache that does not implement this simply means a
// catalog source is not remembered between the index run and the processes
// that consume it.
type CatalogCache interface {
	FindCatalog(ctx context.Context, projectID, warehouseID, warehouseHash string) ([]string, error)
	SaveCatalog(ctx context.Context, projectID, warehouseID, warehouseHash string, refs []string) error
}

// CatalogRefsFor reads a datasource's catalog refs from the cache, returning
// nil when there are none to read.
//
// Shared by every caller that has to decide whether a datasource is usable,
// because they must all decide it the same way: a cache that does not support
// catalogs, a lookup that fails, and a source that genuinely has no catalog
// are one answer — "nothing is known" — and each caller then treats the
// datasource as having no catalog items rather than guessing it has some.
// Guessing the other way would let a failed lookup vouch for stale points.
//
// A lookup error is reported to the caller as well as returned empty, so it
// can say which datasource lost its catalog and why, rather than silently
// treating a transient failure as an empty source.
func CatalogRefsFor(ctx context.Context, cache SchemaCache, projectID, warehouseID, warehouseHash string) ([]string, error) {
	cc, ok := cache.(CatalogCache)
	if !ok {
		return nil, nil
	}
	refs, err := cc.FindCatalog(ctx, projectID, warehouseID, warehouseHash)
	if err != nil {
		return nil, err
	}
	return refs, nil
}

// SearchAuthority builds the set of refs a datasource may legitimately return
// from a search: its cached tables plus its cached catalog items.
//
// Both halves are needed. Omitting the tables would drop every warehouse hit;
// omitting the catalog items drops every hit from a source that has no tables,
// which is the whole of what such a source can offer.
func SearchAuthority(schemas map[string]models.TableSchema, catalogRefs []string) map[string]bool {
	set := make(map[string]bool, len(schemas)+len(catalogRefs))
	for table := range schemas {
		set[table] = true
	}
	for _, ref := range catalogRefs {
		set[ref] = true
	}
	return set
}

// WarehouseConfigHash is warehouse.ConfigHash for this package's config type.
//
// The hash itself moved to the warehouse package because the collection it
// keys is read outside this module, and the hash is the only thing telling a
// row that describes the datasource as it IS from one describing it as it USED
// TO BE. A second implementation of it elsewhere would be a second chance to
// disagree about which rows are current.
func WarehouseConfigHash(cfg models.WarehouseConfig) string {
	return warehouse.ConfigHash(
		cfg.Provider, cfg.ProjectID, cfg.Location,
		cfg.Datasets, cfg.FilterField, cfg.FilterValue, cfg.Config,
	)
}
