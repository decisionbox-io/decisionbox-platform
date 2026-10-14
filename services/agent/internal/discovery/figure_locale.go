package discovery

import (
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"golang.org/x/text/number"
)

// How a figure is written down, which is not the same question as what language the prose
// is in.
//
// Go rendered every figure in US notation regardless: comma for thousands, point for the
// decimal. In English that is invisible. In Turkish it is a factor of a thousand -- a
// reader there sees "746.779" as seven hundred forty-six thousand. Worse than being wrong
// consistently, both notations appeared inside one insight: the model wrote "-746.779 ₺"
// in a heading it authored itself and the renderer wrote "-₺746,779" in the body beneath
// it, the same amount twice, with no rule a reader could apply to tell them apart.
//
// A writer is constructed once per run from the project's output language and carried by
// the things that write numbers, rather than passed to each of them. That is the shape
// x/text itself uses -- you hold a message.Printer -- and it is what keeps the prose and
// the figure table from disagreeing: there is one writer and both go through it.
type figureWriter struct {
	p *message.Printer
}

// newFigureWriter resolves the project's output language to a writer. An unrecognised
// language gets English, because a wrong guess about an unfamiliar language is worse than
// the notation everyone has had until now.
func newFigureWriter(outputLanguage string) figureWriter {
	return figureWriter{p: message.NewPrinter(tagFor(outputLanguage))}
}

// usWriter is the notation every figure was written in before this existed. Tests that
// are not about notation use it so their expectations stay readable.
var usWriter = newFigureWriter("English")

// tagFor maps the language names the product offers in Settings to a CLDR tag.
//
// Matched case-insensitively and accepting the endonym, because that is what a non-English
// deployment is likely to have stored. The grouping and decimal marks themselves come from
// CLDR via x/text rather than from a table here -- which is how Hindi gets 12,34,567
// instead of the 1,234,567 a hand-written three-digit rule would produce.
func tagFor(outputLanguage string) language.Tag {
	switch strings.ToLower(strings.TrimSpace(outputLanguage)) {
	case "turkish", "türkçe", "turkce":
		return language.Turkish
	case "german", "deutsch":
		return language.German
	case "spanish", "español", "espanol":
		return language.Spanish
	case "french", "français", "francais":
		return language.French
	case "italian", "italiano":
		return language.Italian
	case "portuguese", "português", "portugues":
		return language.Portuguese
	case "dutch", "nederlands":
		return language.Dutch
	case "polish", "polski":
		return language.Polish
	case "russian", "русский":
		return language.Russian
	case "indonesian", "bahasa indonesia":
		return language.Indonesian
	case "vietnamese", "tiếng việt", "tieng viet":
		return language.Vietnamese
	case "japanese", "日本語":
		return language.Japanese
	case "korean", "한국어":
		return language.Korean
	case "simplified chinese", "简体中文":
		return language.SimplifiedChinese
	case "traditional chinese", "繁體中文":
		return language.TraditionalChinese
	case "hindi", "हिन्दी":
		return language.Hindi
	case "arabic", "العربية":
		return language.Arabic
	case "hebrew", "עברית":
		return language.Hebrew
	case "thai", "ไทย":
		return language.Thai
	}
	return language.English
}

// digits writes one number with exactly the places the figure declares, grouped and
// pointed the way the language does it.
//
// Exactly those places, not CLDR's default for the locale: the interval a figure claims is
// half the last place PRINTED, and renderedValue derives that with its own strconv call.
// If this rounded differently the two would disagree and the claimed interval would be
// wrong, so both ends are pinned to f.Places() and there is a test for it.
//
// Only the digits are localised. The currency symbol stays in front and the percent sign
// stays behind, in every language -- CLDR would move both (Turkish writes %64, German
// 64 %), but the guards that stop a marker being doubled match them by position, and this
// file's history is one of a position guard shipping with a hole in it. Moving the markers
// is a change to those guards, not to this function.
func (w figureWriter) digits(v float64, places int) string {
	return w.p.Sprint(number.Decimal(v,
		number.MinFractionDigits(places), number.MaxFractionDigits(places)))
}
