package discovery

import (
	"context"
	"testing"
)

// TestBudgetedMaxOutputTokens_AcceptanceCase pins the exact scenario from
// issue #347: a 202,752-context model with ~138,753 input tokens and a 64,000
// default output cap must have its output reduced so input + output fits.
func TestBudgetedMaxOutputTokens_AcceptanceCase(t *testing.T) {
	const window, input, effOut = 202752, 138753, 64000
	out := budgetedMaxOutputTokens(window, input, effOut, defaultAnalysisMinOutputTokens)

	// margin = 15% of window (approximate-counter tier in llm.NewBudget).
	margin := window * 15 / 100
	ceiling := window - input - analysisReservedSystemTokens - margin
	if out > ceiling {
		t.Fatalf("output %d exceeds the window ceiling %d (window-input-system-margin)", out, ceiling)
	}
	if input+out > window {
		t.Fatalf("input+output=%d overflows the window %d", input+out, window)
	}
	if out <= 0 {
		t.Fatalf("output must be positive, got %d", out)
	}
}

func TestBudgetedMaxOutputTokens_SmallWindowClampsToFloor(t *testing.T) {
	// Input alone exceeds the (under-estimated) window → clamp to the floor,
	// never negative. The adaptive retry is the net if the floor still 400s.
	floor := defaultAnalysisMinOutputTokens
	out := budgetedMaxOutputTokens(131072, 138753, 64000, floor)
	if out != floor {
		t.Fatalf("expected the floor %d when input>window, got %d", floor, out)
	}
}

func TestBudgetedMaxOutputTokens_LargeHeadroomReturnsCap(t *testing.T) {
	// Tiny input, huge window → return exactly the effective output cap, never
	// more.
	out := budgetedMaxOutputTokens(1000000, 500, 64000, 8192)
	if out != 64000 {
		t.Fatalf("expected the output cap 64000 with large headroom, got %d", out)
	}
}

func TestBudgetedMaxOutputTokens_OutputOverrideLowersCap(t *testing.T) {
	// The caller passes a lowered effOut (operator max_output_tokens override
	// applied upstream via ClampMaxTokens). Output must not exceed it.
	out := budgetedMaxOutputTokens(1000000, 500, 32768, 8192)
	if out != 32768 {
		t.Fatalf("expected the lowered cap 32768, got %d", out)
	}
}

func TestAnalysisPickerBudgetTokens(t *testing.T) {
	def := AnalysisQueryResultsBudgetTokens // 200_000

	// 128K window: budget must drop below the 200K default (protective).
	if got := analysisPickerBudgetTokens(def, 128000, 64000); got >= def || got <= 0 {
		t.Fatalf("128K window: expected 0 < budget < %d, got %d", def, got)
	}

	// 1M window: plenty of room, keep the default soft cap.
	if got := analysisPickerBudgetTokens(def, 1000000, 64000); got != def {
		t.Fatalf("1M window: expected the default %d, got %d", def, got)
	}

	// Small window where the output cap leaves no room for input (Ollama: live
	// info reports only the context length, output stays the 64K default) →
	// falls to the small floor, NOT the 200K default (else input alone overflows
	// the window and re-triggers the very 400 this guards against).
	if got := analysisPickerBudgetTokens(def, 8192, 64000); got != minPickerBudgetTokens {
		t.Fatalf("tiny window: expected the picker floor %d, got %d", minPickerBudgetTokens, got)
	}

	// Degenerate window: falls to the floor, never the 200K default.
	if got := analysisPickerBudgetTokens(def, 0, 64000); got != minPickerBudgetTokens {
		t.Fatalf("zero window: expected the picker floor %d, got %d", minPickerBudgetTokens, got)
	}
}

func TestBudgetedMaxOutputTokens_OutputCapBelowFloor(t *testing.T) {
	// A model whose documented output cap (4096, e.g. Mistral Large) is below
	// the default floor must never be asked for more than it allows.
	out := budgetedMaxOutputTokens(128000, 1000, 4096, defaultAnalysisMinOutputTokens)
	if out != 4096 {
		t.Fatalf("output cap below floor: got %d, want 4096 (cap wins over floor)", out)
	}
}

func TestBudgetedMaxOutputTokens_OutputNeverExceedsWindow(t *testing.T) {
	// Output cap larger than the (auto-detected, small) window → bounded to the
	// window, never requesting more than the context can hold.
	out := budgetedMaxOutputTokens(8192, 100, 64000, defaultAnalysisMinOutputTokens)
	if out > 8192 {
		t.Fatalf("output %d must not exceed the window 8192", out)
	}
}

func TestApproxTokens(t *testing.T) {
	// rune/4: 40 chars -> ~10 tokens.
	if got := approxTokens(context.Background(), "1234567890123456789012345678901234567890"); got != 10 {
		t.Fatalf("expected 10 tokens for 40 runes, got %d", got)
	}
	if got := approxTokens(context.Background(), ""); got != 0 {
		t.Fatalf("empty prompt should be 0 tokens, got %d", got)
	}
}

func TestAnalysisMinOutputTokens_EnvOverride(t *testing.T) {
	if got := analysisMinOutputTokens(); got != defaultAnalysisMinOutputTokens {
		t.Fatalf("default floor = %d, want %d", got, defaultAnalysisMinOutputTokens)
	}
	t.Setenv(analysisMinOutputTokensEnv, "4096")
	if got := analysisMinOutputTokens(); got != 4096 {
		t.Fatalf("env override floor = %d, want 4096", got)
	}
	t.Setenv(analysisMinOutputTokensEnv, "0")
	if got := analysisMinOutputTokens(); got != defaultAnalysisMinOutputTokens {
		t.Fatalf("invalid env should fall back to %d, got %d", defaultAnalysisMinOutputTokens, got)
	}
}
