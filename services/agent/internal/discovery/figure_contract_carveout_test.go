package discovery

import (
	"strings"
	"testing"
)

// What this pins, from a live run on a Turkish warehouse:
//
// Two insights in one run shipped the literal text "{{window}}" to the reader.
// The token appears nowhere in any prompt -- the only references the model was
// ever shown are {{f1}}, {{f2}} and {{f3}} from the contract's own examples -- so
// it was constructed, and both insights that wrote it declared f1, f2 and f3 and
// had no figure for a window.
//
// It was constructed because the prompt asked for something it then forbade. The
// insight rules require a superlative claim to "name the exact data window it is
// derived from, in the same sentence", and this contract ended with "every count,
// share, amount and average you report -- is an insight's figure, so reference
// it". A date window is a value, no `kind` declares one, and the only way to obey
// both instructions is a reference that cannot exist.
//
// The recommendation contract had the answer already: a section saying what stays
// as ordinary text, naming a threshold, a window and a band as things the model is
// setting rather than reading. The insight contract had no such section at all,
// so the two halves of the same format disagreed about the same question.

// carveoutMarkers are the things a contract must name as staying in the prose.
var carveoutMarkers = []string{"window", "threshold"}

func TestBothFigureContractsSayWhatStaysAsOrdinaryText(t *testing.T) {
	for name, contract := range map[string]string{
		"insight contract":        figureContract,
		"recommendation contract": recommendationFigureContract,
	} {
		lower := strings.ToLower(contract)
		if !strings.Contains(lower, "ordinary text") {
			t.Errorf("the %s does not say what stays as ordinary text, so everything in it "+
				"reads as something to declare or reference", name)
			continue
		}
		for _, marker := range carveoutMarkers {
			if !strings.Contains(lower, marker) {
				t.Errorf("the %s does not name a %s among the things that stay in the prose; "+
					"a value the rules require and the format cannot express is what produced "+
					"a literal {{window}} in shipped text", name, marker)
			}
		}
	}
}

func TestTheInsightContractDoesNotEndByDemandingEverythingBeAFigure(t *testing.T) {
	// The ordering is the instruction. A carve-out that sits above an unqualified
	// "everything else is a figure" is a carve-out the last line takes back.
	lower := strings.ToLower(figureContract)
	carve := strings.Index(lower, "ordinary text")
	if carve < 0 {
		t.Skip("covered by the test above")
	}
	for _, absolute := range []string{"every count, share, amount and average"} {
		if at := strings.Index(lower, absolute); at >= 0 && at > carve {
			t.Errorf("the insight contract repeats %q after its carve-out, which reads as "+
				"withdrawing it", absolute)
		}
	}
}
