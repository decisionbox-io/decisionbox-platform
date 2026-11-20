package scoring

import (
	"math"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// Severity, derived from the evidence rather than written by the analysis
// model.
//
// The model used to pick the word and a matching `risk_score` float as it
// wrote each insight, and nothing recomputed either. A finding whose headline
// number the warehouse had refuted could still call itself critical, and did.
//
// The run already holds the only honest magnitude available to it: the numbers
// the model declared as DATA -- value, unit, and the arithmetic over the
// evidence that produced them -- each of which Go re-evaluated against the
// rows and stamped holds / fails / undecidable. Severity is a function of
// those and of the analysis area's weight, and of nothing the model said about
// how much its finding matters.
//
// What it deliberately does not use is `affected_count`. That field is a
// model-authored integer, coerced tolerantly from whatever shape arrived, and
// there is no population denominator anywhere in a discovery result to turn it
// into a share: a step's row count is the rows one query returned and a table's
// row count is the table's size, neither of which is the cohort. A ratio built
// from it would be a number with no meaning -- the defect this engine exists to
// remove, reintroduced one field over.

// severityInput is the slice of Input the severity axis reads. A named view
// rather than a second parameter list, so the two axes can be read separately
// without the signatures drifting.
type severityInput struct {
	figures      []gomodels.Figure
	verdicts     []gomodels.FigureVerdict
	homeCurrency string
	weight       float64
}

// scoreSeverity derives the magnitude, the weight-free score, the band, the
// driver and the engine's sentence for it.
//
// Returns the fields rather than writing them, so Score composes one struct in
// one place and this function stays a pure calculation that a table test can
// drive directly.
func scoreSeverity(in severityInput, cfg SeverityConfig) (
	magnitude float64,
	score float64,
	band string,
	driver gomodels.SeverityDriver,
	reason string,
) {
	amount, share := qualifyingFigures(in)
	return severityFromCandidates(severityCandidates{
		amount:       amount,
		share:        share,
		homeCurrency: in.homeCurrency,
		weight:       in.weight,
	}, cfg)
}

// severityCandidates is everything the severity calculation needs: the two
// figures that may band a magnitude, the currency the document is in, and the
// weight of its area.
type severityCandidates struct {
	amount       *gomodels.SeverityFigure
	share        *gomodels.SeverityFigure
	homeCurrency string
	weight       float64
}

// severityFromCandidates is THE severity calculation: precedence, magnitude,
// driver, rounding, clamp, band and sentence.
//
// One copy, because two things compute severity and this feature rests on them
// agreeing. A fresh run extracts its candidates from the figures a step
// produced; the settings preview extracts them from the driver a previous run
// stored. Those extractions are genuinely different and stay separate — but
// everything after them was duplicated, and a change to either copy would have
// ranked the same candidates differently depending on which path asked. That
// disagreement is the one failure the preview cannot have: it is sold as "the
// numbers the next run produces".
//
// A parity sweep asserts the two paths agree. This is what makes them agree.
func severityFromCandidates(c severityCandidates, cfg SeverityConfig) (
	magnitude float64,
	score float64,
	band string,
	driver gomodels.SeverityDriver,
	reason string,
) {
	amount, share := c.amount, c.share

	switch {
	case amount != nil && amountBandsApply(c.homeCurrency, cfg):
		magnitude = magnitudeFor(magnitudeBandFor(amount.Value, cfg.AmountBands), cfg)
		driver = gomodels.SeverityDriver{
			Kind:       gomodels.SeverityDriverAmount,
			Quantified: true,
			Figure:     amount,
			Amount:     amount,
			// The candidate the amount beat. Stored for the reason the amount
			// is stored on a share-banded document: the driver carries what
			// banded the magnitude plus every candidate it beat, so a
			// re-score under a configuration with different thresholds can
			// reproduce this choice instead of guessing at it.
			Share: share,
		}
	case share != nil:
		magnitude = magnitudeFor(magnitudeBandFor(share.Value, cfg.ShareBands), cfg)
		driver = gomodels.SeverityDriver{
			Kind:       gomodels.SeverityDriverShare,
			Quantified: true,
			Figure:     share,
			Amount:     amount,
		}
	default:
		// No figure this engine would use. The area baseline stands in, and
		// the label says so -- it is never silently the low band.
		magnitude = cfg.BaselineMagnitude
		driver = gomodels.SeverityDriver{
			Kind:       gomodels.SeverityDriverBaseline,
			Quantified: false,
			Amount:     amount,
		}
	}

	magnitude = round(magnitude)
	// Clamped, because risk_score carries this number and its [0, 1] contract
	// is what every reader that prints it as a percentage relies on. A weight
	// above 1 on a top-band magnitude is the case that would otherwise escape
	// it.
	score = round(math.Min(1.0, magnitude*c.weight))
	band = severityBandFor(score, cfg)
	return magnitude, score, band, driver, severityReason(band, driver, c.homeCurrency, cfg)
}

// qualifyingFigures picks the largest checked amount in the document's own
// home currency and the largest checked share.
//
// "Largest" is by ABSOLUTE value. A revenue figure can legitimately be
// negative -- an excess or a diff against a baseline -- and a 12% fall matters
// exactly as much as a 12% rise, so ranking by the signed value would rank
// every decline last.
//
// A figure qualifies only when its own declared arithmetic reproduced it
// against the rows. `undecidable` does not qualify and neither does a figure
// with no verdict at all: both mean the number was not checked, and the whole
// point of this axis is that the magnitude was.
func qualifyingFigures(in severityInput) (amount, share *gomodels.SeverityFigure) {
	// A repeated figure id cannot be joined to a verdict at all, so every
	// declaration of it is excluded.
	//
	// The evaluator emits one verdict per DECLARATION, so a map keyed by id
	// keeps the last while a loop over the figures reaches the first: an
	// insight declaring a failing 99% `f1` and then a holding 1% `f1` would
	// grade the 99% value against the 1% figure's verdict, and band high off
	// a number the warehouse refuted. Which is the single thing this axis
	// exists to make impossible.
	//
	// Excluded rather than disambiguated, because there is no honest way to
	// pick: the ids are the only link between a figure and its verdict, and a
	// duplicated id means that link does not exist. The same answer the
	// reference index and the renderer already give an ambiguous id.
	ambiguous := make(map[string]bool)
	seen := make(map[string]bool, len(in.figures))
	for _, f := range in.figures {
		if seen[f.ID] {
			ambiguous[f.ID] = true
		}
		seen[f.ID] = true
	}

	byID := make(map[string]gomodels.FigureVerdict, len(in.verdicts))
	for _, v := range in.verdicts {
		byID[v.ID] = v
	}

	for _, f := range in.figures {
		if f.ID == "" || ambiguous[f.ID] {
			continue
		}
		v, ok := byID[f.ID]
		if !ok || v.Status != gomodels.FigureHolds {
			continue
		}
		// Both the ranking and the banding read severityFigure's checked
		// quantity, so the figure that wins "largest" is the figure whose
		// number the band is then computed from.
		switch f.Unit {
		case gomodels.UnitCurrency:
			// The home-currency rule. A figure that STATES a currency other
			// than the warehouse's own is the one case the ingest-time
			// settlement leaves alone, because on a multi-currency warehouse
			// that statement is the only thing distinguishing a foreign
			// amount from a local one -- so comparing it here is comparing
			// the model's own claim, which is what has to be excluded. An
			// empty home currency excludes every amount: the caller could not
			// determine one, and guessing is how an amount read from one
			// datasource gets banded against another's thresholds.
			// BOTH operands normalised, because neither is guaranteed to
			// have been.
			//
			// A figure stored before CurrencySymbol upper-cased unmapped
			// codes carries `cad`, and a resumed analysis draft reuses that
			// figure without settling it again — while the profile side is
			// resolved fresh to `CAD`. Comparing them raw excluded the
			// amount, so resuming a draft written by an older build lost a
			// checked amount the page still shows. One answer to "is this the
			// same currency", applied to whatever each side happens to hold.
			if in.homeCurrency == "" || CurrencySymbol(f.Currency) != CurrencySymbol(in.homeCurrency) {
				continue
			}
			amount = larger(amount, severityFigure(f, v))
		case gomodels.UnitPercent:
			share = larger(share, severityFigure(f, v))
		}
	}
	return amount, share
}

// amountBandsApply reports whether this document's amount may be banded at
// all: there have to be thresholds, and they have to be written in the money
// the document is reported in.
//
// The currency check is the whole reason the configuration carries a currency.
// Thresholds are per-datasource because money is, and a home currency is
// resolved PER DOCUMENT from the datasource whose step produced its figures --
// so one run can hold documents in two currencies, and a deployment that
// wrote "2,000,000 is high" about liras must not have it decide a dollar
// amount. An unstated configured currency means "whatever this document is
// in", which is the only honest reading of a bare threshold and is also the
// behaviour of every deployment that configures no thresholds at all.
//
// A document with no home currency never reaches here with an amount:
// qualifyingFigures excludes every amount when the currency could not be
// determined, for the same reason this function exists.
func amountBandsApply(homeCurrency string, cfg SeverityConfig) bool {
	if len(cfg.AmountBands) == 0 {
		return false
	}
	return cfg.AmountCurrency == "" || cfg.AmountCurrency == homeCurrency
}

// larger returns whichever figure has the greater absolute value, keeping the
// incumbent on a tie.
//
// Keeping the incumbent makes the choice the figure list's own order, which is
// the order the model declared them in and is stable across re-scores of the
// same document. An unstable pick would mean a stored driver that changed
// identity every time validation was re-run.
func larger(cur, next *gomodels.SeverityFigure) *gomodels.SeverityFigure {
	if cur == nil {
		return next
	}
	if next == nil {
		return cur
	}
	if math.Abs(next.Value) > math.Abs(cur.Value) {
		return next
	}
	return cur
}

// severityFigure projects a figure and its verdict onto what the driver
// stores. Display comes off the VERDICT rather than being re-rendered, so the
// number beside the band is character-for-character the one in the sentence.
//
// Value is the CHECKED quantity, not the one the model declared, and the
// difference is not hypothetical. A figure holds when its rendered number
// matches what the warehouse produced at the precision it is printed to, so
// `value: 4.6, decimals: 0` publishes "5%" and holds against a warehouse 5.4.
// Banding on 4.6 would put it below a 5% cutoff that the number a reader sees
// is above -- so two digits the model never published, and nothing ever
// checked, would decide the ranking. The evaluated quantity is the one the
// verdict is about.
func severityFigure(f gomodels.Figure, v gomodels.FigureVerdict) *gomodels.SeverityFigure {
	return &gomodels.SeverityFigure{
		FigureID: f.ID,
		Display:  v.Display,
		Value:    checkedValue(v),
		Unit:     f.Unit,
		Currency: f.Currency,
		Verdict:  v.Status,
	}
}

// checkedValue is the quantity a holding verdict certifies: what Go evaluated
// against the rows, and nothing else.
//
// Evaluated unconditionally, with NO fallback to Claimed, and the absence of
// that fallback is the point. Evaluated is omitempty, so a zero is stored as
// an absent field -- but a zero evaluation is a real and reachable answer: a
// claim of 400,000 rendered at millions scale with no decimals prints "$0M"
// and legitimately holds against a warehouse zero. Falling back to Claimed
// there resurrects the model's unrounded 400,000 and lets a figure a reader
// sees as $0M outrank a checked $100K -- which is the unchecked-digits defect
// this function exists to remove, reintroduced by the guard against it.
//
// So absent reads as zero, which for a holding verdict is what it means.
func checkedValue(v gomodels.FigureVerdict) float64 {
	return v.Evaluated
}

// bandFor2 finds the band a figure's absolute value falls in, or "" when no
// cutoff matches -- which is every value against an unconfigured band set.
//
// The bands are declared descending and the first match wins, so a value above
// the top cutoff lands in the top band rather than falling through.
func magnitudeBandFor(value float64, bands []MagnitudeBand) string {
	v := math.Abs(value)
	for _, b := range bands {
		if v >= b.At {
			return b.Band
		}
	}
	return gomodels.SeverityBandLow
}

// magnitudeFor is what a band is worth as a magnitude.
func magnitudeFor(band string, cfg SeverityConfig) float64 {
	switch band {
	case gomodels.SeverityBandHigh:
		return cfg.HighMagnitude
	case gomodels.SeverityBandMedium:
		return cfg.MediumMagnitude
	default:
		return cfg.LowMagnitude
	}
}

// severityBandFor is which band the weighted score lands in. Cutoffs are
// inclusive, matching the confidence axis.
func severityBandFor(score float64, cfg SeverityConfig) string {
	switch {
	case score >= cfg.HighAt:
		return gomodels.SeverityBandHigh
	case score >= cfg.MediumAt:
		return gomodels.SeverityBandMedium
	default:
		return gomodels.SeverityBandLow
	}
}

// severityBandLabels are the engine's own words for each band, used to open
// its sentence.
var severityBandLabels = map[string]string{
	gomodels.SeverityBandHigh:   "High",
	gomodels.SeverityBandMedium: "Medium",
	gomodels.SeverityBandLow:    "Low",
}

// severityReason is the engine's sentence for a band and its driver.
//
// Written here and stored on the document, for the reason the confidence
// reason is: the dashboard, an API client, the clarifying-questions digest and
// any summary reader quote one wording rather than each composing its own.
func severityReason(band string, driver gomodels.SeverityDriver, homeCurrency string, cfg SeverityConfig) string {
	label := severityBandLabels[band]
	if label == "" {
		// A band this build has no word for, which only a calibration that
		// added one without naming it can produce. A vague sentence is a
		// better answer than an empty field: a band with no explanation
		// beside it is the one shape this layer must not produce.
		label = "Severity"
	}

	switch driver.Kind {
	case gomodels.SeverityDriverAmount:
		return label + " — driven by " + figureWords(driver.Figure) + ", checked against warehouse rows."
	case gomodels.SeverityDriverShare:
		if driver.Amount != nil {
			// The configured-amount-bands case is the exception, not this
			// one: with no amount bands set, a document that states a
			// grounded amount still bands on its share. Saying both is what
			// keeps the number a reader cares about on screen without
			// claiming it set the band.
			return label + " — banded on " + figureWords(driver.Figure) +
				", with " + figureWords(driver.Amount) + " at stake. Both checked against warehouse rows."
		}
		return label + " — driven by " + figureWords(driver.Figure) + ", checked against warehouse rows."
	default:
		if driver.Amount != nil {
			// Reachable two ways, and they are different facts about the
			// deployment, so they are different sentences.
			//
			// Saying "no amount bands are configured" about a deployment that
			// HAS configured them — in another currency — sent a reader to a
			// settings page that already held a ladder, to add the thing it
			// said was missing. And this sentence is STORED on the document,
			// so it outlives the misunderstanding.
			if len(cfg.AmountBands) > 0 && !amountBandsApply(homeCurrency, cfg) {
				return label + " — not quantified, so the area baseline applies. " +
					figureWords(driver.Amount) + " is at stake, but the configured amount thresholds are written in " +
					ladderCurrencyWords(cfg) + " and this finding is in " + homeCurrencyWords(homeCurrency) +
					", so they cannot rank it."
			}
			// The amount is real and is shown; what is missing is a cutoff to
			// band it against.
			return label + " — not quantified, so the area baseline applies. " +
				figureWords(driver.Amount) + " is at stake but no amount bands are configured to rank it."
		}
		return label + " — not quantified: this finding states no checked figure to measure it by, " +
			"so the area baseline applies."
	}
}

// ladderCurrencyWords and homeCurrencyWords name a currency in a sentence, or
// say plainly that none was stated -- which is a different fact from a
// mismatch and the one a reader can act on.
func ladderCurrencyWords(cfg SeverityConfig) string {
	if cfg.AmountCurrency == "" {
		return "no particular currency"
	}
	return cfg.AmountCurrency
}

func homeCurrencyWords(home string) string {
	if home == "" {
		return "no stated currency"
	}
	return home
}

// figureWords renders a driver figure as prose: the number as the insight's own
// text writes it, falling back to the reference when the verdict carried no
// rendition.
func figureWords(f *gomodels.SeverityFigure) string {
	if f == nil {
		return "an unnamed figure"
	}
	if f.Display != "" {
		return f.Display
	}
	if f.FigureID != "" {
		return "figure " + f.FigureID
	}
	return "an unnamed figure"
}

// InheritedSeverity is one insight's severity half, as a recommendation
// inherits it.
//
// A recommendation declares only `ref` and `sum` figures over the figures its
// insights already declared, so computing a magnitude from them would
// re-measure the insight's own number and count it twice. Inheriting is both
// cheaper and more honest: the recommendation is as important as the most
// important thing it addresses.
type InheritedSeverity struct {
	MagnitudeScore float64
	SeverityBand   string
	SeverityScore  float64
	SeverityDriver *gomodels.SeverityDriver
	SeverityReason string
	AreaWeight     float64
	WeightSource   *gomodels.WeightSource

	// RiskFlags travels with the rest of the severity half: an action about a
	// compliance exposure is a compliance action. A recommendation has no
	// analysis area of its own to derive flags from, which is the same reason
	// the band is inherited rather than measured.
	RiskFlags []string
}

// InheritSeverity picks the severity half a recommendation takes on: the
// highest severity score among the insights it cites.
//
// Max rather than mean. A recommendation that addresses one severe finding and
// two mild ones is as important as the severe one, and averaging would rank it
// below a recommendation addressing a single medium finding -- which inverts
// the thing severity is for.
//
// Ties keep the first, so the inherited driver is the first-cited insight's
// and is stable across re-scores. No citations resolving, or none of them
// carrying a severity band, returns false: the recommendation keeps whatever
// severity half it already had rather than being given a manufactured one.
func InheritSeverity(from []InheritedSeverity) (InheritedSeverity, bool) {
	var best InheritedSeverity
	found := false
	for _, cand := range from {
		if cand.SeverityBand == "" {
			continue
		}
		if !found || cand.SeverityScore > best.SeverityScore {
			best, found = cand, true
		}
	}
	return best, found
}
