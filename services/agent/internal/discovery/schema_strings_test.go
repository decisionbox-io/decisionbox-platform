package discovery

import (
	"fmt"
	"sort"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// TestEverySchemaStringIsBounded holds the whole recommendation schema rather
// than the one field that was observed running away.
//
// A title string of about 25,000 characters spent an entire output budget and
// never closed, three runs in a row, while the schema grammar was being
// enforced the whole time and permitted every token -- a grammar constrains
// structure and says nothing about string length.
//
// Listing the fields a test knows about would only ever catch the fields
// someone remembered, so this walks the schema and fails by path. A string is
// acceptable when it is bounded by length or restricted to an enum -- an enum
// cannot run away, because the grammar admits only the listed values.
//
// The insight, clarifying-question and reflection schemas are NOT walked. They
// describe unbounded strings through the same mechanism and the fix is the
// same, but they are producing results today and the failure has not been
// observed in them, so bounding them is a regression risk deliberately not
// taken here. They are named in unboundedSchemasByDesign below so the gap is
// recorded in code rather than remembered, and tracked in #462.
func TestEverySchemaStringIsBounded(t *testing.T) {
	schemas := map[string]map[string]interface{}{
		"recommendation": recommendationResponseSchema(),
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

// unboundedSchemasByDesign records the schemas that still describe unbounded
// strings, and why. Written down rather than remembered: the same `str :=
// func(desc string)` helper lives in each of them, and it is the pattern that
// produced the runaway in the first place, so a reader who copies it should
// find this note. Tracked in #462.
var unboundedSchemasByDesign = map[string]string{
	"insight":   "the analysis phase is producing insights today; bounding it is a regression risk for a failure not observed there",
	"questions": "same, and its output is short in practice (observed maximum 305 characters)",
	"reflection": "same, and its area-prompt field is the one bound hardest to size from evidence -- " +
		"the ledger collections this lab has are empty, so the only measurement available is the packaged prompts",
}

// TestSchemaBoundsClearWhatModelsActuallyWrite states the evidence the bounds
// were chosen from, in code, so a later edit that tightens one has to argue
// with the measurement rather than with a comment.
//
// The maxima are from the 91 recommendations this pipeline has written. A bound
// is only doing its job if it is far above all of them -- it exists to stop a
// runaway, never to shape a result.
func TestSchemaBoundsClearWhatModelsActuallyWrite(t *testing.T) {
	observed := []struct {
		what  string
		max   int
		bound int
		// headroom is the multiple of the observed maximum this bound must
		// clear. Two, except where something other than the evidence decides
		// the bound -- see description.
		headroom int
	}{
		{"recommendation title (109)", 109, maxSchemaTitleRunes, 2},
		// The one bound squeezed from both sides: the evidence puts a floor
		// under it at 1,191 and grammarRepetitionCeiling puts a hard lid on it
		// at 2,000, because a larger bound is silently ignored on an
		// Ollama-backed runner. 1.5x is what is left between them.
		//
		// This is the field to watch. If a model ever legitimately writes a
		// description past 2,000 characters, raising the bound will not help --
		// above the ceiling it stops applying there at all -- so the answer
		// would be to ask the prompt for less prose, or to accept the clip on
		// that backend deliberately.
		{"recommendation description (1191)", 1191, maxSchemaDescriptionRunes, 1},
		{"expected_impact.reasoning (188)", 188, maxSchemaSentenceRunes, 2},
		{"an action string (178)", 178, maxSchemaListItemRunes, 2},
		{"target_segment (194)", 194, maxSchemaSegmentRunes, 2},
		{"expected_impact.metric (63)", 63, maxSchemaLabelRunes, 2},
	}
	for _, o := range observed {
		// headroom 1 means "the evidence is not what decides this bound"; it
		// still has to clear the evidence, with half again on top.
		want := o.max * o.headroom
		if o.headroom == 1 {
			want = o.max + o.max/2
		}
		if o.bound < want {
			t.Errorf("%s: bound %d is under the %d this field needs over the observed maximum %d; "+
				"a bound that can bind on a real answer is a bug in the bound",
				o.what, o.bound, want, o.max)
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

// TestUnboundedSchemasAreStillTheKnownOnes keeps the recorded gap honest in
// both directions.
//
// A schema bounded later fails here until it is moved into the walked set in
// TestEverySchemaStringIsBounded, so the narrow scope cannot quietly outlive
// the reason for it. And a schema that acquires unbounded strings without
// being recorded fails here too, rather than joining the gap unnoticed --
// which is how one unbounded helper became four.
func TestUnboundedSchemasAreStillTheKnownOnes(t *testing.T) {
	// The reflection schema's richest shape: every branch its evolution mode,
	// its re-judgement flag and its finding-id flag can emit. All three are
	// set to the value that ADDS structure, because an unbounded string this
	// walk never reaches is one it cannot report -- and hasFindingIDs in
	// particular swaps the task-status item for two self-contained branches
	// carrying strings of their own.
	candidates := map[string]map[string]interface{}{
		"insight":    insightResponseSchema(),
		"questions":  questionsResponseSchema(),
		"reflection": reflectionResponseSchema(agentplugin.EvolutionModeAuto, true, true),
	}

	unbounded := map[string]int{}
	for name, schema := range candidates {
		if n := len(findUnboundedStrings(schema, name)); n > 0 {
			unbounded[name] = n
		}
	}

	for name, why := range unboundedSchemasByDesign {
		if _, still := unbounded[name]; !still {
			t.Errorf("%s no longer describes unbounded strings (recorded as: %s) — add it to the "+
				"walked set in TestEverySchemaStringIsBounded and drop it from "+
				"unboundedSchemasByDesign", name, why)
		}
	}
	for name, n := range unbounded {
		if _, recorded := unboundedSchemasByDesign[name]; !recorded {
			t.Errorf("%s describes %d unbounded string(s) and is not a recorded exception; bound them "+
				"with boundedString, or record why not", name, n)
		}
	}
}

// TestSchemaBoundsSurviveGrammarTranslation keeps every bound inside the
// largest one a llama.cpp grammar will honour.
//
// This is the failure mode a bound is least likely to be noticed in. On an
// Ollama-backed deployment a maxLength becomes `char{0,N}`, and llama.cpp
// clamps an upper repetition above MAX_REPETITION_THRESHOLD = 2000 straight
// back to unbounded -- before the rule is built, without an error and without a
// warning. A bound above the ceiling is not a stricter protection there; it is
// no protection, and the only way to find out is to watch a model run away
// again on a backend where the fix was supposed to hold.
func TestSchemaBoundsSurviveGrammarTranslation(t *testing.T) {
	bounds := map[string]int{
		"maxSchemaTitleRunes":       maxSchemaTitleRunes,
		"maxSchemaDescriptionRunes": maxSchemaDescriptionRunes,
		"maxSchemaSentenceRunes":    maxSchemaSentenceRunes,
		"maxSchemaListItemRunes":    maxSchemaListItemRunes,
		"maxSchemaSegmentRunes":     maxSchemaSegmentRunes,
		"maxSchemaLabelRunes":       maxSchemaLabelRunes,
		"maxSchemaIdentifierRunes":  maxSchemaIdentifierRunes,
	}
	for name, bound := range bounds {
		if bound > grammarRepetitionCeiling {
			t.Errorf("%s = %d exceeds grammarRepetitionCeiling (%d): on a llama.cpp-backed runner "+
				"this is silently clamped back to unbounded, so the bound does nothing there",
				name, bound, grammarRepetitionCeiling)
		}
	}

	// And the schema the agent actually sends, in case a field is written with
	// a literal rather than one of the named bounds.
	for _, path := range findOversizedBounds(recommendationResponseSchema(), "recommendation") {
		t.Errorf("recommendation: %s", path)
	}
}

// findOversizedBounds walks a schema and reports every maxLength above the
// ceiling a llama.cpp grammar honours.
func findOversizedBounds(node interface{}, path string) []string {
	m, ok := node.(map[string]interface{})
	if !ok {
		var out []string
		if arr, isArr := node.([]interface{}); isArr {
			for i, v := range arr {
				out = append(out, findOversizedBounds(v, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
		return out
	}

	var out []string
	if n, present := m["maxLength"].(int); present && n > grammarRepetitionCeiling {
		out = append(out, fmt.Sprintf("%s has maxLength %d, above the %d a llama.cpp grammar honours",
			path, n, grammarRepetitionCeiling))
	}
	if props, isMap := m["properties"].(map[string]interface{}); isMap {
		for k, v := range props {
			out = append(out, findOversizedBounds(v, path+"."+k)...)
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not", "anyOf", "oneOf", "allOf"} {
		if v, present := m[key]; present {
			out = append(out, findOversizedBounds(v, path+"."+key)...)
		}
	}
	return out
}
