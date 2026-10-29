package verifier

import (
	"context"
	"errors"
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
		Content:    r.content,
		TokensIn:   100,
		TokensOut:  r.tokensOut,
		StopReason: r.stopReason,
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
	cfg.ApplyModelOutputCaps(8000, 8000)
	ceiling := cfg.Verifier.MaxOutputTokens

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
	cfg.ApplyModelOutputCaps(8000, 8000)

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
	cfg.ApplyModelOutputCaps(8000, 8000)
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
	cfg.ApplyModelOutputCaps(8000, 8000)

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
