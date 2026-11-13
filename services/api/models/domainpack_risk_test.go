package models

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
)

// The risk-configuration rules, and the one rule that matters most: a pack
// that declares no risk configuration is unchanged by their existence.
//
// Every case here starts from the same minimal valid pack the rest of the
// validator's tests use, so a failure is about the risk config and not about
// the pack around it.

// weight is an addressable float64, for the per-area override. A pointer
// because the legal range excludes zero and a value type cannot express
// "stated zero" -- which these tests then rely on being rejectable.
func weight(v float64) *float64 { return &v }

// riskPack is the minimal valid pack with a two-category taxonomy and one
// area mapped to it.
func riskPack() *DomainPack {
	pack := testDomainPack("healthcare", "clinic")
	pack.RiskTaxonomy = []PackRiskCategory{
		{ID: "compliance", Label: "Regulatory compliance", Weight: 3.0},
		{ID: "revenue_leak", Label: "Revenue leakage", Weight: 2.0},
	}
	pack.AnalysisAreas.Base[0].RiskCategories = []string{"compliance"}
	return pack
}

// TestAPackWithNoRiskConfigurationIsUnaffected -- the compatibility criterion,
// asserted at the validator rather than inferred from the rules.
func TestAPackWithNoRiskConfigurationIsUnaffected(t *testing.T) {
	pack := testDomainPack("gaming", "match3")
	if issues := RiskConfigIssues(pack); len(issues) != 0 {
		t.Errorf("a pack with no risk config produced issues: %v", issues)
	}
	if err := ValidateDomainPack(pack); err != nil {
		t.Errorf("a pack with no risk config no longer validates: %v", err)
	}
}

// TestAFullyConfiguredPackValidates -- the positive case, including an area
// mapped to several categories and a per-area override.
func TestAFullyConfiguredPackValidates(t *testing.T) {
	pack := riskPack()
	pack.AnalysisAreas.Base[0].RiskCategories = []string{"compliance", "data_quality"}
	pack.AnalysisAreas.Base[0].SeverityWeight = weight(1.5)
	pack.AnalysisAreas.Categories["clinic"] = []PackAnalysisArea{{
		ID: "billing", Name: "Billing", Description: "Billing analysis",
		Keywords: []string{"billing"}, Priority: 2,
		Prompt:         "Analyze {{DATASET}}: {{QUERY_RESULTS}}",
		RiskCategories: []string{"revenue_leak"},
	}}
	if issues := RiskConfigIssues(pack); len(issues) != 0 {
		t.Errorf("a valid risk config produced issues: %v", issues)
	}
	if err := ValidateDomainPack(pack); err != nil {
		t.Errorf("a valid risk config no longer validates: %v", err)
	}
}

// TestAStatedWeightMustBeInRange walks the boundary, on both the category and
// the area override.
//
// 0 is in the table for the category AND for the area, and they are different
// facts. A category's weight is a plain float64, so zero is the value the
// stored shape cannot distinguish from unstated -- which is why the rule
// requires one rather than defaulting. An area's override is a pointer, so a
// stated zero is reachable and must be rejected on its own terms.
func TestAStatedWeightMustBeInRange(t *testing.T) {
	cases := []struct {
		name  string
		w     float64
		valid bool
	}{
		{"zero", 0, false},
		{"negative", -1, false},
		{"the smallest sensible weight", 0.01, true},
		{"one", 1.0, true},
		{"the ceiling", scoring.MaxSeverityWeight, true},
		{"just over the ceiling", scoring.MaxSeverityWeight + 0.000001, false},
		{"far over the ceiling", 100, false},
	}
	for _, tc := range cases {
		t.Run("category weight "+tc.name, func(t *testing.T) {
			pack := riskPack()
			pack.RiskTaxonomy[0].Weight = tc.w
			issues := RiskConfigIssues(pack)
			if tc.valid && len(issues) != 0 {
				t.Errorf("weight %v should be legal, got %v", tc.w, issues)
			}
			if !tc.valid && len(issues) == 0 {
				t.Errorf("weight %v should be rejected", tc.w)
			}
		})
		t.Run("area override "+tc.name, func(t *testing.T) {
			pack := riskPack()
			pack.AnalysisAreas.Base[0].SeverityWeight = weight(tc.w)
			issues := RiskConfigIssues(pack)
			if tc.valid && len(issues) != 0 {
				t.Errorf("override %v should be legal, got %v", tc.w, issues)
			}
			if !tc.valid && len(issues) == 0 {
				t.Errorf("override %v should be rejected", tc.w)
			}
		})
	}
}

