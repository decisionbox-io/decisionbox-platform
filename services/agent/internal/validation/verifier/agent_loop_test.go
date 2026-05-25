package verifier

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// stubLLM scripts a sequence of Chat responses. Each call to Chat
// returns the next entry. Useful for driving the agent through
// deterministic round sequences.
type stubLLM struct {
	mu       sync.Mutex
	calls    int
	scripted []string
}

func (s *stubLLM) Chat(ctx context.Context, user, system string, max int) (*ai.ChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls >= len(s.scripted) {
		return nil, errors.New("stubLLM: no more scripted responses")
	}
	r := s.scripted[s.calls]
	s.calls++
	return &ai.ChatResult{Content: r, TokensIn: 100, TokensOut: 100}, nil
}

// stubExecutor wires deterministic tool results so the agent's
// round-by-round behaviour is testable. Every method records how many
// times it was called.
type stubExecutor struct {
	lookupCalls   int
	queryCalls    int
	readCalls     int
	queryResult   map[string]any
	readResult    map[string]any
	lookupResult  map[string]any
	queryErr      error
}

func (s *stubExecutor) LookupSchema(ctx context.Context, refs []string) (map[string]any, error) {
	s.lookupCalls++
	if s.lookupResult != nil {
		return s.lookupResult, nil
	}
	return map[string]any{"tables": []map[string]any{}}, nil
}
func (s *stubExecutor) QueryWarehouse(ctx context.Context, sql string) (map[string]any, error) {
	s.queryCalls++
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	if s.queryResult != nil {
		return s.queryResult, nil
	}
	return map[string]any{"row_count": 1, "rows": []map[string]any{{"x": 1}}, "truncated": false}, nil
}
func (s *stubExecutor) ReadStepRows(ctx context.Context, r StepRowsRequest) (map[string]any, error) {
	s.readCalls++
	if s.readResult != nil {
		return s.readResult, nil
	}
	return map[string]any{"step_id": r.StepID, "row_count": 1, "rows": []map[string]any{{"x": 1}}, "truncated": false}, nil
}

// happyVerdictJSON returns a structurally-valid verdict body.
func happyVerdictJSON(mode valmodels.AgentMode) string {
	return fmt.Sprintf(`{"submit_verdict": {"doc_id":"d","doc_kind":"insight","mode":%q,"claims_considered":["h"],"claim_verdicts":[{"claim_text":"h","is_headline":true,"status":"supported","evidence":{"kind":"step_row","step_id":1,"row":{"x":1}}}],"overall":"supported","overall_reason":""}}`, mode)
}

// minimalBundle returns a bundle the agent can run against — DocKind
// is insight, source step 1 is referenced.
func minimalBundle() Bundle {
	return Bundle{
		Doc: DocDigest{Kind: valmodels.DocInsight, ID: "d", Headline: "h", SourceStepIDs: []int{1}, Language: "English"},
		SourceSteps: []SourceStepDigest{{StepID: 1, SampleRows: []map[string]any{{"x": 1}}}},
		Warehouse: WarehouseInfo{Dialect: "BigQuery Standard SQL"},
	}
}

// TestRun_VerifierHappyPath — verifier emits a valid submit_verdict
// on round 0 with a step_row tool first.
func TestRun_VerifierHappyPath(t *testing.T) {
	llm := &stubLLM{scripted: []string{
		`{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`,
		happyVerdictJSON(valmodels.ModeVerifier),
	}}
	a, _ := NewAgent(llm, DefaultConfig())
	v, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Verify err: %v", err)
	}
	if v.Overall != valmodels.StatusSupported {
		t.Errorf("overall = %s, reason=%q", v.Overall, v.OverallReason)
	}
}

// TestRun_RefuterDisciplineRejectsToolLessVerdict — refuter that
// submits without any tool calls is rejected in normal rounds; the
// model then runs a tool and submits, which is accepted.
func TestRun_RefuterDisciplineRejectsToolLessVerdict(t *testing.T) {
	llm := &stubLLM{scripted: []string{
		happyVerdictJSON(valmodels.ModeRefuter), // tool-less attempt — rejected
		`{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`,
		happyVerdictJSON(valmodels.ModeRefuter), // now valid
	}}
	a, _ := NewAgent(llm, DefaultConfig())
	v, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute err: %v", err)
	}
	if v.Overall != valmodels.StatusSupported {
		t.Errorf("overall = %s, reason=%q", v.Overall, v.OverallReason)
	}
	if v.StepReadsUsed != 1 {
		t.Errorf("step_reads_used = %d, want 1", v.StepReadsUsed)
	}
}

