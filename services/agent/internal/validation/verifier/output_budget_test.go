package verifier

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// scriptedReply is one stubbed LLM response, including the fields the loop
// now reads to tell truncation from a malformed answer.
type scriptedReply struct {
	content    string
	stopReason string
	tokensOut  int
	// maxTokensAsked is what the call reports having actually requested,
	// which the adaptive context-overflow retry can lower below what the
	// loop passed in. Zero leaves it unreported.
	maxTokensAsked int
	// reducedBy is the limit that made the adaptive retry lower the budget.
	reducedBy ai.OverflowCause
}

// stubBudgetLLM scripts replies that carry a stop reason and an output-token
// count, which plain stubLLM does not.
type stubBudgetLLM struct {
	mu       sync.Mutex
	calls    int
	maxSeen  []int
	scripted []scriptedReply
}

func (s *stubBudgetLLM) Chat(_ context.Context, _, _ string, max int) (*ai.ChatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.maxSeen = append(s.maxSeen, max)
	if s.calls >= len(s.scripted) {
		return nil, errors.New("stubBudgetLLM: no more scripted responses")
	}
	r := s.scripted[s.calls]
	s.calls++
	return &ai.ChatResult{
		Content:         r.content,
		TokensIn:        100,
		TokensOut:       r.tokensOut,
		StopReason:      r.stopReason,
		MaxTokensAsked:  r.maxTokensAsked,
		BudgetReducedBy: r.reducedBy,
	}, nil
}

// A verdict cut off mid-JSON must be reported as a truncation, not as a model
// that declined to answer.
//
// This is the failure that cost a whole run: the refuter's forced-final
// verdict hit a fixed output ceiling, the JSON would not parse, and every
// document came back `unverifiable` with "produced no verdict" — which reads
// as a model problem and sent us looking at prompts. The two causes need
// opposite fixes, so the reason has to name the ceiling and the knob.
func TestForcedFinalTruncationIsAttributedToTheOutputCeiling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Verifier.MaxRounds = 1
	cfg.Verifier.TokenCap = 50000
	// An operator value, below both the model's cap and the envelope share,
	// so VALIDATION_VERIFIER_MAX_OUTPUT really is the knob that would move it.
	cfg.Verifier.MaxOutputTokens = 8000
	cfg.ApplyModelOutputCaps(128000, 128000)
	ceiling := cfg.Verifier.MaxOutputTokens
	if cfg.Verifier.OutputBound != OutputBoundByOperator {
		t.Fatalf("premise: the operator value should bind, got ceiling=%d bound=%d", ceiling, cfg.Verifier.OutputBound)
	}

	// Both rounds come back cut off at the ceiling, mid-object.
	halfWritten := `{"submit_verdict": {"doc_id": "d1", "claims_considered": ["a"], "claim_verdicts": [{"claim_text": "a", "status": "supp`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: halfWritten, stopReason: "max_tokens", tokensOut: ceiling},
		{content: halfWritten, stopReason: "max_tokens", tokensOut: ceiling},
	}}

	a, err := NewAgent(llm, cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	v, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if v.Overall != valmodels.StatusUnverifiable {
		t.Fatalf("overall = %s, want unverifiable", v.Overall)
	}
	if strings.Contains(v.OverallReason, "produced no verdict") {
		t.Errorf("reason still blames the model: %q", v.OverallReason)
	}
	if !strings.Contains(v.OverallReason, "cut off") {
		t.Errorf("reason does not say the response was cut off: %q", v.OverallReason)
	}
	if !strings.Contains(v.OverallReason, EnvVerifierOutput) {
		t.Errorf("reason does not name the knob to raise (%s): %q", EnvVerifierOutput, v.OverallReason)
	}
}

// The refuter's reason must name the refuter's own knob — raising the
// verifier's would change nothing.
func TestForcedFinalTruncationNamesTheRefutersKnob(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 1
	cfg.Refuter.TokenCap = 50000
	cfg.Refuter.MaxOutputTokens = 8000
	cfg.ApplyModelOutputCaps(128000, 128000)
	if cfg.Refuter.OutputBound != OutputBoundByOperator {
		t.Fatalf("premise: the operator value should bind, got bound=%d", cfg.Refuter.OutputBound)
	}

	cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if !strings.Contains(v.OverallReason, EnvRefuterOutput) {
		t.Errorf("reason names the wrong knob: %q", v.OverallReason)
	}
}

