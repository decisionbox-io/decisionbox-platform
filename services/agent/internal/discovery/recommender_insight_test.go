package discovery

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// recommenderSentInsightFields are the models.Insight json names the
// recommendation prompt carries — the fields the recommender reasons over.
var recommenderSentInsightFields = []string{
	"id", "analysis_area", "name", "description", "severity",
	"affected_count", "risk_score", "confidence", "metrics", "indicators",
	"target_segment", "evidence_quality",
}

// recommenderDroppedInsightFields are the models.Insight json names the
// recommendation prompt deliberately omits, with the reason each is not needed.
var recommenderDroppedInsightFields = map[string]string{
	"description_md": "a second copy of description, in Markdown; the recommender reads the plain text",
	"validation":     "the eligibility filter has already applied the verdict; the prompt never refers to it",
	"source_steps":   "exploration step numbers; the steps themselves are not in this prompt",
	"sql_metadata":   "the recommender does not reason over SQL",
	"discovered_at":  "a timestamp",
}

// fullyPopulatedInsight returns an insight with every field set, the dropped
// ones carrying text distinctive enough to find in a rendered prompt. The
// validation block mirrors the real shape: a verifier and a refuter verdict,
// each with enumerated claims, per-claim reasoning, and a cited row plus the
// SQL that produced it. That is the payload that overflowed a 40,960-token
// window and ended runs with zero recommendations.
func fullyPopulatedInsight(id string) models.Insight {
	verdict := func(mode, marker string) *valmodels.StructuredVerdict {
		return &valmodels.StructuredVerdict{
			DocID:            id,
			DocKind:          valmodels.DocInsight,
			Mode:             valmodels.AgentMode(mode),
			ClaimsConsidered: []string{marker + "-CLAIM"},
			ClaimVerdicts: []valmodels.ClaimVerdict{{
				ClaimText:  marker + "-CLAIM",
				ClaimKind:  "quantitative",
				IsHeadline: true,
				Status:     valmodels.StatusSupported,
				Reasoning:  marker + "-REASONING",
				Evidence: valmodels.ClaimEvidence{
					Kind:     "warehouse_query",
					QuerySQL: marker + "-SQL",
					Row:      map[string]any{marker + "-ROWKEY": 8298},
				},
			}},
			Overall:       valmodels.StatusSupported,
			OverallReason: marker + "-OVERALL",
		}
	}
	return models.Insight{
		ID:            id,
		AnalysisArea:  "churn",
		Name:          "Day 0-to-Day 1 Drop",
		Description:   "67% of new players in the queried window do not return after day 0.",
		DescriptionMd: "MARKDOWN-COPY",
		Severity:      "critical",
		AffectedCount: 16695,
		RiskScore:     0.67,
		Confidence:    0.85,
		Metrics:       map[string]interface{}{"churn_rate": 0.67},
		Indicators:    []string{"Only 33% return after Day 1"},
		TargetSegment: "Players who attempted fewer than 3 levels",
		SourceSteps:   []int{1, 3, 5},
		Quality:       []gowarehouse.QualityCaveat{{Kind: "sampled", Detail: "37 of 412 rows withheld"}},
		SQLMetadata:   &models.SQLMetadata{Query: "SQLMETA-QUERY", RowsReturned: 412},
		DiscoveredAt:  time.Date(2026, 5, 11, 9, 30, 0, 0, time.UTC),
		Validation: &valmodels.InsightValidation{
			Combined: valmodels.StatusSupported,
			Verifier: verdict("verifier", "VERIFIER"),
			Refuter:  verdict("refuter", "REFUTER"),
		},
	}
}

// transcriptMarkers are the strings fullyPopulatedInsight plants in the fields
// the prompt must not carry. Every one is absent from a correct prompt.
var transcriptMarkers = []string{
	"VERIFIER-CLAIM", "VERIFIER-REASONING", "VERIFIER-SQL", "VERIFIER-ROWKEY", "VERIFIER-OVERALL",
	"REFUTER-CLAIM", "REFUTER-REASONING", "REFUTER-SQL", "REFUTER-ROWKEY", "REFUTER-OVERALL",
	"MARKDOWN-COPY", "SQLMETA-QUERY",
}

// droppedJSONKeys are the dropped fields as they would appear in the rendered
// INSIGHTS_DATA. Quoted and colon-terminated so a match can only be a JSON key
// and never prose that happens to name the field.
func droppedJSONKeys() []string {
	keys := make([]string, 0, len(recommenderDroppedInsightFields))
	for name := range recommenderDroppedInsightFields {
		keys = append(keys, `"`+name+`":`)
	}
	return keys
}

func mustMarshalProjection(t *testing.T, insights []models.Insight) string {
	t.Helper()
	b, err := json.MarshalIndent(insightsForRecommenderPrompt(insights), "", "  ")
	if err != nil {
		t.Fatalf("marshalling the recommender projection: %v", err)
	}
	return string(b)
}

