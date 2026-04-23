package mssql

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

var _ gowarehouse.SampleQueryBuilder = (*MSSQLProvider)(nil)

func TestMSSQLProvider_SampleQuery(t *testing.T) {
	p := &MSSQLProvider{}

	tests := []struct {
		name   string
		ds, tb string
		filter string
		limit  int
		want   string
	}{
		{
			name:  "no filter",
			ds:    "dbo", tb: "orders",
			filter: "", limit: 5,
			want: "SELECT TOP 5 * FROM [dbo].[orders] ",
		},
		{
			// The real trigger for this whole PR — MSSQL table names with
			// a leading underscore are rejected without bracket quoting;
			// the old BigQuery-style fallback emitted backticks + LIMIT
			// and had to be LLM-fixed for every such table.
			name:  "underscore-prefixed table with leading chars",
			ds:    "dbo", tb: "_DNA_EADAPTOR_EDESPATCH_31122022",
			filter: "", limit: 5,
			want: "SELECT TOP 5 * FROM [dbo].[_DNA_EADAPTOR_EDESPATCH_31122022] ",
		},
		{
			name:  "with filter",
			ds:    "dbo", tb: "sales",
			filter: "WHERE region = 'EU'", limit: 10,
			want: "SELECT TOP 10 * FROM [dbo].[sales] WHERE region = 'EU'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := p.SampleQuery(tc.ds, tc.tb, tc.filter, tc.limit)
			if got != tc.want {
				t.Errorf("SampleQuery(%q, %q, %q, %d)\ngot:  %q\nwant: %q", tc.ds, tc.tb, tc.filter, tc.limit, got, tc.want)
			}
		})
	}
}
