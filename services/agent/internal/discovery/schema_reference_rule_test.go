package discovery

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// What this file pins: the rule that a measured number is a reference travels on the schema
// as well as in the prompt, it reaches every field that writes prose, and it is absent from
// the one schema where a reference cannot be rendered.
//
// The schemas are walked rather than listed. A test naming the fields it knows about only
// ever catches the fields someone remembered, which is how one unbounded string helper
// became four (see TestEverySchemaStringIsBounded). So every string field in both schemas
// has to be classified below, and a field added without being classified fails.

// referenceFields are the string fields a model writes prose into. Each one's description
// has to show the brace form, so a model reading the field knows a measured number is named
// rather than typed.
//
// Detected by the presence of `{{`, not by matching one exact sentence: estimated_improvement
// carries its own wording because the contract deliberately allows a projection there, and a
// test demanding one fixed string would have forced that field to lie.
var referenceFields = map[string]string{
	"insight.insights.items.name":                                                "the headline a reader sees first",
	"insight.insights.items.description":                                         "the sentence that used to be asked for 'citing the supporting numbers'",
	"insight.insights.items.indicators.items":                                    "an indicator states a measurement as often as the description does",
	"recommendation.recommendations.items.title":                                 "one measured run headlined one buyer count while its own body said another",
	"recommendation.recommendations.items.description":                           "same wording, same invitation, on the phase whose schema is sent on every call",
	"recommendation.recommendations.items.actions.items":                         "an action naming a band or a count is naming an insight's figure",
	"recommendation.recommendations.items.expected_impact.estimated_improvement": "a projection is prose, the measured base it is projected from is a reference",
	"recommendation.recommendations.items.expected_impact.reasoning":             "it explains the impact by restating the numbers behind it",
}

// nonReferenceFields are the remaining string fields, with why the rule does not belong on
// each. Recorded rather than skipped: the reason is the thing worth keeping, and a field
// that quietly joins this set is a field nobody argued about.
var nonReferenceFields = map[string]string{
	"insight.insights.items.severity":       "a fixed word, not prose",
	"insight.insights.items.target_segment": "names a population, carries no measurement",

	// The figure sub-schema describes how a figure is declared. Its wording is already
	// about declaring, so repeating the reference rule inside the thing being referenced
	// would be circular.
	"insight.insights.items.figures.items.id":       "the key itself",
	"insight.insights.items.figures.items.unit":     "notation",
	"insight.insights.items.figures.items.currency": "notation",
	"insight.insights.items.figures.items.scale":    "notation",
	"insight.insights.items.figures.items.kind":     "which arithmetic to run",
	"insight.insights.items.figures.items.column":   "names a column",
	"insight.insights.items.figures.items.row":      "a row selector in the filter grammar",
	"insight.insights.items.figures.items.other":    "a row selector in the filter grammar",
	"insight.insights.items.figures.items.scope":    "a row selector in the filter grammar",

	// A claim is a copy of prose, not authored prose. By the time it is written the
	// sentence it quotes already exists, and it has to match that sentence verbatim for
	// reconciliation to find it -- so asking for a reference here would break the match.
	"insight.insights.items.quantifier_claims.items.claim":        "a verbatim copy of a sentence written elsewhere",
	"insight.insights.items.quantifier_claims.items.kind":         "a fixed word",
	"insight.insights.items.quantifier_claims.items.column":       "names a column",
	"insight.insights.items.quantifier_claims.items.filter":       "the filter grammar",
	"insight.insights.items.quantifier_claims.items.scope":        "the filter grammar",
	"insight.insights.items.quantifier_claims.items.subject":      "the filter grammar",
	"insight.insights.items.quantifier_claims.items.order":        "a fixed word",
	"insight.insights.items.quantifier_claims.items.order_by":     "names a column",
	"insight.insights.items.quantifier_claims.items.top_n_column": "names a column",
	"insight.insights.items.quantifier_claims.items.trend":        "a fixed word",

	"recommendation.recommendations.items.category":       "a short tag",
	"recommendation.recommendations.items.target_segment": "names a population, carries no measurement",
	// A metric is a label. The rule would be weaker than the truth here: no number
	// belongs in it at all, referenced or not, which is what its description now says.
	"recommendation.recommendations.items.expected_impact.metric":           "a metric is a name, not a number",
	"recommendation.recommendations.items.related_insight_ids.items":        "a UUID copied from the input",
	"recommendation.recommendations.items.figures.items.id":                 "the key itself",
	"recommendation.recommendations.items.figures.items.kind":               "ref or sum",
	"recommendation.recommendations.items.figures.items.unit":               "notation",
	"recommendation.recommendations.items.figures.items.scale":              "notation",
	"recommendation.recommendations.items.figures.items.refs.items.insight": "an id copied from the input",
	"recommendation.recommendations.items.figures.items.refs.items.figure":  "a key copied from an insight",
}

