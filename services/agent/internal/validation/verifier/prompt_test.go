package verifier

import (
	"strings"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// Codex MVP-r1 MEDIUM #2 — every {{TOKEN}} placeholder MUST be
// substituted; if any `{{` substring remains in the rendered prompt
// the production code has a silent prompt bug.
func TestRenderSystemPrompt_NoUnresolvedPlaceholders(t *testing.T) {
	systems, err := loadSystemPrompts()
	if err != nil {
		t.Fatalf("loadSystemPrompts: %v", err)
	}
	b := Bundle{
		Doc:                  DocDigest{Kind: valmodels.DocInsight, Headline: "h"},
		Warehouse:            WarehouseInfo{Dialect: "BigQuery Standard SQL", FilterField: "country", FilterValue: "US"},
		Discovery:            DiscoveryContext{Language: "English"},
		SourceStepsTruncated: true,
		SourceStepsOmitted:   2,
	}
	for _, mode := range []valmodels.AgentMode{valmodels.ModeVerifier, valmodels.ModeRefuter} {
		t.Run(string(mode), func(t *testing.T) {
			rendered := renderSystemPrompt(systems[mode], mode, b, 0.20, 30)
			if strings.Contains(rendered, "{{") {
				t.Errorf("unsubstituted placeholder in rendered %s prompt; rendered prompt:\n%s", mode, rendered)
			}
			// Concrete value spot checks.
			if !strings.Contains(rendered, "BigQuery Standard SQL") {
				t.Errorf("dialect not substituted")
			}
			if !strings.Contains(rendered, "English") {
				t.Errorf("language not substituted")
			}
			if !strings.Contains(rendered, "country = 'US'") {
				t.Errorf("filter clause not rendered; expected country = 'US'")
			}
			if !strings.Contains(rendered, "2 source step(s) cited by this insight were omitted") {
				t.Errorf("truncation notice not rendered with N=2 + docKind")
			}
		})
	}
}

// When no truncation, the notice line should not appear.
func TestRenderSystemPrompt_NoTruncationNoticeWhenAllPresent(t *testing.T) {
	systems, _ := loadSystemPrompts()
	b := Bundle{
		Doc:       DocDigest{Kind: valmodels.DocInsight, Headline: "h"},
		Warehouse: WarehouseInfo{Dialect: "PostgreSQL"},
	}
	rendered := renderSystemPrompt(systems[valmodels.ModeVerifier], valmodels.ModeVerifier, b, 0.20, 30)
	if strings.Contains(rendered, "Evidence omitted") {
		t.Errorf("notice should NOT appear when SourceStepsTruncated is false")
	}
}

// Language defaults to English when discovery.Language is empty.
func TestRenderSystemPrompt_LanguageDefault(t *testing.T) {
	systems, _ := loadSystemPrompts()
	b := Bundle{
		Doc:       DocDigest{Kind: valmodels.DocInsight},
		Warehouse: WarehouseInfo{Dialect: "BigQuery Standard SQL"},
	}
	rendered := renderSystemPrompt(systems[valmodels.ModeVerifier], valmodels.ModeVerifier, b, 0.20, 30)
	if !strings.Contains(rendered, "English") {
		t.Errorf("language default 'English' missing from rendered prompt")
	}
}

// Recommendation kind surfaces "recommendation" in the truncation
// notice.
func TestRenderSystemPrompt_RecommendationKindInNotice(t *testing.T) {
	systems, _ := loadSystemPrompts()
	b := Bundle{
		Doc:                  DocDigest{Kind: valmodels.DocRecommendation, Headline: "h"},
		Warehouse:            WarehouseInfo{Dialect: "Snowflake"},
		SourceStepsTruncated: true,
		SourceStepsOmitted:   1,
	}
	rendered := renderSystemPrompt(systems[valmodels.ModeVerifier], valmodels.ModeVerifier, b, 0.20, 30)
	if !strings.Contains(rendered, "cited by this recommendation") {
		t.Errorf("docKind=recommendation should appear in notice; got:\n%s", rendered)
	}
}

// Min sample size: substituted as an integer; the refuter prompt
// uses it in the small-sample threshold rule. Verifier prompt does
// not currently mention it (the rule is refuter-specific).
func TestRenderSystemPrompt_MinSampleSize(t *testing.T) {
	systems, _ := loadSystemPrompts()
	b := Bundle{
		Doc:       DocDigest{Kind: valmodels.DocInsight, Headline: "h"},
		Warehouse: WarehouseInfo{Dialect: "BigQuery Standard SQL"},
	}
	rendered := renderSystemPrompt(systems[valmodels.ModeRefuter], valmodels.ModeRefuter, b, 0.20, 50)
	if !strings.Contains(rendered, "n ≥ 50") {
		t.Errorf("min-sample threshold 50 missing from rendered refuter prompt")
	}
	if strings.Contains(rendered, "{{MIN_SAMPLE_SIZE}}") {
		t.Errorf("placeholder MIN_SAMPLE_SIZE unsubstituted")
	}
}

// Numeric tolerance: substituted as an integer percentage; both
// prompts must render the value and reference it in the tolerance rule.
func TestRenderSystemPrompt_NumericTolerance(t *testing.T) {
	systems, _ := loadSystemPrompts()
	b := Bundle{
		Doc:       DocDigest{Kind: valmodels.DocInsight, Headline: "h"},
		Warehouse: WarehouseInfo{Dialect: "BigQuery Standard SQL"},
	}
	for _, mode := range []valmodels.AgentMode{valmodels.ModeVerifier, valmodels.ModeRefuter} {
		t.Run(string(mode), func(t *testing.T) {
			rendered := renderSystemPrompt(systems[mode], mode, b, 0.25, 30)
			if !strings.Contains(rendered, "±25%") {
				t.Errorf("tolerance 25%% missing from rendered %s prompt; want '±25%%'\n%s", mode, rendered[:500])
			}
			if strings.Contains(rendered, "{{NUMERIC_TOLERANCE_PCT}}") {
				t.Errorf("placeholder NUMERIC_TOLERANCE_PCT unsubstituted")
			}
		})
	}
}
