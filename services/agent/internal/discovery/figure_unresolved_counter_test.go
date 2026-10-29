package discovery

import (
	"encoding/json"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestRenderInsightFigures_CountsTheUnreadableValuesItReports goes through the real
// render path, because the unit tests either side of it did not and that is exactly
// how the counter shipped broken.
//
// unresolvedCauses takes the references the prose still carries, and strayRefs collects
// whole matches -- "{{f1}}", braces included -- since it is built on reAnyRef so an id
// the resolving grammar cannot express is still reported. The classifier looked those up
// as ids, missed every time, and reported every reference as undeclared. A live 27B run
// shipped seven references that were all declared with a value no number reads from, and
// the counter recorded nought while the log blamed the model for not declaring them.
//
// The tests that covered the classifier passed throughout: they handed it bare ids,
// which is the shape the author assumed rather than the shape the caller holds. So this
// one sets ValueMissing the way production does -- by decoding a response -- and asserts
// through renderInsightFigures rather than against the helper.
func TestRenderInsightFigures_CountsTheUnreadableValuesItReports(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      int
	}{
		{
			name: "a date in a numeric field",
			raw: `{"name":"n","description":"unpaid since {{f1}}","figures":[
			  {"id":"f1","value":"2026-03-15","unit":"plain","kind":"cell","step":1}]}`,
			want: 1,
		},
		{
			name: "two vendor names, each referenced once",
			raw: `{"name":"{{f1}} leads","description":"then {{f2}}","figures":[
			  {"id":"f1","value":"ACME HOLDING A.S.","unit":"plain","kind":"cell","step":1},
			  {"id":"f2","value":"BETA SANAYI LTD","unit":"plain","kind":"cell","step":1}]}`,
			want: 2,
		},
		{
			name: "an undeclared reference is not this class",
			raw:  `{"name":"n","description":"over the {{window}}","figures":[]}`,
			want: 0,
		},
		{
			name: "a readable value is not this class",
			raw: `{"name":"n","description":"{{f1}} of revenue","figures":[
			  {"id":"f1","value":24.66,"unit":"percent","kind":"ratio","step":1}]}`,
			want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ins models.Insight
			if err := json.Unmarshal([]byte(tc.raw), &ins); err != nil {
				t.Fatal(err)
			}
			// newFigureWriter, not figureWriter{}: the zero value carries a nil
			// *message.Printer and panics the moment a figure actually renders, which
			// only the readable case here does.
			tally := newFigureWriter("English").renderInsightFigures([]models.Insight{ins})
			if tally.valueUnreadable != tc.want {
				t.Errorf("valueUnreadable = %d, want %d (unresolved=%d) -- the counter does not "+
					"agree with what the render actually could not resolve",
					tally.valueUnreadable, tc.want, tally.unresolved)
			}
		})
	}
}