// A managed gateway may not forward the stop reason. The token count is then
// the only evidence, so "failed to parse AND spent the whole budget" has to
// count as truncation on its own.
func TestTruncationDetectedFromTheTokenCountAlone(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Verifier.MaxRounds = 1
	cfg.Verifier.TokenCap = 50000
	cfg.Verifier.MaxOutputTokens = 8000
	cfg.ApplyModelOutputCaps(128000, 128000)
	ceiling := cfg.Verifier.MaxOutputTokens

	cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: cut, stopReason: "", tokensOut: ceiling},
		{content: cut, stopReason: "", tokensOut: ceiling},
	}}
	a, _ := NewAgent(llm, cfg)
	v, _ := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if !strings.Contains(v.OverallReason, "cut off") {
		t.Errorf("truncation not inferred from the token count: %q", v.OverallReason)
	}
}

// A genuinely malformed answer that stopped early must NOT be blamed on the
// ceiling — otherwise the fix an operator reads is the wrong one.
func TestMalformedButCompleteAnswerIsNotCalledTruncation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Verifier.MaxRounds = 1
	cfg.Verifier.TokenCap = 50000
	cfg.Verifier.MaxOutputTokens = 8000
	cfg.ApplyModelOutputCaps(128000, 128000)

	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: "I am afraid I cannot help with that.", stopReason: "end_turn", tokensOut: 12},
		{content: "I am afraid I cannot help with that.", stopReason: "end_turn", tokensOut: 12},
	}}
	a, _ := NewAgent(llm, cfg)
	v, _ := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if strings.Contains(v.OverallReason, "cut off") {
		t.Errorf("a complete but unusable answer was reported as truncation: %q", v.OverallReason)
	}
	if !strings.Contains(v.OverallReason, "produced no verdict") {
		t.Errorf("reason = %q, want the generic no-verdict reason", v.OverallReason)
	}
}

// ApplyModelOutputCaps precedence, per mode.
//
// The old behaviour — a literal default, clamped down to the layer's model —
// is kept only as the no-information fallback. What changed is the middle
// tier: with no operator value, the ceiling now comes from the model.
func TestApplyModelOutputCapsPrecedence(t *testing.T) {
	// A model that reports a large cap: the ceiling comes from the model,
	// bounded by the loop's envelope (TokenCap / validationOutputShare).
	cfg := DefaultConfig()
	cfg.ApplyModelOutputCaps(128000, 128000)
	if want := cfg.Verifier.TokenCap / validationOutputShare; cfg.Verifier.MaxOutputTokens != want {
		t.Errorf("verifier ceiling = %d, want the envelope share %d", cfg.Verifier.MaxOutputTokens, want)
	}
	if want := cfg.Refuter.TokenCap / validationOutputShare; cfg.Refuter.MaxOutputTokens != want {
		t.Errorf("refuter ceiling = %d, want the envelope share %d", cfg.Refuter.MaxOutputTokens, want)
	}
	// The whole point: the refuter is no longer stuck at its old 3,000.
	if cfg.Refuter.MaxOutputTokens <= fallbackRefuterOutputTokens {
		t.Errorf("refuter ceiling = %d, want more than the old fixed %d",
			cfg.Refuter.MaxOutputTokens, fallbackRefuterOutputTokens)
	}

	// An unknown model (no catalog row, no live metadata) keeps the
	// documented fallbacks rather than collapsing to zero.
	cfg = DefaultConfig()
	cfg.ApplyModelOutputCaps(0, 0)
	if cfg.Verifier.MaxOutputTokens != fallbackVerifierOutputTokens {
		t.Errorf("verifier fallback = %d, want %d", cfg.Verifier.MaxOutputTokens, fallbackVerifierOutputTokens)
	}
	if cfg.Refuter.MaxOutputTokens != fallbackRefuterOutputTokens {
		t.Errorf("refuter fallback = %d, want %d", cfg.Refuter.MaxOutputTokens, fallbackRefuterOutputTokens)
	}

	// An operator value wins over the model's cap when it is lower — tuning
	// the budget down is a legitimate thing to do.
	cfg = DefaultConfig()
	cfg.Verifier.MaxOutputTokens = 2000
	cfg.ApplyModelOutputCaps(128000, 0)
	if cfg.Verifier.MaxOutputTokens != 2000 {
		t.Errorf("operator value = %d, want it honoured (2000)", cfg.Verifier.MaxOutputTokens)
	}

	// An operator value ABOVE what the model accepts is clamped to the
	// model: the provider would reject the request outright, and a rejected
	// call becomes an unverifiable verdict on a document that was fine.
	cfg = DefaultConfig()
	cfg.Verifier.MaxOutputTokens = 4000
	cfg.ApplyModelOutputCaps(1024, 0)
	if cfg.Verifier.MaxOutputTokens != 1024 {
		t.Errorf("operator value = %d, want clamping to the model's 1024", cfg.Verifier.MaxOutputTokens)
	}
}

