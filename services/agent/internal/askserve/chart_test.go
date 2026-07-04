package askserve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/charts"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gowarehouse "github.com/decisionbox-io/decisionbox/libs/go-common/warehouse"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// chartCfg is a tool-loop config with charting enabled (ops kill-switch on).
func chartCfg() Config {
	return Config{
		MaxRounds: 8, MaxQueriesPerTurn: 6, MaxFetchRows: 1000, PreviewRows: 50,
		ChartsEnabled: true, ChartMaxPerAnswer: 3, ChartCaps: charts.DefaultCaps,
	}
}

// chartWarehouse returns a mock whose every query yields a small, chartable
// month/revenue result (a category + a numeric column).
func chartWarehouse() *testutil.MockWarehouseProvider {
	wh := testutil.NewMockWarehouseProvider("ds")
	wh.DefaultResult = &gowarehouse.QueryResult{
		Columns: []string{"month", "revenue"},
		Rows: []map[string]interface{}{
			{"month": "2024-01", "revenue": int64(100)},
			{"month": "2024-02", "revenue": int64(150)},
			{"month": "2024-03", "revenue": int64(210)},
		},
	}
	return wh
}

// validChartInput is a bar chart grounded in the chartWarehouse month/revenue
// preview, sourced from step `step`.
func validChartInput(step string) map[string]any {
	return map[string]any{
		"type":           "bar",
		"title":          "Revenue by month",
		"source_step_id": step,
		"x":              map[string]any{"field": "month", "type": "category"},
		"y":              []any{map[string]any{"field": "revenue"}},
		"data": []any{
			map[string]any{"month": "2024-01", "revenue": 100},
			map[string]any{"month": "2024-02", "revenue": 150},
			map[string]any{"month": "2024-03", "revenue": 210},
		},
	}
}

// runOnceToolsCharts runs a native-tool turn with EnableCharts set on the wire.
func runOnceToolsCharts(t *testing.T, p *scriptedToolProvider, wh *testutil.MockWarehouseProvider) *fakeStore {
	t.Helper()
	store := &fakeStore{}
	r := &runner{cfg: chartCfg(), store: store}
	rt := toolRuntime(p, wh, nil, "")
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "revenue by month?", EnableCharts: true})
	if store.final == nil {
		t.Fatal("turn did not finalize")
	}
	return store
}

func TestLoopTools_ChartOfferedOnlyWhenEnabledAndChartable(t *testing.T) {
	// Round 1 is ungrounded (no query yet) → render_chart withheld. Round 2, after
	// a non-truncated query, render_chart is offered.
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT month, revenue FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "done"}),
	}}
	runOnceToolsCharts(t, p, chartWarehouse())
	if hasTool(p.reqs[0].Tools, string(actRenderChart)) {
		t.Fatal("render_chart must not be offered before any chartable query")
	}
	if !hasTool(p.reqs[1].Tools, string(actRenderChart)) {
		t.Fatal("render_chart should be offered after a non-truncated query")
	}
}

func TestLoopTools_ChartGateOffWithoutCapability(t *testing.T) {
	// ChartsEnabled true but the caller did NOT set EnableCharts → the tool stays
	// dark. This is the enterprise-only entitlement gate (a checker-less agent
	// must not leak the tool).
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT month, revenue FROM ds.t"}),
		toolCall(string(actAnswer), map[string]any{"text": "done"}),
	}}
	store := &fakeStore{}
	r := &runner{cfg: chartCfg(), store: store}
	rt := toolRuntime(p, chartWarehouse(), nil, "")
	// EnableCharts intentionally left false.
	r.run(context.Background(), rt, TurnRequest{TurnID: "t1", SessionID: "s1", ProjectID: "p1", Question: "q"})
	for i, req := range p.reqs {
		if hasTool(req.Tools, string(actRenderChart)) {
			t.Fatalf("render_chart offered on round %d despite EnableCharts=false", i+1)
		}
	}
}

func TestLoopTools_RenderChartGroundsAndPersists(t *testing.T) {
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT month, revenue FROM ds.t"}),
		toolCall(string(actRenderChart), validChartInput("q1")),
		toolCall(string(actAnswer), map[string]any{"text": "revenue rose month over month"}),
	}}
	store := runOnceToolsCharts(t, p, chartWarehouse())
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q", store.final.Status)
	}
	if len(store.events) != 2 {
		t.Fatalf("events = %d, want query_data + render_chart", len(store.events))
	}
	ce := store.events[1]
	if ce.Name != string(actRenderChart) || ce.Error != "" {
		t.Fatalf("render_chart event = %+v", ce)
	}
	spec, ok := ce.Output.(charts.ChartSpec)
	if !ok {
		t.Fatalf("render_chart Output is %T, want charts.ChartSpec", ce.Output)
	}
	if spec.Type != charts.ChartBar || spec.SourceStepID != "q1" || len(spec.Data) != 3 {
		t.Fatalf("persisted spec wrong: %+v", spec)
	}
	if ce.Args["source_step_id"] != "q1" {
		t.Fatalf("render_chart args missing source ref: %+v", ce.Args)
	}
}

