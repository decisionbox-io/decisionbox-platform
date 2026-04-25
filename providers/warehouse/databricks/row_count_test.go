package databricks

import "testing"

// DESCRIBE EXTENDED's Statistics row has drifted format across
// Databricks runtimes. These cases lock in every shape we've seen
// so a runtime upgrade that tweaks the renderer shows up as a unit
// test failure instead of silently returning 0 across the warehouse.

func TestParseDescribeExtendedRowCount(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    int64
		wantOk  bool
	}{
		{"bytes + rows (small table)", "1234 bytes, 42 rows", 42, true},
		{"human size + no-sep rows", "1.2 GB, 2400000 rows", 2400000, true},
		{"thousand-separator rows", "3.5 MB, 2,400,000 rows", 2400000, true},
		{"mib + singular row", "3.5 MiB, 1 rows", 1, true},
		{"zero rows post-truncate", "0 bytes, 0 rows", 0, true},

		{"empty string — no stats computed", "", 0, false},
		{"size only — stats partial", "1.2 GB", 0, false},
		{"rows without count", "some rows", 0, false},
		{"malformed — text only", "statistics not available", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseDescribeExtendedRowCount(c.input)
			if ok != c.wantOk {
				t.Errorf("ok = %v, want %v (input=%q)", ok, c.wantOk, c.input)
			}
			if got != c.want {
				t.Errorf("count = %d, want %d (input=%q)", got, c.want, c.input)
			}
		})
	}
}
