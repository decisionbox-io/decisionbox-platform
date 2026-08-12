package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

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

// WarehouseConfigHash produces a stable SHA-256 over everything the
// warehouse needs to list tables and describe them — provider,
// project_id / catalog / location, dataset list, filter column+value,
// and the provider-specific config map (region, workgroup, auth_method,
// etc.). Credentials are NOT in `Config` (they live in the secret
// provider), so they don't and shouldn't affect the hash.
//
// Any edit that could change what DiscoverSchemas returns changes the
// hash, so the cache's query-by-hash lookup self-invalidates.
func WarehouseConfigHash(cfg models.WarehouseConfig) string {
	// Canonicalise before hashing so map iteration order doesn't
	// introduce spurious cache misses.
	datasets := append([]string(nil), cfg.Datasets...)
	sort.Strings(datasets)

	configKeys := make([]string, 0, len(cfg.Config))
	for k := range cfg.Config {
		configKeys = append(configKeys, k)
	}
	sort.Strings(configKeys)

	var b strings.Builder
	b.WriteString("v1|")
	b.WriteString(cfg.Provider)
	b.WriteString("|pid=")
	b.WriteString(cfg.ProjectID)
	b.WriteString("|loc=")
	b.WriteString(cfg.Location)
	b.WriteString("|ds=")
	b.WriteString(strings.Join(datasets, ","))
	b.WriteString("|filter=")
	b.WriteString(cfg.FilterField)
	b.WriteByte('=')
	b.WriteString(cfg.FilterValue)
	b.WriteString("|cfg=")
	for _, k := range configKeys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(cfg.Config[k])
		b.WriteByte(';')
	}

	// Cross-project reads (a warehouse whose data lives in a different
	// project than the one running queries — BigQuery's data_project_id
	// differing from project_id) render table refs as three-part
	// `dataproject.dataset.table` in the catalog and as the schema-cache
	// key, instead of the two-part `dataset.table` form. That shape is a
	// code-level property the inputs above don't otherwise capture, so
	// stamp it explicitly: this invalidates exactly the cross-project
	// caches that need rediscovery after the shape changed, while
	// single-project caches stay valid (no needless re-index). Bump the
	// marker version if the cross-project ref shape ever changes again.
	if dp := cfg.Config["data_project_id"]; dp != "" && dp != cfg.ProjectID {
		b.WriteString("|xproj-refshape=v2")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
