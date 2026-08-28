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

	// Degenerate window: never returns <= 0.
	if got := analysisPickerBudgetTokens(def, 0, 64000); got != def {
		t.Fatalf("zero window: expected fallback to default %d, got %d", def, got)
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
