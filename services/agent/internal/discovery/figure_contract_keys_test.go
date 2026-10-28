package discovery

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// What this file pins: the contract's instruction is positive, its examples use keys that
// name what they hold, and an unreferenced figure is stated to be a defect.
//
// Each one is here because of a measurement rather than a preference.

// positiveLead is the clause that has to come first. The file's own header says the
// instruction carrying the most weight is the first one, and for most of this layer's life
// that was a prohibition -- "never type a number" -- which says where a number must not go
// and never says where it goes.
const positiveLead = "lives in `figures`"

func TestTheContractLeadsWithWhereANumberGoes(t *testing.T) {
	// Position, not presence. Both clauses are wanted: the rule, then the reason it is not
	// optional. A test for presence alone would pass with them in either order, and the
	// order is the whole point.
	lead := strings.Index(figureContract, positiveLead)
	if lead < 0 {
		t.Fatalf("the insight contract no longer says a number %q, so it never says where one goes", positiveLead)
	}
	prohibition := strings.Index(figureContract, "Never type a number")
	if prohibition < 0 {
		t.Fatal("the insight contract no longer says never to type a number; the positive rule " +
			"needs the reason beside it, or a model has no cost for ignoring it")
	}
	if prohibition < lead {
		t.Errorf("the prohibition at %d comes before the positive rule at %d: the first instruction "+
			"is the one that carries, and a contract that opens by forbidding has still not said "+
			"where a number belongs", prohibition, lead)
	}
}

// TestTheContractSaysAnUnreferencedFigureIsADefect covers the cause that ships nothing a
// reader can see, which is why it went unnoticed while three others were being fixed.
//
// Measured across the lab runs: the small self-hosted model declared 32 figures its own
// prose never referenced -- 13% of every figure it declared, touching 7 of 47 insights --
// while the large model declared none spare. Neither contract said anything about it.
func TestTheContractSaysAnUnreferencedFigureIsADefect(t *testing.T) {
	lower := strings.ToLower(figureContract)
	for _, clause := range []string{"reference every figure you declare", "nothing references"} {
		if !strings.Contains(lower, strings.ToLower(clause)) {
			t.Errorf("the insight contract does not say %q, so a model over-declaring figures is "+
				"never told that is wrong", clause)
		}
	}
}

// reContractRef matches a reference as the contract writes it, so the keys the model is
// shown can be checked against the grammar the resolver actually accepts.
var reContractRef = regexp.MustCompile(`\{\{([^{}]*)\}\}`)

func TestEveryReferenceShownInAContractIsAResolvableKey(t *testing.T) {
	// figureIDPattern is what usableFigures enforces at runtime. A contract that shows a
	// key shape the resolver refuses is teaching a reference that cannot resolve, and the
	// failure arrives as braces in shipped text rather than as an error.
	for name, contract := range map[string]string{
		"insight contract":        figureContract,
		"recommendation contract": recommendationFigureContract,
	} {
		for _, m := range reContractRef.FindAllStringSubmatch(contract, -1) {
			if !figureIDPattern.MatchString(m[1]) {
				t.Errorf("the %s shows %q, which figureIDPattern refuses: a reference the resolver "+
					"cannot read ships to a reader as written", name, m[0])
			}
		}
	}
}

// rePositionalKey matches a key that numbers a figure instead of naming it.
var rePositionalKey = regexp.MustCompile(`^f\d+$`)

func TestTheWorkedExamplesNameTheirKeys(t *testing.T) {
	// A proxy, and worth being honest about what it can and cannot see: no test can judge
	// whether a key is well named. It can see the shape that carries no meaning at all --
	// f1, f2, f12 -- which is what both examples used, and what a model imitating them
	// produced. Everything this layer buys depends on a reference naming the right figure,
	// and a positional key is the one shape that gives a model nothing to check that against.
	for name, keys := range map[string][]string{
		"insight example":        insightExampleFigureIDs(t),
		"recommendation example": recommendationExampleFigureIDs(t),
	} {
		if len(keys) == 0 {
			t.Errorf("%s: no figure ids found, so this test is checking nothing", name)
		}
		for _, k := range keys {
			if rePositionalKey.MatchString(k) {
				t.Errorf("%s declares %q: a positional key is what the measured runs imitated, and "+
					"it tells a model nothing about which figure a sentence meant", name, k)
			}
		}
	}
}

