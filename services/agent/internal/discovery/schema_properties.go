package discovery

import (
	"reflect"
	"strings"
)

// Property order is part of the contract, so it is declared rather than left to chance.
//
// A JSON Schema's `properties` was a map[string]interface{} here, and encoding/json sorts
// map keys. The order a model saw was therefore alphabetical, which is the order of nothing
// -- not the order the prompt's worked example teaches, and not an order anyone chose.
//
// That is invisible on a wire that only validates. It is decisive on a wire that compiles
// the schema into a grammar: json-schema-to-grammar.cpp walks `properties` in order, emits
// the required ones first, and then permits each optional one only at or after its own
// position. A field is reachable exactly once, in sequence, and a model that moves past it
// can never come back.
//
// Measured, run 22, one warehouse, two models, same cache. Alphabetically `figures` sits
// between `description` and `name`, so an insight that wrote `name` second had already
// passed it:
//
//	           carried figures   figures/insight
//	opus-4-8        21 of 21            7.86
//	qwen3.5-27b      5 of 10            1.60
//
// The large model complied with the alphabetical order and kept everything. The small one
// satisfied what was required and closed the object, losing half its figures, its
// indicators and its quantifier claims -- a silent collapse in the evidence layer, caused
// by a key order nobody wrote.
//
// So each object shape below declares its properties as a struct. encoding/json marshals
// struct fields in declaration order, which makes the order intentional, reviewable, and
// identical on every wire. Pinned by TestSchemaPropertyOrderIsDeclaredNotAlphabetical.
//
// Two rules for anyone editing these:
//
//   - Construct the struct in one literal and leave no field nil. A nil map field marshals
//     as `null`, and a property whose schema is `null` is rejected by the grammar wires.
//   - A field that must precede the prose has to be in `required` as well as early in the
//     struct. The grammar emits every required property before any optional one, so an
//     optional `figures` is moved after the required prose however early it is declared.
//     This is the whole reason the measurement above happened.
//
// The shape is evidence, then prose, then the headline: a model writes the rows it read
// before the sentence about them, and the sentence before the title over it.

// insightItemProperties is one insight, in generation order.
type insightItemProperties struct {
	SourceSteps map[string]interface{} `json:"source_steps"`
	Figures     map[string]interface{} `json:"figures"`
	Description map[string]interface{} `json:"description"`
	Indicators  map[string]interface{} `json:"indicators"`
	Name        map[string]interface{} `json:"name"`
	// After the prose it quotes: a claim repeats a sentence verbatim and cannot be
	// written before the sentence exists.
	QuantifierClaims map[string]interface{} `json:"quantifier_claims"`
	Severity         map[string]interface{} `json:"severity"`
	TargetSegment    map[string]interface{} `json:"target_segment"`
	AffectedCount    map[string]interface{} `json:"affected_count"`
	RiskScore        map[string]interface{} `json:"risk_score"`
	Confidence       map[string]interface{} `json:"confidence"`
	Metrics          map[string]interface{} `json:"metrics"`
}

// insightRepairItemProperties is the repair pass's insight: the same object without
// `figures`, because by then every number is rendered text and there is nothing to declare.
//
// A separate struct rather than a field deleted from a copy. The repair schema used to be
// built by copying the analysis schema and deleting a map key, which a struct cannot do --
// and the copy was never the honest description anyway, since the prose fields have to ask
// for something different here. Pinned by TestTheRepairSchemaAsksForNoReference.
type insightRepairItemProperties struct {
	SourceSteps      map[string]interface{} `json:"source_steps"`
	Description      map[string]interface{} `json:"description"`
	Indicators       map[string]interface{} `json:"indicators"`
	Name             map[string]interface{} `json:"name"`
	QuantifierClaims map[string]interface{} `json:"quantifier_claims"`
	Severity         map[string]interface{} `json:"severity"`
	TargetSegment    map[string]interface{} `json:"target_segment"`
	AffectedCount    map[string]interface{} `json:"affected_count"`
	RiskScore        map[string]interface{} `json:"risk_score"`
	Confidence       map[string]interface{} `json:"confidence"`
	Metrics          map[string]interface{} `json:"metrics"`
}

// figureItemProperties is one declared figure: what it is called, the number, where it came
// from, the arithmetic, then how it prints.
type figureItemProperties struct {
	ID       map[string]interface{} `json:"id"`
	Value    map[string]interface{} `json:"value"`
	Step     map[string]interface{} `json:"step"`
	Kind     map[string]interface{} `json:"kind"`
	Column   map[string]interface{} `json:"column"`
	Row      map[string]interface{} `json:"row"`
	Other    map[string]interface{} `json:"other"`
	Scope    map[string]interface{} `json:"scope"`
	Unit     map[string]interface{} `json:"unit"`
	Currency map[string]interface{} `json:"currency"`
	Scale    map[string]interface{} `json:"scale"`
	Decimals map[string]interface{} `json:"decimals"`
	Approx   map[string]interface{} `json:"approx"`
}

