package askserve

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// --- helpers -----------------------------------------------------------

// twoHopState is a turn that has already run q1 against wh_a, returning the
// columns a later hop could carry across.
func twoHopState() *turnState {
	return &turnState{
		req: TurnRequest{ProjectID: "p1"},
		querySummariesByID: map[string]QuerySummary{
			"q1": {Step: "q1", Columns: []string{"user_id", "spend"}},
		},
		queryStepDatasource: map[string]string{"q1": "wh_a"},
	}
}

func wireValidator(t *testing.T, fn agentplugin.JoinKeyValidatorFunc) {
	t.Helper()
	agentplugin.ResetJoinKeyValidatorForTest()
	agentplugin.RegisterJoinKeyValidator("test-validator", fn)
	t.Cleanup(agentplugin.ResetJoinKeyValidatorForTest)
}

// captureJoinTelemetry swaps the tracker for one that records what was reported.
func captureJoinTelemetry(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := recordCrossDatasourceQuery
	recordCrossDatasourceQuery = func(declared bool, outcome string) {
		d := "undeclared"
		if declared {
			d = "declared"
		}
		got = append(got, d+"/"+outcome)
	}
	t.Cleanup(func() { recordCrossDatasourceQuery = prev })
	return &got
}

// --- resolveJoinScope --------------------------------------------------

func TestResolveJoinScope_SingleDatasourceTurnStampsNothing(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	st := twoHopState()

	// The second query runs against the SAME datasource as the first, so no
	// value can have crossed a boundary and the question never arises.
	js := st.resolveJoinScope(context.Background(), "wh_a", nil)
	if js.scoped != nil {
		t.Fatalf("scoped = %v, want nil (nothing to say)", *js.scoped)
	}
	if js.note != "" || js.reject != "" || js.outcome != "" {
		t.Fatalf("want an entirely empty verdict, got %+v", js)
	}
}

func TestResolveJoinScope_FirstQueryOfATurnStampsNothing(t *testing.T) {
	st := &turnState{req: TurnRequest{ProjectID: "p1"}}
	js := st.resolveJoinScope(context.Background(), "wh_a", nil)
	if js.scoped != nil || js.outcome != "" {
		t.Fatalf("the first query of a turn has nothing to have joined to: %+v", js)
	}
}

func TestResolveJoinScope_UndeclaredCrossDatasourceIsNotVerifiedNotRefused(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	st := twoHopState()

	js := st.resolveJoinScope(context.Background(), "wh_b", nil)
	if js.reject != "" {
		t.Fatalf("an omitted declaration must never block the query, got reject=%q", js.reject)
	}
	if js.scoped == nil || *js.scoped {
		t.Fatalf("scoped = %v, want an explicit false", js.scoped)
	}
	if js.outcome != joinOutcomeUndeclared {
		t.Fatalf("outcome = %q, want %q", js.outcome, joinOutcomeUndeclared)
	}
	if !strings.Contains(js.note, "joins_on") {
		t.Fatalf("the note must tell the model how to fix it, got %q", js.note)
	}
}

func TestResolveJoinScope_RejectsADeclarationThatContradictsTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   string
		decl     joinDeclaration
		outcome  string
		contains []string
	}{
		{
			name:     "step that never ran",
			target:   "wh_b",
			decl:     joinDeclaration{SourceStep: "q7", Field: "user_id"},
			outcome:  joinOutcomeRejectedStep,
			contains: []string{"q7", "q1"},
		},
		{
			name:     "step on this same datasource",
			target:   "wh_a",
			decl:     joinDeclaration{SourceStep: "q1", Field: "user_id"},
			outcome:  joinOutcomeRejectedSameDS,
			contains: []string{"q1", "wh_a"},
		},
		{
			name:     "column that step never returned",
			target:   "wh_b",
			decl:     joinDeclaration{SourceStep: "q1", Field: "account_id"},
			outcome:  joinOutcomeRejectedField,
			contains: []string{"account_id", "user_id, spend"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A validator that would verify anything — the rejection must come
			// from the turn's own record, before any report is consulted.
			wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
				t.Fatal("a self-contradictory declaration must be rejected without consulting the report")
				return agentplugin.JoinKeyVerdict{}, nil
			})
			st := twoHopState()
			js := st.resolveJoinScope(context.Background(), tc.target, &tc.decl)
			if js.reject == "" {
				t.Fatalf("want a rejection, got %+v", js)
			}
			if js.scoped != nil {
				t.Fatal("a rejected query produces no result, so it stamps no verdict")
			}
			if js.outcome != tc.outcome {
				t.Fatalf("outcome = %q, want %q", js.outcome, tc.outcome)
			}
			for _, want := range tc.contains {
				if !strings.Contains(js.reject, want) {
					t.Fatalf("rejection %q must contain %q so the model can repair it", js.reject, want)
				}
			}
		})
	}
}

