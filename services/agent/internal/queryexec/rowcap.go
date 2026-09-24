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
	rowCap, capped := inspector.RowCap(query)
	if !capped || rowCount != rowCap {
		return caveats
	}
	// Copy rather than append in place: the slice came from the provider's
	// result and may share a backing array with it.
	out := make([]gowarehouse.QualityCaveat, 0, len(caveats)+1)
	out = append(out, caveats...)
	return append(out, gowarehouse.RowCapCaveat(rowCap))
}
