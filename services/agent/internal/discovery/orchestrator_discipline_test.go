package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/discipline"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// newDisciplineOrch builds an Orchestrator sufficient for prompt-assembly
// tests. The mock warehouse provides SQLDialect / QuoteRef so the dialect
// token substitution path runs.
func newDisciplineOrch() *Orchestrator {
	return &Orchestrator{
		warehouse: testutil.NewMockWarehouseProvider("test_dataset"),
	}
}

// --- buildBaseContext ---

func TestBuildBaseContext_SubstitutesAllTemplateVariablesAndAppendsDiscipline(t *testing.T) {
	o := newDisciplineOrch()
	template := "Profile: {{PROFILE}}\nPrevious: {{PREVIOUS_CONTEXT}}\nLanguage: {{LANGUAGE}}"
	got := o.buildBaseContext(template, "<the-profile>", "<the-previous-context>", "English", "test_dataset")

	wantSubstrings := []string{
		"Profile: <the-profile>",
		"Previous: <the-previous-context>",
		"Language: English",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(got, want) {
			t.Errorf("buildBaseContext did not substitute template variable; missing %q", want)
		}
	}

	if !strings.Contains(got, discipline.BaseContextRules()) {
		t.Errorf("buildBaseContext did not append BaseContextRules() — the platform-enforced discipline rules are missing")
	}

	// No unsubstituted placeholders left.
	for _, leftover := range []string{"{{PROFILE}}", "{{PREVIOUS_CONTEXT}}", "{{LANGUAGE}}"} {
		if strings.Contains(got, leftover) {
			t.Errorf("buildBaseContext left placeholder %q in output", leftover)
		}
	}
}

func TestBuildBaseContext_EmptyTemplateStillEmitsRules(t *testing.T) {
	// A pack with no base-context template (or a custom override that
	// blanked it out) must still produce a prompt carrying the
	// discipline rules — the runtime is authoritative regardless of
	// pack content.
	o := newDisciplineOrch()
	got := o.buildBaseContext("", "p", "pc", "English", "test_dataset")
	if !strings.Contains(got, discipline.BaseContextRules()) {
		t.Errorf("empty template — discipline rules must still be appended")
	}
}

func TestBuildBaseContext_PlaceholdersInSubstitutedValuesArePassedThrough(t *testing.T) {
	// If the profile JSON itself contains "{{LANGUAGE}}" as data (a
	// pathological case), the ReplaceAll on LANGUAGE happens AFTER the
	// profile is in place, so the profile's literal "{{LANGUAGE}}"
	// gets clobbered with the language value. Document this contract
	// — the test pins the order so a future refactor that reorders
	// the substitutions trips the test.
	o := newDisciplineOrch()
	got := o.buildBaseContext("{{PROFILE}}", "this profile mentions {{LANGUAGE}}", "ignored", "French", "test_dataset")
	if !strings.Contains(got, "this profile mentions French") {
		t.Errorf("substitution order regressed — LANGUAGE substitution should apply to whatever PROFILE rendered (got %q)", firstLineOfBaseContextTest(got))
	}
}

// firstLineOfBaseContextTest is a small utility for error messages.
func firstLineOfBaseContextTest(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- buildAnalysisAreaPrompt ---

func TestBuildAnalysisAreaPrompt_SubstitutesAndAppendsDiscipline(t *testing.T) {
	o := newDisciplineOrch()
	got := o.buildAnalysisAreaPrompt(
		"BASE_CONTEXT_BODY",
		"Area template — dataset: {{DATASET}}, total: {{TOTAL_QUERIES}}, rows: {{QUERY_RESULTS}}",
		"events_prod",
		7,
		"[{\"step\":1}]",
		"events_prod",
	)
	wants := []string{
		"BASE_CONTEXT_BODY",
		"dataset: events_prod",
		"total: 7",
		"rows: [{\"step\":1}]",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("buildAnalysisAreaPrompt missing substring %q", w)
		}
	}
	if !strings.Contains(got, discipline.AnalysisRules()) {
		t.Errorf("buildAnalysisAreaPrompt did not append AnalysisRules()")
	}
}

func TestBuildAnalysisAreaPrompt_CustomAreaWithArbitraryBodyStillCarriesRules(t *testing.T) {
	// Regression for the design intent: user-added custom analysis
	// areas (created via PUT /api/v1/projects/{id}/prompts with
	// is_custom=true) carry arbitrary user-supplied prompt content.
	// The discipline rules must append regardless of what the area's
	// body looks like — empty, malformed, with or without template
	// variables, in any language.
	o := newDisciplineOrch()
	customBodies := []string{
		"",
		"single line area",
		"# Markdown heading\n\nSome content",
		"Body with unsubstituted {{UNKNOWN}} placeholder",
		"Body with non-Latin chars: ürün, иванов, 你好",
		"Multi\nline\nbody\nwith\nnewlines",
	}
	for _, body := range customBodies {
		got := o.buildAnalysisAreaPrompt("BASE", body, "ds", 0, "[]", "ds")
		if !strings.Contains(got, discipline.AnalysisRules()) {
			t.Errorf("custom-area body %q dropped the discipline rules", firstLineOfBaseContextTest(body))
		}
		// The user-supplied body must be present somewhere in the output —
		// the rules are an addition, not a replacement.
		if body != "" && !strings.Contains(got, body) {
			t.Errorf("custom-area body %q was lost in assembly", firstLineOfBaseContextTest(body))
		}
	}
}