func TestResolveJoinScope_UnknownStepIsRejectedEvenWhenTheTurnHasNoStepsYet(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	st := &turnState{req: TurnRequest{ProjectID: "p1"}}
	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.reject == "" {
		t.Fatal("a declaration citing a step that cannot exist must be rejected")
	}
	if !strings.Contains(js.reject, "no query has produced a result yet") {
		t.Fatalf("rejection should say the turn has no steps, got %q", js.reject)
	}
}

func TestResolveJoinScope_VerifiedJoinIsScopedAndSaysNothingMore(t *testing.T) {
	var seen agentplugin.JoinKeyRequest
	wireValidator(t, func(_ context.Context, req agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		seen = req
		return agentplugin.JoinKeyVerdict{Verified: true, Detail: "a shared customer key"}, nil
	})
	st := twoHopState()

	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.scoped == nil || !*js.scoped {
		t.Fatalf("scoped = %v, want true", js.scoped)
	}
	// Nothing to correct, so nothing is said: the observation exists to warn.
	if js.note != "" {
		t.Fatalf("a verified join needs no note, got %q", js.note)
	}
	want := agentplugin.JoinKeyRequest{ProjectID: "p1", SourceDatasourceID: "wh_a", TargetDatasourceID: "wh_b", Field: "user_id"}
	if seen != want {
		t.Fatalf("validator saw %+v, want %+v", seen, want)
	}
}

func TestResolveJoinScope_FieldMatchIgnoresCase(t *testing.T) {
	wireValidator(t, func(_ context.Context, req agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		// The field reaches the report as the model wrote it — the report,
		// not this code, decides how names compare.
		if req.Field != "USER_ID" {
			t.Fatalf("field = %q, want the model's own spelling", req.Field)
		}
		return agentplugin.JoinKeyVerdict{Verified: true}, nil
	})
	st := twoHopState()
	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "USER_ID"})
	if js.reject != "" {
		t.Fatalf("column casing varies by warehouse; %q should have matched, got %q", "USER_ID", js.reject)
	}
	if js.scoped == nil || !*js.scoped {
		t.Fatalf("scoped = %v, want true", js.scoped)
	}
}

func TestResolveJoinScope_UnverifiedDeclarationQuotesTheReport(t *testing.T) {
	wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		return agentplugin.JoinKeyVerdict{Detail: "the report lists only campaign_id between these datasources"}, nil
	})
	st := twoHopState()

	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.reject != "" {
		t.Fatalf("a report that does not list a field is not proof the join is wrong; must not block: %q", js.reject)
	}
	if js.scoped == nil || *js.scoped {
		t.Fatalf("scoped = %v, want false", js.scoped)
	}
	if js.outcome != joinOutcomeUnverified {
		t.Fatalf("outcome = %q, want %q", js.outcome, joinOutcomeUnverified)
	}
	if !strings.Contains(js.note, "campaign_id") {
		t.Fatalf("the note must carry the report's own words, got %q", js.note)
	}
}

func TestResolveJoinScope_NoValidatorWiredIsUnverifiedNotRefused(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	st := twoHopState()

	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.reject != "" {
		t.Fatalf("a deployment with no report must still answer questions, got reject=%q", js.reject)
	}
	if js.scoped == nil || *js.scoped {
		t.Fatalf("scoped = %v, want false — nothing checked it", js.scoped)
	}
	if js.outcome != joinOutcomeUnverified {
		t.Fatalf("outcome = %q, want %q", js.outcome, joinOutcomeUnverified)
	}
}

