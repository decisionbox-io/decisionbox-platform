package discovery

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

// What this file pins: the order a model is asked to generate properties in is declared, is
// the same on the schema wire and in the prompt's worked example, and is not alphabetical.
//
// Everything here reads the MARSHALLED JSON rather than the Go value. That is the only form
// that matters -- it is what each provider sends -- and it is the form where the bug lived:
// a map[string]interface{} looks ordered in source and is sorted by encoding/json. A future
// refactor that round-trips the schema through a map would restore alphabetical order with
// no visible change to the code, and these tests are what would fail.

// generationOrder is the order each object is asked for, and the reason for it. Evidence
// before prose before the headline: the rows a model read, then the sentence about them,
// then the title over it.
//
// A field that must arrive before the prose also has to be in `required`, because a grammar
// emits every required property before any optional one whatever the schema order says.
// TestEveryFieldThatMustPrecedeTheProseIsRequired holds that half.
var generationOrder = map[string][]string{
	"insight": {
		"source_steps", "figures", "description", "indicators", "name",
		"quantifier_claims", "severity", "target_segment",
		"affected_count", "risk_score", "confidence", "metrics",
	},
	"figures": {
		"id", "value", "step", "kind",
		"column", "row", "other", "scope",
		"unit", "currency", "scale", "decimals", "approx",
	},
	"quantifier_claims": {
		"claim", "kind", "step",
		"column", "scope", "filter", "subject", "rank", "order",
		"top_n", "top_n_column", "order_by", "trend", "count",
	},
}

// mustPrecedeTheProse are the fields whose whole purpose is to be answered before the
// sentence that describes them. Each one is required for that reason as well as its own.
var mustPrecedeTheProse = map[string][]string{
	"insight":        {"source_steps", "figures"},
	"recommendation": {"related_insight_ids", "figures"},
}

func TestSchemaPropertyOrderIsDeclaredNotAlphabetical(t *testing.T) {
	item := marshalledProperties(t, insightResponseSchema(), "insights")
	for object, want := range generationOrder {
		got := item[object]
		if object == "insight" {
			got = item["<self>"]
		}
		if len(got) == 0 {
			t.Errorf("%s: no properties found in the marshalled schema", object)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s property order in the marshalled schema is\n  %v\nand the declared order is\n  %v",
				object, got, want)
		}
		alphabetical := append([]string(nil), want...)
		sort.Strings(alphabetical)
		if reflect.DeepEqual(got, alphabetical) && len(got) > 1 {
			t.Errorf("%s property order is alphabetical, which is the order of nothing: it is what a "+
				"map[string]interface{} produces, and run 22 measured a small model losing half its "+
				"figures to it", object)
		}
	}
}

func TestTheWorkedExampleTeachesTheSchemasOrder(t *testing.T) {
	// On a wire without constrained decoding the example is the only order signal the
	// model has, so the two have to agree. The example shows a subset of the fields, so
	// the relation is "same relative order", not equality.
	cases := []struct {
		name     string
		example  []string
		declared []string
	}{
		{"insight", exampleKeyOrder(t, figureContract), marshalledProperties(t, insightResponseSchema(), "insights")["<self>"]},
		{"recommendation", exampleKeyOrder(t, recommendationFigureContract), marshalledProperties(t, recommendationResponseSchema(), "recommendations")["<self>"]},
	}
	for _, c := range cases {
		if len(c.example) == 0 {
			t.Errorf("%s: the contract's worked example has no keys", c.name)
			continue
		}
		pos := map[string]int{}
		for i, k := range c.declared {
			pos[k] = i
		}
		last, lastKey := -1, ""
		for _, k := range c.example {
			i, known := pos[k]
			if !known {
				t.Errorf("%s: the example shows %q, which the schema does not describe", c.name, k)
				continue
			}
			if i < last {
				t.Errorf("%s: the example writes %q before %q, the schema asks for them the other way "+
					"round, and on a grammar wire the earlier one is then unreachable", c.name, lastKey, k)
			}
			last, lastKey = i, k
		}
	}
}

func TestEveryFieldThatMustPrecedeTheProseIsRequired(t *testing.T) {
	// The trap this closes, measured in run 22: `figures` was declared early and left
	// optional, a grammar moved every optional property behind the required prose, and the
	// small model wrote its prose and could no longer reach the field -- 1.60 figures per
	// insight against 7.86 on the large model. Declaring it early is not enough.
	for _, c := range []struct {
		object string
		schema map[string]interface{}
		key    string
	}{
		{"insight", insightResponseSchema(), "insights"},
		{"recommendation", recommendationResponseSchema(), "recommendations"},
	} {
		item := itemSchemaUnder(c.schema, c.key)
		if item == nil {
			t.Errorf("%s: no item schema found", c.object)
			continue
		}
		required := map[string]bool{}
		for _, r := range toStringSlice(item["required"]) {
			required[r] = true
		}
		for _, field := range mustPrecedeTheProse[c.object] {
			if !required[field] {
				t.Errorf("%s.%s has to be answered before the prose, and is not required: a grammar "+
					"emits every required property first, so an optional one is moved behind the prose "+
					"however early the struct declares it", c.object, field)
			}
		}
	}
}

