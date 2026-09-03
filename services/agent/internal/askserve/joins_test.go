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
		req:   TurnRequest{ProjectID: "p1"},
		round: 2,
		querySummariesByID: map[string]QuerySummary{
			"q1": {Step: "q1", Columns: []string{"user_id", "spend"}},
		},
		queryStepsByID: map[string]queryStep{
			"q1": {datasource: "wh_a", columns: []string{"user_id", "spend"}, round: 1},
		},
	}
}

// sameBatchState is a turn whose q1 was issued in the round now executing — the
// model wrote both queries before seeing either result.
func sameBatchState() *turnState {
	st := twoHopState()
	st.queryStepsByID["q1"] = queryStep{datasource: "wh_a", columns: []string{"user_id", "spend"}, round: st.round}
	return st
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
	st := &turnState{req: TurnRequest{ProjectID: "p1"}, round: 1}
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
	st := &turnState{req: TurnRequest{ProjectID: "p1"}, round: 1}
	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.reject == "" {
		t.Fatal("a declaration citing a step that cannot exist must be rejected")
	}
	if !strings.Contains(js.reject, "no query result is available to join on yet") {
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
	// Verified is a claim about the KEY. The note must say so, because silence
	// would read as certification of something never checked.
	if !strings.Contains(js.note, "a shared customer key") {
		t.Fatalf("the note must carry the report's own words, got %q", js.note)
	}
	if !strings.Contains(js.note, "your declaration") {
		t.Fatalf("the note must say the filter itself was not checked, got %q", js.note)
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

func TestObservation_SaysNothingAboutScopeOnASingleDatasourceTurn(t *testing.T) {
	// The verdict never arises, so the observation must be byte-identical to
	// what every existing single-datasource turn already renders.
	sum := QuerySummary{Step: "q1", RowCount: 1, Columns: []string{"n"}, Preview: []map[string]interface{}{{"n": 1}}}
	obs := sum.observation()
	for _, word := range []string{"scope", "Scope", "join", "Join", "datasource"} {
		if strings.Contains(obs, word) {
			t.Fatalf("nothing about scope belongs in an ordinary observation (%q): %q", word, obs)
		}
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

// TestPrompt_TextPathShowsTheExactJoinsOnJSON pins that the shape appears where
// the model must reproduce it from prose alone. The parser refuses a wrong
// shape; a refusal the prompt gives no way to satisfy is a dead end.
func TestPrompt_TextPathShowsTheExactJoinsOnJSON(t *testing.T) {
	rt := &ProjectRuntime{Datasources: []DatasourceInfo{{ID: "wh_a"}, {ID: "wh_b"}}, PrimaryID: "wh_a"}
	multi, _ := rt.resolveTurnRouting("")

	text := buildSystemPrompt(rt, multi, Config{}, false)
	for _, want := range []string{`"joins_on":{"source_step":"q1","field":"user_id"}`, "source_step", "field"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the text prompt must show %q, since nothing else tells it the key names:\n%s", want, text)
		}
	}
	// A declaration the parser accepts must be constructible from the prompt.
	act, err := parseTurnAction(`{"query":"SELECT 1","datasource_id":"wh_b","joins_on":{"source_step":"q1","field":"user_id"}}`)
	if err != nil || act.JoinsOn == nil {
		t.Fatalf("the documented form must parse: act=%+v err=%v", act, err)
	}

	// The native path carries the contract in the tool schema instead, so its
	// prompt is not widened with a form the model cannot get wrong there.
	tools := buildSystemPromptForTools(rt, multi, Config{}, false)
	if strings.Contains(tools, `"joins_on":{"source_step"`) {
		t.Fatalf("the tools prompt should not repeat the JSON form:\n%s", tools)
	}

	// A turn that cannot hop gains nothing at all.
	single := &ProjectRuntime{Datasources: []DatasourceInfo{{ID: "wh_a"}}, PrimaryID: "wh_a"}
	pinned, _ := single.resolveTurnRouting("")
	if strings.Contains(buildSystemPrompt(single, pinned, Config{}, false), "joins_on") {
		t.Fatal("a single-datasource prompt must be unchanged")
	}
}

// TestPrompt_TextPathShowsTheFormOnAMixedShapeTurn covers the branch this epic
// actually exists for: a SQL datasource alongside a cube. The vocabulary block
// forks on shape, so the form has to be on both forks — and a mixed turn is the
// one most likely to hop, since a cube is enrichment and cannot answer alone.
func TestPrompt_TextPathShowsTheFormOnAMixedShapeTurn(t *testing.T) {
	for _, tc := range []struct {
		name        string
		datasources []DatasourceInfo
	}{
		{"sql beside a cube", []DatasourceInfo{{ID: "wh_a"}, cubeDatasource("wh_b")}},
		{"every datasource a cube", []DatasourceInfo{cubeDatasource("wh_a"), cubeDatasource("wh_b")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &ProjectRuntime{Datasources: tc.datasources, PrimaryID: tc.datasources[0].ID}
			routing, _ := rt.resolveTurnRouting("")
			got := buildSystemPrompt(rt, routing, Config{}, false)
			if !strings.Contains(got, `"joins_on":{"source_step":"q1","field":"user_id"}`) {
				t.Fatalf("the form must appear on this branch too:\n%s", got)
			}
		})
	}
}

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
	if !strings.Contains(second.ScopeNote, "not something checked here") {
		t.Fatalf("a verified hop must still say what was NOT checked, got %q", second.ScopeNote)
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

// --- same-batch tool calls ---------------------------------------------

func TestResolveJoinScope_RejectsAJoinOnAStepIssuedInTheSameBatch(t *testing.T) {
	wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		t.Fatal("a step the model has not seen must be rejected without consulting the report")
		return agentplugin.JoinKeyVerdict{}, nil
	})
	st := sameBatchState()

	js := st.resolveJoinScope(context.Background(), "wh_b", &joinDeclaration{SourceStep: "q1", Field: "user_id"})
	if js.reject == "" {
		t.Fatalf("want a rejection, got %+v", js)
	}
	if js.scoped != nil {
		t.Fatalf("a rejected query stamps no verdict, got %v", *js.scoped)
	}
	if js.outcome != joinOutcomeRejectedBatch {
		t.Fatalf("outcome = %q, want %q", js.outcome, joinOutcomeRejectedBatch)
	}
	if !strings.Contains(js.reject, "same step") {
		t.Fatalf("the rejection must say why the values could not have come from it, got %q", js.reject)
	}
}

func TestResolveJoinScope_SameBatchQueriesAreNotACrossDatasourceHop(t *testing.T) {
	agentplugin.ResetJoinKeyValidatorForTest()
	st := sameBatchState()

	// Two independent queries issued together against different datasources.
	// Neither could have filtered on the other, so neither is caveated.
	js := st.resolveJoinScope(context.Background(), "wh_b", nil)
	if js.scoped != nil {
		t.Fatalf("scoped = %v, want nil — these queries never touched each other", *js.scoped)
	}
	if js.note != "" || js.outcome != "" {
		t.Fatalf("want an entirely empty verdict, got %+v", js)
	}
}

func TestDescribeQuerySteps_OmitsStepsTheModelHasNotSeen(t *testing.T) {
	st := sameBatchState()
	if got := st.describeQuerySteps(); strings.Contains(got, "q1") {
		t.Fatalf("a repair message must not point at a step the next check rejects: %q", got)
	}
	st = twoHopState()
	if got := st.describeQuerySteps(); !strings.Contains(got, "q1") {
		t.Fatalf("an observed step must be offered: %q", got)
	}
}

// TestExecQuery_BatchedHopIsNeitherCertifiedNorCaveated drives the NATIVE tools
// path with two query_data calls in ONE assistant response — the shape that
// makes the round rule matter, and the one the JSON-text path cannot produce.
func TestExecQuery_BatchedHopIsNeitherCertifiedNorCaveated(t *testing.T) {
	wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		t.Fatal("a batched declaration must be rejected before the report is consulted")
		return agentplugin.JoinKeyVerdict{}, nil
	})
	tracked := captureJoinTelemetry(t)

	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "a", Name: string(actQuery), Input: map[string]any{"query": "SELECT user_id FROM sales.orders", "datasource_id": "wh_a"}},
				{ID: "b", Name: string(actQuery), Input: map[string]any{"query": "SELECT flagged FROM crm.users", "datasource_id": "wh_b",
					"joins_on": map[string]any{"source_step": "q1", "field": "user_id"}}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "done"}),
	}}
	rt := twoDatasourceRuntime(p, whA, whB, nil, nil)
	ensureToolProvider()
	rt.AIClient.SetProvenance("p1", "", "test-tools")

	store := &fakeStore{}
	(&runner{cfg: mwConfig(), store: store}).run(context.Background(),
		rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "q"})

	if len(store.events) != 2 {
		t.Fatalf("want two query events, got %d: %+v", len(store.events), store.events)
	}
	// The first ran; the second declared a join on a result that did not exist
	// when the model wrote it, so it is rejected rather than certified.
	if store.events[1].Error == "" {
		t.Fatalf("the batched declaration should have been rejected, got %+v", store.events[1])
	}
	if len(whB.Calls) != 0 {
		t.Fatalf("the rejected query must not reach the warehouse, got %d calls", len(whB.Calls))
	}
	if len(*tracked) != 1 || (*tracked)[0] != "declared/"+joinOutcomeRejectedBatch {
		t.Fatalf("tracked %v", *tracked)
	}
	// And the first query, whose only company was issued alongside it, carries
	// no cross-datasource caveat.
	if sum := summaryOf(t, store, 0); sum.Scoped != nil {
		t.Fatalf("the first query of a batch has nothing before it: scoped = %v", *sum.Scoped)
	}
}