// The envelope bound exists so a huge output ceiling cannot swallow the
// per-round reserve and send every document straight to its forced final
// round with no evidence gathered — trading a truncated verdict for an
// uninformed one.
func TestOutputCeilingNeverSwallowsTheEnvelope(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyModelOutputCaps(1000000, 1000000)
	for name, m := range map[string]ModeConfig{"verifier": cfg.Verifier, "refuter": cfg.Refuter} {
		if m.MaxOutputTokens >= m.TokenCap {
			t.Errorf("%s ceiling %d is not below its envelope %d", name, m.MaxOutputTokens, m.TokenCap)
		}
	}
}

// NewAgent must never leave the ceiling at zero: a caller that skips
// ApplyModelOutputCaps would otherwise ask the provider for zero output
// tokens, which some providers read as "give me everything".
func TestNewAgentResolvesAnUnsetCeiling(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Verifier.MaxOutputTokens != 0 || cfg.Refuter.MaxOutputTokens != 0 {
		t.Fatalf("DefaultConfig should leave the ceilings unresolved, got %d/%d",
			cfg.Verifier.MaxOutputTokens, cfg.Refuter.MaxOutputTokens)
	}
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: happyVerdictJSON(valmodels.ModeVerifier), stopReason: "end_turn", tokensOut: 500},
	}}
	a, err := NewAgent(llm, cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if _, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(llm.maxSeen) == 0 {
		t.Fatal("the agent made no call")
	}
	for i, got := range llm.maxSeen {
		if got <= 0 {
			t.Errorf("call %d asked for max_tokens = %d", i, got)
		}
	}
}

// midSizedBundle is a bundle whose prompt lands around 5,500 tokens — 50 rows
// of two wide cells, the shape an ordinary insight cites. Big enough that the
// envelope arithmetic matters, small enough that the loop should still afford
// a round.
func midSizedBundle() Bundle {
	rows := make([]map[string]any, 0, 50)
	wide := strings.Repeat("x", 180)
	for i := 0; i < 50; i++ {
		rows = append(rows, map[string]any{"a": wide, "b": wide, "i": i})
	}
	return Bundle{
		Doc:         DocDigest{Kind: valmodels.DocInsight, ID: "d1", Headline: "h", SourceStepIDs: []int{1}, Language: "English"},
		SourceSteps: []SourceStepDigest{{StepID: 1, SampleRows: rows}},
		Warehouse:   WarehouseInfo{Dialect: "BigQuery Standard SQL"},
	}
}

// Raising the output ceiling must not cost the loop its evidence round.
//
// The loop charges its output budget twice — the round it is about to run,
// plus the forced final it must still afford — so an implementation that
// reserved the CEILING made a more generous ceiling buy FEWER rounds: on a
// mid-sized bundle a 5,000-token ceiling left no room for a round that a
// 3,000-token one had afforded, and the refuter went straight to its forced
// final with zero tool calls. Reserving the floor and requesting the ceiling
// is what keeps both properties.
func TestRaisingTheCeilingKeepsTheEvidenceRound(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyModelOutputCaps(128000, 128000)

	exec := &stubExecutor{}
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: `{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`, stopReason: "end_turn", tokensOut: 40},
		{content: happyVerdictJSON(valmodels.ModeRefuter), stopReason: "end_turn", tokensOut: 900},
	}}
	a, err := NewAgent(llm, cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	v, err := a.Refute(context.Background(), midSizedBundle(), exec)
	if err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if exec.readCalls == 0 {
		t.Errorf("the refuter gathered no evidence: it went straight to the forced final round")
	}
	if v.Overall != valmodels.StatusSupported {
		t.Errorf("overall = %s (reason %q), want supported from the evidence-backed verdict", v.Overall, v.OverallReason)
	}
	// A normal round may be granted less than a verdict needs — it emits one
	// action, usually a tool call — but never less than an action takes.
	for i, got := range llm.maxSeen {
		if got < minRoundOutputTokens {
			t.Errorf("call %d asked for %d tokens, below the %d action floor", i, got, minRoundOutputTokens)
		}
	}
}

