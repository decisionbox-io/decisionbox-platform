package bigquery

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Compile-time check: the provider recognises a row cap in its own dialect. A
// provider that silently lacks this gives its users an exemption from the
// truncation caveat, which is the failure mode the caveat exists to prevent —
// so the gap has to break the build rather than go unnoticed at runtime.
var _ gowarehouse.RowCapInspector = (*BigQueryProvider)(nil)

func TestBigQueryProvider_RowCap(t *testing.T) {
	p := &BigQueryProvider{}
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"trailing LIMIT", "SELECT p_type FROM `p.d.part` GROUP BY p_type LIMIT 15", 15, true},
		{"uncapped", "SELECT p_type FROM `p.d.part` GROUP BY p_type", 0, false},
		{"GoogleSQL has no TOP", "SELECT TOP 15 a FROM `p.d.t`", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := p.RowCap(tc.query)
			if n != tc.want || ok != tc.ok {
				t.Errorf("RowCap(%q) = (%d, %v), want (%d, %v)", tc.query, n, ok, tc.want, tc.ok)
			}
		})
	}
}
