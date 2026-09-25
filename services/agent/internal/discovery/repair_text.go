package discovery

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The two repairs that need no model.
//
// A refuted claim is not always a judgement call. When the evaluator reports
// that 302 rows satisfy a filter and the sentence says 12, the correct sentence
// is already known -- asking a model to write it spends a call to be told a
// number Go computed. And when the round cap is spent and the claim is still
// false, removing the sentence is a text edit, not a decision.
//
// Both are deliberately timid. Substitution refuses unless the numeral it would
// change appears exactly once in every field that contains it, because a
// sentence with two 12s gives no evidence about which one is the count.
// Sentence removal refuses unless the claim is gone from every field afterwards,
// because a body that no longer makes the claim beside a Markdown rendition that
// still does is worse than one that consistently does.

// substituteRefutedCounts corrects every refuted cardinality claim whose true
// count Go can compute and whose numeral appears unambiguously in the text. It
// returns the claims it fixed, verbatim as they were refuted, and rewrites the
// insight in place.
//
// Only cardinality. A refuted rank could in principle be renumbered the same
// way, but "the second largest" and "rank 2" are the same claim in different
// words and only one of them is a numeral, so a numeric substitution would
// leave the prose saying the old thing. An "only" claim has no number at all.
// Those need a sentence, which is what the model is for.
func substituteRefutedCounts(ins *models.Insight, evidence map[int]StepRows) []string {
	var fixed []string
	for i := range ins.QuantifierClaims {
		if i >= len(ins.QuantifierVerdicts) {
			break
		}
		if ins.QuantifierVerdicts[i].Status != QuantifierFails {
			continue
		}
		c := &ins.QuantifierClaims[i]
		if c.Kind != QuantifierCardinality || c.Count <= 0 {
			continue
		}
		actual, ok := actualCardinality(*c, evidence)
		if !ok || actual == c.Count {
			continue
		}
		before := c.Claim
		if !substituteCount(ins, c, c.Count, actual) {
			continue
		}
		fixed = append(fixed, before)
	}
	return fixed
}

// actualCardinality recomputes the count a cardinality claim asserts, under the
// same scope rules and the same refusal on a capped step that the evaluator
// applies. It deliberately re-derives rather than parsing the number back out of
// the verdict's Reason: a count read out of a human-readable sentence is a
// second, undeclared format that the message would silently break.
func actualCardinality(c models.QuantifierClaim, evidence map[int]StepRows) (int, bool) {
	ev, ok := evidence[c.Step]
	if !ok || len(ev.Rows) == 0 {
		return 0, false
	}
	if isTruncated(ev.Quality) && !scopedWithinResult(c, len(ev.Rows)) {
		return 0, false
	}
	scope, err := scopeRows(ev.Rows, c)
	if err != nil {
		return 0, false
	}
	if c.Filter == "" {
		return len(scope), true
	}
	matched, err := filterRows(scope, c.Filter)
	if err != nil {
		return 0, false
	}
	return len(matched), true
}

// substituteCount replaces one numeral with another across every field of the
// insight that carries it, all or nothing.
//
// It refuses whenever any single field contains the numeral more than once. Two
// 12s in one sentence mean the substitution has to choose, and choosing wrongly
// turns a claim that was merely false into one that is false about something
// else. Refusing hands the claim to the model instead, which is the slower path
// but not the wrong answer.
func substituteCount(ins *models.Insight, c *models.QuantifierClaim, from, to int) bool {
	fromTok, toTok := strconv.Itoa(from), strconv.Itoa(to)

	fields := []*string{&ins.Name, &ins.Description, &ins.DescriptionMd, &c.Claim}
	for i := range ins.Indicators {
		fields = append(fields, &ins.Indicators[i])
	}

	hits := 0
	for _, f := range fields {
		switch len(standaloneNumber(*f, fromTok)) {
		case 0:
		case 1:
			hits++
		default:
			// Ambiguous in this field, so ambiguous overall.
			return false
		}
	}
	if hits == 0 {
		return false
	}
	for _, f := range fields {
		at := standaloneNumber(*f, fromTok)
		if len(at) != 1 {
			continue
		}
		*f = (*f)[:at[0]] + toTok + (*f)[at[0]+len(fromTok):]
	}
	c.Count = to
	return true
}

