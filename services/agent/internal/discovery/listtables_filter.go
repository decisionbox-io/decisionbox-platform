package discovery

import (
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
var ApplyListTablesFilters = agentplugin.ApplyListTablesFilters

// ResetListTablesFiltersForTest delegates to the agentplugin registry.
var ResetListTablesFiltersForTest = agentplugin.ResetListTablesFiltersForTest

// resetListTablesFiltersForTest is the package-private alias the
// existing tests in this directory still use.
var resetListTablesFiltersForTest = agentplugin.ResetListTablesFiltersForTest
