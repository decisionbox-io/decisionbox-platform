package scoring

import "testing"

// TestCurrencySymbolNormalisesACodeToWhatAFigurePrints.
//
// This is what makes a configured money threshold reachable. A deployment's
// profile may state `USD` while every figure in the warehouse prints `$`, and
// the severity axis compares the two strings exactly — so a settings layer
// that stored, displayed or validated `USD` would accept a ladder the engine
// could never apply. One normalisation, exported, is what keeps those two
// answers the same.
func TestCurrencySymbolNormalisesACodeToWhatAFigurePrints(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"USD", "$"},
		{"usd", "$"},   // case-insensitive: a model that writes `try` means the lira
		{" EUR ", "€"}, // trimmed, because a trailing space is invisible in a form
		{"TRY", "₺"},
		{"$", "$"}, // already a symbol
		{"₺", "₺"},
		// Passed through as written, deliberately: a code is a correct and
		// readable answer where the symbol is ambiguous, and inventing one
		// would be worse than keeping the code. The kronor share "kr", the
		// renminbi's "¥" is the yen's, and the franc prints as "CHF".
		{"SEK", "SEK"},
		{"CNY", "CNY"},
		{"CHF", "CHF"},
		{"", ""},
	} {
		if got := CurrencySymbol(tc.in); got != tc.want {
			t.Errorf("CurrencySymbol(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestACodeThisMapDoesNotCarryIsStillOneCurrency.
//
// The one string the severity axis compares character for character, so the
// casing a caller happened to write must not survive into it: a profile that
// said `cad` and a ladder that said `CAD` were two currencies, and the ladder
// was stored, reported as saved, and ranked nothing.
func TestACodeThisMapDoesNotCarryIsStillOneCurrency(t *testing.T) {
	for _, in := range []string{"CAD", "cad", "Cad", " cad "} {
		if got := CurrencySymbol(in); got != "CAD" {
			t.Errorf("CurrencySymbol(%q) = %q, want CAD", in, got)
		}
	}

	// And the other dollars stay CODES, so they never collide with the "$" a
	// USD ladder normalises to. This is the pairing that matters: a Canadian
	// datasource stating CAD is not ranked by a dollar ladder.
	for _, code := range []string{"CAD", "AUD", "NZD", "SGD", "HKD"} {
		if got := CurrencySymbol(code); got == "$" {
			t.Errorf("CurrencySymbol(%q) = %q — it must not collide with USD", code, got)
		}
	}
	if CurrencySymbol("USD") != "$" {
		t.Errorf("CurrencySymbol(USD) = %q, want $", CurrencySymbol("USD"))
	}

	// A symbol is not cased letters, so nothing here touches one.
	for _, sym := range []string{"$", "€", "₺", "R$", "kr"} {
		if got := CurrencySymbol(sym); got != sym {
			t.Errorf("CurrencySymbol(%q) = %q, want it unchanged", sym, got)
		}
	}

	// Anything longer than a code is whatever the model wrote, untouched: an
	// invented normalisation of free text would be a worse answer than the
	// text.
	if got := CurrencySymbol("us dollars"); got != "us dollars" {
		t.Errorf("CurrencySymbol(%q) = %q, want it unchanged", "us dollars", got)
	}
}
