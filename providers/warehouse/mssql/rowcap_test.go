package mssql

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Compile-time check: the provider recognises a row cap in its own dialect. A
// provider that silently lacks this gives its users an exemption from the
// truncation caveat, which is the failure mode the caveat exists to prevent —
// so the gap has to break the build rather than go unnoticed at runtime.
var _ gowarehouse.RowCapInspector = (*MSSQLProvider)(nil)

func TestMSSQLProvider_RowCap(t *testing.T) {
	p := &MSSQLProvider{}
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"SELECT TOP", "SELECT TOP 15 p_type, SUM(rev) FROM part GROUP BY p_type", 15, true},
		{"parenthesised TOP", "SELECT TOP (15) a FROM t", 15, true},
		{"OFFSET FETCH tail", "SELECT a FROM t ORDER BY a OFFSET 0 ROWS FETCH NEXT 15 ROWS ONLY", 15, true},
		{"TOP PERCENT is not a row cap", "SELECT TOP 10 PERCENT a FROM t", 0, false},
		{"T-SQL has no LIMIT", "SELECT a FROM t LIMIT 15", 0, false},
		{"uncapped", "SELECT a FROM t", 0, false},
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