// The forced final may spend what the rounds left behind, so a verdict is not
// capped at the reservation floor when the envelope has room — that headroom
// is the whole point of resolving the ceiling from the model.
func TestForcedFinalSpendsTheRemainingEnvelope(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 0 // straight to the forced final, nothing spent yet
	cfg.ApplyModelOutputCaps(128000, 128000)

	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: happyVerdictJSON(valmodels.ModeRefuter), stopReason: "end_turn", tokensOut: 900},
	}}
	a, _ := NewAgent(llm, cfg)
	if _, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{}); err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if len(llm.maxSeen) != 1 {
		t.Fatalf("expected one call, got %d", len(llm.maxSeen))
	}
	if llm.maxSeen[0] <= fallbackRefuterOutputTokens {
		t.Errorf("forced final asked for %d tokens, want more than the old fixed %d now that the model allows it",
			llm.maxSeen[0], fallbackRefuterOutputTokens)
	}
	if llm.maxSeen[0] > cfg.Refuter.TokenCap {
		t.Errorf("forced final asked for %d tokens, above its own envelope %d", llm.maxSeen[0], cfg.Refuter.TokenCap)
	}
}

// When the ENVELOPE is what caps the verdict, the message must name the
// envelope knob. Pointing an operator at VALIDATION_*_MAX_OUTPUT there sends
// them to a knob that cannot move the number — the resolution re-applies the
// envelope share — so they would raise it, see no change, and conclude the
// diagnosis was wrong.
func TestTruncationNamesTheEnvelopeKnobWhenTheEnvelopeBinds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 1
	cfg.ApplyModelOutputCaps(0, 128000) // the model allows far more than the envelope
	if cfg.Refuter.OutputBound != OutputBoundByEnvelope {
		t.Fatalf("premise: the envelope should be the binding constraint, ceiling=%d cap=%d bound=%d",
			cfg.Refuter.MaxOutputTokens, cfg.Refuter.TokenCap, cfg.Refuter.OutputBound)
	}

	cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if !strings.Contains(v.OverallReason, EnvRefuterTokenCap) {
		t.Errorf("reason does not name the envelope knob %s: %q", EnvRefuterTokenCap, v.OverallReason)
	}
	if strings.Contains(v.OverallReason, EnvRefuterOutput) {
		t.Errorf("reason names the output knob, which cannot move this number: %q", v.OverallReason)
	}
}

// When the operator's own value is what caps the verdict, the output knob IS
// the one to name — raising the envelope would not help.
func TestTruncationNamesTheOutputKnobWhenTheOperatorValueBinds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 1
	cfg.Refuter.MaxOutputTokens = 1500 // well under the envelope share
	cfg.ApplyModelOutputCaps(0, 128000)
	if cfg.Refuter.OutputBound != OutputBoundByOperator {
		t.Fatalf("premise: the operator value should bind, got ceiling=%d bound=%d",
			cfg.Refuter.MaxOutputTokens, cfg.Refuter.OutputBound)
	}

	cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
	}}
	a, _ := NewAgent(llm, cfg)
	v, _ := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if !strings.Contains(v.OverallReason, EnvRefuterOutput) {
		t.Errorf("reason does not name the output knob %s: %q", EnvRefuterOutput, v.OverallReason)
	}
}

// When the MODEL's own cap is what limits the verdict, no validation setting
// can raise it — the resolution clamps straight back to the model. Sending an
// operator to VALIDATION_*_MAX_OUTPUT there costs them a deploy and teaches
// them to distrust the message.
func TestTruncationSaysSoWhenTheModelCapBinds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Refuter.MaxRounds = 1
	cfg.ApplyModelOutputCaps(0, 1024) // a small model, well under the envelope share
	if cfg.Refuter.OutputBound != OutputBoundByModel {
		t.Fatalf("premise: the model cap should bind, got ceiling=%d bound=%d",
			cfg.Refuter.MaxOutputTokens, cfg.Refuter.OutputBound)
	}

	cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
		{content: cut, stopReason: "max_tokens", tokensOut: cfg.Refuter.MaxOutputTokens},
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Refute(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if !strings.Contains(v.OverallReason, "larger output cap") {
		t.Errorf("reason does not point at the model: %q", v.OverallReason)
	}
	for _, knob := range []string{EnvRefuterOutput, EnvRefuterTokenCap} {
		if strings.Contains(v.OverallReason, knob) {
			t.Errorf("reason names %s, which cannot raise a model-bound ceiling: %q", knob, v.OverallReason)
		}
	}
}

