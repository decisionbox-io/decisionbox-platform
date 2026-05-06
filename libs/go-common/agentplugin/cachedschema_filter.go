package agentplugin

import (
	"context"
	"fmt"
	"sync"
)

// CachedSchemaFilterFunc is invoked after the agent has loaded the
// project's schema map from project_schema_cache and before it builds
// the LLM-facing catalog. Implementations may shrink the input slice
// (allow / deny lists, regex denylists, etc.) and may return a wrapped
// error to surface a misconfiguration to the operator.
//
// Unlike ListTablesFilterFunc, the input here is the set of qualified
// table names (dataset.table) that the agent currently has cached for
// the project — because the discovery run reads from the cache, not
// from a live ListTablesInDataset call. Filters built for run-time use
// register here; filters that constrain freshly-listed warehouse
// tables register with RegisterListTablesFilter.
//
// Filters MUST be pure with respect to the input: the agent owns the
// returned slice and may mutate it freely. Filters MUST NOT add tables
// that were not in the input — the cache is the upper bound at run
// time, just as the warehouse listing is the upper bound at index
// time.
//
// Filters are called in registration order. A non-nil error from any
// filter aborts the discovery run; the orchestrator surfaces the
// error rather than silently proceeding with an unconstrained table
// set, because the user-visible failure mode (the wrong tables hit
// the LLM) would be far harder to diagnose than an explicit error.
type CachedSchemaFilterFunc func(ctx context.Context, projectID string, qualifiedTables []string) ([]string, error)

var (
	cachedSchemaFiltersMu sync.RWMutex
	cachedSchemaFilters   []namedCachedSchemaFilter
)

type namedCachedSchemaFilter struct {
	name string
	fn   CachedSchemaFilterFunc
}

// RegisterCachedSchemaFilter registers fn under name. Plugins call
// this from init() with a blank import; production callers register
// at startup. Empty name or nil fn panics. Re-registering the same
// name panics. Order is preserved so chained filters compose
// deterministically.
func RegisterCachedSchemaFilter(name string, fn CachedSchemaFilterFunc) {
	if name == "" {
		panic("agentplugin: RegisterCachedSchemaFilter called with empty name")
	}
	if fn == nil {
		panic(fmt.Sprintf("agentplugin: RegisterCachedSchemaFilter %q called with nil fn", name))
	}
	cachedSchemaFiltersMu.Lock()
	defer cachedSchemaFiltersMu.Unlock()
	for _, existing := range cachedSchemaFilters {
		if existing.name == name {
			panic(fmt.Sprintf("agentplugin: CachedSchemaFilter %q already registered", name))
		}
	}
	cachedSchemaFilters = append(cachedSchemaFilters, namedCachedSchemaFilter{name: name, fn: fn})
}

// ApplyCachedSchemaFilters runs every registered filter in
// registration order and returns the resulting slice. With no filters
// registered the input slice is returned unchanged (and unmodified)
// so the agent can call this unconditionally.
//
// On the first non-nil error from a filter, returns (nil, error).
func ApplyCachedSchemaFilters(ctx context.Context, projectID string, qualifiedTables []string) ([]string, error) {
	cachedSchemaFiltersMu.RLock()
	filters := make([]namedCachedSchemaFilter, len(cachedSchemaFilters))
	copy(filters, cachedSchemaFilters)
	cachedSchemaFiltersMu.RUnlock()

	if len(filters) == 0 {
		return qualifiedTables, nil
	}
	out := qualifiedTables
	for _, f := range filters {
		next, err := f.fn(ctx, projectID, out)
		if err != nil {
			return nil, fmt.Errorf("cached-schema filter %q: %w", f.name, err)
		}
		out = next
	}
	return out, nil
}

// ResetCachedSchemaFiltersForTest drops every registered filter.
// Test-only — calling this from production resets the registry and
// loses every plugin's filter, which is never what production wants.
func ResetCachedSchemaFiltersForTest() {
	cachedSchemaFiltersMu.Lock()
	defer cachedSchemaFiltersMu.Unlock()
	cachedSchemaFilters = nil
}