func TestBuildAnalysisAreaPrompt_TotalQueriesZero(t *testing.T) {
	// Boundary case: an analysis area with zero relevant steps still
	// emits a prompt — the rules apply to the empty case too, so the
	// LLM is told to follow them even when no data is provided (it
	// should return an empty insights array, but the rules document
	// the constraint).
	o := newDisciplineOrch()
	got := o.buildAnalysisAreaPrompt("BASE", "Template {{TOTAL_QUERIES}}", "ds", 0, "[]", "ds")
	if !strings.Contains(got, "Template 0") {
		t.Errorf("zero totalQueries not substituted into prompt")
	}
	if !strings.Contains(got, discipline.AnalysisRules()) {
		t.Errorf("zero-step area still requires discipline rules")
	}
}

// --- buildRecommendationsPrompt ---

func TestBuildRecommendationsPrompt_SubstitutesAndAppendsDiscipline(t *testing.T) {
	o := newDisciplineOrch()
	got := o.buildRecommendationsPrompt(
		"BASE_CONTEXT",
		"Template summary={{INSIGHTS_SUMMARY}} data={{INSIGHTS_DATA}}",
		"3 insights",
		"[{\"id\":\"a\"}]",
		"test_dataset",
	)
	wants := []string{
		"BASE_CONTEXT",
		"summary=3 insights",
		"data=[{\"id\":\"a\"}]",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("buildRecommendationsPrompt missing substring %q", w)
		}
	}
	if !strings.Contains(got, discipline.RecommendationsRules()) {
		t.Errorf("buildRecommendationsPrompt did not append RecommendationsRules()")
	}
}

func TestBuildRecommendationsPrompt_DiscoveryDateIsCurrentDate(t *testing.T) {
	// The {{DISCOVERY_DATE}} substitution uses time.Now() in YYYY-MM-DD
	// format. We can't assert the exact value without freezing time,
	// but we can assert the placeholder is gone and the substituted
	// value looks like an ISO date.
	o := newDisciplineOrch()
	got := o.buildRecommendationsPrompt("BASE", "Today: {{DISCOVERY_DATE}}", "s", "[]", "ds")
	if strings.Contains(got, "{{DISCOVERY_DATE}}") {
		t.Errorf("DISCOVERY_DATE placeholder not substituted")
	}
	// ISO date regex would be heavier than the test deserves — assert
	// there's a hyphen sequence after "Today: " which any YYYY-MM-DD
	// string satisfies.
	idx := strings.Index(got, "Today: ")
	if idx < 0 {
		t.Fatal("'Today: ' anchor not present")
	}
	tail := got[idx+len("Today: "):]
	if len(tail) < 10 || tail[4] != '-' || tail[7] != '-' {
		t.Errorf("substituted DISCOVERY_DATE does not look like YYYY-MM-DD: %q", tail[:min(20, len(tail))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestBuildRecommendationsPrompt_NoInsightsStillEmitsRules(t *testing.T) {
	// Even when the LLM is invoked with zero insights as input
	// (insightsJSON = "[]"), the recommendation discipline rules
	// must still be appended — they constrain whatever the LLM does
	// produce.
	o := newDisciplineOrch()
	got := o.buildRecommendationsPrompt("BASE", "tpl", "0 insights", "[]", "ds")
	if !strings.Contains(got, discipline.RecommendationsRules()) {
		t.Errorf("empty-insights recommendations still require discipline rules")
	}
}

// --- cross-cutting: all three sites carry their respective rule blocks ---

func TestAllPromptBuildersAppendTheCorrectRuleBlock(t *testing.T) {
	o := newDisciplineOrch()
	cases := []struct {
		name      string
		got       string
		wantRules string
		// All three rule blocks are distinct; assert the OTHER blocks
		// don't appear, so we catch a wiring mix-up (e.g. analysis-area
		// builder accidentally appending RecommendationsRules).
		notWantRules []string
	}{
		{
			"base context",
			o.buildBaseContext("tpl", "p", "pc", "English", "ds"),
			discipline.BaseContextRules(),
			[]string{discipline.AnalysisRules(), discipline.RecommendationsRules()},
		},
		{
			"analysis area",
			o.buildAnalysisAreaPrompt("BASE", "area", "ds", 1, "[]", "ds"),
			discipline.AnalysisRules(),
			[]string{discipline.BaseContextRules(), discipline.RecommendationsRules()},
		},
		{
			"recommendations",
			o.buildRecommendationsPrompt("BASE", "rec_tpl", "summary", "[]", "ds"),
			discipline.RecommendationsRules(),
			[]string{discipline.BaseContextRules(), discipline.AnalysisRules()},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(tc.got, tc.wantRules) {
				t.Errorf("%s: missing expected rule block", tc.name)
			}
			for _, notWant := range tc.notWantRules {
				if strings.Contains(tc.got, notWant) {
					t.Errorf("%s: contains wrong rule block — wires are crossed", tc.name)
				}
			}
		})
	}
}