// standaloneNumber returns the byte offsets at which tok appears as a whole
// number rather than as part of a longer one.
//
// Written by hand rather than as \b<tok>\b because Go's word boundary treats a
// decimal point as a boundary: \b12\b matches inside "1.12" and inside "12.5",
// so a substitution on "12" would rewrite an unrelated figure's digits. Digits,
// '.', ',' and '_' all disqualify a neighbour here.
func standaloneNumber(text, tok string) []int {
	var out []int
	for i := 0; i+len(tok) <= len(text); i++ {
		if text[i:i+len(tok)] != tok {
			continue
		}
		if i > 0 && numAdjacent(text[i-1]) {
			continue
		}
		end := i + len(tok)
		if end < len(text) && numAdjacent(text[end]) {
			continue
		}
		out = append(out, i)
		i = end - 1
	}
	return out
}

func numAdjacent(c byte) bool {
	return (c >= '0' && c <= '9') || c == '.' || c == ',' || c == '_'
}

// dropClaimSentence removes the sentence carrying a claim from the insight's
// body, its Markdown rendition and its indicators, and reports whether the claim
// is gone from the insight afterwards.
//
// Name is never edited, so a claim that is the headline can never be fully
// removed and this always refuses it. That is deliberate: a headline is one
// clause, and removing the claim from it leaves either nothing or a fragment. An
// insight whose name is wrong and recorded as wrong is more use to a reader than
// one whose name is a fragment, so such a claim is reported unrepaired and ships
// with its verdict attached -- the state E1 and E3 already leave a refuted claim
// in. The refusal comes from the all-fields check at the bottom rather than an
// early return here, because that check is the invariant and a second guard for
// the same case was a branch no test could reach.
func dropClaimSentence(ins *models.Insight, claim string) bool {
	if claim == "" {
		return false
	}

	desc, md := ins.Description, ins.DescriptionMd
	kept := make([]string, 0, len(ins.Indicators))
	changed := false
	for _, s := range ins.Indicators {
		if containsFold(s, claim) {
			changed = true
			continue
		}
		kept = append(kept, s)
	}
	if out, ok := dropSentence(desc, claim); ok {
		desc, changed = out, true
	}
	if out, ok := dropSentence(md, claim); ok {
		md, changed = out, true
	}
	if !changed {
		return false
	}
	if len(kept) == 0 {
		kept = nil
	}

	after := *ins
	after.Description, after.DescriptionMd, after.Indicators = desc, md, kept
	if insightMentions(after, claim) {
		// Still there somewhere -- in the untouched Name, or in a Markdown
		// rendition the plain body no longer matches. A half-edited insight is
		// worse than an untouched one: it states the claim in one field and
		// denies it in another, and a reader has no way to tell which the
		// document meant. Nothing is committed.
		return false
	}
	ins.Description, ins.DescriptionMd, ins.Indicators = desc, md, kept
	return true
}

// dropSentence removes the sentence (or Markdown list item) containing needle.
// It returns false when needle is absent, or when the sentence is the whole
// text -- an empty description is not a repair.
func dropSentence(text, needle string) (string, bool) {
	if text == "" || needle == "" {
		return text, false
	}
	idx := indexFold(text, needle)
	if idx < 0 {
		return text, false
	}
	start := sentenceStart(text, idx)
	stop := sentenceEnd(text, idx+len(needle))
	// When the span is a whole line, its own newline goes with it. Leaving the
	// newline behind puts a blank line between the neighbours, which in Markdown
	// ends the list -- so removing one bullet would visibly break the two around
	// it. Where the line was its own paragraph the blank line is restored by the
	// collapse in tidyWhitespace, so both shapes come out right.
	if wholeLine(text, start, stop) {
		stop++
	}
	out := tidyWhitespace(text[:start] + text[stop:])
	if strings.TrimSpace(out) == "" {
		return text, false
	}
	return out, true
}

