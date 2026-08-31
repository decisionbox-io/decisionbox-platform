package ai

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// newBudgetTestEngine builds an ExplorationEngine whose client resolves to the
// package default output cap (64000) for GetMaxOutputTokens — the mock provider
// is unregistered, so catalogCap is the global default. The R3 fields are set
// by each test.
func newBudgetTestEngine(window, outputCap int, reasoning bool) *ExplorationEngine {
	client, _ := New(testutil.NewMockLLMProvider(), "mock-model")
	return &ExplorationEngine{
		client:             client,
		window:             window,
		outputCap:          outputCap,
		reasoningEffective: reasoning,
	}
}

// TestExplorationOutputTokens_NonReasoningIsToday is the big-model no-op proof:
// a non-reasoning model returns exactly today's ceiling — min(catalog cap,
// EXPLORATION_MAX_OUTPUT_TOKENS default 4096) — regardless of the window and
// input estimate. Opus takes this path (unflagged + checkbox off).
func TestExplorationOutputTokens_NonReasoningIsToday(t *testing.T) {
	e := newBudgetTestEngine(200000, 64000, false)
	if got := e.explorationOutputTokens(0); got != defaultExplorationMaxOutputTokens {
		t.Errorf("non-reasoning ceiling = %d, want %d (byte-identical to today)", got, defaultExplorationMaxOutputTokens)
	}
	// A large input estimate must not change the non-reasoning result — the
	// input is only consulted on the reasoning path.
	if got := e.explorationOutputTokens(150000); got != defaultExplorationMaxOutputTokens {
		t.Errorf("non-reasoning ceiling with large input = %d, want %d", got, defaultExplorationMaxOutputTokens)
	}
}

// TestExplorationOutputTokens_NonReasoningIgnoresTinyWindow proves the window
// never leaks into the non-reasoning path: even a tiny window keeps 4096.
func TestExplorationOutputTokens_NonReasoningIgnoresTinyWindow(t *testing.T) {
	e := newBudgetTestEngine(4096, 4096, false)
	if got := e.explorationOutputTokens(3000); got != defaultExplorationMaxOutputTokens {
		t.Errorf("non-reasoning tiny-window ceiling = %d, want %d", got, defaultExplorationMaxOutputTokens)
	}
}

// TestExplorationOutputTokens_ReasoningLargeWindow: a reasoning-effective model
// on a large window gets the raised ceiling (16384), bounded by the output cap.
func TestExplorationOutputTokens_ReasoningLargeWindow(t *testing.T) {
	e := newBudgetTestEngine(200000, 64000, true)
	if got := e.explorationOutputTokens(2000); got != reasoningExplorationOutputTokens {
		t.Errorf("reasoning large-window ceiling = %d, want %d", got, reasoningExplorationOutputTokens)
	}
}

// TestExplorationOutputTokens_ReasoningBudgetedDown: on a small window the
// raised ceiling is budgeted down to leave room for input + reserves, but never
// below the floor.
func TestExplorationOutputTokens_ReasoningBudgetedDown(t *testing.T) {
	const window, inputEst = 8192, 1000
	e := newBudgetTestEngine(window, 64000, true)
	got := e.explorationOutputTokens(inputEst)

	want := gollm.NewBudget(window, 0, explorationReservedSystemTokens, false).Available() - inputEst
	if got != want {
		t.Errorf("reasoning budgeted ceiling = %d, want %d (window-budgeted)", got, want)
	}
	if got >= reasoningExplorationOutputTokens {
		t.Errorf("ceiling %d was not budgeted down from %d", got, reasoningExplorationOutputTokens)
	}
	if got < defaultExplorationMaxOutputTokens {
		t.Errorf("ceiling %d dropped below the floor %d", got, defaultExplorationMaxOutputTokens)
	}
}

// TestExplorationOutputTokens_ReasoningTinyWindowFloor: when even the floor
// doesn't fit, the floor still wins (the #347 adaptive-overflow retry is the
// net that shrinks the actual request further).
func TestExplorationOutputTokens_ReasoningTinyWindowFloor(t *testing.T) {
	e := newBudgetTestEngine(4096, 64000, true)
	if got := e.explorationOutputTokens(3000); got != defaultExplorationMaxOutputTokens {
		t.Errorf("reasoning tiny-window ceiling = %d, want floor %d", got, defaultExplorationMaxOutputTokens)
	}
}

// TestExplorationOutputTokens_ReasoningNoWindowFallsToToday: a reasoning model
// with an unknown window (0) must not regress — it falls back to today's fixed
// ceiling rather than requesting an unbudgeted 16384.
func TestExplorationOutputTokens_ReasoningNoWindowFallsToToday(t *testing.T) {
	e := newBudgetTestEngine(0, 64000, true)
	if got := e.explorationOutputTokens(1000); got != defaultExplorationMaxOutputTokens {
		t.Errorf("reasoning no-window ceiling = %d, want %d (no regression)", got, defaultExplorationMaxOutputTokens)
	}
}

// TestExplorationOutputTokens_ReasoningOutputCapBounds: the raised ceiling never
// exceeds the model's output cap.
func TestExplorationOutputTokens_ReasoningOutputCapBounds(t *testing.T) {
	e := newBudgetTestEngine(200000, 8000, true)
	if got := e.explorationOutputTokens(1000); got != 8000 {
		t.Errorf("reasoning ceiling = %d, want 8000 (bounded by output cap)", got)
	}
}

// TestExplorationOutputTokens_OperatorRaisedEnvHonored: an operator who raised
// EXPLORATION_MAX_OUTPUT_TOKENS above the reasoning default keeps that headroom
// (don't shrink a deployment already tuned up for reasoning, issue #341); the
// non-reasoning path still treats the env as a cap.
func TestExplorationOutputTokens_OperatorRaisedEnvHonored(t *testing.T) {
	t.Setenv(explorationMaxOutputTokensEnv, "32000")

	reasoning := newBudgetTestEngine(200000, 64000, true)
	if got := reasoning.explorationOutputTokens(1000); got != 32000 {
		t.Errorf("reasoning ceiling with raised env = %d, want 32000", got)
	}

	nonReasoning := newBudgetTestEngine(200000, 64000, false)
	if got := nonReasoning.explorationOutputTokens(1000); got != 32000 {
		t.Errorf("non-reasoning ceiling with raised env = %d, want min(64000,32000)=32000", got)
	}
}
