package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

func newCorrelationEngine(lookup CorrelationLookupFunc) *ExplorationEngine {
	return NewExplorationEngine(ExplorationEngineOptions{
		Executors: map[string]*queryexec.QueryExecutor{
			"default":      newRoutingExecutor(testutil.NewMockWarehouseProvider("public")),
			"wh_analytics": newRoutingExecutor(testutil.NewMockWarehouseProvider("ga")),
		},
		PrimaryDatasource: "default",
		CorrelationLookup: lookup,
	})
}

func staticLookup(keys ...agentplugin.CorrelationKey) CorrelationLookupFunc {
	return func(context.Context, string, string) ([]agentplugin.CorrelationKey, error) { return keys, nil }
}

func confirmedPairing() agentplugin.CorrelationKey {
	return agentplugin.CorrelationKey{
		DatasourceID: "wh_analytics", SourceField: "transactionId",
		WithDatasourceID: "default", AnchorColumns: []string{"orders.order_id"},
		Grain: "transaction", State: agentplugin.CorrelationConfirmed,
	}
}

func declaredPairing() agentplugin.CorrelationKey {
	return agentplugin.CorrelationKey{
		DatasourceID: "wh_analytics", SourceField: "customEvent:loyalty_ref",
		WithDatasourceID: "default", AnchorColumns: []string{"customers.loyalty_id"},
		Grain: "customer", State: agentplugin.CorrelationManual,
	}
}

func rejectedPairing() agentplugin.CorrelationKey {
	return agentplugin.CorrelationKey{
		DatasourceID: "wh_analytics", SourceField: "userId",
		WithDatasourceID: "default", AnchorColumns: []string{"orders.customer_id"},
		Grain: "customer", State: agentplugin.CorrelationRejected,
		Reason: "a reviewer checked this and the two sides do not hold the same values",
	}
}

func runCorrelations(engine *ExplorationEngine, pair *CorrelationPair) (string, *models.ExplorationStep) {
	step := &models.ExplorationStep{Step: 1}
	out := engine.executeAction(context.Background(),
		&ExplorationAction{Action: "get_correlations", GetCorrelations: pair}, step)
	return out, step
}

// --- parsing -------------------------------------------------------------

