package discovery

import (
	"fmt"
	"sort"
	"testing"
)

// recommendationItemSchema returns the per-recommendation item subschema, the
// one the grammar applies to every element of the array.
func recommendationItemSchema(t *testing.T) map[string]interface{} {
	t.Helper()
	root := recommendationResponseSchema()
	props, ok := root["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("recommendation schema has no properties")
	}
	arr, ok := props["recommendations"].(map[string]interface{})
	if !ok {
		t.Fatal("recommendation schema has no recommendations property")
	}
	item, ok := arr["items"].(map[string]interface{})
	if !ok {
		t.Fatal("recommendations array declares no items subschema")
	}
	return item
}

// TestRecommendationItemRequiresTheFieldsARecommendationNeeds is the second
// half of the lesson the runaway title taught, and it was hidden by the first.
//
// With the string bounds in place the recommendation phase stopped running
// away and started completing -- and produced three recommendations that were
// a title and nothing else: description empty, actions nil, priority unset,
// expected_impact an empty object. The grammar permitted it, because the item
// required exactly one field.
//
// A grammar is the only layer a small model reliably honours. The pack prompt
// asks for these fields in prose and the discipline rules reference them, but
// prose is advisory to a 27B model in a way a grammar is not: the one field it
// emitted correctly across three recommendations was the one field `required`
// named. So anything a recommendation is meaningless without belongs here.
func TestRecommendationItemRequiresTheFieldsARecommendationNeeds(t *testing.T) {
	// Each entry says what a recommendation missing the field would be, so a
	// later edit that drops one has to disagree with the reason rather than
	// with a list.
	needed := map[string]string{
		"title":               "a recommendation with no title cannot be listed",
		"description":         "a title with no description is a headline, not a recommendation",
		"priority":            "the discipline rules route urgency into this field instead of the prose, so prose without it carries no urgency at all",
		"expected_impact":     "a recommendation that names no expected impact cannot be ranked against another",
		"actions":             "a recommendation with no action is an observation",
		"related_insight_ids": "#347: the citation a small model otherwise omits",
	}

	item := recommendationItemSchema(t)
	required := map[string]bool{}
	for _, v := range toStringSlice(item["required"]) {
		required[v] = true
	}

	var missing []string
	for field := range needed {
		if !required[field] {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)
	for _, field := range missing {
		t.Errorf("recommendation item does not require %q: %s", field, needed[field])
	}
}

// TestEveryRequiredFieldIsADeclaredProperty catches the typo that makes a
// `required` entry do nothing.
//
// A name in `required` that no property declares is not an error in any JSON
// Schema implementation we send to -- it either constrains nothing or, on a
// strict grammar backend, demands a field the model was never told about.
// Either way the protection silently is not there, which is the same class of
// silence as a bound above grammarRepetitionCeiling.
func TestEveryRequiredFieldIsADeclaredProperty(t *testing.T) {
	for _, problem := range findUndeclaredRequired(recommendationResponseSchema(), "recommendation") {
		t.Errorf("%s", problem)
	}
}

// TestRequiredFieldsCannotBeSatisfiedEmpty closes the gap `required` leaves
// open on its own.
//
// `required` makes a key present; it says nothing about the value. A grammar
// is perfectly happy with `"description": ""` and with `"actions": []`, and
// both are indistinguishable downstream from the missing field requiring them
// was meant to prevent -- run 16 persisted exactly that shape for every field
// but one.
//
// Walked rather than listed, and at every level, because the same hole is
// repeated inside the schema: an `expected_impact` object that is present and
// empty ranks no better than an absent one, and a declared figure with an
// empty id or no refs names nothing. A field worth requiring is worth
// requiring content for.
//
// Only required fields. An optional string the model omits is fine, and
// demanding a minimum on one would make omission the cheaper path.
func TestRequiredFieldsCannotBeSatisfiedEmpty(t *testing.T) {
	for _, problem := range findEmptySatisfiableRequired(recommendationResponseSchema(), "recommendation") {
		if _, allowed := emptyByDesign[problem.field]; allowed {
			continue
		}
		t.Errorf("%s", problem.message)
	}

	// And in the other direction: an exception that stops being reachable is an
	// exception that has quietly become a rule nobody checks.
	reported := map[string]bool{}
	for _, problem := range findEmptySatisfiableRequired(recommendationResponseSchema(), "recommendation") {
		reported[problem.field] = true
	}
	for field, why := range emptyByDesign {
		if !reported[field] {
			t.Errorf("%s is recorded in emptyByDesign (%s) but no longer satisfiable empty -- "+
				"drop the exception", field, why)
		}
	}
}

// emptyByDesign records the required fields a grammar may legitimately satisfy
// with an empty value, and why. One entry, and the reason is the whole point:
// a run that found nothing worth recommending must be able to say so. A
// minItems here would make the model invent a recommendation to satisfy the
// grammar, which is the opposite of what every other entry in this file is
// for.
var emptyByDesign = map[string]string{
	"recommendation.recommendations": "a run may legitimately produce no recommendations; " +
		"demanding one would make the model invent it",
}

// findEmptySatisfiableRequired walks a schema and reports every required field
// a grammar could satisfy with an empty value: a string with no minLength, or
// an array with no minItems.
type emptySatisfiable struct {
	field   string
	message string
}

func findEmptySatisfiableRequired(node interface{}, path string) []emptySatisfiable {
	m, ok := node.(map[string]interface{})
	if !ok {
		var out []emptySatisfiable
		if arr, isArr := node.([]interface{}); isArr {
			for i, v := range arr {
				out = append(out, findEmptySatisfiableRequired(v, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
		return out
	}

	var out []emptySatisfiable
	if req, present := m["required"]; present {
		props, _ := m["properties"].(map[string]interface{})
		names := toStringSlice(req)
		sort.Strings(names)
		for _, name := range names {
			prop, declared := props[name].(map[string]interface{})
			if !declared {
				continue // reported by TestEveryRequiredFieldIsADeclaredProperty
			}
			field := path + "." + name
			switch typ, _ := prop["type"].(string); typ {
			case "string":
				if n, ok := prop["minLength"].(int); !ok || n < 1 {
					out = append(out, emptySatisfiable{field, fmt.Sprintf("%s is required but has no "+
						"minLength, so a grammar satisfies it with \"\" -- the empty field requiring it "+
						"was meant to prevent", field)})
				}
			case "array":
				if n, ok := prop["minItems"].(int); !ok || n < 1 {
					out = append(out, emptySatisfiable{field, fmt.Sprintf("%s is required but has no "+
						"minItems, so a grammar satisfies it with [] -- the empty field requiring it "+
						"was meant to prevent", field)})
				}
			case "object":
				// A required object that requires nothing of its own is the same
				// hole one level down: present, empty, and indistinguishable from
				// absent. run 16 shipped expected_impact as {"metric":"",
				// "estimated_improvement":""} for exactly this reason.
				if len(toStringSlice(prop["required"])) == 0 {
					out = append(out, emptySatisfiable{field, fmt.Sprintf("%s is a required object that "+
						"requires none of its own properties, so a grammar satisfies it with {} -- "+
						"name the properties it is worthless without", field)})
				}
			}
		}
	}

	if props, isMap := m["properties"].(map[string]interface{}); isMap {
		for k, v := range props {
			out = append(out, findEmptySatisfiableRequired(v, path+"."+k)...)
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not", "anyOf", "oneOf", "allOf"} {
		if v, present := m[key]; present {
			out = append(out, findEmptySatisfiableRequired(v, path+"."+key)...)
		}
	}
	return out
}

// findUndeclaredRequired walks a schema and reports every `required` entry
// that names no property of the object declaring it.
func findUndeclaredRequired(node interface{}, path string) []string {
	m, ok := node.(map[string]interface{})
	if !ok {
		var out []string
		if arr, isArr := node.([]interface{}); isArr {
			for i, v := range arr {
				out = append(out, findUndeclaredRequired(v, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
		return out
	}

	var out []string
	if req, present := m["required"]; present {
		props, _ := m["properties"].(map[string]interface{})
		for _, name := range toStringSlice(req) {
			if _, declared := props[name]; !declared {
				out = append(out, fmt.Sprintf("%s: required names %q, which is not a declared property, "+
					"so the requirement constrains nothing", path, name))
			}
		}
	}

	if props, isMap := m["properties"].(map[string]interface{}); isMap {
		for k, v := range props {
			out = append(out, findUndeclaredRequired(v, path+"."+k)...)
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not", "anyOf", "oneOf", "allOf"} {
		if v, present := m[key]; present {
			out = append(out, findUndeclaredRequired(v, path+"."+key)...)
		}
	}
	return out
}

// toStringSlice reads a schema keyword that holds a list of names.
func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, isStr := e.(string); isStr {
			out = append(out, s)
		}
	}
	return out
}