func TestEveryRequiredNameIsADeclaredProperty(t *testing.T) {
	// A required name that is not a property is a field the grammar demands and the schema
	// never describes. Cheap to introduce when the two live in different places -- a struct
	// tag here and a string literal there -- which is exactly the new shape.
	for _, c := range []struct {
		object string
		schema map[string]interface{}
		key    string
	}{
		{"insight", insightResponseSchema(), "insights"},
		{"recommendation", recommendationResponseSchema(), "recommendations"},
		{"repair", insightRepairResponseFormat().Schema, "insights"},
	} {
		item := itemSchemaUnder(c.schema, c.key)
		if item == nil {
			t.Errorf("%s: no item schema found", c.object)
			continue
		}
		props := objectProperties(item)
		for _, name := range toStringSlice(item["required"]) {
			if _, declared := props[name]; !declared {
				t.Errorf("%s requires %q, which it does not describe as a property", c.object, name)
			}
		}
	}
}

func TestTheRepairSchemaDescribesEveryFieldItAsksFor(t *testing.T) {
	// The repair schema is built field by field from the analysis one, so a field added
	// there and forgotten here arrives as a nil property, which marshals as `null` and is
	// rejected by the grammar wires.
	props := objectProperties(itemSchemaUnder(insightRepairResponseFormat().Schema, "insights"))
	if len(props) == 0 {
		t.Fatal("the repair schema describes no properties")
	}
	for name, node := range props {
		// Type-asserted, not compared against nil. A nil map[string]interface{} boxed in an
		// interface is not == nil -- the interface holds a type -- so `node == nil` was
		// green while the property marshalled as `null`. The sabotage pass caught that,
		// which is the entire reason for running one.
		m, ok := node.(map[string]interface{})
		if !ok || m == nil {
			t.Errorf("the repair schema's %q is nil, which marshals as null and the grammar wires "+
				"reject; carry it across from the analysis schema or drop the field from "+
				"insightRepairItemProperties", name)
		}
	}
	if _, present := props["figures"]; present {
		t.Error("the repair schema describes `figures`, which the repair path cannot render")
	}
}

// marshalledProperties marshals a schema and returns, per object, the property names in the
// order they appear in the JSON. The envelope's item object is keyed "<self>".
func marshalledProperties(t *testing.T, schema map[string]interface{}, envelopeKey string) map[string][]string {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("the schema does not marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("remarshalled schema does not parse: %v", err)
	}
	out := map[string][]string{}
	collectOrder(t, raw, envelopeKey, out)
	return out
}

// collectOrder finds the envelope's item properties in the marshalled bytes and records the
// key order of that object and of each array-of-object property inside it.
func collectOrder(t *testing.T, raw []byte, envelopeKey string, out map[string][]string) {
	t.Helper()
	var env struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope does not parse: %v", err)
	}
	arr, ok := env.Properties[envelopeKey]
	if !ok {
		t.Fatalf("the envelope has no %q property", envelopeKey)
	}
	var withItems struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(arr, &withItems); err != nil {
		t.Fatalf("%s does not parse: %v", envelopeKey, err)
	}
	var item struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(withItems.Items, &item); err != nil {
		t.Fatalf("%s item does not parse: %v", envelopeKey, err)
	}
	out["<self>"] = jsonKeyOrder(item.Properties)

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item.Properties, &fields); err != nil {
		return
	}
	for name, node := range fields {
		var nested struct {
			Items *struct {
				Properties json.RawMessage `json:"properties"`
			} `json:"items"`
		}
		if err := json.Unmarshal(node, &nested); err != nil || nested.Items == nil {
			continue
		}
		if len(nested.Items.Properties) > 0 {
			out[name] = jsonKeyOrder(nested.Items.Properties)
		}
	}
}

// jsonKeyOrder returns a JSON object's keys in the order the bytes carry them, which is
// what a map cannot tell you and what the grammar wires read.
func jsonKeyOrder(raw json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil
	}
	var keys []string
	depth := 0
	for dec.More() || depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth < 0 {
					return keys
				}
			}
		case string:
			if depth == 0 {
				keys = append(keys, v)
				if err := skipValue(dec); err != nil {
					return keys
				}
			}
		}
	}
	return keys
}

