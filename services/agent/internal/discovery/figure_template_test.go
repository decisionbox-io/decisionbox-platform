package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func templatedInsight() models.Insight {
	return models.Insight{
		Name:        "Revenue concentration: top decile generates {{f1}} of 1997 revenue",
		Description: "The top decile contributed {{f1}} ({{f2}} of {{f3}}).",
		Indicators:  []string{"Top decile: {{f4}} customers, {{f2}}, {{f1}} of revenue"},
		SourceSteps: []int{48},
		Figures: []models.Figure{
			{ID: "f1", Value: 24.66, Unit: models.UnitPercent, Decimals: 2, Step: 48, Kind: models.FigureCell, Column: "pct", Row: "decile = 1"},
			{ID: "f2", Value: 8476238553, Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 2, Step: 48, Kind: models.FigureCell, Column: "revenue", Row: "decile = 1"},
			{ID: "f3", Value: 34373633413, Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 2, Step: 48, Kind: models.FigureSum, Column: "revenue"},
			{ID: "f4", Value: 8668, Unit: models.UnitCount, Step: 48, Kind: models.FigureCell, Column: "customers", Row: "decile = 1"},
		},
	}
}

// TestTemplate_RendersEveryReference is the basic contract: what ships is ordinary prose,
// so nothing downstream has to know this format exists.
func TestTemplate_RendersEveryReference(t *testing.T) {
	ins := []models.Insight{templatedInsight()}
	tally := renderInsightFigures(ins)

	if tally.unresolved != 0 || tally.inlined != 0 {
		t.Fatalf("unresolved=%d inlined=%d, want 0 and 0", tally.unresolved, tally.inlined)
	}
	// f1 three times, f2 twice, f3 and f4 once each.
	if tally.resolved != 7 {
		t.Errorf("resolved = %d references, want 7", tally.resolved)
	}
	for _, field := range []string{ins[0].Name, ins[0].Description, ins[0].Indicators[0]} {
		if strings.Contains(field, "{{") {
			t.Errorf("a reference survived into the shipped text: %q", field)
		}
	}
	if !strings.Contains(ins[0].Name, "24.66%") {
		t.Errorf("name was not rendered: %q", ins[0].Name)
	}
	if !strings.Contains(ins[0].Description, "$8.48B") || !strings.Contains(ins[0].Description, "$34.37B") {
		t.Errorf("description was not rendered: %q", ins[0].Description)
	}
	if !strings.Contains(ins[0].Indicators[0], "8,668") {
		t.Errorf("indicator was not rendered: %q", ins[0].Indicators[0])
	}
}

// TestTemplate_KeepsTheAuthoredProse asserts the audit trail survives. The rendered fields
// are what readers consume; the template is what makes it checkable afterwards that a
// number came from a declaration rather than from the model typing it.
func TestTemplate_KeepsTheAuthoredProse(t *testing.T) {
	ins := []models.Insight{templatedInsight()}
	before := ins[0].Description
	renderInsightFigures(ins)

	if ins[0].FigureTemplate == nil {
		t.Fatal("the template was not kept")
	}
	if ins[0].FigureTemplate.Description != before {
		t.Errorf("template description = %q, want the authored text %q", ins[0].FigureTemplate.Description, before)
	}
	if len(ins[0].FigureTemplate.Indicators) != 1 || !strings.Contains(ins[0].FigureTemplate.Indicators[0], "{{f4}}") {
		t.Errorf("template indicators lost their references: %v", ins[0].FigureTemplate.Indicators)
	}
	// The kept indicators must be a copy, not an alias of the slice that was rendered in
	// place -- otherwise the audit trail is overwritten by the thing it audits.
	if ins[0].FigureTemplate.Indicators[0] == ins[0].Indicators[0] {
		t.Error("the template shares its indicator backing array with the rendered text")
	}
}

// TestTemplate_InlinedNumberIsReported is what keeps the format honest. A model that types
// 24.66% instead of {{f1}} opts that number out of every check in this layer, and the
// guarantee is worth nothing if that is invisible.
func TestTemplate_InlinedNumberIsReported(t *testing.T) {
	ins := []models.Insight{templatedInsight()}
	ins[0].Description = "The top decile contributed 24.66% of revenue, and {{f2}} in absolute terms."

	tally := renderInsightFigures(ins)
	if tally.inlined == 0 {
		t.Fatal("a number typed directly into the prose was not reported")
	}
	fields := bareNumeralFields(*ins[0].FigureTemplate)
	found := false
	for _, f := range fields {
		if f == "description" {
			found = true
		}
	}
	if !found {
		t.Errorf("the offending field was not named: %v", fields)
	}
}

// TestTemplate_YearsAreNotTreatedAsFigures — a period is prose, not a measurement.
// Requiring a declaration for "in 1997" would make the common case unwriteable.
func TestTemplate_YearsAreNotTreatedAsFigures(t *testing.T) {
	for _, prose := range []string{
		"Revenue was flat across 1992-1997 at {{f1}} per year.",
		"In 1997 the top decile held {{f1}}.",
		"The window runs to 2026 and holds {{f1}}.",
	} {
		t.Run(prose, func(t *testing.T) {
			if hasBareNumeral(prose) {
				t.Errorf("a year was read as an undeclared figure: %q", prose)
			}
		})
	}
	// But a real measurement still is one.
	if !hasBareNumeral("The top decile held 24.66% of revenue.") {
		t.Error("a genuine inlined figure was not detected")
	}
}

