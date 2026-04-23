package redshift

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

var _ gowarehouse.SampleQueryBuilder = (*RedshiftProvider)(nil)

func TestRedshiftProvider_SampleQuery(t *testing.T) {
	p := &RedshiftProvider{}

	tests := []struct {
		name   string
		ds, tb string
		filter string
		limit  int
		want   string
	}{
		{
			name:  "no filter",
			ds:    "public", tb: "orders",
			filter: "", limit: 5,
			want: `SELECT * FROM "public"."orders"  LIMIT 5`,
		},
		{
			name:  "with filter",
			ds:    "sales", tb: "transactions",
			filter: "WHERE amount > 100", limit: 50,
			want: `SELECT * FROM "sales"."transactions" WHERE amount > 100 LIMIT 50`,
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
