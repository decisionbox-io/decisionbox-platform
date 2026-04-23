package bigquery

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Compile-time check: the provider implements SampleQueryBuilder. If the
// interface contract drifts the build fails loud.
var _ gowarehouse.SampleQueryBuilder = (*BigQueryProvider)(nil)

func TestBigQueryProvider_SampleQuery(t *testing.T) {
	p := &BigQueryProvider{}

	tests := []struct {
		name   string
		ds, tb string
		filter string
		limit  int
		want   string
	}{
		{
			name:  "no filter",
			ds:    "mydataset", tb: "events",
			filter: "", limit: 5,
			want: "SELECT * FROM `mydataset.events`  LIMIT 5",
		},
		{
			name:  "with filter",
			ds:    "mydataset", tb: "events",
			filter: "WHERE country = 'TR'", limit: 10,
			want: "SELECT * FROM `mydataset.events` WHERE country = 'TR' LIMIT 10",
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