// TestRun_RefuterForcedFinalDowngradeWhenNoTools — refuter that
// runs all rounds without calling tools and still submits on the
// forced-final round is downgraded to partial.
func TestRun_RefuterForcedFinalDowngradeWhenNoTools(t *testing.T) {
	// Tighten config so the loop runs 2 rounds then forces final.
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 2
	cfg.Refuter.TokenCap = 50000
	llm := &stubLLM{scripted: []string{
		happyVerdictJSON(valmodels.ModeRefuter), // r0 tool-less — rejected
		happyVerdictJSON(valmodels.ModeRefuter), // r1 tool-less — rejected (last normal round)
		happyVerdictJSON(valmodels.ModeRefuter), // forced final — accepted but downgraded
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute err: %v", err)
	}
	if v.Overall != valmodels.StatusPartial {
		t.Errorf("overall = %s, reason=%q; want partial via refuter discipline", v.Overall, v.OverallReason)
	}
	if !strings.Contains(v.OverallReason, "refuter discipline") {
		t.Errorf("reason should mention refuter discipline; got %q", v.OverallReason)
	}
}

// TestRun_LookupSchemaDoesNotSatisfyRefuterDiscipline — schema lookups
// are exploration, not evidence; the refuter must still call a row-
// fetching tool.
func TestRun_LookupSchemaDoesNotSatisfyRefuterDiscipline(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 3
	llm := &stubLLM{scripted: []string{
		`{"lookup_schema": ["demo.t"]}`,                       // r0 — only schema
		happyVerdictJSON(valmodels.ModeRefuter),               // r1 tool-less — rejected
		happyVerdictJSON(valmodels.ModeRefuter),               // r2 tool-less — last normal round
		happyVerdictJSON(valmodels.ModeRefuter),               // forced final — downgrade
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute err: %v", err)
	}
	if v.Overall != valmodels.StatusPartial {
		t.Errorf("overall = %s, want partial via refuter discipline", v.Overall)
	}
	if !strings.Contains(v.OverallReason, "refuter discipline") {
		t.Errorf("reason should mention refuter discipline; got %q", v.OverallReason)
	}
}

// TestRun_LLMTransportErrorIsUnverifiable — chat-layer error short-
// circuits to unverifiable (plan §"Tool error handling").
func TestRun_LLMTransportErrorIsUnverifiable(t *testing.T) {
	llm := &stubLLM{scripted: []string{}} // no scripted responses → returns error
	a, _ := NewAgent(llm, DefaultConfig())
	v, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Verify err: %v", err)
	}
	if v.Overall != valmodels.StatusUnverifiable {
		t.Errorf("overall = %s, want unverifiable", v.Overall)
	}
}

// TestRun_ParseErrorRetries — malformed JSON in round 0 ends up in
// recent_tool_errors; round 1 emits a valid verdict.
func TestRun_ParseErrorRetries(t *testing.T) {
	llm := &stubLLM{scripted: []string{
		`not json at all`,
		`{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`,
		happyVerdictJSON(valmodels.ModeVerifier),
	}}
	a, _ := NewAgent(llm, DefaultConfig())
	v, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Verify err: %v", err)
	}
	if v.Overall != valmodels.StatusSupported {
		t.Errorf("overall = %s, reason=%q", v.Overall, v.OverallReason)
	}
}

// TestRun_PerCallStateIsolation — two Verify calls on the same Agent
// produce independent token totals.
func TestRun_PerCallStateIsolation(t *testing.T) {
	llm := &stubLLM{scripted: []string{
		`{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`,
		happyVerdictJSON(valmodels.ModeVerifier),
		`{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`,
		happyVerdictJSON(valmodels.ModeVerifier),
	}}
	a, _ := NewAgent(llm, DefaultConfig())
	v1, _ := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	v2, _ := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	// Each call has 2 chat invocations (read + verdict), each
	// reporting TokensIn=100, TokensOut=100 from the stub.
	if v1.LLMTokensIn != 200 || v2.LLMTokensIn != 200 {
		t.Errorf("token isolation broken: v1=%d v2=%d (each should be 200)", v1.LLMTokensIn, v2.LLMTokensIn)
	}
}