// claimItemProperties is one quantifier claim: the sentence, which kind of claim it is, the
// step that settles it, then the operands that kind needs.
type claimItemProperties struct {
	Claim      map[string]interface{} `json:"claim"`
	Kind       map[string]interface{} `json:"kind"`
	Step       map[string]interface{} `json:"step"`
	Column     map[string]interface{} `json:"column"`
	Scope      map[string]interface{} `json:"scope"`
	Filter     map[string]interface{} `json:"filter"`
	Subject    map[string]interface{} `json:"subject"`
	Rank       map[string]interface{} `json:"rank"`
	Order      map[string]interface{} `json:"order"`
	TopN       map[string]interface{} `json:"top_n"`
	TopNColumn map[string]interface{} `json:"top_n_column"`
	OrderBy    map[string]interface{} `json:"order_by"`
	Trend      map[string]interface{} `json:"trend"`
	Count      map[string]interface{} `json:"count"`
}

// recommendationItemProperties is one recommendation, in generation order: the insights it
// rests on and the numbers it restates, then the prose, then the title over it.
type recommendationItemProperties struct {
	RelatedInsightIDs map[string]interface{} `json:"related_insight_ids"`
	Figures           map[string]interface{} `json:"figures"`
	Description       map[string]interface{} `json:"description"`
	Actions           map[string]interface{} `json:"actions"`
	ExpectedImpact    map[string]interface{} `json:"expected_impact"`
	Title             map[string]interface{} `json:"title"`
	Category          map[string]interface{} `json:"category"`
	Priority          map[string]interface{} `json:"priority"`
	TargetSegment     map[string]interface{} `json:"target_segment"`
	SegmentSize       map[string]interface{} `json:"segment_size"`
	Confidence        map[string]interface{} `json:"confidence"`
}

// expectedImpactProperties is the structured impact object.
type expectedImpactProperties struct {
	Metric               map[string]interface{} `json:"metric"`
	EstimatedImprovement map[string]interface{} `json:"estimated_improvement"`
	Reasoning            map[string]interface{} `json:"reasoning"`
}

// recFigureItemProperties is one recommendation figure: what it is called, how it is
// derived, which insight figures it names, then how it prints. There is no `value` -- the
// platform takes it from the figure named in `refs`.
type recFigureItemProperties struct {
	ID       map[string]interface{} `json:"id"`
	Kind     map[string]interface{} `json:"kind"`
	Refs     map[string]interface{} `json:"refs"`
	Unit     map[string]interface{} `json:"unit"`
	Scale    map[string]interface{} `json:"scale"`
	Decimals map[string]interface{} `json:"decimals"`
	Approx   map[string]interface{} `json:"approx"`
}

// recFigureRefProperties names one insight figure.
type recFigureRefProperties struct {
	Insight map[string]interface{} `json:"insight"`
	Figure  map[string]interface{} `json:"figure"`
}

// schemaProperties reads an object schema's `properties` whether it is one of the ordered
// structs above or a plain map, so a caller that only wants to look a field up does not
// have to know which.
//
// Order is deliberately not preserved here: a map cannot carry it. Anything asserting on
// order must read the marshalled JSON, which is what the schema actually sends --
// schemaPropertyOrder does that.
func schemaProperties(node interface{}) map[string]interface{} {
	switch v := node.(type) {
	case nil:
		return nil
	case map[string]interface{}:
		return v
	}

	rv := reflect.ValueOf(node)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	rt := rv.Type()
	out := make(map[string]interface{}, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name := jsonTagName(rt.Field(i))
		if name == "" {
			continue
		}
		out[name] = rv.Field(i).Interface()
	}
	return out
}

// objectProperties is schemaProperties for the common case of an object schema in hand.
func objectProperties(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	return schemaProperties(schema["properties"])
}

// declaredPropertyNames returns a properties struct's field names in declaration order,
// which is the order encoding/json will marshal them in.
func declaredPropertyNames(node interface{}) []string {
	rv := reflect.ValueOf(node)
	for rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}
	rt := rv.Type()
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		if name := jsonTagName(rt.Field(i)); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// jsonTagName is the key a field marshals under, or "" when it is skipped.
func jsonTagName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}
