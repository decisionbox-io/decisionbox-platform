package discovery

import (
	"encoding/json"
	"strings"
	"testing"

	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func withheld(detail string) gowarehouse.QualityCaveat {
	return gowarehouse.QualityCaveat{Kind: gowarehouse.QualityWithheld, Detail: detail}
}

func sampled(detail string) gowarehouse.QualityCaveat {
	return gowarehouse.QualityCaveat{Kind: gowarehouse.QualitySampled, Detail: detail}
}

// TestAttachSourceQuality is the link that makes a caveat survive the model.
//
// An insight computed over withheld rows reads exactly like one computed over
// complete rows — same shape, same confidence, same plausible number. A model
// that simply did not mention the shortfall would produce a finding
// indistinguishable from a sound one, and nothing downstream could tell them
// apart. Deriving the label from the cited steps is what makes it independent
// of what the model chose to write.
func TestAttachSourceQuality(t *testing.T) {
	stepByID := map[int]*models.ExplorationStep{
		1: {Step: 1, Quality: []gowarehouse.QualityCaveat{withheld("small cohorts omitted")}},
		2: {Step: 2},
		3: {Step: 3, Quality: []gowarehouse.QualityCaveat{sampled("1000 of 50000")}},
	}

	insights := []models.Insight{
		{Name: "from a degraded step", SourceSteps: []int{1}},
		{Name: "from a clean step", SourceSteps: []int{2}},
		{Name: "from both", SourceSteps: []int{1, 3}},
		{Name: "cites nothing"},
	}

	attachSourceQuality(insights, stepByID)

	if len(insights[0].Quality) != 1 || insights[0].Quality[0].Kind != gowarehouse.QualityWithheld {
		t.Errorf("insight from a degraded step = %+v, want the withheld caveat", insights[0].Quality)
	}
	if insights[1].Quality != nil {
		t.Errorf("insight from a clean step = %+v, want no caveat invented", insights[1].Quality)
	}
	if len(insights[2].Quality) != 2 {
		t.Errorf("insight from two degraded steps = %+v, want both caveats", insights[2].Quality)
	}
	if insights[3].Quality != nil {
		t.Errorf("insight citing nothing = %+v, want none", insights[3].Quality)
	}
}

// TestAttachSourceQuality_DeduplicatesAcrossSteps: several steps hitting the
// same threshold is one fact about the evidence, not three. Repeating it would
// make a two-step insight look worse-evidenced than a one-step insight with
// the identical problem.
func TestAttachSourceQuality_DeduplicatesAcrossSteps(t *testing.T) {
	same := withheld("small cohorts omitted")
	stepByID := map[int]*models.ExplorationStep{
		1: {Step: 1, Quality: []gowarehouse.QualityCaveat{same}},
		2: {Step: 2, Quality: []gowarehouse.QualityCaveat{same}},
		3: {Step: 3, Quality: []gowarehouse.QualityCaveat{same, sampled("1000 of 50000")}},
	}
	insights := []models.Insight{{SourceSteps: []int{1, 2, 3}}}

	attachSourceQuality(insights, stepByID)

	if len(insights[0].Quality) != 2 {
		t.Fatalf("Quality = %+v, want the two distinct caveats", insights[0].Quality)
	}
	if insights[0].Quality[0] != same {
		t.Errorf("Quality[0] = %+v, want the first-seen caveat, so ordering is stable", insights[0].Quality[0])
	}
}

// TestAttachSourceQuality_IgnoresUnknownSteps: a model can cite a step number
// that does not exist. That must not panic, and must not silently become a
// clean bill of health for an insight whose real evidence is unknown.
func TestAttachSourceQuality_IgnoresUnknownSteps(t *testing.T) {
	stepByID := map[int]*models.ExplorationStep{
		1: {Step: 1, Quality: []gowarehouse.QualityCaveat{withheld("omitted")}},
		2: nil,
	}
	insights := []models.Insight{{SourceSteps: []int{1, 2, 99}}}

	attachSourceQuality(insights, stepByID)

	if len(insights[0].Quality) != 1 {
		t.Errorf("Quality = %+v, want only the caveat from the step that exists", insights[0].Quality)
	}
}

// TestRenderCompactedSteps_CarriesCaveatsIntoThePrompt is the other half: the
// analysis model must see the caveat beside the rows, because the rows alone
// cannot convey it — they are well-formed whether or not a threshold withheld
// half the population.
func TestRenderCompactedSteps_CarriesCaveatsIntoThePrompt(t *testing.T) {
	rendered := RenderCompactedSteps([]models.ExplorationStep{{
		Step:        1,
		Action:      "query_data",
		Query:       "sessions by city",
		RowCount:    2,
		QueryResult: []map[string]interface{}{{"city": "Dublin", "sessions": 31}},
		Quality:     []gowarehouse.QualityCaveat{withheld("small cohorts omitted")},
	}})

	if !strings.Contains(rendered, "quality_caveats") {
		t.Fatalf("rendered prompt has no caveat field:\n%s", rendered)
	}
	if !strings.Contains(rendered, "small cohorts omitted") {
		t.Errorf("rendered prompt does not carry the caveat detail:\n%s", rendered)
	}
}

// TestRenderCompactedSteps_UnchangedForCleanSteps pins that a prompt built
// from warehouse steps is byte-identical to before. Every SQL warehouse
// declares no caveats, so nothing should appear for them — an empty field on
// every step would be noise in every prompt the product has ever sent.
func TestRenderCompactedSteps_UnchangedForCleanSteps(t *testing.T) {
	rendered := RenderCompactedSteps([]models.ExplorationStep{{
		Step:        1,
		Action:      "query_data",
		Query:       "SELECT 1",
		RowCount:    1,
		QueryResult: []map[string]interface{}{{"n": 1}},
	}})

	if strings.Contains(rendered, "quality_caveats") {
		t.Errorf("a clean step gained a caveat field:\n%s", rendered)
	}

	// And it must still be valid JSON the prompt can embed.
	var probe []map[string]any
	if err := json.Unmarshal([]byte(rendered), &probe); err != nil {
		t.Fatalf("rendered steps are not valid JSON: %v", err)
	}
	if _, present := probe[0]["quality_caveats"]; present {
		t.Error("quality_caveats key present on a clean step")
	}
}

// TestInsightQualityIsNotDecodableFromModelOutput covers the invariant this
// whole design rests on: the caveat is DERIVED, so the model must not be able
// to author it.
//
// Two failures, and the second is the dangerous one. A model emitting a
// caveat could hang a fabricated one on a clean insight. But "quality" is also
// an obvious key for a model to invent while describing a finding — and a
// scalar under a field typed as a list would fail the decode and drop the
// insight entirely, losing a real finding to a stray word.
func TestInsightQualityIsNotDecodableFromModelOutput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "a fabricated caveat list",
			raw:  `{"name":"x","quality":[{"kind":"withheld","detail":"invented"}]}`,
		},
		{
			// The realistic one: a model describing its own confidence.
			name: "a scalar, which must not fail the decode",
			raw:  `{"name":"x","quality":"high"}`,
		},
		{
			name: "an object",
			raw:  `{"name":"x","quality":{"rating":"good"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ins models.Insight
			if err := json.Unmarshal([]byte(tt.raw), &ins); err != nil {
				t.Fatalf("model output must still decode; a stray key cannot cost a real insight: %v", err)
			}
			if ins.Name != "x" {
				t.Errorf("Name = %q, want the insight to decode normally", ins.Name)
			}
			if ins.Quality != nil {
				t.Errorf("Quality = %+v, want nothing authored by the model to reach the field", ins.Quality)
			}
		})
	}
}

// TestInsightQualityStillSerialisesOutward pins the other half: derived
// caveats must reach storage and clients. Ignoring the model's input is not
// the same as suppressing our own output.
func TestInsightQualityStillSerialisesOutward(t *testing.T) {
	ins := models.Insight{
		Name:    "x",
		Quality: []gowarehouse.QualityCaveat{withheld("small cohorts omitted")},
	}
	out, err := json.Marshal(ins)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "small cohorts omitted") {
		t.Errorf("serialised insight lost its derived caveat: %s", out)
	}

	// And a clean insight emits no field at all, so existing consumers see an
	// unchanged shape.
	clean, err := json.Marshal(models.Insight{Name: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(clean), "quality") {
		t.Errorf("a clean insight gained a quality key: %s", clean)
	}
}
