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
var reYearish = regexp.MustCompile(`\b(19|20)\d{2}\b`)

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

		byID := make(map[string]models.Figure, len(ins.Figures))
		for _, f := range ins.Figures {
			if strings.TrimSpace(f.ID) == "" {
				continue
			}
			byID[f.ID] = f
		}

		tpl := models.FigureTemplate{
			Name:        ins.Name,
			Description: ins.Description,
			Indicators:  append([]string(nil), ins.Indicators...),
		}
		ins.FigureTemplate = &tpl

		var unresolved []string
		render := func(s string) string {
			return reFigureRef.ReplaceAllStringFunc(s, func(ref string) string {
				id := reFigureRef.FindStringSubmatch(ref)[1]
				f, ok := byID[id]
				if !ok {
					unresolved = append(unresolved, id)
					return ref
				}
				tally.resolved++
				return renderFigure(f)
			})
		}
		ins.Name = render(ins.Name)
		ins.Description = render(ins.Description)
		for j := range ins.Indicators {
			ins.Indicators[j] = render(ins.Indicators[j])
		}

		// DescriptionMd is derived from Description at parse time, so a template that
		// reached it has to be rendered too or the markdown copy keeps the references.
		if ins.DescriptionMd != "" {
			ins.DescriptionMd = render(ins.DescriptionMd)
		}

		if len(unresolved) > 0 {
			tally.unresolved += len(unresolved)
			applog.WithFields(applog.Fields{
				"insight": ins.Name,
				"refs":    unresolved,
			}).Warn("Insight prose references figures it did not declare; the references ship as written")
		}
		if bare := bareNumeralFields(tpl); len(bare) > 0 {
			tally.inlined += len(bare)
			applog.WithFields(applog.Fields{
				"insight": ins.Name,
				"fields":  bare,
			}).Warn("Insight prose states a number directly instead of declaring it, so nothing checks it")
		}
		if unused := unusedFigures(tpl, ins.Figures); len(unused) > 0 {
			tally.unused += len(unused)
		}
	}
	return tally
}

type figureRenderTally struct {
	resolved   int
	unresolved int
	inlined    int
	unused     int
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

func hasBareNumeral(s string) bool {
	blanked := reFigureRef.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
	blanked = reYearish.ReplaceAllStringFunc(blanked, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
	return reBareDigit.MatchString(blanked)
}

// unusedFigures names figures nothing referenced. Not an error -- a model may declare a
// figure and then phrase it in words -- but worth counting, because a document whose
// declarations are mostly unreferenced is one where the format is being filled in rather
// than used.
func unusedFigures(tpl models.FigureTemplate, figures []models.Figure) []string {
	used := map[string]struct{}{}
	collect := func(s string) {
		for _, m := range reFigureRef.FindAllStringSubmatch(s, -1) {
			used[m[1]] = struct{}{}
		}
	}
	collect(tpl.Name)
	collect(tpl.Description)
	for _, s := range tpl.Indicators {
		collect(s)
	}
	var out []string
	for _, f := range figures {
		if _, ok := used[f.ID]; !ok {
			out = append(out, f.ID)
		}
	}
	sort.Strings(out)
	return out
}