// jsonFieldNames returns the json names a struct marshals, in declaration
// order. Fields tagged "-" are skipped; an untagged field marshals under its Go
// name, so that is what is reported.
func jsonFieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, name)
	}
	return out
}

// TestRecommenderInsightProjectionIsExhaustive is the recurrence guard. The
// recommendation prompt used to carry the whole models.Insight, so a field
// added to the model reached the prompt with nobody choosing to send it — which
// is how the validation transcripts got there. This fails when the model gains
// or renames a field that is neither sent nor named as deliberately dropped, so
// the decision is made deliberately rather than by default.
func TestRecommenderInsightProjectionIsExhaustive(t *testing.T) {
	sent := make(map[string]bool, len(recommenderSentInsightFields))
	for _, n := range recommenderSentInsightFields {
		sent[n] = true
	}

	modelFields := jsonFieldNames(reflect.TypeOf(models.Insight{}))
	seen := make(map[string]bool, len(modelFields))
	for _, name := range modelFields {
		seen[name] = true
		if sent[name] {
			continue
		}
		if _, dropped := recommenderDroppedInsightFields[name]; dropped {
			continue
		}
		t.Errorf("models.Insight field %q is neither sent to the recommender nor listed as deliberately dropped.\n"+
			"Decide which it is: add it to recommenderInsight (and to recommenderSentInsightFields), or record why it "+
			"is not needed in recommenderDroppedInsightFields.", name)
	}

	// The reverse direction: a tag typo here would silently rename a field in
	// the prompt, and the recommender would read a key the model never emits.
	for _, name := range jsonFieldNames(reflect.TypeOf(recommenderInsight{})) {
		if !seen[name] {
			t.Errorf("recommenderInsight emits %q, which is not a json field of models.Insight — check the tag", name)
		}
		if !sent[name] {
			t.Errorf("recommenderInsight emits %q, which is missing from recommenderSentInsightFields", name)
		}
	}
	for _, name := range recommenderSentInsightFields {
		if !seen[name] {
			t.Errorf("recommenderSentInsightFields names %q, which models.Insight does not emit", name)
		}
	}

	// Every dropped name must still be a real field; a rename upstream would
	// otherwise leave a stale excuse behind and let the new name through.
	for name := range recommenderDroppedInsightFields {
		if !seen[name] {
			t.Errorf("recommenderDroppedInsightFields names %q, which models.Insight no longer emits — drop the entry", name)
		}
	}
}

// TestInsightsForRecommenderPrompt_KeyOrderFollowsTheModel pins the rendered
// key order to models.Insight's declaration order, so the payload a project's
// customised prompt sees does not reshuffle.
func TestInsightsForRecommenderPrompt_KeyOrderFollowsTheModel(t *testing.T) {
	projected := jsonFieldNames(reflect.TypeOf(recommenderInsight{}))
	var modelOrder []string
	sent := make(map[string]bool, len(projected))
	for _, n := range projected {
		sent[n] = true
	}
	for _, n := range jsonFieldNames(reflect.TypeOf(models.Insight{})) {
		if sent[n] {
			modelOrder = append(modelOrder, n)
		}
	}
	if !reflect.DeepEqual(projected, modelOrder) {
		t.Errorf("projection key order = %v, want the model's own order for those fields %v", projected, modelOrder)
	}
}

// TestInsightsForRecommenderPrompt_EmptyRendersAsArray covers the two empty
// inputs. Both must render as [] — a nil slice marshalling to `null` would put
// the literal "null" where the prompt promises a JSON array.
func TestInsightsForRecommenderPrompt_EmptyRendersAsArray(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []models.Insight
	}{
		{"nil", nil},
		{"empty", []models.Insight{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustMarshalProjection(t, tc.in); got != "[]" {
				t.Errorf("got %q, want %q", got, "[]")
			}
		})
	}
}

// TestInsightsForRecommenderPrompt_OmitsEmptyOptionalFields keeps absent
// optional fields out of the payload instead of spending tokens on
// `"metrics": null`.
func TestInsightsForRecommenderPrompt_OmitsEmptyOptionalFields(t *testing.T) {
	got := mustMarshalProjection(t, []models.Insight{{ID: "i-1", Name: "n", Description: "d"}})
	for _, key := range []string{`"metrics":`, `"indicators":`, `"target_segment":`, `"evidence_quality":`} {
		if strings.Contains(got, key) {
			t.Errorf("unset optional field still rendered %s in:\n%s", key, got)
		}
	}
	if strings.Contains(got, "null") {
		t.Errorf("projection rendered a null:\n%s", got)
	}
}

// TestInsightsForRecommenderPrompt_KeepsZeroValuedScalars keeps the scalars the
// recommender ranks on present even at zero. They carry no omitempty on the
// model either, and to a model an absent key does not read as zero.
func TestInsightsForRecommenderPrompt_KeepsZeroValuedScalars(t *testing.T) {
	got := mustMarshalProjection(t, []models.Insight{{ID: "i-1"}})
	for _, key := range []string{
		`"id":`, `"analysis_area":`, `"name":`, `"description":`, `"severity":`,
		`"affected_count":`, `"risk_score":`, `"confidence":`,
	} {
		if !strings.Contains(got, key) {
			t.Errorf("zero-valued %s was omitted; it must be present:\n%s", key, got)
		}
	}
}

