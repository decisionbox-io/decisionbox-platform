package discovery

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// What this pins, from a live run on the default Bedrock model:
//
// Eight recommendations shipped with an empty `actions` array, having written the
// same steps as a numbered list inside the description instead. The raw response
// carried no `actions` key at all. Two runs earlier, the same model on the same
// warehouse filled `actions` twenty-seven times.
//
// The schema requires `actions` with at least one item (#464), and that did not
// save it: on the Anthropic wire a ResponseFormat becomes a forced tool's input
// schema, which is validated best-effort rather than enforced at decode the way
// a llama.cpp grammar is. So on this wire the prompt is the only thing asking,
// and the prompt disagreed with itself -- the prose named `actions` as a field
// references are written into, and the worked example beside it showed a
// recommendation made of `title`, `description` and `figures`.
//
// A model copies the shape it is shown. These tests make the two halves of the
// contract agree, so the example cannot quietly stop demonstrating a field the
// prose promises.

// reContractFields reads the field names out of the sentence that says where
// references are substituted: "...writes the checked value into `a`, `b` and `c`
// before anyone reads them."
var reContractFields = regexp.MustCompile("writes the checked value into ([^.]*?) before anyone reads them")

var reBackticked = regexp.MustCompile("`([a-z_]+)`")

// contractProseFields is the set of fields the contract tells the model carry
// references.
func contractProseFields(t *testing.T) []string {
	t.Helper()
	m := reContractFields.FindStringSubmatch(recommendationFigureContract)
	if m == nil {
		t.Fatal("the contract no longer says where the checked value is written; " +
			"this test reads that sentence to learn which fields the example must show")
	}
	var fields []string
	for _, f := range reBackticked.FindAllStringSubmatch(m[1], -1) {
		fields = append(fields, f[1])
	}
	if len(fields) == 0 {
		t.Fatalf("no field names in %q", m[1])
	}
	sort.Strings(fields)
	return fields
}

// workedExample returns the contract's JSON example, wrapped into an object. The
// example is written as a fragment -- the keys of a recommendation, without the
// braces -- because it is showing part of a larger object.
func workedExample(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	const fence = "```json\n"
	i := strings.Index(recommendationFigureContract, fence)
	if i < 0 {
		t.Fatal("the contract has no json example")
	}
	rest := recommendationFigureContract[i+len(fence):]
	j := strings.Index(rest, "```")
	if j < 0 {
		t.Fatal("the contract's json example is not closed")
	}
	body := strings.TrimRight(strings.TrimSpace(rest[:j]), ",")

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte("{"+body+"}"), &obj); err != nil {
		t.Fatalf("the worked example is not parseable as a recommendation fragment: %v\n%s", err, body)
	}
	return obj
}

func TestTheWorkedExampleShowsEveryFieldTheContractSaysCarriesAReference(t *testing.T) {
	prose := contractProseFields(t)
	example := workedExample(t)

	for _, field := range prose {
		if _, ok := example[field]; !ok {
			t.Errorf("the contract says a reference in %q is substituted, and the worked example "+
				"does not show %q -- a model copies the shape it is shown", field, field)
		}
	}
}

func TestTheWorkedExampleReferencesEveryFigureItDeclares(t *testing.T) {
	// An example that declares a figure nothing points at demonstrates the one
	// thing the renderer refuses: a figure is only rendered where the prose
	// references it, and an unused declaration is counted as waste.
	example := workedExample(t)

	var figures []struct{ ID string }
	if raw, ok := example["figures"]; ok {
		if err := json.Unmarshal(raw, &figures); err != nil {
			t.Fatalf("the example's figures array does not decode: %v", err)
		}
	}
	if len(figures) == 0 {
		t.Fatal("the worked example declares no figures, which is the one thing it exists to show")
	}

	prose := string(recommendationFigureContract)
	for _, f := range figures {
		if !strings.Contains(prose, "{{"+f.ID+"}}") {
			t.Errorf("the example declares figure %q and no field references {{%s}}", f.ID, f.ID)
		}
	}
}

func TestEveryReferenceInTheExampleIsADeclaredFigure(t *testing.T) {
	// The mirror: a reference the example does not declare is the unresolved
	// case, which ships "{{f9}}" into a sentence. The example must not model it.
	example := workedExample(t)

	declared := map[string]bool{}
	var figures []struct{ ID string }
	if raw, ok := example["figures"]; ok {
		_ = json.Unmarshal(raw, &figures)
	}
	for _, f := range figures {
		declared[f.ID] = true
	}

	for field, raw := range example {
		if field == "figures" {
			continue
		}
		for _, ref := range reFigureRef.FindAllStringSubmatch(string(raw), -1) {
			if !declared[ref[1]] {
				t.Errorf("the example's %q references {{%s}}, which it never declares", field, ref[1])
			}
		}
	}
}