func TestParseAction_GetCorrelations_KeyDrivenShape(t *testing.T) {
	action, err := ParseAction(`{"thinking":"check first","get_correlations":{"a":"wh_analytics","b":"default"}}`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action.Action != "get_correlations" {
		t.Fatalf("Action = %q, want get_correlations", action.Action)
	}
	if action.GetCorrelations == nil || action.GetCorrelations.A != "wh_analytics" || action.GetCorrelations.B != "default" {
		t.Fatalf("pair = %+v, want the two ids", action.GetCorrelations)
	}
}

// TestParseAction_GetCorrelations_ToolUseEnvelope covers the shape Claude and
// the OpenAI function-calling models emit whatever the prompt asks for.
func TestParseAction_GetCorrelations_ToolUseEnvelope(t *testing.T) {
	action, err := ParseAction(`{"name":"get_correlations","input":{"a":"wh_analytics","b":"default"}}`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action.Action != "get_correlations" {
		t.Fatalf("Action = %q, want get_correlations", action.Action)
	}
	if action.GetCorrelations == nil || action.GetCorrelations.B != "default" {
		t.Fatalf("pair = %+v, want the envelope's input", action.GetCorrelations)
	}
}

// TestParseAction_GetCorrelations_SurvivesBraceyPreamble is the regression
// guard for the extractor's action-key probe.
//
// A reasoning model emits an unbalanced brace in its prose or its <think>
// block before the real action. extractJSON picks the object that carries an
// action KEY, so an action missing from that list is shadowed by whatever
// fragment the scan latched onto — silently, and only on reasoning models.
func TestParseAction_GetCorrelations_SurvivesBraceyPreamble(t *testing.T) {
	const input = "<think>weigh the reviewed keys, consider the set {a, b</think>\n" +
		`{"get_correlations":{"a":"wh_analytics","b":"default"}}`
	action, err := ParseAction(input, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v (extractJSON=%q)", err, extractJSON(input))
	}
	if action.Action != "get_correlations" || action.GetCorrelations == nil {
		t.Fatalf("action = %+v, want the get_correlations object", action)
	}
}

// TestJSONHasActionKey_KnowsGetCorrelations pins the probe itself, in both the
// key-driven and tool-use shapes, so the guard above cannot pass for some
// other reason.
func TestJSONHasActionKey_KnowsGetCorrelations(t *testing.T) {
	for _, s := range []string{
		`{"get_correlations":{"a":"x","b":"y"}}`,
		`{"name":"get_correlations","input":{"a":"x","b":"y"}}`,
	} {
		if !jsonHasActionKey(s) {
			t.Errorf("jsonHasActionKey(%s) = false, want true", s)
		}
	}
}

// TestParseAction_GetCorrelationsOnlyInsideThinkStillErrors keeps the new
// action under the same house rule as the others: an action buried in a
// reasoning block with nothing actionable outside it is masked away and
// re-prompted, rather than executed from the model's own scratch work.
func TestParseAction_GetCorrelationsOnlyInsideThinkStillErrors(t *testing.T) {
	const resp = "<think>{\"get_correlations\":{\"a\":\"wh_analytics\",\"b\":\"default\"}}</think>\nOkay, done thinking."
	if _, err := ParseAction(resp, nil); err == nil {
		t.Fatalf("want an error; extractJSON=%q", extractJSON(resp))
	}
}

// TestParseAction_GetCorrelations_IncompletePairStillDispatches: the executor
// can name this run's datasources back, which is more use to a model than a
// parse error that only says the JSON was wrong.
func TestParseAction_GetCorrelations_IncompletePairStillDispatches(t *testing.T) {
	action, err := ParseAction(`{"get_correlations":{"a":"wh_analytics"}}`, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if action.Action != "get_correlations" {
		t.Fatalf("Action = %q, want get_correlations", action.Action)
	}
}

func TestParseAction_UnknownPayloadNamesGetCorrelations(t *testing.T) {
	_, err := ParseAction(`{"thinking":"nothing useful here"}`, nil)
	if err == nil {
		t.Fatal("want an error for an action-less object")
	}
	// The message is quoted into the repair nudge, so an action it omits is an
	// action the model is never told it could have used.
	if !strings.Contains(err.Error(), "get_correlations") {
		t.Fatalf("error = %q, want it to list get_correlations", err.Error())
	}
}

// --- the answer ----------------------------------------------------------

func TestGetCorrelations_RendersEveryState(t *testing.T) {
	engine := newCorrelationEngine(staticLookup(confirmedPairing(), declaredPairing(), rejectedPairing()))
	out, step := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	if step.QueryPurpose != "get_correlations" {
		t.Fatalf("QueryPurpose = %q", step.QueryPurpose)
	}
	if step.Error != "" {
		t.Fatalf("step.Error = %q, want none", step.Error)
	}
	for _, want := range []string{
		"`wh_analytics`.`transactionId` ↔ `default`.`orders.order_id`",
		"transaction grain, confirmed by a reviewer",
		"`wh_analytics`.`customEvent:loyalty_ref` ↔ `default`.`customers.loyalty_id`",
		"customer grain, declared by a reviewer",
		"`wh_analytics`.`userId` ↔ `default`.`orders.customer_id`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestGetCorrelations_RejectionLeadsAndCarriesItsReason is the whole point of
// the action: a rejection has to arrive as a prohibition with evidence, not as
// one row among several.
func TestGetCorrelations_RejectionLeadsAndCarriesItsReason(t *testing.T) {
	engine := newCorrelationEngine(staticLookup(confirmedPairing(), rejectedPairing()))
	out, _ := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	doNot := strings.Index(out, "DO NOT CORRELATE ON:")
	useInstead := strings.Index(out, "USE INSTEAD")
	if doNot < 0 || useInstead < 0 {
		t.Fatalf("both headings must appear:\n%s", out)
	}
	if doNot > useInstead {
		t.Errorf("the prohibition must come before the alternatives:\n%s", out)
	}
	if !strings.Contains(out, "do not hold the same values") {
		t.Errorf("the rejection must carry its reason:\n%s", out)
	}
	if !strings.Contains(out, "not a preference") {
		t.Errorf("the rejection must say it is not a preference:\n%s", out)
	}
	// The failure mode of a bare prohibition is a model reaching for the
	// neighbouring spelling of the same field.
	if !strings.Contains(out, "spelling") {
		t.Errorf("the spelling escape must be closed:\n%s", out)
	}
	// And a rejection is about two id-spaces, not about a hop direction.
	if !strings.Contains(out, "either direction") {
		t.Errorf("the rejection must hold both ways round:\n%s", out)
	}
}

// TestGetCorrelations_RejectionWithNoAlternativeSaysSo: going quiet here is
// what sends a model off to try the neighbouring key.
func TestGetCorrelations_RejectionWithNoAlternativeSaysSo(t *testing.T) {
	engine := newCorrelationEngine(staticLookup(rejectedPairing()))
	out, _ := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	if !strings.Contains(out, "USE INSTEAD: nothing") {
		t.Errorf("want an explicit 'nothing':\n%s", out)
	}
	if !strings.Contains(out, "record by record") {
		t.Errorf("want the record-grain prohibition:\n%s", out)
	}
	if !strings.Contains(out, "leave the correlation unmade") {
		t.Errorf("want the instruction to report the gap rather than invent a key:\n%s", out)
	}
}

// TestGetCorrelations_EmptyStaysNeutral guards the other direction. The hard
// wording is for decisions somebody made; an unreviewed pair must not turn
// into a soft refusal, or every uncurated project quietly gets more timid
// than it is today.
func TestGetCorrelations_EmptyStaysNeutral(t *testing.T) {
	engine := newCorrelationEngine(staticLookup())
	out, _ := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	if !strings.Contains(out, "says nothing either way") {
		t.Errorf("silence must not read as absence:\n%s", out)
	}
	for _, forbidden := range []string{"DO NOT", "USE INSTEAD", "not a preference"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("an unreviewed pair must not read as a prohibition (%q):\n%s", forbidden, out)
		}
	}
}

// TestGetCorrelations_ReadFailureIsNotSilence: a read that failed has not
// established that a pairing is unreviewed, and a rejection lost behind an
// error is the exact failure this action exists to prevent.
func TestGetCorrelations_ReadFailureIsNotSilence(t *testing.T) {
	engine := newCorrelationEngine(func(context.Context, string, string) ([]agentplugin.CorrelationKey, error) {
		return nil, errors.New("decision store unreachable")
	})
	out, step := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	if step.Error == "" {
		t.Error("a failed read must be recorded on the step")
	}
	if !strings.Contains(out, "NOT the same as no decision being recorded") {
		t.Errorf("a failure must not read as an empty answer:\n%s", out)
	}
	if strings.Contains(out, "says nothing either way") {
		t.Errorf("a failure must not borrow the unreviewed wording:\n%s", out)
	}
}

func TestGetCorrelations_UnknownDatasourceNamesTheValidOnes(t *testing.T) {
	engine := newCorrelationEngine(staticLookup(confirmedPairing()))
	out, step := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "wh_nope"})

	if step.Error == "" {
		t.Error("an unknown id must be recorded on the step")
	}
	if !strings.Contains(out, "wh_nope") {
		t.Errorf("want the offending id named:\n%s", out)
	}
	// A model cannot retry with an id it has not been told about.
	if !strings.Contains(out, "default") || !strings.Contains(out, "wh_analytics") {
		t.Errorf("want this run's datasources listed:\n%s", out)
	}
}

func TestGetCorrelations_OneDatasourceIsNotAPair(t *testing.T) {
	engine := newCorrelationEngine(staticLookup(confirmedPairing()))
	// "" resolves to the primary, so this asks about default ↔ default.
	out, step := runCorrelations(engine, &CorrelationPair{A: "", B: "default"})

	if step.Error == "" {
		t.Error("a single-datasource pair must be recorded on the step")
	}
	if !strings.Contains(out, "TWO datasources") {
		t.Errorf("want the pair requirement stated:\n%s", out)
	}
}

func TestGetCorrelations_MissingPairAsksForOne(t *testing.T) {
	engine := newCorrelationEngine(staticLookup(confirmedPairing()))
	out, step := runCorrelations(engine, nil)

	if step.Error == "" {
		t.Error("a missing pair must be recorded on the step")
	}
	if !strings.Contains(out, `"get_correlations": {"a"`) {
		t.Errorf("want the shape restated:\n%s", out)
	}
}

// TestGetCorrelations_NotWiredIsNotNothingDecided keeps the two apart: a
// deployment that cannot answer has not established that nobody decided.
func TestGetCorrelations_NotWiredIsNotNothingDecided(t *testing.T) {
	engine := newCorrelationEngine(nil)
	out, step := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	if step.Error == "" {
		t.Error("an unwired lookup must be recorded on the step")
	}
	if !strings.Contains(out, "not available on this run") {
		t.Errorf("want the unavailability stated:\n%s", out)
	}
	if strings.Contains(out, "says nothing either way") {
		t.Errorf("an unwired deployment must not borrow the unreviewed wording:\n%s", out)
	}
}

func TestGetCorrelations_BudgetIsReportedAndEnforced(t *testing.T) {
	calls := 0
	engine := NewExplorationEngine(ExplorationEngineOptions{
		Executors: map[string]*queryexec.QueryExecutor{
			"default":      newRoutingExecutor(testutil.NewMockWarehouseProvider("public")),
			"wh_analytics": newRoutingExecutor(testutil.NewMockWarehouseProvider("ga")),
		},
		PrimaryDatasource:           "default",
		MaxCorrelationLookupsPerRun: 1,
		CorrelationLookup: func(context.Context, string, string) ([]agentplugin.CorrelationKey, error) {
			calls++
			return []agentplugin.CorrelationKey{confirmedPairing()}, nil
		},
	})

	pair := &CorrelationPair{A: "wh_analytics", B: "default"}
	first, _ := runCorrelations(engine, pair)
	if !strings.Contains(first, "1 of 1 correlation lookups used") {
		t.Errorf("want the remaining budget surfaced:\n%s", first)
	}

	second, step := runCorrelations(engine, pair)
	if step.Error == "" {
		t.Error("budget exhaustion must be recorded on the step")
	}
	if !strings.Contains(second, "budget exhausted") {
		t.Errorf("want the exhaustion reported:\n%s", second)
	}
	// Exhausting the budget must not quietly retire the prohibitions: they are
	// in the system prompt for the whole run.
	if !strings.Contains(second, "still stand") {
		t.Errorf("want the standing rejections restated:\n%s", second)
	}
	if calls != 1 {
		t.Fatalf("lookup called %d times, want 1", calls)
	}
}

// TestGetCorrelations_GrainlessKeyRendersCleanly: a provider that reports no
// grain must not produce "( grain)" — the qualifier is built from what is
// actually known.
func TestGetCorrelations_GrainlessKeyRendersCleanly(t *testing.T) {
	bare := agentplugin.CorrelationKey{
		DatasourceID: "wh_analytics", SourceField: "orderRef",
		WithDatasourceID: "default", AnchorColumns: []string{"orders.order_id"},
		State: agentplugin.CorrelationConfirmed,
	}
	engine := newCorrelationEngine(staticLookup(bare))
	out, _ := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	if strings.Contains(out, " grain") {
		t.Errorf("a key with no grain must not render an empty one:\n%s", out)
	}
	if !strings.Contains(out, "(confirmed by a reviewer)") {
		t.Errorf("provenance must still render:\n%s", out)
	}
}

func TestGetCorrelations_MultipleAnchorColumnsAreAllNamed(t *testing.T) {
	multi := confirmedPairing()
	multi.AnchorColumns = []string{"orders.order_id", "refunds.order_id"}
	engine := newCorrelationEngine(staticLookup(multi))
	out, _ := runCorrelations(engine, &CorrelationPair{A: "wh_analytics", B: "default"})

	// Which order_id was meant is the reader's call, so both have to be there.
	if !strings.Contains(out, "`orders.order_id`, `refunds.order_id`") {
		t.Errorf("want every column named:\n%s", out)
	}
}

// TestExplorationRepairNudge_OffersTheActionOnlyWhereItExists.
//
// The nudge is sent the moment a response fails to parse. On a run whose
// contract requires the correlation check, a menu of four alternatives arriving
// right after the model's attempt at the fifth is the one moment the retry
// instruction would steer it off the check it was told to make. On a run that
// was never offered the action, teaching it here would be advertising something
// it cannot use.
func TestExplorationRepairNudge_OffersTheActionOnlyWhereItExists(t *testing.T) {
	err := errors.New("action JSON has no query, lookup_schema, search_tables, get_correlations, done flag")

	with := explorationRepairNudge(err, true)
	if !strings.Contains(with, `"get_correlations"`) {
		t.Errorf("want the action in the menu:\n%s", with)
	}
	// The lead lists the actions too, and a lead of four above a menu of five
	// is its own kind of confusing.
	if !strings.Contains(with, "search_tables, get_correlations, or done") {
		t.Errorf("want the lead to match the menu:\n%s", with)
	}

	without := explorationRepairNudge(err, false)
	if strings.Contains(without, "get_correlations") {
		t.Errorf("a run without the action must not be taught it here:\n%s", without)
	}
	// Everything else is unchanged for such a run.
	for _, want := range []string{`"query"`, `"lookup_schema"`, `"search_tables"`, `"done"`} {
		if !strings.Contains(without, want) {
			t.Errorf("missing %s from the unchanged menu:\n%s", want, without)
		}
	}
}
