package scoring

import "strings"

// What currency a figure is in, as one string.
//
// This lives in the engine's package because the severity axis is the thing
// that compares currencies: a configured money threshold applies to an amount
// only when the two agree, character for character. Which means every place
// that stores, displays or validates a threshold has to arrive at the same
// string the engine will see -- the agent resolving a document's home
// currency, and the enterprise settings layer checking a ladder someone typed.
// Three readings of "is this the same currency" is how a ladder comes to be
// saved, reported as saved, and never applied.

// isoCurrencySymbols maps an ISO 4217 code to the symbol that currency prints.
//
// The schema asks for the symbol a figure prints and a model supplies the code about as
// often: on the run above, every figure that did state a currency stated the code, which
// rendered as the code in front of the number. Both are an answer to the question asked,
// so both are accepted and normalised to one of them.
//
// Deliberately short. A code is mapped only where the symbol is unambiguous and in
// common use for that currency; anything else is passed through, because a
// code is a correct, readable answer and an invented symbol is not. That rules out the
// Nordic kronor, which share "kr"; the renminbi, whose "¥" is the yen's; and the franc,
// which prints as "CHF" in the places that use it.
//
// It also rules out the other dollars. CAD and AUD are not here, so they stay
// codes and never collide with the USD ladder's "$" -- but a deployment that
// states the bare symbol "$" for one of them is asserting something no map
// can undo, because "$" does not identify a currency. A deployment reporting
// in more than one currency that prints the same symbol has to state the
// codes; the guide says so, and the engine can only compare what it is told.
var isoCurrencySymbols = map[string]string{
	"USD": "$",
	"EUR": "€",
	"GBP": "£",
	"JPY": "¥",
	"TRY": "₺",
	"INR": "₹",
	"RUB": "₽",
	"KRW": "₩",
	"ILS": "₪",
	"BRL": "R$",
	"NGN": "₦",
	"PHP": "₱",
	"VND": "₫",
	"UAH": "₴",
}

// currencySymbol is what to print for a currency the model named, by code or by symbol.
//
// Case-insensitive on the code, because a model that writes "try" means the lira. The
// symbols are not affected: none of them is a cased letter.
// CurrencySymbol is exported for that reason: the settings layer that
// validates a threshold ladder has to normalise exactly as this does.
func CurrencySymbol(stated string) string {
	c := strings.TrimSpace(stated)
	if c == "" {
		return ""
	}
	if sym, ok := isoCurrencySymbols[strings.ToUpper(c)]; ok {
		return sym
	}
	// A code this map does not carry is passed through UPPER-CASED, not as
	// written.
	//
	// Returning it as written leaked the caller's casing into the one string
	// the engine compares character for character: a profile that said `cad`
	// and a ladder that said `CAD` were two currencies, so the ladder was
	// stored, reported as saved, and ranked nothing -- the defect class this
	// whole axis is built to refuse. Only a three-letter ASCII code is
	// touched, because that is what an ISO code is; a symbol is not cased
	// letters, and anything else a model wrote stays exactly as it wrote it.
	if isISOCode(c) {
		return strings.ToUpper(c)
	}
	return c
}

// isISOCode reports whether this is shaped like an ISO 4217 code: exactly
// three ASCII letters. Deliberately shape alone -- the point is to normalise
// the casing of something a reader would recognise as a code, not to decide
// whether such a currency exists.
func isISOCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return true
}
