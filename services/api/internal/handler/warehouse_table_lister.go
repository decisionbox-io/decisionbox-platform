package handler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// AgentTableLister satisfies WarehouseTableLister by running the agent's
// `--list-tables` mode synchronously (the same RunSync path --test-connection
// uses) and parsing the qualified table names from its stdout JSON. It powers
// the discovery-scope picker before the first index exists.
type AgentTableLister struct {
	runner runner.Runner
}

// NewAgentTableLister wraps an agent runner. Returns nil when the runner is nil
// so callers can wire it unconditionally and the handler simply skips the live
// fallback on builds without an agent runner.
func NewAgentTableLister(r runner.Runner) *AgentTableLister {
	if r == nil {
		return nil
	}
	return &AgentTableLister{runner: r}
}

// listTablesResult is the agent's --list-tables stdout contract.
type listTablesResult struct {
	Success bool     `json:"success"`
	Error   string   `json:"error"`
	Tables  []string `json:"tables"`
}

// ListWarehouseTables runs the agent's --list-tables mode for one datasource
// (empty warehouseID → the project's primary) and returns the qualified table
// names. Errors from the agent (bad credentials, unreachable warehouse) surface
// as an error so the caller can decide whether to degrade to an empty picker.
func (a *AgentTableLister) ListWarehouseTables(ctx context.Context, projectID, warehouseID string) ([]string, error) {
	args := []string{"--list-tables"}
	if warehouseID != "" {
		args = append(args, "--warehouse-id", warehouseID)
	}
	res, err := a.runner.RunSync(ctx, runner.RunSyncOptions{ProjectID: projectID, Args: args})
	// Even on a non-nil error the agent may have printed a JSON error object to
	// stdout; prefer that message. On success, parse the tables.
	var out listTablesResult
	if res != nil {
		if js := extractJSONObject(res.Output); js != nil {
			_ = json.Unmarshal(js, &out)
		}
	}
	if err != nil {
		if out.Error != "" {
			return nil, fmt.Errorf("list tables: %s", out.Error)
		}
		msg := "agent exited with error"
		if res != nil && res.Error != "" {
			msg = res.Error
		}
		return nil, fmt.Errorf("list tables: %s", msg)
	}
	if !out.Success {
		if out.Error != "" {
			return nil, fmt.Errorf("list tables: %s", out.Error)
		}
		return nil, fmt.Errorf("list tables: no result from agent")
	}
	return out.Tables, nil
}
