package validation

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discipline"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// newDisciplineTestValidator constructs a validator suitable for prompt-shape
// assertions. It deliberately avoids wiring an LLM or executor — these tests
// only exercise prompt construction, never run a verification.
func newDisciplineTestValidator() *InsightValidator {
	return NewInsightValidator(InsightValidatorOptions{
		Warehouse: testutil.NewMockWarehouseProvider("test_dataset"),
		Dataset:   "test_dataset",
		Filter:    "",
	})
}

func TestBuildVerificationPrompt_ContainsVerifierRules(t *testing.T) {
	// The platform-enforced verifier rules (V1-V4) must appear in the
	// generated prompt. This is the headline injection assertion: a
	// reviewer can read this test and know that pressing the verifier
	// always carries the discipline gates, regardless of insight shape.
	v := newDisciplineTestValidator()
	insight := &models.Insight{
		ID:            "test-1",
		AnalysisArea:  "revenue",
		Name:          "Weekly revenue trend",
		Description:   "Revenue rose 16% across the queried window.",
		Severity:      "medium",
		AffectedCount: 100,
		SourceSteps:   []int{1, 2},
	}

	got := v.buildVerificationPrompt(insight, nil, nil, false, false)

	if !strings.Contains(got, discipline.VerifierRules()) {
		t.Errorf("buildVerificationPrompt did not inject VerifierRules() into the prompt")
	}
}

func TestBuildVerificationPrompt_VerifierRulesPresentInLoopMode(t *testing.T) {
	// loopMode=true changes the action-instructions block from "raw SQL"
	// to a JSON envelope; the verifier rules must appear in BOTH modes
	// because the lookup-then-query loop calls buildVerificationPrompt
	// once per round.
	v := newDisciplineTestValidator()
	insight := &models.Insight{
		ID:            "test-loop",
		AnalysisArea:  "engagement",
		Name:          "Top week claim",
		Description:   "Week 19 is the all-time highest in revenue.",
		Severity:      "high",
		AffectedCount: 50000,
		SourceSteps:   []int{29, 30},
	}

	got := v.buildVerificationPrompt(insight, nil, nil, false, true)

	if !strings.Contains(got, discipline.VerifierRules()) {
		t.Errorf("buildVerificationPrompt(loopMode=true) did not inject VerifierRules()")
	}
}

func TestBuildVerificationPrompt_VerifierRulesPresentWithLookupContext(t *testing.T) {
	// Non-nil lookups + notFound + truncated path renders a "Lookup
	// Notices" section. The verifier rules must still appear — they
	// are injected after the action instructions in the format string,
	// independent of the notices block.
	v := newDisciplineTestValidator()
	insight := &models.Insight{
		ID:            "test-lookup",
		AnalysisArea:  "monetization",
		Name:          "ARPU peaked in cohort C",
		Description:   "Cohort C has the highest ARPU on record in queried window.",
		Severity:      "high",
		AffectedCount: 1200,
		SourceSteps:   []int{5},
	}
	lookups := []ai.LookupTable{}
	notFound := []string{"prod.missing_table"}

	got := v.buildVerificationPrompt(insight, lookups, notFound, true, true)

	if !strings.Contains(got, discipline.VerifierRules()) {
		t.Errorf("buildVerificationPrompt did not inject VerifierRules() when lookup-notice section is rendered")
	}
	// Sanity: the lookup-notice section is also present (proves we're
	// exercising the right code path).
	if !strings.Contains(got, "Lookup Notices") {
		t.Errorf("expected the Lookup Notices section in the rendered prompt for this path")
	}
}

func TestBuildVerificationPrompt_PreservesExistingPromptContract(t *testing.T) {
	// Injecting VerifierRules must NOT clobber the existing
	// table-naming, dialect-quoting, COUNT-alias, and JSON-payload
	// instructions. The previous prompt contract is load-bearing for
	// the verifier's SQL generation; this test guards against a
	// future refactor that accidentally drops parts of it.
	v := newDisciplineTestValidator()
	insight := &models.Insight{
		ID:            "test-contract",
		AnalysisArea:  "retention",
		Name:          "D7 retention dropped",
		Description:   "D7 retention fell 3pp week over week.",
		AffectedCount: 800,
		SourceSteps:   []int{10},
	}

	got := v.buildVerificationPrompt(insight, nil, nil, false, false)

	mustContain := []struct {
		label string
		want  string
	}{
		{"available-datasets header", "**Available Datasets**"},
		{"SQL dialect header", "**SQL Dialect**"},
		{"filter header", "**Filter**"},
		{"critical-table-name-rules section", "**CRITICAL TABLE NAME RULES**"},
		{"insight-to-verify section", "**Insight to verify**"},
		{"AS count alias instruction", `ALWAYS alias the result as "count"`},
		{"fully qualified table names rule", "fully qualified table names"},
		{"prefer COUNT DISTINCT user_id", "COUNT(DISTINCT user_id)"},
	}
	for _, m := range mustContain {
		if !strings.Contains(got, m.want) {
			t.Errorf("existing prompt contract broken — missing %s: %q", m.label, m.want)
		}
	}
}

func TestBuildVerificationPrompt_RegressionCaseStudy(t *testing.T) {
	// Regression fixture for the case that triggered this work: an
	// insight whose name and description claim an "all-time" highest
	// but whose source steps cover only a partial window. The
	// validator's rendered prompt must carry VerifierRules() — V1
	// instructs the LLM to construct ranking SQL that returns 0 when
	// the claim is wrong, V3 catches the all-time/window mismatch,
	// and V4 catches editorial vocabulary (none present here, but the
	// rule is in scope).
	v := newDisciplineTestValidator()
	insight := &models.Insight{
		ID:            "0d3ea19b-2784-4361-8659-f57ebe15f72a",
		AnalysisArea:  "revenue",
		Name:          "Weekly revenue trend — Week 19 record (all-time)",
		Description:   "Week 19 is the all-time highest week at 59.1M, exceeding all previously observed weeks.",
		Severity:      "high",
		AffectedCount: 59862,
		Indicators: []string{
			"Week 19 = 59.1M (last 10 weeks)",
			"YoY growth 16% across last 5 weeks",
		},
		TargetSegment: "All retail customers",
		SourceSteps:   []int{29, 30, 64, 66},
	}

	got := v.buildVerificationPrompt(insight, nil, nil, false, false)

	// 1. Discipline rules must be present.
	if !strings.Contains(got, discipline.VerifierRules()) {
		t.Fatalf("regression fixture: VerifierRules() not injected into the prompt")
	}

	// 2. The V1 mechanism (CASE WHEN ranking matches THEN run actual
	//    count ELSE 0) must be described so the LLM constructs SQL
	//    that verifies BOTH the ranking AND the affected count in one
	//    statement. The THEN branch must run an independent aggregate
	//    (not just return the claimed number) so the ratio check still
	//    catches inflated/deflated counts.
	mustDescribeV1 := []string{
		"V1. RE-VERIFY THE HEADLINE SUPERLATIVE",
		"CASE",
		"ELSE 0",
		"actual aggregate",
		"self-confirming",
	}
	for _, want := range mustDescribeV1 {
		if !strings.Contains(got, want) {
			t.Errorf("V1 ranking-SQL guidance missing fragment %q", want)
		}
	}

	// 3. The insight JSON must be in the prompt so the LLM has the
	//    headline to verify. The "all-time" claim is in the description.
	if !strings.Contains(got, "all-time") {
		t.Errorf("insight JSON not rendered into the prompt (missing 'all-time' anchor)")
	}
}
