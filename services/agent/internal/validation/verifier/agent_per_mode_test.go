package verifier

import (
	"context"
	"strings"
	"sync"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// namedLLM records every call it receives, so which client answered which
// mode is assertable rather than inferred.
type namedLLM struct {
	name  string
	reply string
	mu    sync.Mutex
	calls int
}

func (l *namedLLM) Chat(_ context.Context, _, _ string, _ int) (*ai.ChatResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	return &ai.ChatResult{Content: l.reply}, nil
}

// submitVerdict is a minimal, parseable submit_verdict action.
const submitVerdict = `{"action":"submit_verdict","verdict":{"overall":"unverifiable","summary":"s","claims":[]}}`

func TestVerifierAndRefuterUseTheirOwnClients(t *testing.T) {
	ver := &namedLLM{name: "verifier", reply: submitVerdict}
	ref := &namedLLM{name: "refuter", reply: submitVerdict}

	a, err := NewAgentPerMode(map[valmodels.AgentMode]LLMClient{
		valmodels.ModeVerifier: ver,
		valmodels.ModeRefuter:  ref,
	}, ver, DefaultConfig())
	if err != nil {
		t.Fatalf("NewAgentPerMode: %v", err)
	}

	if _, err := a.Verify(context.Background(), Bundle{}, nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ver.calls == 0 {
		t.Error("the verifier's own client was not called")
	}
	if ref.calls != 0 {
		t.Errorf("the refuter's client was called %d times during Verify", ref.calls)
	}

	verBefore := ver.calls
	if _, err := a.Refute(context.Background(), Bundle{}, nil); err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if ref.calls == 0 {
		t.Error("the refuter's own client was not called")
	}
	if ver.calls != verBefore {
		t.Errorf("the verifier's client was called during Refute (%d → %d)", verBefore, ver.calls)
	}
}

func TestForcedFinalRoundAlsoUsesTheModesOwnClient(t *testing.T) {
	// The second Chat site — the forced-final round — is reached when the
	// loop runs out of rounds without a parseable verdict. It must select the
	// client the same way as the normal rounds.
	ver := &namedLLM{name: "verifier", reply: "not json"}
	ref := &namedLLM{name: "refuter", reply: "not json"}
	cfg := DefaultConfig()
	cfg.Verifier.MaxRounds = 1
	cfg.Refuter.MaxRounds = 1

	a, err := NewAgentPerMode(map[valmodels.AgentMode]LLMClient{
		valmodels.ModeVerifier: ver,
		valmodels.ModeRefuter:  ref,
	}, ver, cfg)
	if err != nil {
		t.Fatalf("NewAgentPerMode: %v", err)
	}

	if _, err := a.Refute(context.Background(), Bundle{}, nil); err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if ver.calls != 0 {
		t.Errorf("the verifier's client answered %d of the refuter's rounds, including the forced final one", ver.calls)
	}
	if ref.calls < 2 {
		t.Errorf("the refuter's client was called %d times, want the normal round plus the forced final", ref.calls)
	}
}

func TestAMissingModeFallsBackToTheSuppliedClient(t *testing.T) {
	// A caller overriding only one half must not have to supply both.
	only := &namedLLM{name: "fallback", reply: submitVerdict}
	a, err := NewAgentPerMode(map[valmodels.AgentMode]LLMClient{}, only, DefaultConfig())
	if err != nil {
		t.Fatalf("NewAgentPerMode: %v", err)
	}
	if _, err := a.Verify(context.Background(), Bundle{}, nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if _, err := a.Refute(context.Background(), Bundle{}, nil); err != nil {
		t.Fatalf("Refute: %v", err)
	}
	if only.calls == 0 {
		t.Error("the fallback client was never used")
	}
}

func TestNewAgentStillRunsBothModesOnOneClient(t *testing.T) {
	// The original constructor is unchanged, which is what keeps every
	// existing caller and test valid.
	one := &namedLLM{name: "one", reply: submitVerdict}
	a, err := NewAgent(one, DefaultConfig())
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if a.llmFor(valmodels.ModeVerifier) != LLMClient(one) || a.llmFor(valmodels.ModeRefuter) != LLMClient(one) {
		t.Error("NewAgent must serve both modes from the one client")
	}
}

func TestCfgForIsUntouchedByTheClientSplit(t *testing.T) {
	// The split touches client selection only: the per-mode spend envelope,
	// the prompts and the loop are the same.
	cfg := DefaultConfig()
	a, err := NewAgentPerMode(nil, &namedLLM{reply: submitVerdict}, cfg)
	if err != nil {
		t.Fatalf("NewAgentPerMode: %v", err)
	}
	if got := a.cfgFor(valmodels.ModeVerifier); got != cfg.Verifier {
		t.Errorf("verifier cfg = %+v, want %+v", got, cfg.Verifier)
	}
	if got := a.cfgFor(valmodels.ModeRefuter); got != cfg.Refuter {
		t.Errorf("refuter cfg = %+v, want %+v", got, cfg.Refuter)
	}
	if len(a.system) == 0 || !strings.Contains(strings.ToLower(a.system[valmodels.ModeVerifier]), "verif") {
		t.Error("the mode-keyed system prompts were not loaded")
	}
}
