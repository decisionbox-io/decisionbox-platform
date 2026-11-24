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

// MutationQueryResult is a bounded re-read. RowCount is what the source
// returned before MaxRows was applied.
type MutationQueryResult struct {
	Columns  []string
	Rows     []map[string]any
	RowCount int
}

// MutationQuery re-runs a query the turn has already run. See the
// askmutation.QueryFunc contract it is adapted onto in the agentserver.
type MutationQuery func(ctx context.Context, q MutationQueryRequest) (*MutationQueryResult, error)

// mutationQuery builds the re-read a write tool receives for this turn. It is
// execQuery's path with the model removed: the same datasource resolution, the
// same per-query deadline under the turn's, the same warehouse-id context for
// the governance middleware, the same warm connection, and the same executor —
// which carries the tenant filter and runs over read-only credentials.
//
// Three things differ, all because the caller is re-reading its own
// observation rather than asking a new question:
//
//   - The datasource must be named. The model's query may fall back to the
//     project's primary; a re-read may not, because the observation it belongs
//     to was made against one specific warehouse.
//   - No repair. A rewritten query is a different query, and its rows must not
//     be reported as this one's.
//   - It does not spend the turn's query budget. This is not one of the model's
//     queries, and charging it would starve the turn that asked for the work.
func (r *runner) mutationQuery(rt *ProjectRuntime, st *turnState) MutationQuery {
	return func(ctx context.Context, q MutationQueryRequest) (*MutationQueryResult, error) {
		dsID := strings.TrimSpace(q.DatasourceID)
		if dsID == "" {
			return nil, fmt.Errorf("a re-read must name the datasource its result came from")
		}
		if _, ok := rt.datasource(dsID); !ok {
			return nil, fmt.Errorf("datasource %q is not configured for this project", dsID)
		}
		sql := strings.TrimSpace(q.SQL)
		if sql == "" {
			return nil, fmt.Errorf("a re-read needs the query that produced the result")
		}

		// The fetch cap is the process's memory bound, for the same reason
		// summarizeResult re-applies it: a caller asking for more than the loop
		// will ever hold gets the loop's limit, not an unbounded read.
		limit := q.MaxRows
		if limit <= 0 || (r.cfg.MaxFetchRows > 0 && limit > r.cfg.MaxFetchRows) {
			limit = r.cfg.MaxFetchRows
		}

		// Per-query deadline under the turn's: ctx already carries the turn
		// wall-clock, so WithTimeout takes whichever is sooner.
		qctx := ctx
		if r.cfg.QueryTimeout > 0 {
			var cancel context.CancelFunc
			qctx, cancel = context.WithTimeout(ctx, r.cfg.QueryTimeout)
			defer cancel()
		}
		qctx = gowarehouse.WithWarehouseID(qctx, dsID)

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
		out := &MutationQueryResult{Columns: columnsOf(rows), RowCount: res.RowCount}
		if limit > 0 && len(rows) > limit {
			rows = rows[:limit]
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