func TestLoopTools_RenderChartSameBatchAsQueryRefused(t *testing.T) {
	// A render_chart emitted in the SAME batch as a query is refused — charts may
	// only reference a prior-round, already-observed result.
	p := &scriptedToolProvider{responses: []gollm.ChatResponse{
		toolCall(string(actQuery), map[string]any{"query": "SELECT month, revenue FROM ds.t"}),
		{
			StopReason: "tool_use",
			ToolCalls: []gollm.ToolCall{
				{ID: "q2", Name: string(actQuery), Input: map[string]any{"query": "SELECT month, revenue FROM ds.u"}},
				{ID: "c1", Name: string(actRenderChart), Input: validChartInput("q1")},
			},
			Usage: gollm.Usage{InputTokens: 10, OutputTokens: 5},
		},
		toolCall(string(actAnswer), map[string]any{"text": "done"}),
	}}
	store := runOnceToolsCharts(t, p, chartWarehouse())
	// Only the two queries ran; the batched chart was refused (no chart event).
	for _, ev := range store.events {
		if ev.Name == string(actRenderChart) {
			t.Fatalf("render_chart in a query batch should have been refused, not executed: %+v", ev)
		}
	}
	round2Results := p.reqs[2].Messages[len(p.reqs[2].Messages)-1].ToolResults
	var refused bool
	for _, tr := range round2Results {
		if tr.CallID == "c1" && tr.IsError {
			refused = true
		}
	}
	if !refused {
		t.Fatal("the batched render_chart call should receive an error tool_result")
	}
}

// --- direct execRenderChart unit tests (grounding, self-heal, non-grounding, caps) ---

func groundedChartState() *turnState {
	return &turnState{
		round:          2,
		groundedEvents: 1,    // the query already grounded the turn
		chartsEnabled:  true, // entitled for this turn (the gate is tested separately)
		querySummariesByID: map[string]QuerySummary{
			"q1": {
				Step:    "q1",
				Columns: []string{"month", "revenue"},
				Preview: []map[string]interface{}{
					{"month": "2024-01", "revenue": int64(100)},
					{"month": "2024-02", "revenue": int64(150)},
					{"month": "2024-03", "revenue": int64(210)},
				},
			},
		},
	}
}

func TestExecRenderChart_AcceptsGroundedDoesNotGround(t *testing.T) {
	r := &runner{cfg: chartCfg(), store: &fakeStore{}}
	st := groundedChartState()
	raw, _ := json.Marshal(validChartInput("q1"))
	obs := r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})
	if st.chartsRendered != 1 {
		t.Fatalf("chartsRendered = %d, want 1", st.chartsRendered)
	}
	// A chart consumes evidence, it does not produce it — groundedEvents unchanged.
	if st.groundedEvents != 1 {
		t.Fatalf("render_chart must not ground: groundedEvents = %d, want 1", st.groundedEvents)
	}
	if len(st.events) != 1 || st.events[0].Error != "" || st.events[0].Output == nil {
		t.Fatalf("expected one accepted chart event, got %+v", st.events)
	}
	if got := st.events[0].Name; got != string(actRenderChart) {
		t.Fatalf("event name = %q", got)
	}
	_ = obs
}

func TestExecRenderChart_RejectsDerivedNumberAndSelfHeals(t *testing.T) {
	r := &runner{cfg: chartCfg(), store: &fakeStore{}}
	st := groundedChartState()
	bad := validChartInput("q1")
	bad["data"] = []any{map[string]any{"month": "2024-01", "revenue": 999}} // not an observed cell
	raw, _ := json.Marshal(bad)
	obs := r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})
	if st.chartsRendered != 0 {
		t.Fatalf("a rejected chart must not count: chartsRendered = %d", st.chartsRendered)
	}
	if len(st.events) != 1 || st.events[0].Error == "" {
		t.Fatalf("rejected chart should persist an error event: %+v", st.events)
	}
	if st.groundedEvents != 1 {
		t.Fatalf("rejected chart must not change grounding: %d", st.groundedEvents)
	}
	if obs == "" || obs[:14] != "Chart rejected" {
		t.Fatalf("observation should be a repair prompt, got %q", obs)
	}
}

func TestExecRenderChart_RejectsUnknownSourceStep(t *testing.T) {
	r := &runner{cfg: chartCfg(), store: &fakeStore{}}
	st := groundedChartState()
	raw, _ := json.Marshal(validChartInput("q7")) // no such step
	r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})
	if len(st.events) != 1 || st.events[0].Error == "" {
		t.Fatalf("unknown source step should be rejected: %+v", st.events)
	}
	if st.chartsRendered != 0 {
		t.Fatal("unknown-source chart must not count as rendered")
	}
}

