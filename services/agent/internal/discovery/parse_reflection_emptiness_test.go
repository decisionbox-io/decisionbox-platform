package discovery

import "testing"

// When a reflection is empty, and when it only looks empty.
//
// reflect.DeepEqual against a zero parsedReflection only catches a literal `{}`. A model
// that spells out every field as an empty array produces something semantically empty whose
// slices are non-nil, so it compared unequal, passed as an answer, and silenced the retry
// that would have asked again.

func TestParseReflection_ExplicitlyEmptyAloneIsAccepted(t *testing.T) {
	// The other half: reflecting and finding nothing to change is a legitimate answer
	// when nothing follows it.
	const in = `{"covered_tables":[],"next_tasks":[]}`
	got, err := parseReflection(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("got nil")
	}
}

func TestParseReflection_OneFilledFieldIsNotEmpty(t *testing.T) {
	// A single populated field is an answer, so the emptiness test must not swallow it.
	for name, in := range map[string]string{
		"summary only": `{"coverage_summary":"orders covered","covered_tables":[]}` + "\n" + `{"covered_areas":["revenue"]}`,
		"one table":    `{"covered_tables":["orders"]}` + "\n" + `{"covered_areas":["revenue"]}`,
		"one task":     `{"next_tasks":[{"title":"t","text":"x"}]}` + "\n" + `{"covered_areas":["revenue"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseReflection(in)
			if err != nil {
				t.Fatalf("err = %v, want nil: a populated field is an answer and the first value wins", err)
			}
			if got == nil {
				t.Fatal("got nil")
			}
		})
	}
}

// JSON null unmarshals into a non-pointer struct without error, so a bare `null` came
// back as an empty reflection and was accepted instead of retried.
func TestParseReflection_BareNullIsAnError(t *testing.T) {
	for _, in := range []string{"null", "  null  ", "[]", `"nothing to reflect on"`, "42"} {
		got, err := parseReflection(in)
		if err == nil {
			t.Errorf("parseReflection(%q) = %+v, nil; want an error: the contract is an object", in, got)
		}
	}
}
