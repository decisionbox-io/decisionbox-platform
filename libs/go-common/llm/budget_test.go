package llm

import (
	"context"
	"strings"
	"testing"
)

// --- Budget --------------------------------------------------------

func TestNewBudget_ExactTierUsesNarrowMargin(t *testing.T) {
	b := NewBudget(200000, 2048, 200, true)
	wantMargin := 200000 * ExactCounterMarginPct / 100
	if b.SafetyMargin != wantMargin {
		t.Fatalf("SafetyMargin = %d, want %d (5%% of 200000)", b.SafetyMargin, wantMargin)
	}
}

func TestNewBudget_ApproxTierUsesWideMargin(t *testing.T) {
	b := NewBudget(200000, 2048, 200, false)
	wantMargin := 200000 * ApproxCounterMarginPct / 100
	if b.SafetyMargin != wantMargin {
		t.Fatalf("SafetyMargin = %d, want %d (15%% of 200000)", b.SafetyMargin, wantMargin)
	}
}

func TestBudgetAvailable_SubtractsEveryReserve(t *testing.T) {
	b := NewBudget(200000, 2048, 500, true)
	want := 200000 - 2048 - 500 - (200000 * ExactCounterMarginPct / 100)
	if got := b.Available(); got != want {
		t.Fatalf("Available = %d, want %d", got, want)
	}
}

func TestBudgetAvailable_ZeroWhenReservesExceedCap(t *testing.T) {
	// Reserves dwarf the model cap — Available must clamp to 0, not
	// go negative.
	b := NewBudget(1000, 5000, 5000, false)
	if got := b.Available(); got != 0 {
		t.Fatalf("Available = %d, want 0 (reserves exceed cap)", got)
	}
}

func TestBudgetAvailable_ZeroCapYieldsZero(t *testing.T) {
	b := NewBudget(0, 0, 0, true)
	if got := b.Available(); got != 0 {
		t.Fatalf("Available = %d, want 0 (zero cap)", got)
	}
}

func TestNewBudget_ClampsNegativeInputs(t *testing.T) {
	// Misconfigured upstream cap should not panic or wrap to a
	// large positive value via signed-arithmetic. Negative reserves
	// likewise clamp.
	b := NewBudget(-100, -50, -10, false)
	if b.ModelMaxInput != 0 || b.ReservedOutput != 0 || b.ReservedSystem != 0 {
		t.Fatalf("expected all negative inputs clamped to 0, got %+v", b)
	}
	if got := b.Available(); got != 0 {
		t.Fatalf("Available = %d, want 0 (everything clamped)", got)
	}
}

func TestBudgetMargin_ExactVsApproxDifferOnLargeCap(t *testing.T) {
	// Sanity: an exact counter yields a smaller margin than an
	// approximate counter for the same cap.
	exact := NewBudget(1_000_000, 0, 0, true)
	approx := NewBudget(1_000_000, 0, 0, false)
	if exact.SafetyMargin >= approx.SafetyMargin {
		t.Fatalf("expected exact margin < approx margin; exact=%d approx=%d",
			exact.SafetyMargin, approx.SafetyMargin)
	}
}

// --- ApproximateCounter -------------------------------------------

func TestApproximateCounter_Empty(t *testing.T) {
	got, err := ApproximateCounter{}.Count(context.Background(), "")
	if err != nil {
		t.Fatalf("Count(\"\") errored: %v", err)
	}
	if got != 0 {
		t.Fatalf("Count(\"\") = %d, want 0", got)
	}
}

func TestApproximateCounter_WhitespaceOnlyIsZero(t *testing.T) {
	// Whitespace-only strings have no semantic content — billing
	// them as tokens would falsely report budget pressure for an
	// empty-feeling turn.
	for _, s := range []string{" ", "  ", "\t", "\n", " \t\n\r "} {
		got, err := ApproximateCounter{}.Count(context.Background(), s)
		if err != nil {
			t.Fatalf("Count(%q) errored: %v", s, err)
		}
		if got != 0 {
			t.Fatalf("Count(%q) = %d, want 0 (whitespace-only)", s, got)
		}
	}
}

func TestApproximateCounter_SingleRune(t *testing.T) {
	// Ceiling division — a single character must cost 1 token, not 0.
	got, err := ApproximateCounter{}.Count(context.Background(), "x")
	if err != nil {
		t.Fatalf("Count(\"x\") errored: %v", err)
	}
	if got != 1 {
		t.Fatalf("Count(\"x\") = %d, want 1", got)
	}
}

func TestApproximateCounter_ExactBoundary(t *testing.T) {
	// 4 chars → exactly 1 token by the rune/4 rule.
	got, err := ApproximateCounter{}.Count(context.Background(), "abcd")
	if err != nil {
		t.Fatalf("Count errored: %v", err)
	}
	if got != 1 {
		t.Fatalf("Count(\"abcd\") = %d, want 1", got)
	}
}

func TestApproximateCounter_RoundsUp(t *testing.T) {
	// 5 chars → 2 tokens (ceiling).
	got, _ := ApproximateCounter{}.Count(context.Background(), "abcde")
	if got != 2 {
		t.Fatalf("Count(\"abcde\") = %d, want 2", got)
	}
}

func TestApproximateCounter_LongInputProportional(t *testing.T) {
	s := strings.Repeat("a", 4000)
	got, _ := ApproximateCounter{}.Count(context.Background(), s)
	if got != 1000 {
		t.Fatalf("Count(4000 'a') = %d, want 1000", got)
	}
}

func TestApproximateCounter_MultiByteCountedAsRunes(t *testing.T) {
	// "中文字" is 3 runes, not 9 bytes. The counter operates on runes
	// so multi-byte content tokenizes as 1 token (3 runes / 4 → 1).
	got, _ := ApproximateCounter{}.Count(context.Background(), "中文字")
	if got != 1 {
		t.Fatalf("Count(CJK 3 runes) = %d, want 1 (ceiling of 3/4)", got)
	}
}

func TestApproximateCounter_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ApproximateCounter{}.Count(ctx, "anything")
	if err == nil {
		t.Fatal("Count returned nil error after ctx cancel; expected ctx.Err()")
	}
}

// --- IsExact -------------------------------------------------------

func TestIsExact_NilIsNotExact(t *testing.T) {
	if IsExact(nil) {
		t.Fatal("IsExact(nil) = true, want false")
	}
}

func TestIsExact_ApproximateIsNotExact(t *testing.T) {
	if IsExact(ApproximateCounter{}) {
		t.Fatal("IsExact(ApproximateCounter{}) = true, want false")
	}
}

// fakeExactCounter is a stand-in TokenCounter used to verify
// IsExact treats any non-ApproximateCounter implementation as exact.
type fakeExactCounter struct{}

func (fakeExactCounter) Count(_ context.Context, text string) (int, error) {
	return len(text), nil
}

func TestIsExact_NonApproximateIsExact(t *testing.T) {
	if !IsExact(fakeExactCounter{}) {
		t.Fatal("IsExact(fakeExactCounter) = false, want true")
	}
}
