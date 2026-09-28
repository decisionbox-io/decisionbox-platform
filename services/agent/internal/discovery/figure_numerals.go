package discovery

import (
	"regexp"
	"strconv"
	"strings"
)

// Numeral extraction, for counting coverage and for nothing else.
//
// This is the classifier that did not work. Measured against three
// hand-adjudicated corpora it found every false figure a numeral check could see
// -- 11 of 11 -- at 7-14% precision, and no narrowing closed the gap without
// costing a red-proof. Six sound sentences rewritten per real catch is how a
// checker starts costing more than it catches, so it drives nothing.
//
// Counting how many numerals a document wrote is a different job, and this is
// good enough for it. An over-extracted numeral costs one spurious "undeclared"
// in a counter. Nothing branches on whether a value here is right, only on
// whether the model said where it came from -- which is why the same code that
// was disqualifying as a truth check is exactly right as a coverage check.
//
// The comparable failure this exists to make visible: a quarter of insights in
// the measured corpora declared no quantifier claim at all, and that was
// invisible until someone counted by hand. Coverage that is not measured is
// coverage that is assumed.

var (
	// Dates are masked before anything else so "1998-08-02" does not become
	// three numerals.
	reDateish = regexp.MustCompile(`\d{4}-\d{2}-\d{2}(?:T[\d:.]+Z?)?|\d{4}-\d{2}`)

	reNumeral = regexp.MustCompile(
		`([$€£]\s?)?(-)?(\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:\.\d+)?)` +
			`(\s?%|\s?[KMB](?:[^A-Za-z]|$)|\s?(?:thousand|million|billion)\b|x(?:[^A-Za-z]|$))?`)

	// A numeral immediately after these is naming a step, not measuring
	// anything.
	reStepRef = regexp.MustCompile(`(?i)(step|query|q)\s*$`)
)

var numeralScale = map[string]float64{
	"k": 1e3, "m": 1e6, "b": 1e9,
	"thousand": 1e3, "million": 1e6, "billion": 1e9,
}

// writtenNumerals returns the distinct numeric values a document's prose states.
//
// Deduplicated by value, because a figure repeated in the description and an
// indicator is one figure and counting it twice would make coverage reward
// terseness rather than declaration.
func writtenNumerals(texts ...string) []float64 {
	seen := make(map[float64]struct{})
	var out []float64
	for _, text := range texts {
		for _, v := range numeralsIn(text) {
			key := roundTo(v, 6)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

func numeralsIn(text string) []float64 {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	masked := reDateish.ReplaceAllStringFunc(text, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
	var out []float64
	for _, m := range reNumeral.FindAllStringSubmatchIndex(masked, -1) {
		cur := group(masked, m, 1)
		sign := group(masked, m, 2)
		digits := group(masked, m, 3)
		suffix := strings.ToLower(strings.TrimSpace(strings.Trim(group(masked, m, 4), "^$")))
		suffix = strings.TrimRight(suffix, ".,;:)")

		// A numeral glued to a preceding word character is an identifier, not a
		// measurement: Q4, col1, Brand#35, x2. "#" is in the set because a brand
		// or part label is the commonest identifier in these documents and
		// without it every "Brand#35" became a reading of thirty-five.
		if s := m[2*3]; s > 0 {
			prev := masked[s-1]
			if isWordByte(prev) || prev == '.' || prev == '_' || prev == '#' {
				continue
			}
		}

		before := masked[maxInt(0, m[0]-12):m[0]]
		if reStepRef.MatchString(before) {
			continue
		}

		bare := cur == "" && suffix == ""
		frac := ""
		if i := strings.IndexByte(digits, '.'); i >= 0 {
			frac = digits[i+1:]
		}
		n, err := strconv.ParseFloat(strings.ReplaceAll(digits, ",", ""), 64)
		if err != nil {
			continue
		}
		if bare && frac == "" {
			iv := int(n)
			// A bare four-digit number in this range is a year, and a bare
			// number under thirteen is too common to attribute to any row.
			// Both exclusions are inherited from the pre-registered metric.
			//
			// The year exclusion is known to hide a real error: one corpus
			// wrote its window as "1992-2098" twice. Changing it here would
			// confound two interventions in one arm, so it is recorded as its
			// own candidate rather than folded into this one.
			if iv >= 1900 && iv <= 2100 {
				continue
			}
			if iv <= 12 {
				continue
			}
		}
		if sign == "-" {
			n = -n
		}
		if scale, ok := numeralScale[suffix]; ok {
			n *= scale
		}
		out = append(out, n)
	}
	return out
}

func group(s string, idx []int, n int) string {
	if 2*n+1 >= len(idx) || idx[2*n] < 0 {
		return ""
	}
	return s[idx[2*n]:idx[2*n+1]]
}

func isWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func roundTo(v float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