// TestTheInsightWorkedExampleIsInternallyConsistent is the insight-side counterpart of the
// recommendation example tests, which until now covered one contract of the two.
//
// The example is the only part of this text a model reliably imitates, so an example whose
// prose references a figure it does not declare teaches exactly the defect this layer
// exists to stop -- and nothing was checking it.
func TestTheInsightWorkedExampleIsInternallyConsistent(t *testing.T) {
	example := insightWorkedExample(t)

	var figures []struct {
		ID string `json:"id"`
	}
	if raw, ok := example["figures"]; ok {
		if err := json.Unmarshal(raw, &figures); err != nil {
			t.Fatalf("the insight example's figures array does not parse: %v", err)
		}
	} else {
		t.Fatal("the insight example declares no figures")
	}

	prose := insightExampleProse(t, example)
	declared := map[string]bool{}
	for _, f := range figures {
		declared[f.ID] = true
		if !strings.Contains(prose, "{{"+f.ID+"}}") {
			t.Errorf("the insight example declares %q and never references it, which is the "+
				"over-declaration the contract now tells the model not to do", f.ID)
		}
	}
	for _, m := range reContractRef.FindAllStringSubmatch(prose, -1) {
		if !declared[m[1]] {
			t.Errorf("the insight example's prose references %q, which it never declares: the "+
				"example itself would ship braces to a reader", m[0])
		}
	}
}

// insightWorkedExample returns the insight contract's JSON example, wrapped into an object.
// Written as a fragment -- the keys of an insight, without the braces -- the same way the
// recommendation example is, because it shows part of a larger object.
func insightWorkedExample(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	const fence = "```json\n"
	i := strings.Index(figureContract, fence)
	if i < 0 {
		t.Fatal("the insight contract has no json example")
	}
	rest := figureContract[i+len(fence):]
	j := strings.Index(rest, "```")
	if j < 0 {
		t.Fatal("the insight contract's json example is not closed")
	}
	body := strings.TrimRight(strings.TrimSpace(rest[:j]), ",")

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+body+"}"), &obj); err != nil {
		t.Fatalf("the insight worked example is not parseable as an insight fragment: %v\n%s", err, body)
	}
	return obj
}

// insightExampleProse joins the example's reference-carrying fields.
func insightExampleProse(t *testing.T, example map[string]json.RawMessage) string {
	t.Helper()
	var parts []string
	for _, key := range []string{"name", "description"} {
		raw, ok := example[key]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("the insight example's %q does not parse as a string: %v", key, err)
		}
		parts = append(parts, s)
	}
	if len(example["indicators"]) > 0 {
		var ind []string
		if err := json.Unmarshal(example["indicators"], &ind); err == nil {
			parts = append(parts, ind...)
		}
	}
	return strings.Join(parts, " ")
}

func insightExampleFigureIDs(t *testing.T) []string {
	t.Helper()
	return exampleFigureIDs(t, insightWorkedExample(t), "insight")
}

func recommendationExampleFigureIDs(t *testing.T) []string {
	t.Helper()
	return exampleFigureIDs(t, workedExample(t), "recommendation")
}

func exampleFigureIDs(t *testing.T, example map[string]json.RawMessage, which string) []string {
	t.Helper()
	raw, ok := example["figures"]
	if !ok {
		t.Fatalf("the %s example declares no figures", which)
	}
	var figures []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &figures); err != nil {
		t.Fatalf("the %s example's figures array does not parse: %v", which, err)
	}
	out := make([]string, 0, len(figures))
	for _, f := range figures {
		out = append(out, f.ID)
	}
	return out
}

// TestTheContractSaysAHeadlineMayOnlyNameADeclaredFigure covers the sentence that reaches
// for a number the rest of the finding did not need.
//
// Measured in run 23, the one token that reached a reader in an otherwise clean round: an
// insight declared seven figures, referenced all seven, and led its `name` with an overdue
// total it never declared. Every operand for that sum was present and the sum was not. The
// contract had a rule about declaring figures the sentences use and a rule about using
// every figure declared, and neither covers a headline naming something that does not exist.
//
// Both contracts, because the penalty differs and a model told only the insight-side version
// has been told the cheaper one: an insight ships its braces to a reader, a recommendation
// is dropped whole.
func TestTheContractSaysAHeadlineMayOnlyNameADeclaredFigure(t *testing.T) {
	for name, contract := range map[string]string{
		"insight contract":        figureContract,
		"recommendation contract": recommendationFigureContract,
	} {
		lower := strings.ToLower(contract)
		if !strings.Contains(lower, "headline may only name a figure you declared") {
			t.Errorf("the %s does not say a headline may only name a declared figure, so the one "+
				"sentence that introduces a number the rest of the finding never needed is "+
				"ungoverned", name)
		}
	}
	// The insight side has to say where an undeclared reference ends up, because nothing
	// drops it: the reader sees the braces. Without that the rule reads as a style note.
	if !strings.Contains(strings.ToLower(figureContract), "reaches the reader as braces") {
		t.Error("the insight contract states the headline rule without its consequence; an " +
			"insight carrying an unresolved reference is not dropped, it ships")
	}
	// The recommendation side has to say the opposite consequence for the same reason.
	if !strings.Contains(strings.ToLower(recommendationFigureContract), "dropped rather than") {
		t.Error("the recommendation contract states the headline rule without its consequence; " +
			"a recommendation carrying an unresolved reference is dropped whole, which is a " +
			"different and larger cost than the insight side's")
	}
}
