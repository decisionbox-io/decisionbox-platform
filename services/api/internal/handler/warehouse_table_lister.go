package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

const (
	// listTablesJobHeadroomSecs is how much longer the Kubernetes Job runs than
	// the agent's own listing deadline (LIST_TABLES_TIMEOUT_SECONDS). Pod
	// scheduling/startup eats into the Job's ActiveDeadlineSeconds (which counts
	// from Job creation) but not the agent's internal deadline (from process
	// start), so without headroom a large listing could be force-killed before
	// the agent emits its JSON result. Generous enough to cover scheduling +
	// image pull on a cold node.
	listTablesJobHeadroomSecs = 60
	// listTablesCtxHeadroomSecs keeps the API wait just beyond the Job deadline
	// so the request context is the outermost bound, not the first to fire.
	listTablesCtxHeadroomSecs = 15
)

// listTablesTimeoutSecs is the agent's list-tables budget in seconds, from the
// SAME LIST_TABLES_TIMEOUT_SECONDS env the agent reads (default 120s) and which
// is forwarded to the spawned agent so both agree. The Kubernetes Job deadline
// and the API context deadline are derived from it with headroom (see above).
func listTablesTimeoutSecs() int {
	secs := 120
	if v := os.Getenv("LIST_TABLES_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			secs = n
		}
	}
	return secs
}

// AgentTableLister satisfies WarehouseTableLister by running the agent's
// `--list-tables` mode synchronously (the same RunSync path --test-connection
// uses) and parsing the qualified table names from its stdout JSON. It powers
// the discovery-scope picker before the first index exists.
type AgentTableLister struct {
	runner      runner.Runner
	timeoutSecs int
}

// NewAgentTableLister wraps an agent runner. Returns nil when the runner is nil
// so callers can wire it unconditionally and the handler simply skips the live
// fallback on builds without an agent runner.
func NewAgentTableLister(r runner.Runner) *AgentTableLister {
	if r == nil {
		return nil
	}
	return &AgentTableLister{runner: r, timeoutSecs: listTablesTimeoutSecs()}
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
	// The agent's own deadline (LIST_TABLES_TIMEOUT_SECONDS, forwarded to it) is
	// a.timeoutSecs; give the Kubernetes Job deadline headroom beyond that so pod
	// startup can't force-kill it before the agent emits its result, and keep the
	// request context just beyond the Job deadline so it's the outermost bound.
	jobDeadlineSecs := a.timeoutSecs
	if jobDeadlineSecs > 0 {
		jobDeadlineSecs += listTablesJobHeadroomSecs
	}
	opts := runner.RunSyncOptions{ProjectID: projectID, Args: args, TimeoutSeconds: jobDeadlineSecs}
	if jobDeadlineSecs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(jobDeadlineSecs+listTablesCtxHeadroomSecs)*time.Second)
		defer cancel()
	}
	res, err := a.runner.RunSync(ctx, opts)
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