func TestEveryProseFieldCarriesTheReferenceRule(t *testing.T) {
	found := schemaStringFields(t)

	for path, why := range referenceFields {
		desc, present := found[path]
		if !present {
			t.Errorf("%s is listed as a prose field and no longer exists in the schema (%s); "+
				"drop it from referenceFields or restore it", path, why)
			continue
		}
		if !strings.Contains(desc, "{{") {
			t.Errorf("%s does not show the {{key}} form, so the field a model writes prose into "+
				"never says a measured number is named rather than typed (%s); description: %q",
				path, why, desc)
		}
	}

	// And the other direction: every string field is classified, so a new prose field
	// cannot arrive without someone deciding which set it is in.
	var unclassified []string
	for path := range found {
		_, prose := referenceFields[path]
		_, other := nonReferenceFields[path]
		if !prose && !other {
			unclassified = append(unclassified, path)
		}
		if prose && other {
			t.Errorf("%s is in both referenceFields and nonReferenceFields", path)
		}
	}
	sort.Strings(unclassified)
	for _, path := range unclassified {
		t.Errorf("%s is a string field in neither referenceFields nor nonReferenceFields: if a "+
			"model writes prose into it, carry the rule; if not, record why not", path)
	}
	for path := range nonReferenceFields {
		if _, present := found[path]; !present {
			t.Errorf("%s is recorded as carrying no reference rule but no longer exists; "+
				"drop it from nonReferenceFields", path)
		}
	}
}

// TestTheRepairSchemaAsksForNoReference is the other half of the fix that removed `figures`
// from the repair schema.
//
// Deleting the array stopped the repair declaring a figure. It did not stop the field
// descriptions asking for a reference -- and once every prose field carried the rule, the
// repair schema inherited an instruction to write {{key}} alongside an array that had just
// been removed, which is exactly the placeholder the deletion exists to prevent.
func TestTheRepairSchemaAsksForNoReference(t *testing.T) {
	repair := insightRepairResponseFormat()
	if repair == nil || repair.Schema == nil {
		t.Fatal("the repair response format carries no schema")
	}
	found := map[string]string{}
	walkSchemaStrings(repair.Schema, "repair", found)

	for path, desc := range found {
		if strings.Contains(desc, figureReferenceRule) {
			t.Errorf("the repair schema's %s still carries the reference rule, which names a "+
				"`figures` array this schema does not have; description: %q", path, desc)
		}
	}
	for _, field := range []string{"repair.insights.items.name", "repair.insights.items.description",
		"repair.insights.items.indicators.items"} {
		desc, present := found[field]
		if !present {
			t.Errorf("%s is missing from the repair schema", field)
			continue
		}
		if !strings.Contains(desc, repairProseOnlyRule) {
			t.Errorf("%s does not tell the repair its numbers are already rendered, so a field "+
				"left silent about numbers is how the first contradiction arrived; description: %q",
				field, desc)
		}
	}
}

// reQuotedPositionalKey matches a positional key offered as an example in a description.
var reQuotedPositionalKey = regexp.MustCompile("[\"`]f[0-9]+[\"`]")

func TestNoSchemaDescriptionTeachesAPositionalKey(t *testing.T) {
	// The contract's examples were changed to name their keys; a schema still offering
	// "f1" as the example would put the two wires back into disagreement, and this is the
	// wire attached to the field.
	for path, desc := range schemaStringFields(t) {
		if m := reQuotedPositionalKey.FindString(desc); m != "" {
			t.Errorf("%s offers %s as the example key: a positional key tells a model nothing "+
				"about which figure a sentence meant", path, m)
		}
	}
}

// reCurrencySymbol matches a hardcoded currency in a description that is not about currency.
var reCurrencySymbol = regexp.MustCompile(`[$£€₺¥]`)

func TestNoProseFieldDescriptionHardcodesACurrency(t *testing.T) {
	// Scoped to the prose fields on purpose. A currency symbol belongs in the figure
	// `currency` field's own description, which is where the symbol is being asked for;
	// the measured failure was a currency symbol in a field description that nothing
	// checks -- `estimated_improvement` documented as `"+$4,975/month"` -- on warehouses
	// that report in other currencies. This is the same default that left every currency
	// figure unmarked until insightCurrencyFieldDescription was rewritten.
	found := schemaStringFields(t)
	for path := range referenceFields {
		desc, present := found[path]
		if !present {
			continue
		}
		if m := reCurrencySymbol.FindString(desc); m != "" {
			t.Errorf("%s hardcodes %q in its description: the warehouse's currency is the "+
				"model's to state, and a symbol in an example teaches it as the default",
				path, m)
		}
	}
}

// schemaStringFields walks both response schemas and returns every string field's path and
// description.
func schemaStringFields(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	walkSchemaStrings(insightResponseSchema(), "insight", out)
	walkSchemaStrings(recommendationResponseSchema(), "recommendation", out)
	if len(out) == 0 {
		t.Fatal("the walk found no string fields, so every assertion over it is vacuous")
	}
	return out
}

// walkSchemaStrings recurses a JSON Schema, recording path -> description for string fields.
// Keyed explicitly through the nesting keywords rather than over every value, so a
// description that happens to be a map cannot be mistaken for a schema.
func walkSchemaStrings(node interface{}, path string, out map[string]string) {
	m, ok := node.(map[string]interface{})
	if !ok {
		if arr, isArr := node.([]interface{}); isArr {
			for _, v := range arr {
				walkSchemaStrings(v, path, out)
			}
		}
		return
	}
	if tp, _ := m["type"].(string); tp == "string" {
		desc, _ := m["description"].(string)
		out[path] = desc
	}
	if props, isMap := m["properties"].(map[string]interface{}); isMap {
		for k, v := range props {
			walkSchemaStrings(v, path+"."+k, out)
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not", "anyOf", "oneOf", "allOf"} {
		if v, present := m[key]; present {
			walkSchemaStrings(v, path+"."+key, out)
		}
	}
}
