package discovery

import (
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Which currency a figure is in, settled before anything reads it.
//
// Figure.Symbol answers "$" for a figure that states no currency, and that default was
// chosen deliberately: when Go took over rendering, "omit for dollars" kept the common
// case unchanged rather than demanding every deployment restate the obvious. The cost
// only showed up on a warehouse that does not report in dollars. A run there left
// `currency` unset on most of its currency figures and printed a dollar sign over every
// one of them, including the amount in the sentence that shipped -- where the model had
// written the real currency as a word in the prose while leaving the field empty.
//
// The fact was never missing. The project profile states the warehouse's currency, and
// every analysis prompt carries it in the business context. Most of that run's analysis
// calls simply did not carry it into the structured field, which is what an optional
// field instructed to be omitted invites.
//
// So the currency is settled here, at ingest, rather than at print. Figure.Symbol is the
// only reader of the field, and everything downstream goes through it -- the renderer,
// the reference index that carries a currency into a recommendation, storage, the
// dashboard, the exports. Filling the field once makes a stored figure self-describing
// for all of them; defaulting at each print site would leave the stored number ambiguous
// and need every site to know about the project.
//
// What this cannot decide is a figure in a currency other than the warehouse's own. That
// warehouse reports multi_currency with EUR and USD in use, so a stated currency is
// always kept and only an unstated one is filled. Which currency a particular row is in
// is the model's to say, and the prompt is where it is asked -- see figureContract.

// isoCurrencySymbols maps an ISO 4217 code to the symbol that currency prints.
//
// The schema asks for the symbol a figure prints and a model supplies the code about as
// often: on the run above, every figure that did state a currency stated the code, which
// rendered as the code in front of the number. Both are an answer to the question asked,
// so both are accepted and normalised to one of them.
//
// Deliberately short. A code is mapped only where the symbol is unambiguous and in
// common use for that currency; anything else is passed through as written, because a
// code is a correct, readable answer and an invented symbol is not. That rules out the
// Nordic kronor, which share "kr"; the renminbi, whose "¥" is the yen's; and the franc,
// which prints as "CHF" in the places that use it.
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
func currencySymbol(stated string) string {
	c := strings.TrimSpace(stated)
	if c == "" {
		return ""
	}
	if sym, ok := isoCurrencySymbols[strings.ToUpper(c)]; ok {
		return sym
	}
	return c
}

// localCurrency reads the warehouse's own currency out of the project profile.
//
// The profile is free-form by design -- it is written by pack acceptance and rendered
// into the prompt as JSON -- so this reads one documented path and treats anything else
// as unset. An absent or malformed profile returns "", which leaves every figure exactly
// as the model wrote it and keeps Figure.Symbol's existing default.
func localCurrency(profile map[string]interface{}) string {
	financials, ok := profile["financials"].(map[string]interface{})
	if !ok {
		return ""
	}
	stated, ok := financials["local_currency"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stated)
}

// currencyPlan answers what currency an unmarked figure is in.
//
// Per datasource, not per run. A run can span several warehouses -- the orchestrator
// holds one provider and one config per datasource and routes a step to the one it
// belongs to -- and two warehouses can report in different currencies. One currency
// applied to every figure would stamp the primary's onto an amount read from a
// secondary, which is the mislabelling this file exists to prevent, in a new place.
type currencyPlan struct {
	// byWarehouse is each datasource's own currency, keyed the way a step tags itself.
	// A datasource that states none is absent and resolves to the project's, which is
	// what WarehouseConfig.Profile documents ("empty falls back to Project.Profile").
	byWarehouse map[string]string
	project     string
	// agreed is the currency to use for a figure whose own datasource cannot be
	// determined -- a figure citing a step that is not in the index. It is set only
	// while every datasource resolves to the same currency; where they disagree there
	// is no safe answer and nothing is filled.
	agreed string
}

// newCurrencyPlan reads the project's profile and each datasource's own.
//
// warehouses is empty on a legacy single-warehouse run, which leaves the project's
// currency as the only answer -- the behaviour a single-warehouse deployment wants and
// the one every existing run has.
func newCurrencyPlan(projectProfile map[string]interface{}, warehouses []models.WarehouseConfig) currencyPlan {
	p := currencyPlan{byWarehouse: map[string]string{}, project: localCurrency(projectProfile)}

	distinct := map[string]struct{}{}
	for _, wh := range warehouses {
		id := wh.ID
		if id == "" {
			id = models.DefaultWarehouseID
		}
		if c := localCurrency(wh.Profile); c != "" {
			p.byWarehouse[id] = c
		}
		distinct[p.forWarehouse(id)] = struct{}{}
	}
	if len(warehouses) == 0 {
		distinct[p.project] = struct{}{}
	}
	if len(distinct) == 1 {
		for c := range distinct {
			p.agreed = c
		}
	}
	return p
}

// forWarehouse is a datasource's currency: its own, or the project's where it states
// none. An unknown id resolves to the project's for the same reason.
func (p currencyPlan) forWarehouse(id string) string {
	if id == "" {
		id = models.DefaultWarehouseID
	}
	if c, ok := p.byWarehouse[id]; ok {
		return c
	}
	return p.project
}

// forStep is the currency of the datasource a figure's cited step queried.
//
// A figure names the step its number came from, and a step is tagged with the
// datasource it ran against. An untagged step is the legacy shape and resolves to the
// project's currency; a step that is not in the index at all is a figure citing
// something that did not happen, so there is nothing to read its currency from and the
// answer is whatever every datasource agrees on -- nothing, where they do not.
func (p currencyPlan) forStep(step int, stepByID map[int]*models.ExplorationStep) string {
	if s, ok := stepByID[step]; ok && s != nil {
		return p.forWarehouse(s.WarehouseID)
	}
	return p.agreed
}

// settleCurrencies fills and normalises the currency on a figure list, in place.
//
// A figure that states its own currency is only normalised, never replaced: on a
// multi-currency warehouse that statement is the only thing distinguishing an amount in
// another currency from a local one.
func settleCurrencies(figures []models.Figure, currencyFor func(models.Figure) string) {
	for i := range figures {
		f := &figures[i]
		// Asked only of a currency figure. Symbol answers "$" for a count too, so an
		// ungated fill would stamp a currency onto counts and percents, where nothing
		// reads it and it describes nothing.
		if f.Unit != models.UnitCurrency {
			continue
		}
		if strings.TrimSpace(f.Currency) == "" {
			f.Currency = currencySymbol(currencyFor(*f))
			continue
		}
		f.Currency = currencySymbol(f.Currency)
	}
}

// settleInsightFigureCurrencies settles every insight's figures against the currency of
// the datasource each figure was read from. Called once, on the way out of the analysis
// parse and before anything reads a figure, so every later phase sees a figure that says
// what it is in.
func settleInsightFigureCurrencies(insights []models.Insight, plan currencyPlan, stepByID map[int]*models.ExplorationStep) {
	for i := range insights {
		settleCurrencies(insights[i].Figures, func(f models.Figure) string {
			return plan.forStep(f.Step, stepByID)
		})
	}
}

// normalizeRecommendationFigureCurrencies normalises a stated currency on a
// recommendation's figures and fills nothing.
//
// No default here, deliberately. A recommendation figure's currency is the one the
// insight it references prints, adopted once the reference resolves -- the symbol was
// never the model's to state at this layer, and adoptResolvedFigureValues fills it only
// while the field is empty. Defaulting it would fill the field that adoption tests for,
// so a recommendation restating an insight in another currency would print the local one
// instead.
func normalizeRecommendationFigureCurrencies(recs []models.Recommendation) {
	for i := range recs {
		settleCurrencies(recs[i].Figures, func(models.Figure) string { return "" })
	}
}
