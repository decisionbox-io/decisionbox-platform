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

	settleInsightFigureCurrencies(insights, projectCurrencyPlan("TRY"), nil)

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

	settleInsightFigureCurrencies(insights, projectCurrencyPlan("TRY"), nil)

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

	settleInsightFigureCurrencies(insights, projectCurrencyPlan(""), nil)

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

	settleInsightFigureCurrencies(insights, projectCurrencyPlan("TRY"), nil)

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

// projectCurrencyPlan is the legacy single-warehouse shape: no datasources configured,
// so the project's currency is the only answer there is.
func projectCurrencyPlan(currency string) currencyPlan {
	return newCurrencyPlan(profileWithCurrency(currency), nil)
}

func profileWithCurrency(currency string) map[string]interface{} {
	return map[string]interface{}{"financials": map[string]interface{}{"local_currency": currency}}
}

func datasource(id, currency string) models.WarehouseConfig {
	wh := models.WarehouseConfig{ID: id}
	if currency != "" {
		wh.Profile = profileWithCurrency(currency)
	}
	return wh
}

func TestAFigureTakesTheCurrencyOfTheDatasourceItWasReadFrom(t *testing.T) {
	// A run can span datasources: the orchestrator holds one provider and one config
	// per warehouse and routes each step to the one it belongs to. Two of them can
	// report in different currencies, and an amount read from the second is in the
	// second's -- stamping the primary's over it is this file's own defect in a new
	// place.
	plan := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", "TRY"),
		datasource("eu", "EUR"),
	})
	stepByID := map[int]*models.ExplorationStep{
		3: {Step: 3, WarehouseID: "default"},
		7: {Step: 7, WarehouseID: "eu"},
	}
	insights := []models.Insight{{Figures: []models.Figure{
		{ID: "f1", Value: 1, Unit: models.UnitCurrency, Step: 3, Kind: "cell"},
		{ID: "f2", Value: 2, Unit: models.UnitCurrency, Step: 7, Kind: "cell"},
	}}}

	settleInsightFigureCurrencies(insights, plan, stepByID)

	if got := insights[0].Figures[0].Symbol(); got != "₺" {
		t.Errorf("the primary datasource's figure got %q", got)
	}
	if got := insights[0].Figures[1].Symbol(); got != "€" {
		t.Errorf("the secondary datasource's figure got %q, want the euro it was read in", got)
	}
}

func TestADatasourceWithNoCurrencyOfItsOwnFallsBackToTheProjects(t *testing.T) {
	// What WarehouseConfig.Profile documents: empty falls back to Project.Profile.
	plan := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{datasource("extra", "")})
	stepByID := map[int]*models.ExplorationStep{1: {Step: 1, WarehouseID: "extra"}}
	insights := []models.Insight{{Figures: []models.Figure{
		{ID: "f1", Value: 1, Unit: models.UnitCurrency, Step: 1, Kind: "cell"},
	}}}

	settleInsightFigureCurrencies(insights, plan, stepByID)

	if got := insights[0].Figures[0].Symbol(); got != "₺" {
		t.Errorf("got %q, want the project's currency", got)
	}
}

func TestAnUntaggedStepIsTheLegacyShape(t *testing.T) {
	// A single-warehouse run tags no step, which is every run before multi-warehouse.
	plan := newCurrencyPlan(profileWithCurrency("TRY"), nil)
	stepByID := map[int]*models.ExplorationStep{1: {Step: 1}}
	insights := []models.Insight{{Figures: []models.Figure{
		{ID: "f1", Value: 1, Unit: models.UnitCurrency, Step: 1, Kind: "cell"},
	}}}

	settleInsightFigureCurrencies(insights, plan, stepByID)

	if got := insights[0].Figures[0].Symbol(); got != "₺" {
		t.Errorf("got %q, want the project's currency", got)
	}
}

func TestAFigureCitingAStepThatDoesNotExistIsFilledOnlyWhereThereIsNoDoubt(t *testing.T) {
	// A figure can cite a step the run never took -- the unresolved-reference gate
	// exists because models do that. There is then nothing to read a datasource from,
	// so the only safe fill is a currency every datasource agrees on.
	agree := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", "TRY"), datasource("second", "TRY"),
	})
	disagree := newCurrencyPlan(profileWithCurrency("TRY"), []models.WarehouseConfig{
		datasource("default", "TRY"), datasource("eu", "EUR"),
	})
	orphan := func() []models.Insight {
		return []models.Insight{{Figures: []models.Figure{
			{ID: "f1", Value: 1, Unit: models.UnitCurrency, Step: 99, Kind: "cell"},
		}}}
	}

	ins := orphan()
	settleInsightFigureCurrencies(ins, agree, map[int]*models.ExplorationStep{})
	if got := ins[0].Figures[0].Symbol(); got != "₺" {
		t.Errorf("datasources agreeing: got %q, want the agreed currency", got)
	}

	ins = orphan()
	settleInsightFigureCurrencies(ins, disagree, map[int]*models.ExplorationStep{})
	if c := ins[0].Figures[0].Currency; c != "" {
		t.Errorf("datasources disagreeing: stamped %q, where no currency is knowable", c)
	}
}
