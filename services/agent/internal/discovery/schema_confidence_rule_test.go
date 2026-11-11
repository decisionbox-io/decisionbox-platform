package discovery

import (
	"strings"
	"testing"
)

// What this file pins: none of the three schemas a model answers asks for a
// confidence number.
//
// It exists because removing the ask is only half the mechanism and the half
// that CAN come back silently. A model cannot author the number — the field is
// tagged json:"-" so a volunteered key is ignored, and the scorer clears and
// overwrites regardless — but a property re-added to one of these structs
// would spend tokens asking for an answer nobody reads, and would put the
// schema wire back into disagreement with the prompts. Shaped as an inverse
// guard like TestTheRepairSchemaAsksForNoReference: a property added anywhere
// under any of the three schemas fails here.

// TestTheSchemasAskForNoConfidence walks all three response formats.
func TestTheSchemasAskForNoConfidence(t *testing.T) {
	repair := insightRepairResponseFormat()
	if repair == nil || repair.Schema == nil {
		t.Fatal("the repair response format carries no schema")
	}

	for _, tc := range []struct {
		name   string
		schema map[string]interface{}
	}{
		{"insight", insightResponseSchema()},
		{"insight repair", repair.Schema},
		{"recommendation", recommendationResponseSchema()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := schemaPropertyPaths(tc.schema, tc.name)
			if len(paths) == 0 {
				t.Fatalf("the %s walk found no properties, so every assertion over it is vacuous", tc.name)
			}
			for _, path := range paths {
				if strings.HasSuffix(path, ".confidence") {
					t.Errorf("the %s schema asks for %s again. The number is derived from the "+
						"validation verdict and nothing reads an answer to this, so asking for "+
						"one spends tokens and puts the schema back into disagreement with every "+
						"prompt that no longer shows it.", tc.name, path)
				}
			}
		})
	}
}

// TestNoSchemaDescriptionAsksForConfidence catches the other shape the ask can
// return in: a property that is not called `confidence` but whose description
// tells the model to rate its own certainty.
func TestNoSchemaDescriptionAsksForConfidence(t *testing.T) {
	for path, desc := range schemaStringFields(t) {
		lower := strings.ToLower(desc)
		if !strings.Contains(lower, "confidence") {
			continue
		}
		t.Errorf("%s asks the model about confidence (%q). The number is derived from the "+
			"validation verdict; a model asked to state its own certainty is grading its own "+
			"work.", path, desc)
	}
}

// schemaPropertyPaths lists every property path under an object schema, so a
// re-added field is found wherever it is nested.
func schemaPropertyPaths(schema map[string]interface{}, prefix string) []string {
	var out []string
	for name, node := range objectProperties(schema) {
		path := prefix + "." + name
		out = append(out, path)
		child, ok := node.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, schemaPropertyPaths(child, path)...)
		if items, ok := child["items"].(map[string]interface{}); ok {
			out = append(out, schemaPropertyPaths(items, path+".items")...)
		}
	}
	return out
}