// Several constraints can land on the same number, and then the remedy has to
// name the one that is hardest to move: raising an easier one is clamped
// straight back by the harder one, so the operator deploys, sees no change,
// and stops trusting the message.
func TestTiedConstraintsResolveToTheHardestOne(t *testing.T) {
	// Operator value == model cap: raising VALIDATION_*_MAX_OUTPUT is clamped
	// back to the model, so the model is what binds.
	cfg := DefaultConfig()
	cfg.Refuter.MaxOutputTokens = 1024
	cfg.ApplyModelOutputCaps(0, 1024)
	if cfg.Refuter.OutputBound != OutputBoundByModel {
		t.Errorf("operator value tied with the model cap: bound = %d, want model-bound", cfg.Refuter.OutputBound)
	}

	// Envelope share == model cap: raising TokenCap grows the share, but the
	// model cap then binds, so again the model is the honest answer.
	cfg = DefaultConfig()
	share := cfg.Refuter.TokenCap / validationOutputShare
	cfg.ApplyModelOutputCaps(0, share)
	if cfg.Refuter.OutputBound != OutputBoundByModel {
		t.Errorf("envelope share tied with the model cap (%d): bound = %d, want model-bound",
			share, cfg.Refuter.OutputBound)
	}

	// No tie: the operator's value is below both, and raising it works.
	cfg = DefaultConfig()
	cfg.Refuter.MaxOutputTokens = 1200
	cfg.ApplyModelOutputCaps(0, 128000)
	if cfg.Refuter.OutputBound != OutputBoundByOperator {
		t.Errorf("untied operator value: bound = %d, want operator-bound", cfg.Refuter.OutputBound)
	}
}

// The adaptive context-overflow retry lowers max_tokens mid-call, so the
// budget the loop asked for was never the one in force. Diagnosing against our
// own number names a ceiling that never applied — and sends the operator to a
// validation knob when the model's window was the limit.
func TestTruncationReportsTheBudgetActuallyInForce(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Verifier.MaxRounds = 1
	cfg.ApplyModelOutputCaps(128000, 0)
	asked := cfg.Verifier.MaxOutputTokens
	const retried = 1004 // what the overflow retry settled on

	cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: cut, stopReason: "max_tokens", tokensOut: retried, maxTokensAsked: retried, reducedBy: ai.OverflowContextWindow},
		{content: cut, stopReason: "max_tokens", tokensOut: retried, maxTokensAsked: retried, reducedBy: ai.OverflowContextWindow},
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !strings.Contains(v.OverallReason, strconv.Itoa(retried)) {
		t.Errorf("reason does not report the %d-token budget that was in force: %q", retried, v.OverallReason)
	}
	if strings.Contains(v.OverallReason, strconv.Itoa(asked)) {
		t.Errorf("reason reports the %d-token budget we asked for, which never applied: %q", asked, v.OverallReason)
	}
	if !strings.Contains(v.OverallReason, "context window") {
		t.Errorf("reason does not blame the model's window: %q", v.OverallReason)
	}
	for _, knob := range []string{EnvVerifierOutput, EnvVerifierTokenCap} {
		if strings.Contains(v.OverallReason, knob) {
			t.Errorf("reason names %s, but the call had already been reduced to fit the model: %q", knob, v.OverallReason)
		}
	}
}

