package agentplugin

import (
	"context"
	"fmt"
	"sync"
)

// ListTablesFilterFunc is invoked after the warehouse driver returns
// the raw table list for a dataset and before the agent calls per-
// table schema discovery. Implementations may shrink the returned
// slice (allow / deny lists, regex denylists, etc.) and may return a
// wrapped error to surface a misconfiguration to the operator.
//
// Filters MUST be pure with respect to the input: the agent owns the
// returned slice and may mutate it freely. Filters that need a copy
// should clone before mutating.
//
// Filters MUST NOT add tables that were not in the input. The list of
// tables the warehouse exposes is the upper bound — a filter exists
// to constrain it, never to invent.
//
// Filters are called in registration order. A non-nil error from any
// filter aborts schema discovery for that dataset; the orchestrator
// logs the error and skips the dataset (existing semantic for
// ListTables failures, see schema_discovery.go).
type ListTablesFilterFunc func(ctx context.Context, projectID, dataset string, tables []string) ([]string, error)

var (
	listTablesFiltersMu sync.RWMutex
	listTablesFilters   []namedListTablesFilter
)

type namedListTablesFilter struct {
	name string
	fn   ListTablesFilterFunc
}

// RegisterListTablesFilter registers fn as a list-tables filter under
// name. Plugins call this from init() with a blank import; production
// callers register at startup. Empty name or nil fn panics.
// Re-registering the same name panics.
//
// Registration order is preserved so chained filters compose
// deterministically (e.g. an allow-list followed by a deny-list).
func RegisterListTablesFilter(name string, fn ListTablesFilterFunc) {
	if name == "" {
		panic("agentplugin: RegisterListTablesFilter called with empty name")
	}
	if fn == nil {
		panic(fmt.Sprintf("agentplugin: RegisterListTablesFilter %q called with nil fn", name))
	}
	listTablesFiltersMu.Lock()
	defer listTablesFiltersMu.Unlock()
	for _, existing := range listTablesFilters {
		if existing.name == name {
			panic(fmt.Sprintf("agentplugin: ListTablesFilter %q already registered", name))
		}
	}
	listTablesFilters = append(listTablesFilters, namedListTablesFilter{name: name, fn: fn})
}

// ApplyListTablesFilters runs every registered filter in registration
// order and returns the resulting table slice. With no filters
// registered the input slice is returned unchanged (and unmodified)
// so the agent can continue to call this unconditionally.
//
// On the first non-nil error from a filter, ApplyListTablesFilters
// returns (nil, error). Subsequent filters are not invoked — the
// caller treats this as a dataset-listing failure and surfaces it the
// same way an error from ListTablesInDataset is handled today.
func ApplyListTablesFilters(ctx context.Context, projectID, dataset string, tables []string) ([]string, error) {
	listTablesFiltersMu.RLock()
	filters := make([]namedListTablesFilter, len(listTablesFilters))
	copy(filters, listTablesFilters)
	listTablesFiltersMu.RUnlock()

	if len(filters) == 0 {
		return tables, nil
	}

	out := tables
	for _, f := range filters {
		next, err := f.fn(ctx, projectID, dataset, out)
		if err != nil {
			return nil, fmt.Errorf("list-tables filter %q: %w", f.name, err)
		}
		out = next
	}
	return out, nil
}

// resetListTablesFiltersForTest clears every registered filter. Test-only.
func resetListTablesFiltersForTest() {
	listTablesFiltersMu.Lock()
	defer listTablesFiltersMu.Unlock()
	listTablesFilters = nil
}

// ResetListTablesFiltersForTest is the exported test helper.
// Production code MUST NOT call this.
func ResetListTablesFiltersForTest() {
	resetListTablesFiltersForTest()
}
