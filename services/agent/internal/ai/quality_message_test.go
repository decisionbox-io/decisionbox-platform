package ai

import (
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// TestFormatQualityCaveats covers what the exploring model is told when a
// result is not a faithful answer to its query.
//
// It is worded as an instruction rather than a note on purpose: a caveat the
// model reads and does not act on is worth nothing, because the rows are
// well-formed, the numbers add up, and every conclusion drawn from them looks
// sound.
func TestFormatQualityCaveats(t *testing.T) {
	out := formatQualityCaveats([]gowarehouse.QualityCaveat{
		{Kind: gowarehouse.QualityWithheld, Detail: "small cohorts omitted"},
		{Kind: gowarehouse.QualityTruncated, Detail: "tail collapsed"},
	})

	if !strings.Contains(out, "not a faithful answer") {
		t.Errorf("output = %q, want it to state plainly that the result is not faithful", out)
	}
	for _, want := range []string{"small cohorts omitted", "tail collapsed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to carry %q", out, want)
		}
	}
	// The consequence, not just the fact.
	if !strings.Contains(out, "Do not present a total, share or ranking") {
		t.Errorf("output = %q, want it to say what not to do with these rows", out)
	}
}

// TestFormatQualityCaveats_SilentWhenThereIsNothingToSay pins that a clean
// result's message is unchanged. Every SQL warehouse declares no caveats, so
// emitting an empty section would add noise to every step of every run the
// product has ever done — and would train the model to skip the section on the
// rare occasion it matters.
func TestFormatQualityCaveats_SilentWhenThereIsNothingToSay(t *testing.T) {
	if got := formatQualityCaveats(nil); got != "" {
		t.Errorf("formatQualityCaveats(nil) = %q, want empty", got)
	}
	if got := formatQualityCaveats([]gowarehouse.QualityCaveat{}); got != "" {
		t.Errorf("formatQualityCaveats(empty) = %q, want empty", got)
	}
}

// TestFormatQuerySuccess_ShowsCaveatsBeforeTheRows covers the wiring, not just
// the helper. A caveat can be carried onto the step and still never reach the
// model, which is the failure this whole chain exists to prevent — so the
// assertion is that the assembled message contains it, and contains it BEFORE
// the rows. A model that reads well-formed rows first has already concluded by
// the time it reaches a footnote.
func TestFormatQuerySuccess_ShowsCaveatsBeforeTheRows(t *testing.T) {
	e := &ExplorationEngine{}
	msg := e.formatQuerySuccess(&queryexec.ExecuteResult{
		Data:     []map[string]interface{}{{"city": "Dublin", "sessions": 31}},
		RowCount: 1,
		Quality:  []gowarehouse.QualityCaveat{{Kind: gowarehouse.QualityWithheld, Detail: "small cohorts omitted"}},
	})

	caveatAt := strings.Index(msg, "small cohorts omitted")
	rowsAt := strings.Index(msg, "**Results**")
	if caveatAt < 0 {
		t.Fatalf("the caveat never reached the model:\n%s", msg)
	}
	if rowsAt < 0 || caveatAt > rowsAt {
		t.Errorf("the caveat appears after the rows; it must precede them:\n%s", msg)
	}
}

// TestFormatQuerySuccess_CleanResultIsUnchanged pins that a warehouse result —
// which never carries a caveat — reads exactly as it did before.
func TestFormatQuerySuccess_CleanResultIsUnchanged(t *testing.T) {
	e := &ExplorationEngine{}
	msg := e.formatQuerySuccess(&queryexec.ExecuteResult{
		Data:     []map[string]interface{}{{"n": 1}},
		RowCount: 1,
	})

	if strings.Contains(msg, "not a faithful answer") {
		t.Errorf("a clean result gained a caveat section:\n%s", msg)
	}
	if !strings.Contains(msg, "Query executed successfully.") || !strings.Contains(msg, "**Results**") {
		t.Errorf("the ordinary message shape changed:\n%s", msg)
	}
}