// TestInsightsForRecommenderPrompt_DropsValidationWhateverItsShape covers the
// validation shapes a run actually produces: a verdict-only block (validation
// ran but emitted no transcript), a nil block (the fail-open insight
// filterEligibleInsights forwards), and the full two-agent transcript. None of
// them may reach the prompt, and all three must render identically.
func TestInsightsForRecommenderPrompt_DropsValidationWhateverItsShape(t *testing.T) {
	base := func() models.Insight {
		return models.Insight{ID: "i-1", AnalysisArea: "churn", Name: "n", Description: "d", Severity: "high"}
	}
	verdictOnly := base()
	verdictOnly.Validation = &valmodels.InsightValidation{Combined: valmodels.StatusSupported}
	disabled := base()
	disabled.Validation = &valmodels.InsightValidation{Combined: valmodels.StatusValidationDisabled}
	full := base()
	full.Validation = fullyPopulatedInsight("i-1").Validation

	none := mustMarshalProjection(t, []models.Insight{base()})
	for _, tc := range []struct {
		name string
		in   models.Insight
	}{
		{"verdict only", verdictOnly},
		{"validation disabled", disabled},
		{"full transcript", full},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := mustMarshalProjection(t, []models.Insight{tc.in})
			if got != none {
				t.Errorf("validation changed the payload:\n got %s\nwant %s", got, none)
			}
		})
	}
}

// TestInsightsForRecommenderPrompt_SizeIndependentOfTranscript is the fix
// stated as an assertion: however large the validation transcript grows, the
// payload does not.
func TestInsightsForRecommenderPrompt_SizeIndependentOfTranscript(t *testing.T) {
	withTranscript := fullyPopulatedInsight("i-1")

	huge := fullyPopulatedInsight("i-1")
	claims := make([]valmodels.ClaimVerdict, 0, 200)
	for i := 0; i < 200; i++ {
		claims = append(claims, valmodels.ClaimVerdict{
			ClaimText: strings.Repeat("claim ", 40),
			Reasoning: strings.Repeat("reasoning ", 60),
			Evidence:  valmodels.ClaimEvidence{Kind: "warehouse_query", QuerySQL: strings.Repeat("SELECT 1 ", 40)},
		})
	}
	huge.Validation.Verifier.ClaimVerdicts = claims
	huge.Validation.Refuter.ClaimVerdicts = claims

	small := mustMarshalProjection(t, []models.Insight{withTranscript})
	big := mustMarshalProjection(t, []models.Insight{huge})
	if small != big {
		t.Errorf("payload grew with the transcript: %d vs %d bytes", len(small), len(big))
	}
	// Sanity: the transcript really is the bulk of the untrimmed insight, so
	// this test would fail loudly if the projection regressed to sending it.
	raw, err := json.MarshalIndent([]models.Insight{huge}, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the raw insight: %v", err)
	}
	if len(raw) < 10*len(big) {
		t.Errorf("expected the untrimmed insight to dwarf the projection; raw=%d projected=%d", len(raw), len(big))
	}
}

// TestInsightsForRecommenderPrompt_DoesNotMutateInput guards the issue's second
// acceptance point. The same slice is read afterwards by the
// recommendation-validation phase (which unions SourceSteps) and persisted for
// the dashboard, so the originals must come back untouched — including the
// Metrics map, which the projection shares by reference rather than copying.
func TestInsightsForRecommenderPrompt_DoesNotMutateInput(t *testing.T) {
	insights := []models.Insight{fullyPopulatedInsight("i-1"), fullyPopulatedInsight("i-2")}
	want := []models.Insight{fullyPopulatedInsight("i-1"), fullyPopulatedInsight("i-2")}

	_ = insightsForRecommenderPrompt(insights)

	if !reflect.DeepEqual(insights, want) {
		t.Error("insightsForRecommenderPrompt mutated its input")
	}
	// Spelled out for the fields whose loss would be silent downstream.
	for i := range insights {
		if insights[i].Validation == nil || insights[i].Validation.Verifier == nil || insights[i].Validation.Refuter == nil {
			t.Errorf("insights[%d] lost its validation transcript", i)
		}
		if len(insights[i].SourceSteps) != 3 {
			t.Errorf("insights[%d].SourceSteps = %v, want 3 entries", i, insights[i].SourceSteps)
		}
		if insights[i].SQLMetadata == nil || insights[i].DescriptionMd == "" || insights[i].DiscoveredAt.IsZero() {
			t.Errorf("insights[%d] lost sql_metadata, description_md or discovered_at", i)
		}
		if got := insights[i].Metrics["churn_rate"]; got != 0.67 {
			t.Errorf("insights[%d].Metrics was written through the shared map: churn_rate = %v", i, got)
		}
	}
}