func TestResolveJoinScope_AnUnreachableReportDoesNotFailTheQuery(t *testing.T) {
	wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		return agentplugin.JoinKeyVerdict{Verified: true}, errors.New("schema cache unreachable")
	})
	st := twoHopState()

	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.reject != "" {
		t.Fatalf("an unreachable report is not a reason to refuse the user's query, got %q", js.reject)
	}
	if js.scoped == nil || *js.scoped {
		t.Fatalf("scoped = %v, want false — and never the verdict an errored validator returned", js.scoped)
	}
	if js.outcome != joinOutcomeValidatorError {
		t.Fatalf("outcome = %q, want %q", js.outcome, joinOutcomeValidatorError)
	}
	if !strings.Contains(js.note, "unreachable") {
		t.Fatalf("the note should say what went wrong, got %q", js.note)
	}
}

// --- telemetry ---------------------------------------------------------

func TestJoinScopeTrack_ReportsOnlyWhenTheQuestionArose(t *testing.T) {
	got := captureJoinTelemetry(t)
	joinScope{}.track(false)
	if len(*got) != 0 {
		t.Fatalf("an ordinary single-source query must report nothing, got %v", *got)
	}
	joinScope{outcome: joinOutcomeUndeclared}.track(false)
	joinScope{outcome: joinOutcomeVerified}.track(true)
	joinScope{outcome: joinOutcomeRejectedField}.track(true)
	want := []string{"undeclared/" + joinOutcomeUndeclared, "declared/" + joinOutcomeVerified, "declared/" + joinOutcomeRejectedField}
	if strings.Join(*got, "|") != strings.Join(want, "|") {
		t.Fatalf("tracked %v, want %v", *got, want)
	}
}

// --- observation rendering ---------------------------------------------

func TestObservation_OmitsTheScopeLineWhenThereIsNothingToSay(t *testing.T) {
	sum := QuerySummary{Step: "q1", RowCount: 1, Columns: []string{"n"}, Preview: []map[string]interface{}{{"n": 1}}}
	scoped := true
	withVerdict := sum
	withVerdict.Scoped = &scoped

	if sum.observation() != withVerdict.observation() {
		t.Fatalf("a verified result must read exactly like an unremarkable one:\n%q\nvs\n%q", sum.observation(), withVerdict.observation())
	}
	if strings.Contains(sum.observation(), "verified") {
		t.Fatalf("nothing about scope belongs in an ordinary observation: %q", sum.observation())
	}
}

func TestObservation_ScopeNoteSitsAheadOfTheSourcesOwnCaveats(t *testing.T) {
	scoped := false
	sum := QuerySummary{
		Step: "q2", RowCount: 1, Columns: []string{"n"}, Preview: []map[string]interface{}{{"n": 1}},
		Scoped: &scoped, ScopeNote: "SCOPE-NOTE-MARKER",
		Quality: []gowarehouse.QualityCaveat{{Kind: "sampled", Detail: "12% sample"}},
	}
	obs := sum.observation()
	scopeAt := strings.Index(obs, "SCOPE-NOTE-MARKER")
	caveatAt := strings.Index(obs, "12% sample")
	if scopeAt < 0 || caveatAt < 0 {
		t.Fatalf("both the scope note and the source caveat must be rendered:\n%s", obs)
	}
	// The source saying "these rows are not the answer" is the stronger claim,
	// so it keeps the tail position the loop reserves for corrections.
	if scopeAt > caveatAt {
		t.Fatalf("the scope note must precede the source's caveats:\n%s", obs)
	}
}

func TestObservation_ScopeNoteIsLastWhenTheSourceReportedNothing(t *testing.T) {
	scoped := false
	sum := QuerySummary{
		Step: "q2", RowCount: 1, Columns: []string{"n"}, Preview: []map[string]interface{}{{"n": 1}},
		Scoped: &scoped, ScopeNote: "SCOPE-NOTE-MARKER",
	}
	obs := sum.observation()
	if !strings.HasSuffix(obs, "SCOPE-NOTE-MARKER") {
		t.Fatalf("with no caveats to follow it the note takes the tail position:\n%s", obs)
	}
}