func TestExecRenderChart_HonorsMaxPerAnswer(t *testing.T) {
	cfg := chartCfg()
	cfg.ChartMaxPerAnswer = 1
	r := &runner{cfg: cfg, store: &fakeStore{}}
	st := groundedChartState()
	raw, _ := json.Marshal(validChartInput("q1"))
	r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})
	obs := r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})
	if st.chartsRendered != 1 {
		t.Fatalf("chart cap not enforced: chartsRendered = %d, want 1", st.chartsRendered)
	}
	if obs == "" || obs[:19] != "chart limit reached" {
		t.Fatalf("second chart should hit the per-answer cap, got %q", obs)
	}
}

func TestExecRenderChart_RefusedWhenNotEntitled(t *testing.T) {
	// The tool is only offered when charts are enabled, but the JSON-text parser
	// accepts render_chart regardless and a provider could return an unoffered
	// call. execRenderChart must refuse for a non-entitled turn and persist
	// nothing — a prompt-injected chart must not become an artifact.
	r := &runner{cfg: chartCfg(), store: &fakeStore{}}
	st := groundedChartState()
	st.chartsEnabled = false // entitlement absent for this turn
	raw, _ := json.Marshal(validChartInput("q1"))
	obs := r.execRenderChart(context.Background(), st, &turnAction{Kind: actRenderChart, Chart: raw})
	if len(st.events) != 0 {
		t.Fatalf("a non-entitled chart must not persist an event: %+v", st.events)
	}
	if st.chartsRendered != 0 {
		t.Fatal("a non-entitled chart must not count as rendered")
	}
	if obs == "" {
		t.Fatal("expected a refusal observation")
	}
}

// --- native + text parity for render_chart parsing ---

func TestParseTurnAction_RenderChart(t *testing.T) {
	// Plain key form (JSON-text fallback path).
	act, err := parseTurnAction(`{"render_chart":{"type":"bar","source_step_id":"q2","x":{"field":"m"},"y":[{"field":"v"}],"data":[{"m":"a","v":1}]}}`)
	if err != nil {
		t.Fatalf("parse err = %v", err)
	}
	if act.Kind != actRenderChart || len(act.Chart) == 0 {
		t.Fatalf("parsed = %+v", act)
	}
	// The captured raw chart must decode to a spec.
	var probe map[string]any
	if json.Unmarshal(act.Chart, &probe) != nil || probe["source_step_id"] != "q2" {
		t.Fatalf("act.Chart not the chart object: %s", act.Chart)
	}

	// Tool-use envelope form.
	act, err = parseTurnAction(`{"name":"render_chart","input":{"type":"kpi","source_step_id":"q1","kpi":{"value":5,"value_field":"c"}}}`)
	if err != nil {
		t.Fatalf("envelope err = %v", err)
	}
	if act.Kind != actRenderChart {
		t.Fatalf("envelope kind = %q", act.Kind)
	}
}

func TestParseTurnAction_RenderChartWithAnyOtherActionRejected(t *testing.T) {
	// A chart mixed with ANY other action (terminal OR a data action) is rejected
	// rather than silently dropping the other action — matching the native batch
	// refusal. The chart references a prior query, so it must be emitted alone.
	for _, in := range []string{
		`{"render_chart":{"type":"bar","source_step_id":"q1"},"answer":"done"}`,
		`{"render_chart":{"type":"bar","source_step_id":"q1"},"decline":"x"}`,
		`{"render_chart":{"type":"bar","source_step_id":"q1"},"query":"SELECT 1"}`,
		`{"render_chart":{"type":"bar","source_step_id":"q1"},"search_tables":"users"}`,
		`{"render_chart":{"type":"bar","source_step_id":"q1"},"lookup_schema":["ds.t"]}`,
	} {
		if _, err := parseTurnAction(in); err == nil {
			t.Fatalf("expected rejection for chart+other-action payload: %s", in)
		}
	}
	// A chart on its own still parses.
	if act, err := parseTurnAction(`{"render_chart":{"type":"bar","source_step_id":"q1"}}`); err != nil || act.Kind != actRenderChart {
		t.Fatalf("a lone chart should parse: act=%+v err=%v", act, err)
	}
}

func TestToolCallToAction_RenderChart(t *testing.T) {
	tc := gollm.ToolCall{ID: "c1", Name: string(actRenderChart), Input: validChartInput("q2")}
	act, err := toolCallToAction(tc)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if act.Kind != actRenderChart {
		t.Fatalf("kind = %q", act.Kind)
	}
	spec, err := charts.Decode(act.Chart, charts.DefaultCaps)
	if err != nil {
		t.Fatalf("captured chart did not decode: %v", err)
	}
	if spec.SourceStepID != "q2" {
		t.Fatalf("decoded source = %q", spec.SourceStepID)
	}
}
