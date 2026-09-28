package discovery

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Correcting a refuted figure needs no model.
//
// A refuted figure arrives with its arithmetic already evaluated, so the right
// number is known and the wrong one is known as text. Every false figure measured
// across the three hand-adjudicated corpora is a numeral substitution and nothing
// more -- 100,000 for 99,996, 150,004 for 150,000, 47.1% and 49.7% for 49.3%. A
// rewrite round for any of them would be an LLM call to retype a number Go has.
//
// This is the figure equivalent of substituteRefutedCounts, and it inherits that
// pass's refusals wholesale, because they are the ones that keep a free correction
// from writing a new falsehood: all fields or none; decline wherever the numeral
// appears twice in one field; decline wherever another occurrence measures a
// different thing; decline when AffectedCount carries the same number, since that
// field is an int and correcting the prose around it would leave the document
// disagreeing with its own structured data.
//
// What it will not do is touch a figure whose numeral is too short to be distinctive.
// Substituting "5" across an insight would rewrite every unrelated five in it, and
// the unit check alone is not enough protection at that length.

// minSubstitutableDigits is how many characters a written numeral must have before
// this pass will swap it.
//
// Three, so "150" is eligible and "49" is not without a decimal point or a
// separator. Short numerals recur for unrelated reasons in these documents -- a
// decile index, a bucket edge, a brand number -- and the all-or-nothing refusals
// below turn every such coincidence into a declined repair anyway. Requiring length
// up front makes the common case a cheap skip rather than a walk over every field.
const minSubstitutableDigits = 3

// substituteRefutedFigures corrects in place every refuted figure it can do safely,
// and returns what it changed.
func substituteRefutedFigures(ins *models.Insight) []models.FigureFix {
	var fixed []models.FigureFix
	for i := range ins.FigureClaims {
		if i >= len(ins.FigureVerdicts) {
			break
		}
		v := ins.FigureVerdicts[i]
		if v.Status != models.FigureFails {
			continue
		}
		c := &ins.FigureClaims[i]
		from, to, ok := renderFigureSwap(c.Figure, v.Claimed, v.Evaluated)
		if !ok {
			continue
		}
		if !substituteFigureText(ins, c, from, to) {
			continue
		}
		c.Value = v.Evaluated
		fixed = append(fixed, models.FigureFix{
			Figure: c.Figure, From: v.Claimed, To: v.Evaluated,
			Text: from + " -> " + to,
		})
	}
	return fixed
}

// renderFigureSwap works out the two numeral tokens: the one the prose wrote, and
// the evaluated value rendered the same way.
//
// Rendered the same way, not merely printed, because the replacement has to read
// like the sentence it lands in. "49.7%" becomes "49.3%", one decimal place, not
// "49.342999999999996". "100,000" becomes "99,996" with its separator kept. A
// figure written in scaled units keeps its scale: a refuted "$6.9B" is rendered
// against the same billions its suffix declares.
func renderFigureSwap(figure string, claimed, evaluated float64) (from, to string, ok bool) {
	// The SAME numeral the precision was read from, selected by matching Value
	// rather than by position. Taking the first numeral here was half of a measured
	// corruption: a figure like "5.0 pct of $34.86B" had its interval read off the
	// 5.0, was refuted for it, and then had that 5.0 rewritten into a billions
	// figure -- after which re-settling reported the insight as holding.
	sel, selected := selectNumeral(figure, claimed)
	if !selected {
		return "", "", false
	}
	digits := sel.digits
	if len(strings.ReplaceAll(digits, ",", "")) < minSubstitutableDigits {
		return "", "", false
	}

	decimals := 0
	if i := strings.IndexByte(digits, '.'); i >= 0 {
		decimals = len(digits) - i - 1
	}
	scale := 1.0
	if s, isScaled := numeralScale[sel.suffix]; isScaled {
		scale = s
	}

	rendered := strconv.FormatFloat(math.Abs(evaluated)/scale, 'f', decimals, 64)
	if strings.Contains(digits, ",") {
		rendered = groupThousands(rendered)
	}
	if rendered == digits {
		// Rendering the correct value at the written precision reproduces the
		// written numeral. Nothing to substitute, and swapping a token for itself
		// would be recorded as a repair that changed nothing.
		return "", "", false
	}
	return digits, rendered, true
}

// groupThousands inserts comma separators into an already-rendered decimal string.
func groupThousands(s string) string {
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String() + frac
}

