package postgres

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Compile-time check: the provider recognises a row cap in its own dialect. A
// provider that silently lacks this gives its users an exemption from the
// truncation caveat, which is the failure mode the caveat exists to prevent —
// so the gap has to break the build rather than go unnoticed at runtime.
var _ gowarehouse.RowCapInspector = (*PostgresProvider)(nil)

func TestPostgresProvider_RowCap(t *testing.T) {
	p := &PostgresProvider{}
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"trailing LIMIT", "SELECT p_type, SUM(rev) FROM part GROUP BY p_type ORDER BY 2 DESC LIMIT 15", 15, true},
		{"ANSI FETCH FIRST", "SELECT a FROM t ORDER BY a FETCH FIRST 10 ROWS ONLY", 10, true},
		{"uncapped", "SELECT a FROM t GROUP BY a", 0, false},
		{"subquery LIMIT does not bound the output", "SELECT k, COUNT(*) FROM (SELECT k FROM u LIMIT 100) s GROUP BY k", 0, false},
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
