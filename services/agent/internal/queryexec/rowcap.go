package queryexec

import (
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// appendRowCapCaveat adds a truncation caveat when the query's own row cap,
// rather than the data, decided how many rows came back.
//
// It asks the runner rather than reading the SQL itself. Matching LIMIT here
// would cover Postgres, Redshift, BigQuery, Databricks and Snowflake and
// silently exempt MSSQL and Oracle, whose dialects cap with TOP and FETCH
// FIRST — an exemption invisible at the call site and indistinguishable, in
// the output, from a warehouse whose queries are never capped.
//
// Returns the caveats unchanged when the runner cannot recognise a cap, when
// the query has none, or when fewer rows came back than the cap allowed.
func appendRowCapCaveat(caveats []gowarehouse.QualityCaveat, runner gowarehouse.QueryRunner, query string, rowCount int) []gowarehouse.QualityCaveat {
	inspector, ok := runner.(gowarehouse.RowCapInspector)
	if !ok {
		return caveats
	}
	// Copy rather than append in place: the slice came from the provider's
	// result and may share a backing array with it.
	out := make([]gowarehouse.QualityCaveat, 0, len(caveats)+2)
	out = append(out, caveats...)

	if rowCap, capped := inspector.RowCap(query); capped && rowCount == rowCap {
		out = append(out, gowarehouse.RowCapCaveat(rowCap))
	}

	// An offset makes the result a page whatever the row count. `LIMIT 100 OFFSET
	// 100` returning 17 rows never trips the cap check, yet a hundred rows were
	// deliberately skipped and the page reads exactly like a complete small result
	// -- so the cap check alone exempts every final page from the caveat.
	if off, ok := rowOffsetInspectorOf(runner); ok {
		if skipped, paginated := off.RowOffset(query); paginated {
			out = append(out, gowarehouse.RowOffsetCaveat(skipped))
		}
	}

	if len(out) == len(caveats) {
		return caveats
	}
	return out
}

// rowOffsetInspectorOf finds the nearest RowOffsetInspector a runner can reach,
// through the same unwrap chain RowCap uses -- middleware erases an optional
// capability unless it re-exposes it.
func rowOffsetInspectorOf(runner gowarehouse.QueryRunner) (gowarehouse.RowOffsetInspector, bool) {
	if i, ok := runner.(gowarehouse.RowOffsetInspector); ok {
		return i, true
	}
	type unwrapper interface{ Unwrap() gowarehouse.Provider }
	var p gowarehouse.Provider
	if u, ok := runner.(unwrapper); ok {
		p = u.Unwrap()
	} else {
		return nil, false
	}
	for range 16 {
		if p == nil {
			return nil, false
		}
		if i, ok := p.(gowarehouse.RowOffsetInspector); ok {
			return i, true
		}
		u, ok := p.(unwrapper)
		if !ok {
			return nil, false
		}
		next := u.Unwrap()
		if next == p {
			return nil, false
		}
		p = next
	}
	return nil, false
}
