package discovery

import (
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// attachFigureVerdicts evaluates each insight's figures against the rows of the steps
// they cite, and records what Go concluded.
//
// Records rather than rejects, for the reason attachQuantifierVerdicts does: a gate with
// no correction path behind it converts one wrong number into a discarded document, and a
// document carrying one wrong figure usually carries a dozen right ones.
//
// There is no coverage counter any more, and its absence is the point. Coverage was a
// measurement because the model typed numbers into prose and Go had to go looking for the
// ones it had not declared -- 37% of them, and seven times as likely to be ungroundable
// as the declared ones. With the figure as data and the prose referencing it, a number
// that is not declared cannot appear in a rendered sentence at all, so there is nothing
// to count. What replaces it is a compliance check on the template, in figure_template.go.
func attachFigureVerdicts(insights []models.Insight, stepByID map[int]*models.ExplorationStep) {
	for i := range insights {
		ins := &insights[i]

		// Cleared unconditionally and before any early return. Both fields are derived,
		// and their JSON tags are ordinary, so a model that emits one has its own verdict
		// decoded straight onto the insight. Having just been told to emit `figures`,
		// volunteering the sibling is an obvious thing to do -- and an insight that
		// declared nothing is exactly where a volunteered verdict would survive unnoticed.
		ins.FigureVerdicts = nil
		ins.FigureCorrections = nil
		// The template too, and for the same reason. An insight with no figures and no
		// references is skipped by renderInsightFigures, so a model-authored
		// `evidence_figure_template` on one of those survived persistence and API
		// serialisation as the platform's own provenance record -- the model authoring the
		// proof that its numbers came from declarations. The recommendation pass already
		// cleared all three; this one cleared two.
		ins.FigureTemplate = nil

		if len(ins.Figures) == 0 {
			continue
		}
		verdicts := EvaluateFigures(ins.Figures, quantifierEvidence(*ins, stepByID))
		ins.FigureVerdicts = verdicts
		for _, v := range verdicts {
			if v.Status != models.FigureFails {
				continue
			}
			applog.WithFields(applog.Fields{
				"insight":   ins.Name,
				"figure":    v.ID,
				"display":   v.Display,
				"step":      v.Step,
				"kind":      v.Kind,
				"claimed":   v.Claimed,
				"evaluated": v.Evaluated,
				"reason":    v.Reason,
			}).Warn("Figure is refuted by the arithmetic the insight declared for it")
		}
	}
}