// TestAStatedZeroOverrideIsNotReadAsUnset is the pointer decision, as a test.
// With a value type this case would be indistinguishable from "no override"
// and would pass -- silently weighting the area 1.0 instead of refusing a
// configuration that asked for 0.
func TestAStatedZeroOverrideIsNotReadAsUnset(t *testing.T) {
	stated := riskPack()
	stated.AnalysisAreas.Base[0].SeverityWeight = weight(0)
	if issues := RiskConfigIssues(stated); len(issues) == 0 {
		t.Error("severity_weight: 0 was accepted; a stated zero must be rejected, not read as unset")
	}

	unset := riskPack()
	unset.AnalysisAreas.Base[0].SeverityWeight = nil
	if issues := RiskConfigIssues(unset); len(issues) != 0 {
		t.Errorf("no override should be legal, got %v", issues)
	}
}

// TestANonFiniteWeightIsRejected -- NaN and the infinities.
//
// Not reachable through JSON: encoding/json refuses both an out-of-range
// literal and the words, which is asserted below rather than assumed, because
// it is the reason the only route left is a BSON double or a Go caller. Worth
// a test anyway: every comparison against NaN is false, so a range check
// written as "reject when outside" rather than "accept when inside" would let
// a NaN weight through and multiply a magnitude to NaN.
func TestANonFiniteWeightIsRejected(t *testing.T) {
	for _, raw := range []string{`1e999`, `-1e999`, `NaN`, `Infinity`} {
		var w float64
		if err := json.Unmarshal([]byte(raw), &w); err == nil {
			t.Errorf("encoding/json accepted %s as a float64 (%v); the comment above is wrong", raw, w)
		}
	}

	for _, w := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		pack := riskPack()
		pack.AnalysisAreas.Base[0].SeverityWeight = &w
		if issues := RiskConfigIssues(pack); len(issues) == 0 {
			t.Errorf("override %v was accepted", w)
		}

		cat := riskPack()
		cat.RiskTaxonomy[0].Weight = w
		if issues := RiskConfigIssues(cat); len(issues) == 0 {
			t.Errorf("category weight %v was accepted", w)
		}
	}
}

// TestATaxonomyIDMustBeASlugAndUnique
func TestATaxonomyIDMustBeASlugAndUnique(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		valid bool
	}{
		{"lowercase", "compliance", true},
		{"with an underscore", "revenue_leak", true},
		{"with a hyphen", "data-quality", true},
		{"with digits", "tier1_risk", true},
		{"empty", "", false},
		{"uppercase", "Compliance", false},
		{"spaces", "revenue leak", false},
		{"leading underscore", "_compliance", false},
		{"trailing hyphen", "compliance-", false},
		{"single character", "c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pack := riskPack()
			// One category, so a case whose id happens to collide with the
			// second declared one is not asserting uniqueness by accident.
			pack.RiskTaxonomy = pack.RiskTaxonomy[:1]
			pack.RiskTaxonomy[0].ID = tc.id
			// The area cited the old id, so re-point it rather than asserting
			// two failures at once.
			pack.AnalysisAreas.Base[0].RiskCategories = nil
			issues := RiskConfigIssues(pack)
			if tc.valid && len(issues) != 0 {
				t.Errorf("id %q should be legal, got %v", tc.id, issues)
			}
			if !tc.valid && len(issues) == 0 {
				t.Errorf("id %q should be rejected", tc.id)
			}
		})
	}

	t.Run("declared twice", func(t *testing.T) {
		pack := riskPack()
		pack.RiskTaxonomy[1].ID = "compliance"
		issues := RiskConfigIssues(pack)
		if len(issues) == 0 {
			t.Fatal("a duplicated category id was accepted")
		}
		if !strings.Contains(strings.Join(issues, "; "), "declared twice") {
			t.Errorf("the message does not say the id is duplicated: %v", issues)
		}
	})

	t.Run("label is required", func(t *testing.T) {
		pack := riskPack()
		pack.RiskTaxonomy[0].Label = "   "
		if issues := RiskConfigIssues(pack); len(issues) == 0 {
			t.Error("a category with no label was accepted; the label is what a reader sees")
		}
	})
}

