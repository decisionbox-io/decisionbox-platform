package discovery

import (
	"strings"
	"testing"
)

// What this file pins: the insight schema reaches the model on the call that writes the
// insights, the switch that turns that off exists, and every string the schema describes is
// bounded except the one field measured to have no bound available.

func TestTheFirstAnalysisCallCarriesTheSchemaByDefault(t *testing.T) {
	// Default on. The field descriptions are the schema's half of the numbers contract, and
	// a description on a call no model receives is not a contract -- which is what the
	// measurement found: across 189 recorded analysis steps exactly one ever took the
	// retry path the schema used to be attached to.
	t.Setenv(analysisStructuredOutputEnv, "")
	if !analysisStructuredOutputEnabled() {
		t.Error("the first analysis call does not carry the insight schema by default, so every " +
			"field description in it is text no model sees on the call that writes insights")
	}
}

func TestTheSchemaOnTheFirstAnalysisCallCanBeTurnedOff(t *testing.T) {
	// The escape hatch has to work, because this is the one change in this layer that
	// alters the shape of a call rather than its text, on the phase that was deliberately
	// left byte-identical for models already producing good results.
	for _, off := range []string{"false", "0", "FALSE"} {
		t.Setenv(analysisStructuredOutputEnv, off)
		if analysisStructuredOutputEnabled() {
			t.Errorf("%s=%q did not restore the plain first call; a deployment that regresses "+
				"would have to wait for an image", analysisStructuredOutputEnv, off)
		}
	}
}

// insightUnboundedByDesign is the single string the insight schema still leaves unbounded,
// and it is a measurement rather than a preference.
//
// Insight descriptions run p50 815, p95 1,146, p99 1,464, maximum 1,643 over the 562
// insights this pipeline has written. grammarRepetitionCeiling is 2,000 and a maxLength
// above it is silently clamped back to unbounded on an Ollama-backed runner, so the only
// bound that applies there is 1.22x the observed maximum -- a bound that can clip a real
// answer. No bound is the honest answer; a comfortable-looking 2,000 is not.
const insightUnboundedByDesign = "insight.insights.items.description"

func TestTheInsightSchemaBoundsEveryStringItCan(t *testing.T) {
	// The schema was unbounded throughout while it was attached on a corrective retry
	// alone, which was a recorded and reasonable risk: no grammar was applying to it. That
	// reasoning ended the moment it went on every analysis call, and this is the test that
	// records the new position -- every string bounded, one exception, named.
	unbounded := findUnboundedStrings(insightResponseSchema(), "insight")
	got := map[string]bool{}
	for _, path := range unbounded {
		got[path] = true
	}

	if !got[insightUnboundedByDesign] {
		t.Errorf("%s is now bounded. If a bound above %d was added it does nothing on an "+
			"Ollama-backed runner, and one below it can clip a description measured at 1,643 "+
			"runes; if the prompt now asks for less prose, re-measure and update the constant "+
			"and this test together", insightUnboundedByDesign, grammarRepetitionCeiling)
	}
	for path := range got {
		if path == insightUnboundedByDesign {
			continue
		}
		t.Errorf("%s is an unbounded string in a schema that is now sent on every analysis "+
			"call, so a grammar lets it run forever there; bound it with boundedString from a "+
			"measured maximum", path)
	}
}

// TestTheInsightSchemaStillDescribesWhatItAsksFor guards the mechanical risk in converting
// twenty field descriptions from an unbounded helper to bounded ones: a description lost in
// the edit is invisible, because a schema with no description is still a valid schema.
func TestTheInsightSchemaStillDescribesWhatItAsksFor(t *testing.T) {
	for path, desc := range schemaStringFields(t) {
		if !strings.HasPrefix(path, "insight.") {
			continue
		}
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description: a model is being asked for a field with nothing "+
				"said about it, and an empty description is not a schema error", path)
		}
	}
}

// insightRequiredFields are the fields an insight cannot lack, and the share of the 562
// insights this pipeline has written that carried each one.
//
// The number is the argument. A field a model omits legitimately cannot be required without
// forcing it to invent one, so `figures` (88%) and `quantifier_claims` (61%) are absent from
// this set on purpose: a finding that states no number declares no figure.
var insightRequiredFields = map[string]string{
	"name":         "562 of 562",
	"description":  "562 of 562",
	"severity":     "562 of 562",
	"source_steps": "562 of 562",
}

func TestTheInsightSchemaRequiresWhatAnInsightCannotLack(t *testing.T) {
	// The schema required nothing while it was attached on a retry alone. Sending it on
	// every analysis call turns that into a contract that obliges no field: a
	// grammar-constrained model satisfies it with a name and no finding, the tolerant
	// parser accepts that, and nothing retries.
	items := insightItemSchema(t)

	required := map[string]bool{}
	for _, r := range toStringSlice(items["required"]) {
		required[r] = true
	}
	if len(required) == 0 {
		t.Fatal("the insight item requires no field, so a model can satisfy the schema with an " +
			"empty object and the tolerant parser will accept it without a retry")
	}
	for field, evidence := range insightRequiredFields {
		if !required[field] {
			t.Errorf("%s is not required, and it was present in %s real insights; a schema sent "+
				"on every call that does not oblige it permits a finding without one", field, evidence)
		}
	}
	for _, overreach := range []string{"figures", "quantifier_claims"} {
		if required[overreach] {
			t.Errorf("%s is required, which forces a model to invent one: it was present in "+
				"88%% and 61%% of real insights respectively, because a finding that states no "+
				"number declares no figure", overreach)
		}
	}
}

func TestEveryRequiredInsightFieldRejectsAnEmptyValue(t *testing.T) {
	// `required` makes a key present and says nothing about its value: a grammar satisfies
	// a required string with "" and a required array with []. That is documented on
	// nonEmptyString because it already happened once -- a run wrote a title, closed the
	// object, and every skipped field persisted as its zero value, which reads downstream
	// exactly like the absent field `required` was added to prevent.
	items := insightItemSchema(t)
	props, _ := items["properties"].(map[string]interface{})
	for _, field := range toStringSlice(items["required"]) {
		node, ok := props[field].(map[string]interface{})
		if !ok {
			t.Errorf("%s is required but the schema describes no such property", field)
			continue
		}
		switch node["type"] {
		case "string":
			if n, _ := node["minLength"].(int); n < 1 {
				t.Errorf("required string %s has no minLength, so a grammar satisfies it with \"\"", field)
			}
		case "array":
			if n, _ := node["minItems"].(int); n < 1 {
				t.Errorf("required array %s has no minItems, so a grammar satisfies it with []", field)
			}
		}
	}
}

// insightItemSchema returns the per-insight object from the envelope.
func insightItemSchema(t *testing.T) map[string]interface{} {
	t.Helper()
	props, ok := insightResponseSchema()["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("the insight schema has no properties")
	}
	arr, ok := props["insights"].(map[string]interface{})
	if !ok {
		t.Fatal("the insight schema has no insights array")
	}
	items, ok := arr["items"].(map[string]interface{})
	if !ok {
		t.Fatal("the insights array describes no item")
	}
	return items
}
