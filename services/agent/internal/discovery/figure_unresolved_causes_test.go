package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// What these pin, from run 20 on a Turkish warehouse:
//
// Two insights shipped literal "{{f4}}", "{{f6}}" and "{{f3}}" to the reader. All
// three ids WERE declared, had no duplicate and were legal under the reference
// grammar -- so every explanation the log offered at the time was wrong. What the
// model had actually done was put a 43-character vendor name and an ISO date into
// `value`, a field typed number. flexFloatOK cannot read either, ValueMissing is
// set, Value keeps the 0 the decode fell back to, and the original string is gone.
//
// Establishing that took re-reading raw responses out of Mongo, where a fifth of
// them carry warehouse bytes that are not valid UTF-8 and do not decode. These
// tests exist so the next occurrence says so in the log instead.

func TestUnresolvedCauses_NamesTheCauseForEachKind(t *testing.T) {
	declared := []models.Figure{
		{ID: "f1", Unit: "currency", Kind: "cell", Value: 1234},
		// the run-20 shape: declared, a value, and nothing numeric in it
		{ID: "f3", Unit: "plain", Kind: "cell", ValueMissing: true, ValueRaw: `"2026-03-15"`},
		{ID: "dup", Unit: "count", Kind: "cell", Value: 1},
		{ID: "dup", Unit: "count", Kind: "cell", Value: 2},
		{ID: "bad-id", Unit: "plain", Kind: "cell", Value: 7},
	}
	lines, unreadable := unresolvedCauses(declared, []string{"window", "f3", "dup", "bad-id"})

	if unreadable != 1 {
		t.Errorf("value-unreadable count = %d, want 1 -- it is the class the fix depends on", unreadable)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"window: referenced but never declared",
		"f3: declared plain/cell with a value no number reads from: \"\\\"2026-03-15\\\"\"",
		"dup: declared more than once",
		"bad-id: id is outside the reference grammar",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("no cause line matched %q; got:\n%s", want, joined)
		}
	}
}

// The raw value is the whole point: without it the log says a figure was unreadable
// and not what made it unreadable, which is the difference between "the model is
// citing names and dates" and "something is wrong with figures".
func TestUnresolvedCauses_CarriesTheRawValueItCouldNotRead(t *testing.T) {
	declared := []models.Figure{
		{ID: "f4", Unit: "plain", Kind: "cell", ValueMissing: true, ValueRaw: `"ACME HOLDING SANAYI VE TICARET A.S."`},
	}
	lines, _ := unresolvedCauses(declared, []string{"f4"})
	if len(lines) != 1 {
		t.Fatalf("got %d cause lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "ACME HOLDING") {
		t.Errorf("the cause line does not carry what the model sent, so the log cannot "+
			"say why it was unreadable; got %q", lines[0])
	}
}

// A reference repeated across name, description and indicators is one defect, not
// three, and a log that lists it three times reads as a worse problem than it is.
func TestUnresolvedCauses_ReportsEachIDOnce(t *testing.T) {
	declared := []models.Figure{{ID: "f1", Unit: "plain", Kind: "cell", ValueMissing: true, ValueRaw: `"x"`}}
	lines, unreadable := unresolvedCauses(declared, []string{"f1", "f1", "f1"})
	if len(lines) != 1 {
		t.Errorf("got %d cause lines for one id referenced three times, want 1: %v", len(lines), lines)
	}
	if unreadable != 1 {
		t.Errorf("value-unreadable count = %d for one bad figure, want 1", unreadable)
	}
}
