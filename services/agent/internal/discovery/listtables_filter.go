package discovery

import (
	"context"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// The list-tables filter registry lives in libs/go-common/agentplugin
// alongside the rest of the agent-plugin hooks. This file keeps the
// in-package aliases callers inside services/agent already use so the
// move is invisible to existing code.

// ListTablesFilterFunc aliases agentplugin.ListTablesFilterFunc.
type ListTablesFilterFunc = agentplugin.ListTablesFilterFunc

// RegisterListTablesFilter delegates to the agentplugin registry.
func RegisterListTablesFilter(name string, fn ListTablesFilterFunc) {
	agentplugin.RegisterListTablesFilter(name, fn)
}

// ApplyListTablesFilters delegates to the agentplugin registry.
// Wrapper function (not a var alias) so the API surface is fixed at
// build time and grep / godoc treat it as a regular exported function.
func ApplyListTablesFilters(ctx context.Context, projectID, dataset string, tables []string) ([]string, error) {
	return agentplugin.ApplyListTablesFilters(ctx, projectID, dataset, tables)
}

// resetListTablesFiltersForTest is the package-private helper the
// existing tests in this directory use. Test-only — kept lowercase so
// non-test code can't reach for it accidentally.
func resetListTablesFiltersForTest() {
	agentplugin.ResetListTablesFiltersForTest()
}
