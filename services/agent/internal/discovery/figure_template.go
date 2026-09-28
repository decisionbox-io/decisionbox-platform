package discovery

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Turning a template back into prose.
//
// The model writes "{{f1}} of 1997 revenue" and declares f1 as data; this renders the
// reference and writes the result into the insight's own name, description and
// indicators. Everything downstream -- the API, the dashboard, the exec summary, the
// validation phase -- reads those fields and never learns this format exists.
//
// The templates are kept beside the rendered text. They are the audit trail: the one
// thing that makes it checkable after the fact that a number in the prose came from a
// declaration rather than from the model typing it.

// reFigureRef matches a reference: {{f1}}, with optional spaces.
var reFigureRef = regexp.MustCompile(`\{\{\s*([A-Za-z][A-Za-z0-9_]*)\s*\}\}`)

// reBareDigit finds a digit outside any reference, after references are blanked.
//
// This is a compliance check, not a measurement, and the distinction is the whole reason
// it is allowed to exist here. It does not extract a number, parse one, or form an
// opinion about a value: it asks whether the template contains a digit at all in a place
// where only a reference should be. A model that inlines "24.66%" instead of "{{f1}}"
// silently opts out of every check in this layer, and the format's guarantee is worth
// nothing if that is invisible.
//
// Years are excluded because a period is prose, not a measurement -- "in 1997", "the
// 1992-1997 window" -- and requiring a declaration for one would make the common case
// unwriteable.
var reBareDigit = regexp.MustCompile(`\d`)