// --- summary serialisation ---------------------------------------------

func TestQuerySummary_ScopedIsAbsentUnlessItWasDecided(t *testing.T) {
	js, err := json.Marshal(QuerySummary{Step: "q1", Columns: []string{"n"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(js), "scoped") {
		t.Fatalf("an undecided verdict must not appear in the persisted summary: %s", js)
	}
	no := false
	js, err = json.Marshal(QuerySummary{Step: "q1", Columns: []string{"n"}, Scoped: &no})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(js), `"scoped":false`) {
		// omitempty on a *bool omits nil, not false — a decided "no" is the
		// whole point and must survive to the record.
		t.Fatalf("a decided false must be persisted as false: %s", js)
	}
}

// --- tool definition ---------------------------------------------------

func TestToolQueryData_JoinsOnAppearsOnlyForAMultiDatasourceTurn(t *testing.T) {
	single := toolQueryData(false, false)
	if _, ok := single.InputSchema["properties"].(map[string]interface{})["joins_on"]; ok {
		t.Fatal("a single-datasource turn has nothing to join across; its tool definition must be unchanged")
	}
	multi := toolQueryData(true, false)
	spec, ok := multi.InputSchema["properties"].(map[string]interface{})["joins_on"].(map[string]interface{})
	if !ok {
		t.Fatal("a multi-datasource turn must offer joins_on")
	}
	req, _ := spec["required"].([]string)
	if len(req) != 2 || req[0] != "source_step" || req[1] != "field" {
		t.Fatalf("joins_on required = %v, want both halves", req)
	}
	props, _ := spec["properties"].(map[string]interface{})
	if _, ok := props["source_step"]; !ok {
		t.Fatal("joins_on must declare source_step")
	}
	if _, ok := props["field"]; !ok {
		t.Fatal("joins_on must declare field")
	}
}

// --- argument parsing (both paths) -------------------------------------

// gollmToolCall wraps a query_data tool input as the provider delivers it.
func gollmToolCall(input map[string]any) gollm.ToolCall {
	return gollm.ToolCall{ID: "c1", Name: string(actQuery), Input: input}
}

func TestToolCallToAction_JoinsOn(t *testing.T) {
	base := map[string]any{"query": "SELECT 1"}
	with := func(j any) map[string]any {
		in := map[string]any{"query": "SELECT 1"}
		in["joins_on"] = j
		return in
	}

	t.Run("absent is not an error", func(t *testing.T) {
		act, err := toolCallToAction(gollmToolCall(base))
		if err != nil || act.JoinsOn != nil {
			t.Fatalf("act=%+v err=%v; an omitted declaration is allowed", act, err)
		}
	})
	t.Run("complete is carried through", func(t *testing.T) {
		act, err := toolCallToAction(gollmToolCall(with(map[string]any{"source_step": " q1 ", "field": " user_id "})))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if act.JoinsOn == nil || act.JoinsOn.SourceStep != "q1" || act.JoinsOn.Field != "user_id" {
			t.Fatalf("JoinsOn = %+v, want the trimmed halves", act.JoinsOn)
		}
	})
	t.Run("empty object is the same as absent", func(t *testing.T) {
		act, err := toolCallToAction(gollmToolCall(with(map[string]any{})))
		if err != nil || act.JoinsOn != nil {
			t.Fatalf("act=%+v err=%v", act, err)
		}
	})
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"missing field", map[string]any{"source_step": "q1"}},
		{"missing source_step", map[string]any{"field": "user_id"}},
		{"blank field", map[string]any{"source_step": "q1", "field": "  "}},
		{"not an object", "q1.user_id"},
	} {
		t.Run(tc.name+" is rejected", func(t *testing.T) {
			// Half a declaration reads like a checked hop and is not one.
			if _, err := toolCallToAction(gollmToolCall(with(tc.in))); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestParseTurnAction_JoinsOn(t *testing.T) {
	t.Run("plain key", func(t *testing.T) {
		act, err := parseTurnAction(`{"query":"SELECT 1","datasource_id":"wh_b","joins_on":{"source_step":"q1","field":"user_id"}}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if act.JoinsOn == nil || act.JoinsOn.SourceStep != "q1" || act.JoinsOn.Field != "user_id" {
			t.Fatalf("JoinsOn = %+v", act.JoinsOn)
		}
	})
	t.Run("tool envelope", func(t *testing.T) {
		act, err := parseTurnAction(`{"name":"query_data","input":{"query":"SELECT 1","joins_on":{"source_step":"q2","field":"order_id"}}}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if act.JoinsOn == nil || act.JoinsOn.SourceStep != "q2" || act.JoinsOn.Field != "order_id" {
			t.Fatalf("JoinsOn = %+v", act.JoinsOn)
		}
	})
	t.Run("absent", func(t *testing.T) {
		act, err := parseTurnAction(`{"query":"SELECT 1"}`)
		if err != nil || act.JoinsOn != nil {
			t.Fatalf("act=%+v err=%v", act, err)
		}
	})
	t.Run("half declared is rejected", func(t *testing.T) {
		if _, err := parseTurnAction(`{"query":"SELECT 1","joins_on":{"source_step":"q1"}}`); err == nil {
			t.Fatal("want an error for a declaration missing its field")
		}
	})
	t.Run("empty object is the same as absent", func(t *testing.T) {
		act, err := parseTurnAction(`{"query":"SELECT 1","joins_on":{}}`)
		if err != nil || act.JoinsOn != nil {
			t.Fatalf("act=%+v err=%v", act, err)
		}
	})
}

// --- the prompt --------------------------------------------------------

func TestPrompt_TellsAMultiDatasourceTurnToDeclareItsHop(t *testing.T) {
	rt := &ProjectRuntime{Datasources: []DatasourceInfo{{ID: "wh_a"}, {ID: "wh_b"}}, PrimaryID: "wh_a"}
	multi, _ := rt.resolveTurnRouting("")
	if !strings.Contains(buildSystemPrompt(rt, multi, Config{}, false), "joins_on") {
		t.Fatal("a turn that can hop must be told how to declare the hop")
	}
	// A turn with one datasource cannot hop, so its prompt must not gain a word.
	single := &ProjectRuntime{Datasources: []DatasourceInfo{{ID: "wh_a"}}, PrimaryID: "wh_a"}
	pinned, _ := single.resolveTurnRouting("")
	if strings.Contains(buildSystemPrompt(single, pinned, Config{}, false), "joins_on") {
		t.Fatal("a single-datasource prompt must be unchanged")
	}
}

// --- end to end through the loop ---------------------------------------

// hopTurn drives a two-hop turn: q1 on wh_a, then a second query on wh_b
// carrying whatever joins_on clause the caller supplies.
func hopTurn(t *testing.T, joins string) (*fakeStore, *testutil.MockWarehouseProvider) {
	t.Helper()
	whA := testutil.NewMockWarehouseProvider("sales")
	whA.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"user_id"},
		Rows:    []map[string]interface{}{{"user_id": int64(7)}},
	}
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"query":"SELECT user_id FROM sales.orders","datasource_id":"wh_a"}`,
		`{"query":"SELECT flagged FROM crm.users WHERE user_id IN (7)","datasource_id":"wh_b"` + joins + `}`,
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)
	return runMW(t, rt, TurnRequest{}, nil), whB
}

func summaryOf(t *testing.T, store *fakeStore, i int) QuerySummary {
	t.Helper()
	sum, ok := store.events[i].Output.(QuerySummary)
	if !ok {
		t.Fatalf("event %d output is %T, want a QuerySummary", i, store.events[i].Output)
	}
	return sum
}

func TestExecQuery_UndeclaredSecondHopIsReturnedButNotVerified(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	tracked := captureJoinTelemetry(t)

	store, whB := hopTurn(t, "")
	if len(whB.Calls) != 1 {
		t.Fatalf("the query must still run — an omitted declaration never blocks: %d calls", len(whB.Calls))
	}
	if len(store.events) != 2 {
		t.Fatalf("want two query events, got %d", len(store.events))
	}
	// The first hop had nothing before it, so it says nothing about scope.
	if first := summaryOf(t, store, 0); first.Scoped != nil {
		t.Fatalf("the first hop must stamp no verdict, got %v", *first.Scoped)
	}
	second := summaryOf(t, store, 1)
	if second.Scoped == nil || *second.Scoped {
		t.Fatalf("Scoped = %v, want false", second.Scoped)
	}
	if !strings.Contains(second.ScopeNote, "joins_on") {
		t.Fatalf("ScopeNote = %q", second.ScopeNote)
	}
	if len(*tracked) != 1 || (*tracked)[0] != "undeclared/"+joinOutcomeUndeclared {
		t.Fatalf("tracked %v", *tracked)
	}
}

func TestExecQuery_DeclaredAndVerifiedHopIsScoped(t *testing.T) {
	wireValidator(t, func(_ context.Context, req agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		if req.SourceDatasourceID != "wh_a" || req.TargetDatasourceID != "wh_b" || req.Field != "user_id" {
			t.Fatalf("validator saw %+v", req)
		}
		return agentplugin.JoinKeyVerdict{Verified: true, Detail: "shared customer key"}, nil
	})
	tracked := captureJoinTelemetry(t)

	store, whB := hopTurn(t, `,"joins_on":{"source_step":"q1","field":"user_id"}`)
	if len(whB.Calls) != 1 {
		t.Fatalf("the query should have run, got %d calls", len(whB.Calls))
	}
	second := summaryOf(t, store, 1)
	if second.Scoped == nil || !*second.Scoped {
		t.Fatalf("Scoped = %v, want true", second.Scoped)
	}
	if second.ScopeNote != "" {
		t.Fatalf("a verified hop needs no note, got %q", second.ScopeNote)
	}
	// The claim is persisted next to the query it qualifies.
	if got, _ := store.events[1].Args["joins_on"].(map[string]any); got == nil || got["field"] != "user_id" {
		t.Fatalf("the declaration must be recorded on the event, got %v", store.events[1].Args["joins_on"])
	}
	if len(*tracked) != 1 || (*tracked)[0] != "declared/"+joinOutcomeVerified {
		t.Fatalf("tracked %v", *tracked)
	}
}

func TestExecQuery_ContradictoryDeclarationRunsNoQuery(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	tracked := captureJoinTelemetry(t)

	store, whB := hopTurn(t, `,"joins_on":{"source_step":"q1","field":"nonexistent"}`)
	if len(whB.Calls) != 0 {
		t.Fatalf("a contradictory declaration must cost no warehouse work, got %d calls", len(whB.Calls))
	}
	if len(store.events) != 2 {
		t.Fatalf("want two events (the hop and the rejection), got %d", len(store.events))
	}
	rej := store.events[1]
	if rej.Error == "" || !strings.Contains(rej.Error, "nonexistent") {
		t.Fatalf("the rejection must be recorded with its reason, got %q", rej.Error)
	}
	if rej.Output != nil {
		t.Fatalf("a rejected query produced no result: %v", rej.Output)
	}
	if len(*tracked) != 1 || (*tracked)[0] != "declared/"+joinOutcomeRejectedField {
		t.Fatalf("tracked %v", *tracked)
	}
}

func TestExecQuery_RejectedDeclarationDoesNotGroundTheTurn(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		// The very first action declares a join to a step that does not exist.
		`{"query":"SELECT 1 FROM crm.users","datasource_id":"wh_b","joins_on":{"source_step":"q1","field":"user_id"}}`,
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runMW(t, rt, TurnRequest{}, nil)
	if len(whB.Calls) != 0 {
		t.Fatalf("no query should have run, got %d", len(whB.Calls))
	}
	// A rejected call observed no data, so it must not unlock the answer.
	if store.final.Status != "declined" {
		t.Fatalf("status = %q; an ungrounded turn must not answer", store.final.Status)
	}
}
