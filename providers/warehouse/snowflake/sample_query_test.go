package snowflake

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

var _ gowarehouse.SampleQueryBuilder = (*SnowflakeProvider)(nil)

func TestSnowflakeProvider_SampleQuery(t *testing.T) {
	p := &SnowflakeProvider{}

	tests := []struct {
		name   string
		ds, tb string
		filter string
		limit  int
		want   string
	}{
		{
			// Standard Snowflake case: names stored uppercase (unquoted
			// identifiers are folded to uppercase at DDL time). We
			// double-quote here so the names come through exactly as
			// configured — typically already uppercase for sample data.
			name:  "uppercase names (standard case)",
			ds:    "TPCDS_SF100TCL", tb: "CUSTOMER",
			filter: "", limit: 5,
			want: `SELECT * FROM "TPCDS_SF100TCL"."CUSTOMER"  LIMIT 5`,
		},
		{
			name:  "with filter",
			ds:    "PUBLIC", tb: "ORDERS",
			filter: "WHERE o_orderdate > '2023-01-01'", limit: 10,
			want: `SELECT * FROM "PUBLIC"."ORDERS" WHERE o_orderdate > '2023-01-01' LIMIT 10`,
		},
		{
			// Case-sensitive names created with `CREATE TABLE "MixedCase"`
			// stay mixed-case when double-quoted.
			name:  "preserves mixed case",
			ds:    "public", tb: "MixedCase",
			filter: "", limit: 5,
			want: `SELECT * FROM "public"."MixedCase"  LIMIT 5`,
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