// Dates and years are masked before the digit check. A period is prose, not a
// measurement, and requiring a declaration for one would make the common case
// unwriteable. Whole dates go first: the first live run reported "01", "06", "30" and
// "02" as undeclared figures, which were the fragments of "1998-08-02" left behind once
// only the year had been masked.
var reDateish = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b|\b\d{4}-\d{2}\b|\b(19|20)\d{2}\b`)

// renderInsightFigures replaces every reference in an insight's prose with its figure's
// rendered text, in place, and records the templates it rendered from.
//
// Unresolved references are left as written rather than blanked. A reader seeing
// "{{f7}}" in a shipped sentence knows something went wrong; a reader seeing a sentence
// with a hole in it does not, and neither does any downstream consumer.
func renderInsightFigures(insights []models.Insight) figureRenderTally {
	var tally figureRenderTally
	for i := range insights {
		ins := &insights[i]
		if len(ins.Figures) == 0 && !hasFigureRef(*ins) {
			continue
		}

		tpl := models.FigureTemplate{
			Name:        ins.Name,
			Description: ins.Description,
			Indicators:  append([]string(nil), ins.Indicators...),
		}
		ins.FigureTemplate = &tpl

		r := newFigureRenderer(ins.Figures, &tally)
		ins.Name = r.render(ins.Name)
		ins.Description = r.render(ins.Description)
		for j := range ins.Indicators {
			ins.Indicators[j] = r.render(ins.Indicators[j])
		}

		// DescriptionMd is derived from Description at parse time, so a template that
		// reached it has to be rendered too or the markdown copy keeps the references.
		if ins.DescriptionMd != "" {
			ins.DescriptionMd = r.render(ins.DescriptionMd)
		}

		if len(r.unresolved) > 0 {
			applog.WithFields(applog.Fields{
				"insight": ins.Name,
				"refs":    r.unresolved,
			}).Warn("Insight prose references figures it did not declare; the references ship as written")
		}
		if bare := bareNumeralFields(tpl); len(bare) > 0 {
			tally.inlined += len(bare)
			applog.WithFields(applog.Fields{
				"insight": ins.Name,
				"fields":  bare,
			}).Warn("Insight prose states a number directly instead of declaring it, so nothing checks it")
		}
		tally.unused += len(unusedFigures(r.used, ins.Figures))
	}
	return tally
}

// renderRecommendationFigures replaces every reference in a recommendation's prose with
// its figure's rendered text, in place, and records the templates it rendered from.
//
// The same pass as the insight one, over different fields, through the same renderer. It
// runs after the references have been resolved and their values adopted, so what lands in
// the text is the value the insight's own figure was checked at -- which is the whole
// point of the layer. A headline and a body that reference the same figure cannot
// disagree, because there is only one number and Go writes it twice.
func renderRecommendationFigures(recs []models.Recommendation) figureRenderTally {
	var tally figureRenderTally
	for i := range recs {
		rec := &recs[i]
		if len(rec.Figures) == 0 && !hasRecommendationFigureRef(*rec) {
			continue
		}

		tpl := models.RecommendationFigureTemplate{
			Title:             rec.Title,
			Description:       rec.Description,
			Actions:           append([]string(nil), rec.Actions...),
			ImpactMetric:      rec.ExpectedImpact.Metric,
			ImpactImprovement: rec.ExpectedImpact.EstimatedImprovement,
			ImpactReasoning:   rec.ExpectedImpact.Reasoning,
		}
		rec.FigureTemplate = &tpl

		r := newFigureRenderer(rec.Figures, &tally)
		rec.Title = r.render(rec.Title)
		rec.Description = r.render(rec.Description)
		for j := range rec.Actions {
			rec.Actions[j] = r.render(rec.Actions[j])
		}
		rec.ExpectedImpact.Metric = r.render(rec.ExpectedImpact.Metric)
		rec.ExpectedImpact.EstimatedImprovement = r.render(rec.ExpectedImpact.EstimatedImprovement)
		rec.ExpectedImpact.Reasoning = r.render(rec.ExpectedImpact.Reasoning)

		// DescriptionMd is derived from Description, the same as an insight's.
		if rec.DescriptionMd != "" {
			rec.DescriptionMd = r.render(rec.DescriptionMd)
		}

		if len(r.unresolved) > 0 {
			applog.WithFields(applog.Fields{
				"recommendation": rec.Title,
				"refs":           r.unresolved,
			}).Warn("Recommendation prose references figures it did not declare; the references ship as written")
		}
		if bare := bareRecommendationNumeralFields(tpl); len(bare) > 0 {
			tally.inlined += len(bare)
			applog.WithFields(applog.Fields{
				"recommendation": rec.Title,
				"fields":         bare,
			}).Warn("Recommendation prose states a number directly instead of referencing an insight figure, so nothing checks it")
		}
		tally.unused += len(unusedFigures(r.used, rec.Figures))
	}
	return tally
}

// figureRenderer renders references from one document's figure table.
//
// Shared between the insight pass and the recommendation pass deliberately. The notation
// guard it carries -- never repeat a marker the surrounding text already wrote -- was
// fixed at the suffix end after one run shipped "24.66%%", and the very next run shipped
// "(~~$151,417)" from the prefix end. One fix per end was one fix short; two copies of
// the rule in two files would be the same mistake spread wider.
type figureRenderer struct {
	byID  map[string]models.Figure
	tally *figureRenderTally
	// used records which figures the prose actually referenced, collected while
	// rendering rather than by a second scan of the template.
	used       map[string]struct{}
	unresolved []string
}

func newFigureRenderer(figures []models.Figure, tally *figureRenderTally) *figureRenderer {
	byID := make(map[string]models.Figure, len(figures))
	for _, f := range figures {
		if strings.TrimSpace(f.ID) == "" {
			continue
		}
		byID[f.ID] = f
	}
	return &figureRenderer{byID: byID, tally: tally, used: map[string]struct{}{}}
}

// render replaces every reference in s with its figure's rendered text.
//
// Unresolved references are left as written rather than blanked. A reader seeing "{{f7}}"
// in a shipped sentence knows something went wrong; a reader seeing a sentence with a hole
// in it does not, and neither does any downstream consumer.
func (r *figureRenderer) render(s string) string {
	// Index-aware rather than a plain ReplaceAllStringFunc, because the text
	// immediately around a reference decides whether a marker would be doubled.
	var b strings.Builder
	last := 0
	for _, m := range reFigureRef.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(s[last:m[0]])
		id := s[m[2]:m[3]]
		f, ok := r.byID[id]
		if !ok {
			r.unresolved = append(r.unresolved, id)
			r.tally.unresolved++
			b.WriteString(s[m[0]:m[1]])
			last = m[1]
			continue
		}
		r.used[id] = struct{}{}
		r.tally.resolved++
		text := renderFigure(f)
		// A marker the template already writes is dropped from the render, at both
		// ends. Whatever notation the surrounding text already carries, the render does
		// not repeat.
		if f.Approx && strings.HasSuffix(b.String(), "~") {
			text = strings.TrimPrefix(text, "~")
			r.tally.markerKept++
		}
		if suffix := unitSuffix(f.Unit); suffix != "" && strings.HasPrefix(s[m[1]:], suffix) {
			text = strings.TrimSuffix(text, suffix)
			r.tally.markerKept++
		}
		b.WriteString(text)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

type figureRenderTally struct {
	resolved   int
	unresolved int
	inlined    int
	unused     int
	// markerKept counts references whose notation -- a unit suffix, an approximation
	// tilde -- the template already wrote, so the render dropped its own rather than
	// doubling it.
	markerKept int
}

func hasFigureRef(ins models.Insight) bool {
	if reFigureRef.MatchString(ins.Name) || reFigureRef.MatchString(ins.Description) {
		return true
	}
	for _, s := range ins.Indicators {
		if reFigureRef.MatchString(s) {
			return true
		}
	}
	return false
}

func hasRecommendationFigureRef(rec models.Recommendation) bool {
	fields := []string{
		rec.Title, rec.Description,
		rec.ExpectedImpact.Metric, rec.ExpectedImpact.EstimatedImprovement, rec.ExpectedImpact.Reasoning,
	}
	fields = append(fields, rec.Actions...)
	for _, s := range fields {
		if reFigureRef.MatchString(s) {
			return true
		}
	}
	return false
}

// bareNumeralFields names the template fields carrying a digit outside a reference.
func bareNumeralFields(tpl models.FigureTemplate) []string {
	var out []string
	check := func(label, s string) {
		if hasBareNumeral(s) {
			out = append(out, label)
		}
	}
	check("name", tpl.Name)
	check("description", tpl.Description)
	for i, s := range tpl.Indicators {
		check(fmt.Sprintf("indicator[%d]", i), s)
	}
	return out
}

// bareRecommendationNumeralFields names the recommendation template fields carrying a
// digit outside a reference.
//
// The impact fields are excluded, and that exclusion is the measured part. A projection
// ("+1,500 first-time buyers", "a conservative 3% conversion") is the model's own estimate
// rather than a reading of the warehouse, and expected_impact is where the schema already
// puts one. Counting those as undeclared numbers would report the one place in a
// recommendation where a typed number is the correct thing to write.
func bareRecommendationNumeralFields(tpl models.RecommendationFigureTemplate) []string {
	var out []string
	check := func(label, s string) {
		if hasBareNumeral(s) {
			out = append(out, label)
		}
	}
	check("title", tpl.Title)
	check("description", tpl.Description)
	for i, s := range tpl.Actions {
		check(fmt.Sprintf("action[%d]", i), s)
	}
	return out
}

func hasBareNumeral(s string) bool {
	blanked := reFigureRef.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
	blanked = reDateish.ReplaceAllStringFunc(blanked, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
	return reBareDigit.MatchString(blanked)
}

// unusedFigures names figures nothing referenced, from the set the render collected.
//
// Not an error -- a model may declare a figure and then phrase it in words -- but worth
// counting, because a document whose declarations are mostly unreferenced is one where the
// format is being filled in rather than used.
func unusedFigures(used map[string]struct{}, figures []models.Figure) []string {
	var out []string
	for _, f := range figures {
		if _, ok := used[f.ID]; !ok {
			out = append(out, f.ID)
		}
	}
	sort.Strings(out)
	return out
}