// TestTemplate_UnresolvedReferenceShipsAsWritten. A reader seeing "{{f7}}" knows something
// went wrong; a reader seeing a sentence with a hole in it does not, and nor does any
// downstream consumer.
func TestTemplate_UnresolvedReferenceShipsAsWritten(t *testing.T) {
	ins := []models.Insight{templatedInsight()}
	ins[0].Description = "The top decile contributed {{f1}} and {{f9}} besides."

	tally := renderInsightFigures(ins)
	if tally.unresolved != 1 {
		t.Fatalf("unresolved = %d, want 1", tally.unresolved)
	}
	if !strings.Contains(ins[0].Description, "{{f9}}") {
		t.Errorf("the unresolved reference was silently dropped: %q", ins[0].Description)
	}
	if !strings.Contains(ins[0].Description, "24.66%") {
		t.Errorf("the resolvable reference was not rendered: %q", ins[0].Description)
	}
}

// TestTemplate_UnusedFigureIsCountedNotAnError. A model may declare a figure and then
// phrase it in words; that is not wrong. But a document whose declarations are mostly
// unreferenced is one where the format is being filled in rather than used.
func TestTemplate_UnusedFigureIsCountedNotAnError(t *testing.T) {
	ins := []models.Insight{templatedInsight()}
	ins[0].Figures = append(ins[0].Figures, models.Figure{
		ID: "f9", Value: 17, Unit: models.UnitCount, Step: 48, Kind: models.FigureCell, Column: "customers", Row: "decile = 9",
	})
	tally := renderInsightFigures(ins)
	if tally.unused != 1 {
		t.Fatalf("unused = %d, want 1", tally.unused)
	}
	if tally.unresolved != 0 {
		t.Errorf("an unused figure was counted as an unresolved reference")
	}
}

// TestTemplate_UntouchedWhenThereIsNothingToRender keeps the pass free on a document that
// declares no figures, which is what a legacy or a hand-written insight looks like.
func TestTemplate_UntouchedWhenThereIsNothingToRender(t *testing.T) {
	ins := []models.Insight{{
		Name:        "No figures here",
		Description: "Prose only.",
	}}
	before := ins[0]
	tally := renderInsightFigures(ins)
	if tally != (figureRenderTally{}) {
		t.Fatalf("tally = %+v, want zero", tally)
	}
	if ins[0].Name != before.Name || ins[0].Description != before.Description {
		t.Error("the insight was modified")
	}
	if ins[0].FigureTemplate != nil {
		t.Error("a template was stored for an insight that has none")
	}
}

// TestTemplate_MarkdownCopyIsRenderedToo. DescriptionMd is derived from Description at
// parse time, so a template that reached it has to be rendered or the markdown copy keeps
// the references — and the dashboard renders that copy, not the plain one.
func TestTemplate_MarkdownCopyIsRenderedToo(t *testing.T) {
	ins := []models.Insight{templatedInsight()}
	ins[0].DescriptionMd = "**{{f1}}** of 1997 revenue"
	renderInsightFigures(ins)
	if strings.Contains(ins[0].DescriptionMd, "{{") {
		t.Fatalf("the markdown copy kept its references: %q", ins[0].DescriptionMd)
	}
	if !strings.Contains(ins[0].DescriptionMd, "24.66%") {
		t.Fatalf("the markdown copy was not rendered: %q", ins[0].DescriptionMd)
	}
}

// TestTemplate_DoesNotDoubleAUnitSuffix is the red-proof for the second defect the first
// live run shipped: a template writing its own "%" or its own unit word beside a reference
// whose unit renders the same thing.
//
// The run shipped "Inter-order interval averages 180.1 days days across repeat buyers".
// The days unit is gone -- a word is prose -- but the collision is still reachable for the
// symbol units, so the render checks what follows the reference before appending.
func TestTemplate_DoesNotDoubleAUnitSuffix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		template string
		fig      models.Figure
		want     string
	}{
		{"the template writes the percent sign", "Share is {{f1}}% of revenue",
			models.Figure{ID: "f1", Value: 24.66, Unit: models.UnitPercent, Decimals: 2},
			"Share is 24.66% of revenue"},
		{"the template leaves the percent sign to the unit", "Share is {{f1}} of revenue",
			models.Figure{ID: "f1", Value: 24.66, Unit: models.UnitPercent, Decimals: 2},
			"Share is 24.66% of revenue"},
		{"the template writes the multiple sign", "Spend is {{f1}}x higher",
			models.Figure{ID: "f1", Value: 4.83, Unit: models.UnitMultiple, Decimals: 2},
			"Spend is 4.83x higher"},
		{"a word unit is the prose's job", "Interval averages {{f1}} days",
			models.Figure{ID: "f1", Value: 180.06, Unit: models.UnitPlain, Decimals: 1},
			"Interval averages 180.1 days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ins := []models.Insight{{Name: tc.template, SourceSteps: []int{1}, Figures: []models.Figure{tc.fig}}}
			renderInsightFigures(ins)
			if ins[0].Name != tc.want {
				t.Fatalf("rendered %q, want %q", ins[0].Name, tc.want)
			}
		})
	}
}

// TestTemplate_DatesAreNotUndeclaredFigures. The run reported "01", "06", "30" and "02" as
// numbers stated without a declaration; they were the fragments of "1998-08-02" left behind
// when only the year had been masked.
func TestTemplate_DatesAreNotUndeclaredFigures(t *testing.T) {
	for _, prose := range []string{
		"Ship dates run 1992-01-02 to 1998-08-02 across the window.",
		"The 1998-08 partial month holds {{f1}}.",
		"Across 1992-1997 revenue held near {{f1}}.",
	} {
		t.Run(prose, func(t *testing.T) {
			if hasBareNumeral(prose) {
				t.Errorf("a date was read as an undeclared figure: %q", prose)
			}
		})
	}
	if !hasBareNumeral("The top decile held 24.66% of revenue.") {
		t.Error("a genuine inlined figure was not detected")
	}
}
