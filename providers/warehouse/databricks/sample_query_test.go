package databricks

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

var _ gowarehouse.SampleQueryBuilder = (*DatabricksProvider)(nil)

func TestDatabricksProvider_SampleQuery(t *testing.T) {
	p := &DatabricksProvider{}

	tests := []struct {
		name   string
		ds, tb string
		filter string
		limit  int
		want   string
	}{
		{
			name:  "no filter",
			ds:    "default", tb: "events",
			filter: "", limit: 5,
			want: "SELECT * FROM `default`.`events`  LIMIT 5",
		},
		{
			name:  "with filter",
			ds:    "analytics", tb: "sessions",
			filter: "WHERE device_type = 'mobile'", limit: 15,
			want: "SELECT * FROM `analytics`.`sessions` WHERE device_type = 'mobile' LIMIT 15",
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