// skipValue consumes one JSON value, whatever its shape.
func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if d != '{' && d != '[' {
		return nil
	}
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
	}
	return nil
}

// exampleKeyOrder returns the top-level key order of a contract's worked JSON example.
func exampleKeyOrder(t *testing.T, contract string) []string {
	t.Helper()
	const fence = "```json\n"
	i := strings.Index(contract, fence)
	if i < 0 {
		return nil
	}
	rest := contract[i+len(fence):]
	j := strings.Index(rest, "```")
	if j < 0 {
		return nil
	}
	body := strings.TrimRight(strings.TrimSpace(rest[:j]), ",")
	return jsonKeyOrder(json.RawMessage("{" + body + "}"))
}

// itemSchemaUnder returns the per-item object beneath an envelope array.
func itemSchemaUnder(schema map[string]interface{}, key string) map[string]interface{} {
	props := objectProperties(schema)
	arr, _ := props[key].(map[string]interface{})
	items, _ := arr["items"].(map[string]interface{})
	return items
}

// TestAStringifiedNestedArrayIsStillRecoveredInsideAnInsight is the receive side of the
// ordered-properties change, and it exists because the send side passing cleanly was not
// enough.
//
// Claude 5.x sometimes returns a nested array of objects as one JSON-encoded string inside
// a forced tool's input ({"figures": "[{...}]"}), which every caller's parser rejects.
// NormalizeStructuredToolResponse recovers that by walking the schema and decoding any
// string where the schema says array or object. The walk read `properties` with a type
// assertion to map[string]interface{} -- so the moment the insight item's properties became
// a struct, the walk reached the envelope, found the insights array, descended to the item,
// and stopped. Recovery still "ran" and touched nothing. A response that used to be
// salvaged would have been dropped instead, on the two wires this pipeline actually uses.
//
// Driven through the exported entry point with the real analysis schema, because the bug was
// in the interaction between the two and a hand-built schema would have missed it.
func TestAStringifiedNestedArrayIsStillRecoveredInsideAnInsight(t *testing.T) {
	format := insightResponseFormat()
	resp := &gollm.ChatResponse{
		StopReason: "tool_use",
		ToolCalls: []gollm.ToolCall{{
			Name: format.Name,
			Input: map[string]interface{}{
				"insights": []interface{}{map[string]interface{}{
					"name":        "a finding",
					"description": "prose referring to {{total_revenue}}",
					// The shapes the model stringifies: an array of objects, an array of
					// strings, and the claims array whose loss is silent.
					"figures":           `[{"id":"total_revenue","value":12,"step":1,"kind":"sum"}]`,
					"indicators":        `["one indicator"]`,
					"quantifier_claims": `[{"claim":"a claim","kind":"only","step":1}]`,
					"source_steps":      `[1]`,
				}},
			},
		}},
	}
	if err := gollm.NormalizeStructuredToolResponse(resp, true, format); err != nil {
		t.Fatalf("normalising the forced-tool reply failed: %v", err)
	}

	var env struct {
		Insights []struct {
			Figures []struct {
				ID string `json:"id"`
			} `json:"figures"`
			Indicators       []string         `json:"indicators"`
			SourceSteps      []int            `json:"source_steps"`
			QuantifierClaims []map[string]any `json:"quantifier_claims"`
		} `json:"insights"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &env); err != nil {
		t.Fatalf("the normalised content does not parse as an insight envelope, which is the "+
			"failure this test exists for: %v\n%s", err, resp.Content)
	}
	if len(env.Insights) != 1 {
		t.Fatalf("want 1 insight, got %d", len(env.Insights))
	}
	got := env.Insights[0]
	if len(got.Figures) != 1 || got.Figures[0].ID != "total_revenue" {
		t.Errorf("the stringified figures array was not decoded, so the insight ships with no "+
			"declared figure and its reference resolves to nothing: %+v", got.Figures)
	}
	if len(got.Indicators) != 1 {
		t.Errorf("the stringified indicators array was not decoded: %+v", got.Indicators)
	}
	if len(got.SourceSteps) != 1 {
		t.Errorf("the stringified source_steps array was not decoded: %+v", got.SourceSteps)
	}
	if len(got.QuantifierClaims) != 1 {
		t.Errorf("the stringified quantifier_claims array was not decoded, which is the quiet "+
			"one: the claim is stripped and the sentence it guards ships unchecked: %+v",
			got.QuantifierClaims)
	}
}
