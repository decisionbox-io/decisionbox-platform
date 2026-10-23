package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The defect these tests pin: on a warehouse that does not report in dollars, a shipped
// sentence carried a dollar sign in front of the amount and the warehouse's real currency
// spelled out as a word immediately after it. Go wrote the symbol from a figure that left
// `currency` unset, and most of that run's currency figures left it unset. The currency
// was never missing -- the project profile states it, and every analysis prompt carries
// it.

func currencyFigure(id string, v float64) models.Figure {
	return models.Figure{ID: id, Value: v, Unit: models.UnitCurrency, Scale: "millions", Step: 1, Kind: "cell"}
}

func TestAnUnmarkedCurrencyFigurePrintsTheWarehousesCurrency(t *testing.T) {
	insights := []models.Insight{{
		Name:    "anomaly of {{f1}}",
		Figures: []models.Figure{currencyFigure("f1", 1234567890)},
	}}

	settleInsightFigureCurrencies(insights, "TRY")

	written := newFigureWriter("Turkish").renderFigure(insights[0].Figures[0])
	if strings.Contains(written, "$") {
		t.Errorf("a lira amount printed a dollar sign: %q", written)
	}
	if !strings.Contains(written, "₺") {
		t.Errorf("want the lira symbol in %q", written)
	}
}

func TestAnISOCodeBecomesTheSymbolItPrints(t *testing.T) {
	// The field is specified as the symbol a figure prints, and a model writes the
	// code about as often: on the run above, every figure that stated a currency at all
	// stated the code, which rendered as the code in front of the number.
	// Padding is trimmed rather than read as part of the code. Its own assertion
	// because a map key written with spaces reads like a typo.
	if got := currencySymbol("  TRY "); got != "₺" {
		t.Errorf("a padded code did not normalise: %q", got)
	}

	for code, want := range map[string]string{
		"TRY": "₺", "USD": "$", "EUR": "€", "GBP": "£", "try": "₺",
		// Already a symbol: left exactly as written.
		"₺": "₺", "€": "€",
		// No single symbol in common use, or one shared with another currency --
		// passed through rather than guessed at.
		"CHF": "CHF", "SEK": "SEK", "CNY": "CNY",
		// Not a currency this layer knows. The model's word stands; inventing a
		// symbol for it would be worse than printing what it asked for.
		"XYZ": "XYZ",
	} {
		if got := currencySymbol(code); got != want {
			t.Errorf("currencySymbol(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestACurrencyTheModelStatedIsNeverReplaced(t *testing.T) {
	// multi_currency is true on the warehouse this came from: individual documents
	// are in EUR and USD. A default that overwrote a stated currency would turn a
	// euro amount into lira, which is worse than the dollar it replaced.
	insights := []models.Insight{{Figures: []models.Figure{
		{ID: "f1", Value: 10, Unit: models.UnitCurrency, Currency: "€", Step: 1, Kind: "cell"},
		{ID: "f2", Value: 20, Unit: models.UnitCurrency, Currency: "USD", Step: 1, Kind: "cell"},
	}}}

	settleInsightFigureCurrencies(insights, "TRY")

	if got := insights[0].Figures[0].Symbol(); got != "€" {
		t.Errorf("euro figure became %q", got)
	}
	if got := insights[0].Figures[1].Symbol(); got != "$" {
		t.Errorf("dollar figure became %q", got)
	}
}

func TestWithoutAProjectCurrencyNothingChanges(t *testing.T) {
	// Every deployment that has not configured one keeps the behaviour it has had:
	// the figure stays unmarked and Symbol falls through to its dollar default.
	insights := []models.Insight{{Figures: []models.Figure{currencyFigure("f1", 1)}}}

	settleInsightFigureCurrencies(insights, "")

	if c := insights[0].Figures[0].Currency; c != "" {
		t.Errorf("stamped %q with no project currency to stamp", c)
	}
	if got := insights[0].Figures[0].Symbol(); got != "$" {
		t.Errorf("Symbol() = %q, want the unchanged default", got)
	}
}

func TestOnlyCurrencyFiguresAreTouched(t *testing.T) {
	// Symbol answers "$" for any figure, including a count. Stamping one would put a
	// currency on a number where nothing reads it and it describes nothing.
	insights := []models.Insight{{Figures: []models.Figure{
		{ID: "f1", Value: 6, Unit: models.UnitCount, Step: 1, Kind: "cell"},
		{ID: "f2", Value: 24.66, Unit: models.UnitPercent, Step: 1, Kind: "cell"},
	}}}

	settleInsightFigureCurrencies(insights, "TRY")

	for _, f := range insights[0].Figures {
		if f.Currency != "" {
			t.Errorf("%s (%s) was stamped %q", f.ID, f.Unit, f.Currency)
		}
	}
}

func TestLocalCurrencyComesFromTheProfile(t *testing.T) {
	for name, tc := range map[string]struct {
		profile map[string]interface{}
		want    string
	}{
		"stated":        {map[string]interface{}{"financials": map[string]interface{}{"local_currency": "TRY"}}, "TRY"},
		"padded":        {map[string]interface{}{"financials": map[string]interface{}{"local_currency": "  TRY "}}, "TRY"},
		"empty":         {map[string]interface{}{"financials": map[string]interface{}{"local_currency": ""}}, ""},
		"no key":        {map[string]interface{}{"financials": map[string]interface{}{"multi_currency": true}}, ""},
		"no financials": {map[string]interface{}{"business_info": map[string]interface{}{}}, ""},
		"not a string":  {map[string]interface{}{"financials": map[string]interface{}{"local_currency": 42}}, ""},
		"not a map":     {map[string]interface{}{"financials": "TRY"}, ""},
		"nil profile":   {nil, ""},
	} {
		if got := localCurrency(tc.profile); got != tc.want {
			t.Errorf("%s: localCurrency = %q, want %q", name, got, tc.want)
		}
	}
}

func TestARecommendationFigureIsNormalisedButNeverDefaulted(t *testing.T) {
	// A recommendation figure's currency is the one the insight it references prints
	// -- adopted after the reference resolves, never stated here. Defaulting it would
	// fill the field that adoption tests for, and a recommendation restating a euro
	// insight would print the local currency instead. See adoptResolvedFigureValues.
	recs := []models.Recommendation{{Figures: []models.Figure{
		{ID: "f1", Unit: models.UnitCurrency, Kind: "ref"},
		{ID: "f2", Unit: models.UnitCurrency, Currency: "TRY", Kind: "ref"},
	}}}

	normalizeRecommendationFigureCurrencies(recs)

	if c := recs[0].Figures[0].Currency; c != "" {
		t.Errorf("defaulted a recommendation figure to %q, which adoption can no longer fill", c)
	}
	if c := recs[0].Figures[1].Currency; c != "₺" {
		t.Errorf("a stated code was not normalised: %q", c)
	}
}

func TestNeitherPromptTellsTheModelToOmitTheCurrency(t *testing.T) {
	// The wording is why the field went unset: both layers said to omit it for
	// dollars, so an unmarked figure meant dollars by instruction. A reader of
	// either one has to come away knowing the field is always stated.
	for name, text := range map[string]string{
		"figure contract": figureContract,
		"insight schema":  insightCurrencyFieldDescription,
	} {
		lower := strings.ToLower(text)
		for _, forbidden := range []string{"omit it for dollars", "omit for dollars"} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s still says %q", name, forbidden)
			}
		}
		if !strings.Contains(lower, "always") {
			t.Errorf("%s does not tell the model the currency is always stated", name)
		}
	}
}