// TestAnAreaMayOnlyCiteAKnownCategory -- including the case that must be
// ACCEPTED: a global id on a pack that declares no taxonomy at all. That is
// how a pack gets risk flags without having to invent a taxonomy, and
// rejecting it would make the global set unreachable for exactly the packs
// that need it most.
func TestAnAreaMayOnlyCiteAKnownCategory(t *testing.T) {
	t.Run("an id the pack declares", func(t *testing.T) {
		pack := riskPack()
		pack.AnalysisAreas.Base[0].RiskCategories = []string{"revenue_leak"}
		if issues := RiskConfigIssues(pack); len(issues) != 0 {
			t.Errorf("a declared id was rejected: %v", issues)
		}
	})

	t.Run("a global id with no taxonomy", func(t *testing.T) {
		pack := testDomainPack("gaming", "match3")
		for _, id := range scoring.GlobalRiskCategories() {
			pack.AnalysisAreas.Base[0].RiskCategories = []string{id}
			if issues := RiskConfigIssues(pack); len(issues) != 0 {
				t.Errorf("global id %q was rejected on a pack with no taxonomy: %v", id, issues)
			}
		}
	})

	t.Run("an id nothing declares", func(t *testing.T) {
		pack := riskPack()
		pack.AnalysisAreas.Base[0].RiskCategories = []string{"churn_risk"}
		issues := RiskConfigIssues(pack)
		if len(issues) == 0 {
			t.Fatal("an undeclared category id was accepted")
		}
		joined := strings.Join(issues, "; ")
		if !strings.Contains(joined, "churn_risk") {
			t.Errorf("the message does not name the offending id: %v", issues)
		}
		// The pack generator attributes an issue to the section that has to
		// fix it by substring. An area's mapping is fixed by re-generating
		// the areas, so this message must name the AREA and must not mention
		// the taxonomy section.
		if !strings.Contains(joined, "base area 0") {
			t.Errorf("the message does not name the area: %v", issues)
		}
		if strings.Contains(joined, "risk_taxonomy") {
			t.Errorf("the message mentions risk_taxonomy, which would attribute an area's "+
				"mapping failure to the taxonomy section: %v", issues)
		}
	})

	t.Run("an empty id", func(t *testing.T) {
		pack := riskPack()
		pack.AnalysisAreas.Base[0].RiskCategories = []string{""}
		if issues := RiskConfigIssues(pack); len(issues) == 0 {
			t.Error("an empty category id was accepted")
		}
	})

	t.Run("the same id twice", func(t *testing.T) {
		pack := riskPack()
		pack.AnalysisAreas.Base[0].RiskCategories = []string{"compliance", "compliance"}
		issues := RiskConfigIssues(pack)
		if len(issues) == 0 {
			t.Fatal("a duplicated citation was accepted")
		}
		if !strings.Contains(strings.Join(issues, "; "), "cited twice") {
			t.Errorf("the message does not say the id is duplicated: %v", issues)
		}
	})

	t.Run("a category area is checked too", func(t *testing.T) {
		pack := riskPack()
		pack.AnalysisAreas.Categories["clinic"] = []PackAnalysisArea{{
			ID: "billing", Name: "Billing", Description: "Billing analysis",
			Keywords: []string{"billing"}, Priority: 2,
			Prompt:         "Analyze {{DATASET}}: {{QUERY_RESULTS}}",
			RiskCategories: []string{"nonsense"},
		}}
		issues := RiskConfigIssues(pack)
		if len(issues) == 0 {
			t.Fatal("a per-category area's mapping was not checked")
		}
		if !strings.Contains(strings.Join(issues, "; "), `category "clinic" area 0`) {
			t.Errorf("the message does not locate the area: %v", issues)
		}
	})
}

