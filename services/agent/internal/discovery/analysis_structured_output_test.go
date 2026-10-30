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

// requiredByDesign is, for each object the insight schema describes, the fields it cannot
// lack and the fields it must not require. Both directions matter: a schema sent on every
// call that obliges nothing permits an empty answer, and one that obliges too much forces a
// model to invent a field the kind does not use.
//
// The shares are measured over what this pipeline has actually written -- 562 insights and
// the 439 quantifier claims inside them -- so a change here has to argue with a number.
// requiredByDesign is, for each object the insight schema describes, the fields it cannot
// lack, the fields that may arrive empty, and the fields it must not require.
//
// Three categories because `required` is doing two jobs. One is the obvious one: an insight
// without a description is not a finding. The other is ordering -- a grammar emits every
// required property before any optional one, so a field that has to reach the model before
// the prose must be required whatever the struct order says. `figures` is in the set for
// that reason, and may legitimately be empty.
//
// The shares are measured over what this pipeline has written: 562 insights and the 439
// quantifier claims inside them. A change here has to argue with a number.
var requiredByDesign = []struct {
	object       string
	required     map[string]string // field -> evidence
	emptyAllowed map[string]string // required for order, but [] is a legitimate answer
	forbidden    map[string]string // field -> why requiring it would be wrong
}{
	{
		object: "insight",
		required: map[string]string{
			"name":         "562 of 562",
			"description":  "562 of 562",
			"severity":     "562 of 562",
			"source_steps": "562 of 562",
			"figures":      "required to keep it in front of the prose; see emptyAllowed",
			"indicators":   "561 of 562, and likewise before the prose",
		},
		emptyAllowed: map[string]string{
			"figures":    "a finding that states no number declares no figure; forcing one would be worse than the ordering bug it is here to fix",
			"indicators": "an indicator with no number in it needs no declaration, and the contract says so",
		},
		forbidden: map[string]string{
			"quantifier_claims": "61% -- it quotes prose that does not exist yet when the evidence is written, so optional and last is correct",
		},
	},
	{
		// The same defect one level down, and sharper: an incomplete claim is marked
		// undecidable by EvaluateQuantifierClaims, and undecidable does not reach repair,
		// so the claim is lost rather than corrected.
		object: "quantifier_claims",
		required: map[string]string{
			"claim": "439 of 439",
			"kind":  "439 of 439",
			"step":  "439 of 439",
		},
		forbidden: map[string]string{
			"column":   "62% -- only the kinds that rank or assert a direction use it",
			"subject":  "51% -- a rank claim's row, meaningless for the others",
			"rank":     "50% -- likewise",
			"filter":   "37%",
			"count":    "26% -- required for a cardinality claim and wrong for the rest",
			"trend":    "1.4% -- monotonic only",
			"order_by": "1.4% -- monotonic only",
		},
	},
	{
		// Pre-existing: this object had its required list before the schema was ever
		// attached, which is why it was already right.
		object: "figures",
		required: map[string]string{
			"id":    "the key the prose names",
			"value": "the number itself",
			"step":  "the rows the arithmetic runs over",
			"kind":  "which arithmetic to run",
		},
		forbidden: map[string]string{
			"currency": "set only on a currency figure",
			"scale":    "notation, omitted to write the number in full",
			"other":    "a diff's right operand; absent for a cell or a sum",
		},
	},
}

func TestTheInsightSchemaRequiresWhatAnInsightCannotLack(t *testing.T) {
	// The schema required nothing while it was attached on a retry alone. Sending it on
	// every analysis call turns that into a contract that obliges no field: a
	// grammar-constrained model satisfies it with a name and no finding, the tolerant
	// parser accepts that, and nothing retries.
	for _, c := range requiredByDesign {
		obj := insightObjectSchema(t, c.object)
		required := map[string]bool{}
		for _, r := range toStringSlice(obj["required"]) {
			required[r] = true
		}
		if len(required) == 0 {
			t.Errorf("%s requires no field, so a model can satisfy it with an empty object and "+
				"the tolerant parser will accept it without a retry", c.object)
			continue
		}
		for field, evidence := range c.required {
			if !required[field] {
				t.Errorf("%s.%s is not required, and it was present in %s; a schema sent on every "+
					"call that does not oblige it permits an answer without one", c.object, field, evidence)
			}
		}
		for field, why := range c.forbidden {
			if required[field] {
				t.Errorf("%s.%s is required, which forces a model to invent one: %s",
					c.object, field, why)
			}
		}
	}
}

func TestEveryRequiredInsightFieldRejectsAnEmptyValue(t *testing.T) {
	// `required` makes a key present and says nothing about its value: a grammar satisfies
	// a required string with "" and a required array with []. That is documented on
	// nonEmptyString because it already happened once -- a run wrote a title, closed the
	// object, and every skipped field persisted as its zero value, which reads downstream
	// exactly like the absent field `required` was added to prevent.
	for _, c := range requiredByDesign {
		obj := insightObjectSchema(t, c.object)
		props := schemaProperties(obj["properties"])
		for _, field := range toStringSlice(obj["required"]) {
			node, ok := props[field].(map[string]interface{})
			if !ok {
				t.Errorf("%s.%s is required but the schema describes no such property", c.object, field)
				continue
			}
			if why, ok := c.emptyAllowed[field]; ok {
				// Required to hold its position in front of the prose, not because the
				// model always has something to put in it.
				if n, _ := node["minItems"].(int); n > 0 {
					t.Errorf("%s.%s carries minItems %d but is allowed to be empty: %s",
						c.object, field, n, why)
				}
				continue
			}
			switch node["type"] {
			case "string":
				if n, _ := node["minLength"].(int); n < 1 {
					t.Errorf("required string %s.%s has no minLength, so a grammar satisfies it with \"\"",
						c.object, field)
				}
			case "array":
				if n, _ := node["minItems"].(int); n < 1 {
					t.Errorf("required array %s.%s has no minItems, so a grammar satisfies it with []",
						c.object, field)
				}
			}
		}
	}
}

// insightObjectSchema returns one of the objects the insight envelope describes: the
// per-insight item itself, or the item of one of its arrays.
func insightObjectSchema(t *testing.T, object string) map[string]interface{} {
	t.Helper()
	items := insightItemSchemaFrom(insightResponseSchema())
	if items == nil {
		t.Fatal("the insight schema describes no insight item")
	}
	if object == "insight" {
		return items
	}
	itemProps := objectProperties(items)
	if itemProps == nil {
		t.Fatal("the insight item has no properties")
	}
	nested, ok := itemProps[object].(map[string]interface{})
	if !ok {
		t.Fatalf("the insight item has no %q property", object)
	}
	inner, ok := nested["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s describes no item", object)
	}
	return inner
}
