package databricks

import (
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
)

// Compile-time check: the provider recognises a row cap in its own dialect. A
// provider that silently lacks this gives its users an exemption from the
// truncation caveat, which is the failure mode the caveat exists to prevent —
// so the gap has to break the build rather than go unnoticed at runtime.
var _ gowarehouse.RowCapInspector = (*DatabricksProvider)(nil)

func TestDatabricksProvider_RowCap(t *testing.T) {
	p := &DatabricksProvider{}
	tests := []struct {
		name  string
		query string
		want  int
		ok    bool
	}{
		{"trailing LIMIT", "SELECT a FROM `c`.`s`.`t` LIMIT 12", 12, true},
		{"uncapped", "SELECT a FROM `c`.`s`.`t`", 0, false},
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