// TestRiskConfigIssuesAccumulatesWhileValidateReturnsTheFirst is the contract
// the pack generator depends on: its auto-fix loop has three attempts, and one
// issue per attempt would spend the budget on a taxonomy with three bad
// weights in it.
func TestRiskConfigIssuesAccumulatesWhileValidateReturnsTheFirst(t *testing.T) {
	pack := riskPack()
	pack.RiskTaxonomy[0].Weight = 9
	pack.RiskTaxonomy[1].Weight = -1
	pack.RiskTaxonomy[1].Label = ""
	pack.AnalysisAreas.Base[0].RiskCategories = []string{"unknown_one", "unknown_two"}
	pack.AnalysisAreas.Base[0].SeverityWeight = weight(0)

	issues := RiskConfigIssues(pack)
	if len(issues) < 6 {
		t.Errorf("expected every issue in one pass, got %d: %v", len(issues), issues)
	}

	err := ValidateDomainPack(pack)
	if err == nil {
		t.Fatal("ValidateDomainPack accepted a pack with six risk-config issues")
	}
	if err.Error() != issues[0] {
		t.Errorf("ValidateDomainPack reported %q, want the first accumulated issue %q", err.Error(), issues[0])
	}
}

// TestRiskConfigIssuesIsDeterministic -- the issue list is rendered into the
// generator's retry context, and Go randomises map iteration. An order that
// jitters re-shuffles that context on every attempt.
func TestRiskConfigIssuesIsDeterministic(t *testing.T) {
	build := func() *DomainPack {
		pack := riskPack()
		pack.AnalysisAreas.Categories = map[string][]PackAnalysisArea{}
		for _, cat := range []string{"zebra", "alpha", "middle"} {
			pack.AnalysisAreas.Categories[cat] = []PackAnalysisArea{{
				ID: cat + "_area", Name: cat, Description: cat,
				Keywords: []string{cat}, Priority: 1,
				Prompt:         "Analyze {{DATASET}}: {{QUERY_RESULTS}}",
				RiskCategories: []string{"nope_" + cat},
			}}
		}
		return pack
	}
	first := strings.Join(RiskConfigIssues(build()), "\n")
	for i := 0; i < 20; i++ {
		if got := strings.Join(RiskConfigIssues(build()), "\n"); got != first {
			t.Fatalf("issue order is not stable across runs:\n%s\n--- vs ---\n%s", first, got)
		}
	}
	// And it is sorted, so the order is a decision rather than a coincidence
	// of one map's layout.
	alpha := strings.Index(first, `"alpha"`)
	middle := strings.Index(first, `"middle"`)
	zebra := strings.Index(first, `"zebra"`)
	if alpha >= middle || middle >= zebra {
		t.Errorf("per-category issues are not in sorted key order:\n%s", first)
	}
}

// TestANilPackProducesNoIssues -- the callers include a generator whose parse
// can produce nothing at all, and a nil dereference there would report a crash
// where a validation failure belongs.
func TestANilPackProducesNoIssues(t *testing.T) {
	if issues := RiskConfigIssues(nil); issues != nil {
		t.Errorf("a nil pack produced %v", issues)
	}
}