// TestExecQuery_BatchInALaterRoundIsStillNotObserved pins that a step records
// the round it actually ran in. A turn whose first round runs one query and
// whose SECOND round batches two more is the only shape that can tell a real
// round from a hardcoded one: with a constant, the batched q2 would look like
// an earlier result and q3's declaration on it would be accepted.
func TestExecQuery_BatchInALaterRoundIsStillNotObserved(t *testing.T) {
	wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		t.Fatal("a step from this same round must be rejected before the report is consulted")
		return agentplugin.JoinKeyVerdict{}, nil
	})

	whA := testutil.NewMockWarehouseProvider("sales")
	whA.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"user_id"},
		Rows:    []map[string]interface{}{{"user_id": int64(7)}},
	}
	whB := testutil.NewMockWarehouseProvider("crm")
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		// Round 1: one query, observed normally.
		toolCall(string(actQuery), map[string]any{"query": "SELECT user_id FROM sales.a", "datasource_id": "wh_a"}),
		// Round 2: two queries at once; the second cites the first of THIS batch.
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "a", Name: string(actQuery), Input: map[string]any{"query": "SELECT user_id FROM sales.b", "datasource_id": "wh_a"}},
				{ID: "b", Name: string(actQuery), Input: map[string]any{"query": "SELECT flagged FROM crm.users", "datasource_id": "wh_b",
					"joins_on": map[string]any{"source_step": "q2", "field": "user_id"}}},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "done"}),
	}}
	rt := twoDatasourceRuntime(p, whA, whB, nil, nil)
	ensureToolProvider()
	rt.AIClient.SetProvenance("p1", "", "test-tools")

	store := &fakeStore{}
	(&runner{cfg: mwConfig(), store: store}).run(context.Background(),
		rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "q"})

	if len(store.events) != 3 {
		t.Fatalf("want three query events, got %d", len(store.events))
	}
	if store.events[2].Error == "" {
		t.Fatalf("a declaration on a step from this same round must be rejected, got %+v", store.events[2])
	}
	if !strings.Contains(store.events[2].Error, "same step") {
		t.Fatalf("rejection = %q, want it to name the reason", store.events[2].Error)
	}
	if len(whB.Calls) != 0 {
		t.Fatalf("the rejected query must not reach the warehouse, got %d calls", len(whB.Calls))
	}
}

