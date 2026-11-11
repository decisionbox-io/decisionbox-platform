package discovery

import (
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Deriving an insight's grade instead of asking the model for it.
//
// The model used to author all three numbers -- how confident it was, how
// severe the finding was, and a risk score -- and nothing recomputed any of
// them. Measured over 160 stored insights the confidence ran 0.75-0.99 with a
// mean of 0.93, a quarter of its range correlating with nothing, while nine of
// those insights had been contradicted by the warehouse and forty-three
// carried arithmetic Go refuted and could not repair. A reader sorting thirty
// findings by confidence was sorting by noise, and `severity` was the same
// self-assessment in words: a finding whose headline number the warehouse had
// refuted could still call itself critical, and did.
//
// Everything the derivation needs is already on the document by the time these
// run: the verdict the verifier and refuter combined, what the sources said
// about the rows, what Go concluded about each declared figure and claim, and
// whether the published prose still carries a placeholder. The scorer reads
// those and writes the grade.
//
// The two functions below CLEAR before they write, unconditionally and
// including the case where the document has no verdict and no figure at all.
// That is the whole correctness argument, and it is why editing the prompts
// would not have been enough: a pack's seeds are never overwritten on upgrade
// and a project snapshots its area prompts at creation, so an existing
// deployment keeps asking the model for these numbers forever. Clearing means
// the answer is discarded whether or not it was asked for.

// scoreInsights derives every insight's confidence, severity and priority in
// place.
//
// A free function rather than a method, because the on-demand "Run validation"
// path re-scores one document outside any run and has no orchestrator to reach
// through. The config is passed in rather than resolved here so that two areas
// of one run can never disagree about the calibration they scored against.
//
// The run context it needs beyond the documents is grouped, because the
// severity axis reads two things only a live run can supply and both are easy
// to forget at a call site: which datasource each figure was read from, and
// which figures repair severed from their sentences. See scoringContext.
func scoreInsights(insights []models.Insight, sc scoringContext) {
	for i := range insights {
		in := insightScoringInput(insights[i], sc)
		// Clear, then write. Every field, every time: a document arriving
		// with the model's own numbers and a stale sub-document from a
		// previous attempt has to come out carrying none of them.
		insights[i].Scoring = nil
		insights[i].Confidence = 0
		insights[i].Severity = ""
		insights[i].RiskScore = 0

		derived := scoring.Score(in)
		insights[i].Scoring = &derived
		insights[i].Confidence = derived.ConfidenceScore
		// Mirrored onto the document's own fields, which keep their bson keys
		// and types: `severity` is a live Qdrant match filter and sits inside
		// the dedup embedding text, and `risk_score` is read by API clients.
		// What changed is how they are POPULATED, so every existing consumer
		// keeps working and starts reading a grounded value.
		insights[i].Severity = derived.SeverityBand
		insights[i].RiskScore = derived.SeverityScore
	}
}

// scoreRecommendations derives every recommendation's grade in place:
// confidence from its OWN verdict, severity inherited from the insights it
// cites.
//
// Confidence is its own because a recommendation is a separate claim -- the
// verifier checked whether this action follows from those findings, and that
// verdict is the one a reader is being told about. Inheriting it would report
// how well the evidence was checked, not how well the recommendation was.
//
// Severity is inherited for the opposite reason. A recommendation's figures
// are `ref` and `sum` over figures its insights already declared, so measuring
// them would band a magnitude off numbers already banded once and report the
// result as independent evidence.
func scoreRecommendations(recs []models.Recommendation, insights []models.Insight, cfg scoring.Config) {
	severityByInsight := make(map[string]scoring.InheritedSeverity, len(insights))
	for _, ins := range insights {
		if ins.ID == "" || ins.Scoring == nil {
			continue
		}
		severityByInsight[ins.ID] = scoring.InheritedSeverity{
			MagnitudeScore: ins.Scoring.MagnitudeScore,
			SeverityBand:   ins.Scoring.SeverityBand,
			SeverityScore:  ins.Scoring.SeverityScore,
			SeverityDriver: ins.Scoring.SeverityDriver,
			SeverityReason: ins.Scoring.SeverityReason,
			AreaWeight:     ins.Scoring.AreaWeight,
			WeightSource:   ins.Scoring.WeightSource,
		}
	}

	for i := range recs {
		in := recommendationScoringInput(recs[i], cfg)
		recs[i].Scoring = nil
		recs[i].Confidence = 0
		recs[i].Severity = ""
		recs[i].Priority = 0

		derived := scoring.ScoreRecommendation(in, citedSeverities(recs[i], severityByInsight))
		recs[i].Scoring = &derived
		recs[i].Confidence = derived.ConfidenceScore
		recs[i].Severity = derived.SeverityBand
		// The integer the field has always carried, repopulated from the
		// tier. Zero when nothing could be inherited, which is the field's
		// own "unset" rather than the bottom of the scale.
		recs[i].Priority = scoring.RecommendationPriority(derived.PriorityTier)
	}
}

// citedSeverities is the severity half of each insight a recommendation cites,
// in citation order.
//
// Order matters because inheritance keeps the first on a tie, so the inherited
// driver is the first-cited insight's and is stable across re-scores. An id
// that resolves to nothing is skipped: the generation phase already drops a
// recommendation whose citations cannot be resolved, so this only happens on a
// re-score that has one document in hand rather than the run.
func citedSeverities(rec models.Recommendation, byInsight map[string]scoring.InheritedSeverity) []scoring.InheritedSeverity {
	out := make([]scoring.InheritedSeverity, 0, len(rec.RelatedInsightIDs))
	for _, id := range rec.RelatedInsightIDs {
		if sev, ok := byInsight[id]; ok {
			out = append(out, sev)
		}
	}
	return out
}

// scoringContext is everything the scorer needs beyond the documents.
//
// A struct rather than four parameters, because three of its fields are things
// only a live run can supply and a call site that forgets one gets a silently
// weaker grade rather than a compile error. The on-demand "Run validation"
// path deliberately supplies only the calibration, and the zero value of each
// other field is the right answer there: it holds one stored document, has no
// datasource context, and keeps the document's severity frozen anyway.
type scoringContext struct {
	cfg scoring.Config

	// currencies and stepByID resolve each insight's home currency, which
	// decides which of its amounts the severity axis may compare. A zero plan
	// resolves every insight to "" and bands on share.
	currencies currencyPlan
	stepByID   map[int]*models.ExplorationStep

	// writer answers which of an insight's figures repair severed from the
	// prose. Its zero value answers "none", which is correct for a document
	// no repair pass has touched and for the re-score path, where the
	// severity half is not recomputed at all.
	writer figureWriter
}

// insightScoringInput collects what the engine reads off one insight.
func insightScoringInput(ins models.Insight, sc scoringContext) scoring.Input {
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
		// The figures a reader can still see, not every figure declared.
		//
		// Repair edits text and leaves figures alone, so an insight whose 80%
		// indicator repair deleted still carries that figure, its value and
		// its holding verdict -- and banding off it would rank the finding on
		// a number removed from the text that shipped. The same filter also
		// drops a duplicated id, which cannot be joined to a verdict at all.
		//
		// The VERDICTS are passed whole and deliberately so: a refuted figure
		// still docks confidence whether or not repair later rewrote its
		// sentence, because the deduction is about what the check found rather
		// than about what the prose ended up saying.
		Figures:      sc.writer.figuresStillBehindTheProse(ins),
		HomeCurrency: sc.currencies.forInsight(ins, sc.stepByID),
		AreaID:       ins.AnalysisArea,
		Config:       sc.cfg,
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
//
// No figures and no home currency, deliberately: ScoreRecommendation does not
// measure a recommendation's own numbers, and handing it the means to would
// make that a one-line change away rather than a decision.
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
