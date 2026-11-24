package handler

import (
	"context"
	"encoding/json"
	"errors"
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

// How a live listing failed, as far as the API can tell without reading the
// driver's own message. The handler logs which of these it was: coarse, but it
// is the one diagnostic that can be written to a log sink safely, and it
// separates the cases an operator would act on differently — a refusal (bad
// credentials, an unreadable catalogue, a table filter erroring for every
// dataset) from an agent that could not run or produced nothing to parse.
var (
	// ErrListingRefused is the agent reporting a failure of its own: it ran,
	// and said no. Its message is in the agent's output, not in these errors.
	ErrListingRefused = errors.New("warehouse listing refused")
	// ErrListingAgentFailed is the agent exiting badly — image, runner, or
	// environment, rather than anything about the warehouse.
	ErrListingAgentFailed = errors.New("listing agent failed to run")
	// ErrListingNoResult is a clean exit with nothing parseable on stdout,
	// which means the agent and the API disagree about the contract.
	ErrListingNoResult = errors.New("listing agent returned no result")
)

// listTablesResult is the agent's --list-tables stdout contract.
type listTablesResult struct {
	Success bool     `json:"success"`
	Error   string   `json:"error"`
	Tables  []string `json:"tables"`
	// UnlistedDatasets names the datasets the agent could not read. It skips
	// those and still succeeds as long as one listed, so this is the only way to
	// tell a complete catalogue from a partial one. An agent older than this
	// field omits it, which reads as nil — see WarehouseTableListing.Complete.
	UnlistedDatasets []string `json:"unlisted_datasets"`
}

// WarehouseTableListing is one live enumeration's outcome: the qualified names,
// plus the datasets that could not be read.
type WarehouseTableListing struct {
	Tables []string
	// UnlistedDatasets is empty when every configured dataset reported a result,
	// and nil when the agent did not report on it at all.
	UnlistedDatasets []string
	// Complete is false when at least one DATASET could not be read. Dataset
	// granularity is all this can honestly claim: a provider may also drop an
	// individual table whose metadata it can't fetch and still return the rest
	// with no error (BigQuery does — see decisionbox-platform#587), and
	// ListTablesInDataset has no channel to report that. So Complete means
	// "every dataset answered", not "every table in the warehouse is here".
	//
	// An agent that predates unlisted_datasets reports nothing either way and is
	// taken at its word (true) rather than marking every listing suspect: the
	// field's absence is an old image, not evidence of a failure, and treating
	// it as one would warn about every normal listing from that deployment.
	// API and agent ship from one release train, so the skew is a self-hosted
	// upgrade window rather than a steady state.
	Complete bool
}

// ListWarehouseTables runs the agent's --list-tables mode for one datasource
// (empty warehouseID → the project's primary) and returns the qualified table
// names, plus whether every configured dataset could be read. Errors from the
// agent (bad credentials, unreachable warehouse) surface as an error so the
// caller can decide whether to degrade to an empty picker.
func (a *AgentTableLister) ListWarehouseTables(ctx context.Context, projectID, warehouseID string) (WarehouseTableListing, error) {
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
	// Each returned error WRAPS one of the sentinels above, so a caller can log
	// the class without the message. The message itself carries the driver's
	// text and belongs only where an admin asked for it.
	if err != nil {
		if out.Error != "" {
			return WarehouseTableListing{}, fmt.Errorf("%w: %s", ErrListingRefused, out.Error)
		}
		msg := "agent exited with error"
		if res != nil && res.Error != "" {
			msg = res.Error
		}
		return WarehouseTableListing{}, fmt.Errorf("%w: %s", ErrListingAgentFailed, msg)
	}
	if !out.Success {
		if out.Error != "" {
			return WarehouseTableListing{}, fmt.Errorf("%w: %s", ErrListingRefused, out.Error)
		}
		return WarehouseTableListing{}, ErrListingNoResult
	}
	return WarehouseTableListing{
		Tables:           out.Tables,
		UnlistedDatasets: out.UnlistedDatasets,
		Complete:         len(out.UnlistedDatasets) == 0,
	}, nil
}
