package ai

import (
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// The renderer these tests used to cover moved to warehouse.CaveatInstruction,
// where it is tested directly — Ask needs the same wording, and two phrasings
// of the same caveat would be two answers to "how bad is this". What is still
// this package's own is that the exploration message CARRIES it, which is what
// the remaining tests here cover.

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