// substituteFigureText replaces one numeral with another across every text field of
// the insight, all or nothing, under the same rules substituteCount applies.
func substituteFigureText(ins *models.Insight, c *models.FigureClaim, from, to string) bool {
	// AffectedCount is an int. Correcting the prose while it keeps the old number
	// leaves the document disagreeing with its own structured field, which the API,
	// the validation ordering and the recommendation inputs all read; overwriting
	// it blindly is the other error, because "entities affected" need not be the
	// same quantity as the figure that happens to match it.
	if bare, err := strconv.ParseFloat(strings.ReplaceAll(from, ",", ""), 64); err == nil {
		if float64(ins.AffectedCount) == bare {
			return false
		}
	}

	fields := []*string{&ins.Name, &ins.Description, &ins.DescriptionMd, &c.Figure}
	for i := range ins.Indicators {
		fields = append(fields, &ins.Indicators[i])
	}
	// The quantifier claims' text too. Each is a quotation of a sentence in the
	// prose, so correcting the prose and leaving the quotation stale would make the
	// two disagree about what the insight says -- and those strings are what a
	// reader and a later repair round both match on. Included in the ambiguity
	// checks as well as the rewrite, so a numeral that means something else inside a
	// claim declines the whole substitution like any other field.
	for i := range ins.QuantifierClaims {
		fields = append(fields, &ins.QuantifierClaims[i].Claim)
	}

	// What the figure measures, read from the declaration's own text. Every other
	// occurrence has to be measuring the same thing, or this is not one quantity
	// restated -- it is two numbers that happen to match.
	unit := unitAfter(c.Figure, from)
	if unit == "" {
		return false
	}

	hits := 0
	for _, f := range fields {
		switch len(standaloneNumber(*f, from)) {
		case 0:
			continue
		case 1:
			if unitAfter(*f, from) != unit {
				// The same numeral measuring something else. Refuse the whole
				// substitution rather than skip the field: fixing the body and
				// leaving the headline stale is the headline-contradicts-body shape
				// this project already ships too much of.
				return false
			}
			hits++
		default:
			return false
		}
	}
	if hits == 0 {
		return false
	}
	for _, f := range fields {
		at := standaloneNumber(*f, from)
		if len(at) != 1 {
			continue
		}
		*f = (*f)[:at[0]] + to + (*f)[at[0]+len(from):]
	}
	return true
}

// repairRefutedFigures runs the substitution pass over every insight, re-settles the
// verdicts against the text that now ships, and records what changed.
//
// Re-settling matters as much as the substitution: the verdicts are what every
// measurement downstream reads, and verdicts describing prose that has since been
// edited would report the run as worse than it shipped.
//
// Anything the pass declines stays refuted and visible. There is deliberately no
// fallback to a rewrite round or to removing the sentence -- both exist for
// quantifier claims, where the correction is not known; here it is, so a declined
// substitution means the numeral was ambiguous in the prose, and the honest outcome
// is a refuted figure a reader can still see.
func repairRefutedFigures(areaID string, insights []models.Insight, stepByID map[int]*models.ExplorationStep) int {
	total := 0
	for i := range insights {
		ins := &insights[i]
		ins.FigureFixes = nil
		if countRefutedFigures(ins.FigureVerdicts) == 0 {
			continue
		}
		fixed := substituteRefutedFigures(ins)
		if len(fixed) == 0 {
			continue
		}
		ins.FigureFixes = fixed
		ins.FigureVerdicts = EvaluateFigureClaims(ins.FigureClaims, quantifierEvidence(*ins, stepByID))
		coverage := figureCoverage(*ins)
		ins.FigureCoverage = &coverage
		total += len(fixed)

		texts := make([]string, 0, len(fixed))
		for _, f := range fixed {
			texts = append(texts, f.Text)
		}
		applog.WithFields(applog.Fields{
			"area":      areaID,
			"insight":   ins.Name,
			"figures":   texts,
			"remaining": countRefutedFigures(ins.FigureVerdicts),
		}).Info(fmt.Sprintf("Corrected %d refuted figure(s) in Go; no rewrite needed", len(fixed)))
	}
	return total
}

func countRefutedFigures(verdicts []models.FigureVerdict) int {
	n := 0
	for _, v := range verdicts {
		if v.Status == models.FigureFails {
			n++
		}
	}
	return n
}
