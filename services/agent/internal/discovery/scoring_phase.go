package discovery

import (
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Deriving an insight's confidence instead of asking the model for it.
//
// The model used to author the number, and nothing recomputed it. Measured
// over 160 stored insights it ran 0.75-0.99 with a mean of 0.93 -- a quarter
// of its range, correlating with nothing -- while nine of those insights had
// been contradicted by the warehouse and forty-three carried arithmetic Go
// refuted and could not repair. A reader sorting thirty findings by confidence
// was sorting by noise.
//
// Everything the derivation needs is already on the document by the time these
// run: the verdict the verifier and refuter combined, what the sources said
// about the rows, what Go concluded about each declared figure and claim, and
// whether the published prose still carries a placeholder. The scorer reads
// those and writes the grade.
//
// The two functions below CLEAR before they write, unconditionally and
// including the case where the document has no verdict at all. That is the
// whole correctness argument, and it is why editing the prompts would not have
// been enough: a pack's seeds are never overwritten on upgrade and a project
// snapshots its area prompts at creation, so an existing deployment keeps
// asking the model for the number forever. Clearing means the answer is
// discarded whether or not it was asked for.

// scoreInsights derives every insight's confidence in place.
//
// A free function rather than a method, because the on-demand "Run validation"
// path re-scores one document outside any run and has no orchestrator to reach
// through. The config is passed in rather than resolved here so that two areas
// of one run can never disagree about the calibration they scored against.
func scoreInsights(insights []models.Insight, cfg scoring.Config) {
	for i := range insights {
		in := insightScoringInput(insights[i], cfg)
		// Clear, then write. Both fields, every time: a document arriving with
		// the model's own number and a stale sub-document from a previous
		// attempt has to come out carrying neither.
		insights[i].Scoring = nil
		insights[i].Confidence = 0

		derived := scoring.Score(in)
		insights[i].Scoring = &derived
		insights[i].Confidence = derived.ConfidenceScore
	}
}

// scoreRecommendations derives every recommendation's confidence in place,
// from its OWN verdict rather than from the insights it cites.
//
// Its own, because a recommendation is a separate claim: the verifier checked
// whether this action follows from those findings, and that verdict is the one
// a reader is being told about. Inheriting the insights' confidence would
// report how well the evidence was checked, not how well the recommendation
// was.
func scoreRecommendations(recs []models.Recommendation, cfg scoring.Config) {
	for i := range recs {
		in := recommendationScoringInput(recs[i], cfg)
		recs[i].Scoring = nil
		recs[i].Confidence = 0

		derived := scoring.Score(in)
		recs[i].Scoring = &derived
		recs[i].Confidence = derived.ConfidenceScore
	}
}

// insightScoringInput collects what the confidence axis reads off one insight.
func insightScoringInput(ins models.Insight, cfg scoring.Config) scoring.Input {
	in := scoring.Input{
		QualityCaveats:     ins.Quality,
		FigureVerdicts:     ins.FigureVerdicts,
		QuantifierVerdicts: ins.QuantifierVerdicts,
		// Counted off the prose that ships rather than off the figure list,
		// because an unresolved reference ships as written and the figure
		// field that would have said so is not persisted. Counting the text
		// works identically on a live insight and on one re-read from storage,
		// which is what the on-demand re-score needs.
		UnresolvedFigures: len(unresolvedRefsInInsight(ins)),
		Config:            cfg,
	}
	applyValidationToInput(&in, ins.Validation)
	return in
}

// recommendationScoringInput collects the same off one recommendation.
//
// Two of the five deductions are structurally absent here and that is correct,
// not a gap: a recommendation is shown insights rather than warehouse rows, so
// it carries no source-quality caveats and declares no quantifier claims. The
// unresolved-reference count is computed anyway, because the gate that drops
// such recommendations runs in the generation phase and an on-demand re-score
// of a stored document has no gate in front of it.
func recommendationScoringInput(rec models.Recommendation, cfg scoring.Config) scoring.Input {
	in := scoring.Input{
		FigureVerdicts:    rec.FigureVerdicts,
		UnresolvedFigures: len(unresolvedRefsInRecommendation(rec)),
		Config:            cfg,
	}
	applyValidationToInput(&in, rec.Validation)
	return in
}

// applyValidationToInput reads the three things the verdict block contributes.
//
// A nil block is a document validation never reached, and it leaves Verdict
// empty -- which the engine scores as never-checked rather than as zero
// confidence. Those are different statements, and conflating them is how an
// unvalidated finding ends up ranked below one the warehouse contradicted.
func applyValidationToInput(in *scoring.Input, v *models.InsightValidation) {
	if v == nil {
		return
	}
	in.Verdict = v.Combined
	in.RefuterDisabled = v.RefuterDisabled
	// Either agent hitting the end of the retained sample narrowed the
	// evidence this document was checked against, so either one counts. The
	// flag is per-agent because they page independently.
	in.StepReadBeyondRetained = (v.Verifier != nil && v.Verifier.StepReadBeyondRetained) ||
		(v.Refuter != nil && v.Refuter.StepReadBeyondRetained)
}

// unresolvedRefsInInsight returns every reference-shaped placeholder left in
// the fields a reader actually sees.
//
// The insight twin of unresolvedRefsInRecommendation, and DescriptionMd is
// included for the reason it is there: the dashboard renders the Markdown
// rendition in preference to the plain text, so a placeholder surviving only
// in that copy is one a reader still sees.
//
// reAnyRef rather than the resolving pattern, for the reason the recommendation
// gate uses it: the schema accepts any string as a figure id, so a model can
// declare `revenue-total`, have the id grammar refuse it, and ship
// "{{revenue-total}}" into the published sentence -- which the resolving
// pattern does not match. A check on what a reader will see has to match what
// a reader will see.
func unresolvedRefsInInsight(ins models.Insight) []string {
	fields := []string{ins.Name, ins.Description, ins.DescriptionMd}
	fields = append(fields, ins.Indicators...)

	var refs []string
	for _, s := range fields {
		refs = append(refs, reAnyRef.FindAllString(s, -1)...)
	}
	return refs
}