// The two overflow causes have different remedies, and the reduced budget
// alone cannot tell them apart. Recommending a larger context window for a
// model whose window was ample — only its output cap was exceeded — points at
// the wrong model entirely.
func TestTruncationRemedyFollowsTheOverflowCause(t *testing.T) {
	run := func(t *testing.T, cause ai.OverflowCause) string {
		t.Helper()
		cfg := DefaultConfig()
		cfg.Verifier.MaxRounds = 1
		cfg.ApplyModelOutputCaps(128000, 0)
		const retried = 4015
		cut := `{"submit_verdict": {"doc_id": "d1", "claim_verd`
		llm := &stubBudgetLLM{scripted: []scriptedReply{
			{content: cut, stopReason: "max_tokens", tokensOut: retried, maxTokensAsked: retried, reducedBy: cause},
			{content: cut, stopReason: "max_tokens", tokensOut: retried, maxTokensAsked: retried, reducedBy: cause},
		}}
		a, _ := NewAgent(llm, cfg)
		v, err := a.Verify(context.Background(), minimalBundle(), &stubExecutor{})
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		return v.OverallReason
	}

	if reason := run(t, ai.OverflowOutputCap); !strings.Contains(reason, "larger output cap") {
		t.Errorf("output-cap overflow: reason = %q, want it to name the output cap", reason)
	} else if strings.Contains(reason, "context window") {
		t.Errorf("output-cap overflow: reason blames the context window: %q", reason)
	}

	if reason := run(t, ai.OverflowContextWindow); !strings.Contains(reason, "larger context window") {
		t.Errorf("window overflow: reason = %q, want it to name the context window", reason)
	}
}

// The forced final now spends what the rounds left behind, so everything a
// call actually sends has to be counted — including the system frame, which
// goes out on every one of them. It used to be uncounted and get away with it:
// nothing tried to spend the envelope down to the last token.
func TestTheEnvelopeIsNotOverspent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyModelOutputCaps(128000, 128000)

	// A refuter run that gathers evidence and then submits, so both a normal
	// round and the forced final draw on the envelope.
	llm := &stubBudgetLLM{scripted: []scriptedReply{
		{content: `{"read_step_rows": {"step_id": 1, "offset": 0, "limit": 10}}`, stopReason: "end_turn", tokensOut: 40},
		{content: `{"query_warehouse": "select 1"}`, stopReason: "end_turn", tokensOut: 40},
		{content: happyVerdictJSON(valmodels.ModeRefuter), stopReason: "end_turn", tokensOut: 900},
		{content: happyVerdictJSON(valmodels.ModeRefuter), stopReason: "end_turn", tokensOut: 900},
	}}
	a, _ := NewAgent(llm, cfg)
	v, err := a.Refute(context.Background(), midSizedBundle(), &stubExecutor{})
	if err != nil {
		t.Fatalf("Refute: %v", err)
	}

	// The envelope bounds input + output for the whole document. The stub
	// reports 100 input tokens per call, so what the loop can be held to is
	// the budget it ASKED for: no call may be granted more than the envelope
	// still had room for.
	spent := 0
	for i, asked := range llm.maxSeen {
		spent += 100 + asked
		if spent > cfg.Refuter.TokenCap {
			t.Errorf("call %d pushed the committed budget to %d, over the %d envelope (asked: %v)",
				i, spent, cfg.Refuter.TokenCap, llm.maxSeen)
			break
		}
	}
	if v.Overall == "" {
		t.Error("no verdict produced")
	}
}

// The forced final is the one call that must always be able to emit a
// complete verdict, which is what every round reserves for. It must never be
// squeezed below that floor, however much the rounds spent.
func TestForcedFinalIsNeverSqueezedBelowTheVerdictFloor(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ApplyModelOutputCaps(128000, 128000)

	// Rounds that keep gathering until the loop runs out, so the forced final
	// is reached with as little envelope left as the loop allows.
	replies := make([]scriptedReply, 0, cfg.Refuter.MaxRounds+1)
	for i := 0; i < cfg.Refuter.MaxRounds; i++ {
		replies = append(replies, scriptedReply{
			content: `{"query_warehouse": "select 1"}`, stopReason: "end_turn", tokensOut: 2000,
		})
	}
	replies = append(replies, scriptedReply{
		content: happyVerdictJSON(valmodels.ModeRefuter), stopReason: "end_turn", tokensOut: 900,
	})
	llm := &stubBudgetLLM{scripted: replies}
	a, _ := NewAgent(llm, cfg)
	if _, err := a.Refute(context.Background(), midSizedBundle(), &stubExecutor{}); err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if len(llm.maxSeen) == 0 {
		t.Fatal("no calls were made")
	}
	forced := llm.maxSeen[len(llm.maxSeen)-1]
	if forced < cfg.Refuter.MinOutputTokens {
		t.Errorf("forced final asked for %d tokens, below the %d verdict floor (all calls: %v)",
			forced, cfg.Refuter.MinOutputTokens, llm.maxSeen)
	}
}