func TestExecQuery_APanickingValidatorDegradesTheTurnInsteadOfKillingIt(t *testing.T) {
	wireValidator(t, func(context.Context, agentplugin.JoinKeyRequest) (agentplugin.JoinKeyVerdict, error) {
		panic("the report blew up")
	})
	tracked := captureJoinTelemetry(t)

	store, whB := hopTurn(t, `,"joins_on":{"source_step":"q1","field":"user_id"}`)

	// The user asked a question; a broken report is not a reason not to answer
	// it, only a reason not to certify the join.
	if len(whB.Calls) != 1 {
		t.Fatalf("the query should still have run, got %d calls", len(whB.Calls))
	}
	second := summaryOf(t, store, 1)
	if second.Scoped == nil || *second.Scoped {
		t.Fatalf("Scoped = %v, want false", second.Scoped)
	}
	if !strings.Contains(second.ScopeNote, "panicked") {
		t.Fatalf("ScopeNote = %q, want it to say the check failed", second.ScopeNote)
	}
	if len(*tracked) != 1 || (*tracked)[0] != "declared/"+joinOutcomeValidatorError {
		t.Fatalf("tracked %v", *tracked)
	}
}

// TestJoinsOn_BothPathsAgree runs the same declarations through the native tool
// parser and the JSON-text parser and requires identical verdicts.
//
// They diverged once: the text path decoded into a typed struct, so a real
// attempt spelled with the wrong keys lost every key it had and arrived looking
// like no attempt at all — no repair for the model, and a turn filed as an
// undeclared hop in the very measurement that decides whether undeclared hops
// get refused.
func TestJoinsOn_BothPathsAgree(t *testing.T) {
	for _, tc := range []struct {
		name    string
		joins   string
		wantErr bool
		want    *joinDeclaration
	}{
		{name: "complete", joins: `{"source_step":"q1","field":"user_id"}`, want: &joinDeclaration{SourceStep: "q1", Field: "user_id"}},
		{name: "absent", joins: ``},
		{name: "null", joins: `null`},
		{name: "empty object", joins: `{}`},
		{name: "missing field", joins: `{"source_step":"q1"}`, wantErr: true},
		{name: "missing source_step", joins: `{"field":"user_id"}`, wantErr: true},
		{name: "blank halves", joins: `{"source_step":"  ","field":"  "}`, wantErr: true},
		{name: "wrong key names entirely", joins: `{"step":"q1","column":"user_id"}`, wantErr: true},
		{name: "one right key, one wrong", joins: `{"source_step":"q1","column":"user_id"}`, wantErr: true},
		{name: "not an object", joins: `"q1.user_id"`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Text path.
			payload := `{"query":"SELECT 1"}`
			if tc.joins != "" {
				payload = `{"query":"SELECT 1","joins_on":` + tc.joins + `}`
			}
			textAct, textErr := parseTurnAction(payload)

			// Native tool path.
			in := map[string]any{"query": "SELECT 1"}
			if tc.joins != "" && tc.joins != "null" {
				var v any
				if err := json.Unmarshal([]byte(tc.joins), &v); err != nil {
					t.Fatalf("bad fixture: %v", err)
				}
				in["joins_on"] = v
			}
			toolAct, toolErr := toolCallToAction(gollmToolCall(in))

			if (textErr != nil) != tc.wantErr {
				t.Fatalf("text path err = %v, wantErr = %v", textErr, tc.wantErr)
			}
			if (toolErr != nil) != tc.wantErr {
				t.Fatalf("tool path err = %v, wantErr = %v", toolErr, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			for _, got := range []struct {
				path string
				decl *joinDeclaration
			}{{"text", textAct.JoinsOn}, {"tool", toolAct.JoinsOn}} {
				switch {
				case tc.want == nil && got.decl != nil:
					t.Fatalf("%s path: JoinsOn = %+v, want nil", got.path, got.decl)
				case tc.want != nil && got.decl == nil:
					t.Fatalf("%s path: JoinsOn = nil, want %+v", got.path, tc.want)
				case tc.want != nil && *got.decl != *tc.want:
					t.Fatalf("%s path: JoinsOn = %+v, want %+v", got.path, got.decl, tc.want)
				}
			}
		})
	}
}

// TestParseTurnAction_EnvelopeStubDoesNotHideARealDeclaration covers a payload
// carrying BOTH a tool envelope and a top-level joins_on stub. A stub is how a
// model spells "none", so it must not outrank the declaration beside it — the
// hop would be filed as undeclared with the declaration sitting right there.
func TestParseTurnAction_EnvelopeStubDoesNotHideARealDeclaration(t *testing.T) {
	for _, stub := range []string{`null`, `{}`, `{"source_step":"","field":""}`} {
		t.Run(stub, func(t *testing.T) {
			act, err := parseTurnAction(`{"name":"query_data","joins_on":` + stub +
				`,"input":{"query":"SELECT 1","joins_on":{"source_step":"q1","field":"user_id"}}}`)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if act.JoinsOn == nil {
				t.Fatal("the envelope's real declaration must survive the stub")
			}
			if act.JoinsOn.SourceStep != "q1" || act.JoinsOn.Field != "user_id" {
				t.Fatalf("JoinsOn = %+v", act.JoinsOn)
			}
		})
	}
	// A malformed top-level value keeps precedence: it carries content, so it
	// is an attempt, and the model gets it corrected rather than silently
	// replaced by the envelope's.
	if _, err := parseTurnAction(`{"name":"query_data","joins_on":{"step":"q1"},` +
		`"input":{"query":"SELECT 1","joins_on":{"source_step":"q1","field":"user_id"}}}`); err == nil {
		t.Fatal("a top-level attempt with wrong keys must be corrected, not overwritten")
	}

	// A real top-level declaration still outranks the envelope's, as every
	// other key-driven field does.
	act, err := parseTurnAction(`{"name":"query_data","joins_on":{"source_step":"q2","field":"order_id"},` +
		`"input":{"query":"SELECT 1","joins_on":{"source_step":"q1","field":"user_id"}}}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if act.JoinsOn == nil || act.JoinsOn.SourceStep != "q2" {
		t.Fatalf("JoinsOn = %+v, want the top-level declaration to win", act.JoinsOn)
	}
}
