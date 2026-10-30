package models

import (
	"encoding/json"
	"strings"
	"testing"
)

// Value is a float64, so a value the decode cannot read leaves 0 behind and the
// original is gone -- and 0 is indistinguishable from a model that declared zero.
// A live run put a vendor name and an ISO date in that field and the only way that
// was ever established was replaying the raw response out of the database, a fifth
// of which does not decode. ValueRaw keeps the evidence at the one moment it exists.
func TestFigureUnmarshal_KeepsTheValueItCouldNotRead(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"an ISO date", `{"id":"f3","value":"2026-03-15","unit":"plain","kind":"cell"}`, "2026-03-15"},
		{"a vendor name", `{"id":"f4","value":"ACME HOLDING A.S.","unit":"plain","kind":"cell"}`, "ACME HOLDING"},
		{"a scaled literal", `{"id":"f5","value":"1.2M","unit":"currency","kind":"cell"}`, "1.2M"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f Figure
			if err := json.Unmarshal([]byte(tc.raw), &f); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !f.ValueMissing {
				t.Fatalf("ValueMissing is false, so the layer above will treat %q as a real value", tc.want)
			}
			if !strings.Contains(f.ValueRaw, tc.want) {
				t.Errorf("ValueRaw = %q, want it to carry %q -- without it a log can say a "+
					"figure was unreadable but not what made it so", f.ValueRaw, tc.want)
			}
		})
	}
}

// A figure that decoded fine must not carry the field, or a log would report a
// problem on every healthy figure.
func TestFigureUnmarshal_LeavesValueRawEmptyWhenTheValueReads(t *testing.T) {
	for _, raw := range []string{
		`{"id":"f1","value":24.66,"unit":"percent","kind":"ratio"}`,
		`{"id":"f2","value":"8,476,238,553","unit":"currency","kind":"sum"}`,
		`{"id":"f6","value":0,"unit":"count","kind":"count"}`,
	} {
		var f Figure
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if f.ValueMissing {
			t.Errorf("%s decoded as ValueMissing; a real value was refused", raw)
		}
		if f.ValueRaw != "" {
			t.Errorf("%s left ValueRaw=%q on a figure that read fine", raw, f.ValueRaw)
		}
	}
}

// `value` is model output of no bounded length and this field is destined for a log line.
func TestFigureUnmarshal_BoundsTheRawValueItKeeps(t *testing.T) {
	long := strings.Repeat("x", 5000)
	var f Figure
	if err := json.Unmarshal([]byte(`{"id":"f1","value":"`+long+`","unit":"plain","kind":"cell"}`), &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(f.ValueRaw) > maxValueRawChars+3 {
		t.Errorf("ValueRaw is %d chars; a log line must not carry unbounded model output", len(f.ValueRaw))
	}
}