// wholeLine reports whether [start, stop) covers a line with nothing else on it.
func wholeLine(text string, start, stop int) bool {
	if stop >= len(text) || text[stop] != '\n' {
		return false
	}
	return start == 0 || text[start-1] == '\n'
}

// sentenceStart walks back from idx to the start of the sentence or list item
// containing it. A '.' only ends a sentence when whitespace follows, so "17.5%"
// and "v1.2" are not boundaries.
func sentenceStart(text string, idx int) int {
	start := 0
	for j := idx - 1; j >= 0; j-- {
		c := text[j]
		if c == '\n' {
			start = j + 1
			break
		}
		if (c == '.' || c == '!' || c == ';') && j+1 < len(text) && isSpaceByte(text[j+1]) {
			start = j + 1
			break
		}
		if c == '?' && j+1 < len(text) && isSpaceByte(text[j+1]) {
			start = j + 1
			break
		}
	}
	for start < idx && isSpaceByte(text[start]) {
		start++
	}
	return start
}

// sentenceEnd walks forward from the end of the needle to the end of its
// sentence, inclusive of the terminator. A newline ends it too, which is what
// removes a whole Markdown bullet rather than its text alone.
func sentenceEnd(text string, from int) int {
	for j := from; j < len(text); j++ {
		c := text[j]
		if c == '\n' {
			return j
		}
		if c == '.' || c == '!' || c == '?' || c == ';' {
			if j+1 >= len(text) || isSpaceByte(text[j+1]) {
				return j + 1
			}
		}
	}
	return len(text)
}

func isSpaceByte(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

var (
	reRunOfSpaces = regexp.MustCompile(`[ \t]{2,}`)
	reRunOfLines  = regexp.MustCompile(`\n{3,}`)
)

// tidyWhitespace closes the gap a removed sentence leaves behind, without
// reflowing the rest: two spaces become one, three or more newlines become the
// blank line that separates Markdown blocks.
func tidyWhitespace(s string) string {
	s = reRunOfSpaces.ReplaceAllString(s, " ")
	s = reRunOfLines.ReplaceAllString(s, "\n\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// insightMentions reports whether the claim text still appears anywhere a reader
// would see it.
func insightMentions(ins models.Insight, claim string) bool {
	if containsFold(ins.Name, claim) || containsFold(ins.Description, claim) ||
		containsFold(ins.DescriptionMd, claim) {
		return true
	}
	for _, s := range ins.Indicators {
		if containsFold(s, claim) {
			return true
		}
	}
	return false
}

func containsFold(haystack, needle string) bool { return indexFold(haystack, needle) >= 0 }

// indexFold is a case-insensitive strings.Index returning a byte offset into
// haystack. Case-insensitive because the claim is copied out of the prose by the
// model, and a capital at a sentence start is the difference that would
// otherwise make a claim unfindable in the sentence it came from.
//
// The offset is only usable for slicing while lowercasing preserves byte length,
// which it does for ASCII and does not for every script (Turkish dotted I grows
// a byte). Where it does not, this falls back to an exact match: the caller
// slices with what comes back, and an offset into a differently-sized string
// would cut mid-rune.
func indexFold(haystack, needle string) int {
	if needle == "" {
		return -1
	}
	lowerHay := strings.ToLower(haystack)
	if len(lowerHay) != len(haystack) {
		return strings.Index(haystack, needle)
	}
	lowerNeedle := strings.ToLower(needle)
	if len(lowerNeedle) != len(needle) {
		return strings.Index(haystack, needle)
	}
	return strings.Index(lowerHay, lowerNeedle)
}
