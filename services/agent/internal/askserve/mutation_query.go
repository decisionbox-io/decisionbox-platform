package askserve

import (
	"context"
	"fmt"
	"strings"
	"time"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// MutationQueryRequest names one result a write tool wants to re-read.
type MutationQueryRequest struct {
	// DatasourceID is required: a re-read addresses a specific observation, and
	// the tool knows which datasource produced it. Resolving an empty one to the
	// primary would read the wrong warehouse for any turn that queried another.
	DatasourceID string
	// SQL runs verbatim. Never parsed, never rewritten.
	SQL string
	// MaxRows bounds the rows returned; 0 means the loop's own fetch cap, which
	// clamps it either way.
	MaxRows int
}

// MutationQueryResult is a bounded read. RowCount is what the source returned
// before MaxRows was applied.
type MutationQueryResult struct {
	Columns  []string
	Rows     []map[string]any
	RowCount int
	// Quality carries what the SOURCE said about this result's fidelity — rows
	// it withheld, values it sampled, a cap the query reached. Dropping it
	// would hand a caller len(Rows) == RowCount for a result that is not the
	// whole population, which is exactly the mistake a grounded consumer must
	// not make. Nil for every SQL warehouse that declared nothing.
	Quality []gowarehouse.QualityCaveat
}

// MutationQuery re-runs a query the turn has already run. See the
// askmutation.QueryFunc contract it is adapted onto in the agentserver.
type MutationQuery func(ctx context.Context, q MutationQueryRequest) (*MutationQueryResult, error)

// mutationQuery builds the bounded read a write tool receives for this turn.
//
// What it actually grants, stated plainly rather than by its intended use: a
// tool may run ANY query it supplies, read-only and tenant-scoped, against any
// datasource configured for the project. It is for re-reading a result the
// conversation already observed — that is why it exists and what its only
// caller does — but nothing here verifies that the query was observed, so the
// capability must be described as the wider one it is.
//
// Verifying it is not available at this layer and would break the use it is
// for: the result being re-read usually belongs to an EARLIER turn, so the
// turn's own query log cannot confirm it, and reaching into session storage
// from the loop to check would couple the loop to a store it deliberately
// knows nothing about. What bounds it instead:
//
//   - the datasource must be one of the project's, named explicitly — the
//     model's query may fall back to the primary, this may not, because a
//     re-read belongs to one specific warehouse;
//   - the tenant filter and read-only credentials of that datasource, both the
//     executor's, identical to every query_data call;
//   - no repair, so a rewritten query's rows are never returned as this one's;
//   - a row cap and the turn's deadline;
//   - a per-turn count (Config.MaxReReadsPerTurn). These do not spend the
//     model's query budget — charging them would starve the turn that asked
//     for the work — and that is precisely why they need a bound of their own.
func (r *runner) mutationQuery(rt *ProjectRuntime, st *turnState) MutationQuery {
	// The TURN's context, captured here rather than taken from the caller. A
	// plugin handing in a fresh one would lose the project id the warehouse
	// middleware scopes by and the turn deadline that bounds the read; the
	// caller's context can only shorten or cancel, never extend or detach.
	turnCtx := st.ctx
	return func(ctx context.Context, q MutationQueryRequest) (*MutationQueryResult, error) {
		// A turn built without its context (the early-failure path, a test)
		// must not take the read down with it: fall back to the caller's,
		// which is the behaviour before the turn context was threaded here.
		if turnCtx == nil {
			turnCtx = ctx
		}
		// A caller that is already done gets no warehouse call at all. Relying
		// on the cancellation watch below would race a fast query, and the
		// read's whole result would be discarded anyway.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Reserved atomically. The loop serialises mutation INVOCATIONS, not
		// the callbacks inside one: a tool reading several results
		// concurrently would otherwise have two calls take the same remaining
		// slot and run past the bound.
		if r.cfg.MaxReReadsPerTurn > 0 {
			if used := st.reReadsUsed.Add(1); int(used) > r.cfg.MaxReReadsPerTurn {
				return nil, fmt.Errorf("this turn has already made its %d permitted re-reads", r.cfg.MaxReReadsPerTurn)
			}
		} else {
			st.reReadsUsed.Add(1)
		}
		dsID := strings.TrimSpace(q.DatasourceID)
		if dsID == "" {
			return nil, fmt.Errorf("a re-read must name the datasource its result came from")
		}
		if _, ok := rt.datasource(dsID); !ok {
			return nil, fmt.Errorf("datasource %q is not configured for this project", dsID)
		}
		// A PINNED turn is the user's explicit choice of warehouse, and it
		// binds this read too. Refusing is the only honest answer: running the
		// query against the pinned datasource instead would silently change
		// what the result is OF, and running it against the one it names would
		// query a warehouse the user excluded from this turn.
		if pin := st.routing.pinned; pin != "" && pin != dsID {
			return nil, fmt.Errorf("this turn is pinned to datasource %q, so a result from %q cannot be read again here", pin, dsID)
		}
		// Trimmed only to reject a blank request; EXECUTED verbatim. Trimming
		// what runs would change a query whose trailing character is part of a
		// quoted identifier, and the contract here is that the query runs as
		// given.
		if strings.TrimSpace(q.SQL) == "" {
			return nil, fmt.Errorf("a re-read needs the query that produced the result")
		}
		sql := q.SQL

		// The fetch cap is the process's memory bound, for the same reason
		// summarizeResult re-applies it: a caller asking for more than the loop
		// will ever hold gets the loop's limit, not an unbounded read.
		limit := q.MaxRows
		if limit <= 0 || (r.cfg.MaxFetchRows > 0 && limit > r.cfg.MaxFetchRows) {
			limit = r.cfg.MaxFetchRows
		}

		// Run under the TURN's context, narrowed by the caller's. The turn's
		// carries the project id and its wall-clock; the caller's may only make
		// the read end sooner.
		qctx, cancel := context.WithCancel(turnCtx)
		defer cancel()
		if d, ok := ctx.Deadline(); ok {
			var dcancel context.CancelFunc
			qctx, dcancel = context.WithDeadline(qctx, d)
			defer dcancel()
		}
		// And honour an outright cancellation of the caller's context, which a
		// deadline alone would not catch.
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-stop:
			}
		}()
		if r.cfg.QueryTimeout > 0 {
			var qcancel context.CancelFunc
			qctx, qcancel = context.WithTimeout(qctx, r.cfg.QueryTimeout)
			defer qcancel()
		}
		qctx = gowarehouse.WithWarehouseID(qctx, dsID)

		// The MERGED context, before any connection is opened. A turn that has
		// already expired while the plugin passes a live context would
		// otherwise reach acquireConn, which starts its build under
		// context.WithoutCancel — a connection that keeps being established
		// after this read has returned an error.
		if err := qctx.Err(); err != nil {
			return nil, err
		}

		conn, release, err := rt.acquireConn(qctx, dsID)
		if err != nil {
			return nil, fmt.Errorf("could not connect to datasource %q: %w", dsID, err)
		}
		defer release()

		start := time.Now()
		res, err := conn.Executor.ExecuteWithFixOpts(qctx, sql, "re-read an observed result", queryexec.FixOpts{NoRepair: true})
		if err != nil {
			return nil, err
		}

		rows := res.Data
		// The provider's own column metadata where it gave any, falling back
		// to the row keys. A query that matched nothing still has columns —
		// Postgres reads them before iterating rows — and a caller writing a
		// header row for an empty result needs them.
		cols := res.Columns
		if len(cols) == 0 {
			cols = columnsOf(rows)
		}
		out := &MutationQueryResult{
			Columns:  cols,
			RowCount: res.RowCount,
			// Carried, never summarised: a caveat's own words name what was
			// degraded, and only the source knows. Without it a caller sees
			// len(Rows) == RowCount and reads a sampled result as the whole
			// population.
			Quality: res.Quality,
		}
		if limit > 0 && len(rows) > limit {
			// COPIED, not resliced: a reslice keeps the whole warehouse
			// response and every row map in it alive for as long as the caller
			// holds the capped result, which for a six-figure result is the
			// difference between megabytes and tens of them.
			kept := make([]map[string]any, limit)
			copy(kept, rows[:limit])
			rows = kept
		}
		out.Rows = rows

		applog.WithFields(applog.Fields{
			"project_id":    st.req.ProjectID,
			"turn_id":       st.req.TurnID,
			"datasource_id": dsID,
			"rows":          len(out.Rows),
			"row_count":     out.RowCount,
			"time_ms":       time.Since(start).Milliseconds(),
		}).Debug("ask-serve: re-read an observed result for a write tool")

		return out, nil
	}
}
