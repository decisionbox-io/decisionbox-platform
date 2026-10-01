package discovery

// The trailing-prose rejection was not specific to insights. cleanJSONResponse is
// shared by four parsers, and all four decoded with json.Unmarshal, which rejects a
// valid value followed by anything. A model that answers and then explains itself was
// told its answer was malformed in every one of the four phases.
//
// Insights had the captured evidence (see insight_parse_trailing_test.go); these three
// are the same defect at the same helper, with the same shape applied.

import "testing"

func TestParseRecommendations_TrailingProse(t *testing.T) {
	const in = `{"recommendations":[
		{"title":"Recover the bulk-discount margin","priority":"high"},
		{"title":"Investigate the single returning region","priority":"medium"}
	]}

I have limited this to two recommendations because the remaining findings did not
carry an affected population I could size.`
	recs, dropped, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 2 || dropped != 0 {
		t.Fatalf("got %d recommendations, %d dropped; want 2, 0", len(recs), dropped)
	}
	if recs[0].Title != "Recover the bulk-discount margin" {
		t.Errorf("first title = %q", recs[0].Title)
	}
}

func TestParseRecommendations_TrailingProseAfterBareArray(t *testing.T) {
	const in = `[{"title":"A","priority":"high"}]

That is the only action the insights support.`
	recs, _, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d recommendations, want 1", len(recs))
	}
}

func TestParseRecommendations_TruncatedStillFails(t *testing.T) {
	const in = `{"recommendations":[{"title":"A"`
	if _, _, err := parseRecommendations(in); err == nil {
		t.Fatal("err = nil, want an error: a truncated response must still be retried")
	}
}

func TestParseReflection_TrailingProse(t *testing.T) {
	const in = `{"drop_steps":[4,7]}

I have dropped those two because both queries returned a single aggregate row that no
insight cites.`
	got, err := parseReflection(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("got nil reflection")
	}
}

func TestParseReflection_TruncatedStillFails(t *testing.T) {
	if _, err := parseReflection(`{"drop_steps":[4,`); err == nil {
		t.Fatal("err = nil, want an error")
	}
}

func TestParseQuestions_TrailingProse(t *testing.T) {
	const in = `{"questions":[
		{"question":"Which region drives the return rate?"},
		{"question":"Are bulk discounts approved anywhere?"}
	]}

Those are the two the data can answer; a third on shipping delays is not supported.`
	qs, raw, err := parseQuestions(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if raw != 2 || len(qs) != 2 {
		t.Fatalf("got %d kept of %d raw; want 2 of 2", len(qs), raw)
	}
}

func TestParseQuestions_TruncatedStillFails(t *testing.T) {
	if _, _, err := parseQuestions(`{"questions":[{"question":"a"`); err == nil {
		t.Fatal("err = nil, want an error")
	}
}
