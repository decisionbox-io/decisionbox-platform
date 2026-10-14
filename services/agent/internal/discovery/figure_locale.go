package discovery

import "strings"

// How a language writes a number, which is not the same question as what language the
// prose is in.
//
// Go rendered every figure in US notation regardless: comma for thousands, point for the
// decimal. In English that is invisible. In Turkish it is a factor of a thousand -- a
// reader there sees "746.779" as seven hundred forty-six thousand, and "₺30.63M" as a
// thousands-grouped 3,063. Worse than being wrong consistently, the two notations
// appeared in one insight: the model wrote "-746.779 ₺" in a heading it authored itself
// and the renderer wrote "-₺746,779" in the body beneath it, the same amount twice, with
// no rule a reader could apply to tell which was which.
//
// Only the separators are localised here. The currency symbol stays in front of the
// digits in every locale, because moving it is a different change: the guards that stop a
// symbol being doubled match it as a prefix, and this file's own history is one of a
// prefix-only guard shipping with a hole in it.
type numberLocale struct {
	group   string // between thousands
	decimal string // before the fraction
}

// usNotation is the notation every figure was rendered in before this existed, and it
// stays the default for any language not listed below -- including the ones whose
// grouping is not a flat three digits. Hindi groups 12,34,567 and Chinese counts in
// ten-thousands; writing those as 1,234,567 is what we have always done and is a separate
// piece of work from the decimal mark, so they are deliberately left alone rather than
// half-converted.
var usNotation = numberLocale{group: ",", decimal: "."}

// commaDecimal is the other common European convention: the marks swap.
var commaDecimal = numberLocale{group: ".", decimal: ","}

// localeFor maps a project's output language to its number notation.
//
// Keyed by the language names the product offers in Settings, matched case-insensitively
// and accepting the endonym as well, because that is what a non-English deployment is
// likely to have stored. An unknown language keeps US notation: a wrong guess about an
// unfamiliar language is worse than the behaviour everyone has had until now.
func localeFor(language string) numberLocale {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "turkish", "türkçe", "turkce",
		"german", "deutsch",
		"spanish", "español", "espanol",
		"french", "français", "francais",
		"italian", "italiano",
		"portuguese", "português", "portugues",
		"dutch", "nederlands",
		"polish", "polski",
		"russian", "русский",
		"indonesian", "bahasa indonesia",
		"vietnamese", "tiếng việt", "tieng viet":
		return commaDecimal
	}
	return usNotation
}
