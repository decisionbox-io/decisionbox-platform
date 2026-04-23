package postgres

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

var _ gowarehouse.SampleQueryBuilder = (*PostgresProvider)(nil)

func TestPostgresProvider_SampleQuery(t *testing.T) {
	p := &PostgresProvider{}

	tests := []struct {
		name   string
		ds, tb string
		filter string
		limit  int
		want   string
	}{
		{
			name:  "no filter",
			ds:    "public", tb: "users",
			filter: "", limit: 5,
			want: `SELECT * FROM "public"."users"  LIMIT 5`,
		},
		{
			name:  "with filter",
			ds:    "analytics", tb: "events",
			filter: "WHERE event_type = 'click'", limit: 20,
			want: `SELECT * FROM "analytics"."events" WHERE event_type = 'click' LIMIT 20`,
		},
		{
			// PostgreSQL preserves the exact case of double-quoted
			// identifiers. User-configured names like "MyTable" stay
			// literal instead of being lowercased.
			name:  "preserves case via double-quoting",
			ds:    "Public", tb: "MyTable",
			filter: "", limit: 5,
			want: `SELECT * FROM "Public"."MyTable"  LIMIT 5`,
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
