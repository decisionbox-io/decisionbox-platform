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
