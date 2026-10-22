package discovery

import (
	"fmt"
	"sort"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// TestEverySchemaStringIsBounded closes the class rather than its instances.
//
// Four schemas each carried their own `str := func(desc string)` helper, and
// all four therefore described strings a decode-time grammar would let run
// forever. One of them did: a title string of about 25,000 characters that
// spent an entire output budget and never closed, three runs in a row, while
// the grammar was being enforced the whole time and permitted every token.
//
// Listing the fields a test knows about would only ever catch the fields
// someone remembered, so this walks each schema we actually send and fails by
// path. A string is acceptable when it is bounded by length or restricted to an
// enum -- an enum cannot run away, because the grammar admits only the listed
// values.
func TestEverySchemaStringIsBounded(t *testing.T) {
	schemas := map[string]map[string]interface{}{
		"recommendation": recommendationResponseSchema(),
		"insight":        insightResponseSchema(),
		"questions":      questionsResponseSchema(),
	}
	// The reflection schema is shaped by the project's evolution mode, and the
	// mode decides which branches are even present -- so every mode is walked,
	// or a branch only one of them emits would go unchecked.
	for _, mode := range []agentplugin.EvolutionMode{
		agentplugin.EvolutionModeOff,
		agentplugin.EvolutionModeSuggestOnly,
		agentplugin.EvolutionModeAdminApproval,
		agentplugin.EvolutionModeAuto,
	} {
		for _, rejudge := range []bool{false, true} {
			schemas[fmt.Sprintf("reflection[%s,rejudge=%v]", mode, rejudge)] =
				reflectionResponseSchema(mode, rejudge)
		}
	}

	for name, schema := range schemas {
		unbounded := findUnboundedStrings(schema, name)
		sort.Strings(unbounded)
		for _, path := range unbounded {
			t.Errorf("%s: %s is a string with neither maxLength nor enum, so a grammar lets it run "+
				"forever; use boundedString with a measured bound, or an enum", name, path)
		}
	}
}

// TestSchemaBoundsClearWhatModelsActuallyWrite states the evidence the bounds
// were chosen from, in code, so a later edit that tightens one has to argue
// with the measurement rather than with a comment.
//
// The maxima are from this pipeline's own output: 91 recommendations, 407
// insights, 60 clarifying questions, and the longest analysis-area prompt
// shipped in a domain pack. A bound is only doing its job if it is far above
// all of them -- it exists to stop a runaway, never to shape a result.
func TestSchemaBoundsClearWhatModelsActuallyWrite(t *testing.T) {
	observed := []struct {
		what  string
		max   int
		bound int
	}{
		{"recommendation title (109) / insight name (126)", 126, maxSchemaTitleRunes},
		{"recommendation description (1191) / insight description (1567)", 1567, maxSchemaDescriptionRunes},
		{"expected_impact.reasoning (188)", 188, maxSchemaSentenceRunes},
		{"an action string (178)", 178, maxSchemaListItemRunes},
		{"target_segment (194)", 194, maxSchemaSegmentRunes},
		{"expected_impact.metric (63)", 63, maxSchemaLabelRunes},
		{"a clarifying question (305)", 305, maxSchemaQuestionRunes},
		{"the longest packaged analysis-area prompt (6777)", 6777, maxSchemaPromptRunes},
	}
	const wantHeadroom = 2 // never less than twice the largest thing ever seen
	for _, o := range observed {
		if o.bound < o.max*wantHeadroom {
			t.Errorf("%s: bound %d gives less than %dx headroom over the observed maximum %d; "+
				"a bound that can bind on a real answer is a bug in the bound",
				o.what, o.bound, wantHeadroom, o.max)
		}
	}
}

// findUnboundedStrings walks a JSON Schema and returns the path of every string
// field that is neither length-bounded nor enum-restricted.
func findUnboundedStrings(node interface{}, path string) []string {
	m, ok := node.(map[string]interface{})
	if !ok {
		var out []string
		if arr, isArr := node.([]interface{}); isArr {
			for i, v := range arr {
				out = append(out, findUnboundedStrings(v, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
		return out
	}

	var out []string
	if t, _ := m["type"].(string); t == "string" {
		_, bounded := m["maxLength"]
		_, enumerated := m["enum"]
		if !bounded && !enumerated {
			out = append(out, path)
		}
	}

	// Recurse through the places a schema nests: properties, items, and the
	// composition keywords. Keyed explicitly rather than over every value, so
	// a description that happens to be a map cannot be mistaken for a schema.
	if props, isMap := m["properties"].(map[string]interface{}); isMap {
		for k, v := range props {
			out = append(out, findUnboundedStrings(v, path+"."+k)...)
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not"} {
		if v, present := m[key]; present {
			out = append(out, findUnboundedStrings(v, path+"."+key)...)
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if v, present := m[key]; present {
			out = append(out, findUnboundedStrings(v, path+"."+key)...)
		}
	}
	return out
}
