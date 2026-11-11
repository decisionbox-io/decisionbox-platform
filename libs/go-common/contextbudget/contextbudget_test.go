package contextbudget

import (
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

func TestTokens(t *testing.T) {
	t.Run("blank costs nothing", func(t *testing.T) {
		for _, s := range []string{"", "   ", "\n\t "} {
			if got := Tokens(s); got != 0 {
				t.Errorf("Tokens(%q) = %d, want 0 — a prompt that trims to nothing is not billed", s, got)
			}
		}
	})

	t.Run("agrees with the counter the callers size prompts with", func(t *testing.T) {
		// The whole point of routing through ApproximateCounter is that this
		// package and the agent's own approxTokens cannot drift onto two
		// different heuristics. Pin that rather than the formula.
		s := strings.Repeat("ledger finding about orders and customers. ", 40)
		want, err := gollm.ApproximateCounter{}.Count(t.Context(), s)
		if err != nil {
			t.Fatalf("counter: %v", err)
		}
		if got := Tokens(s); got != want {
			t.Errorf("Tokens = %d, ApproximateCounter = %d — the two must not drift", got, want)
		}
	})

	t.Run("a single rune still costs a token", func(t *testing.T) {
		if got := Tokens("ş"); got != 1 {
			t.Errorf("Tokens(single rune) = %d, want 1 (ceiling division)", got)
		}
	})
}

func TestAvailableInput(t *testing.T) {
	cases := []struct {
		name                                   string
		window, reservedOutput, reservedSystem int
		want                                   int
	}{
		// 131072 − 16384 − 512 − 15% margin (19660) = 94516
		{"large window leaves most of itself", 131072, 16384, 512, 94516},
		{"reserves exceeding the window yield zero, not negative", 4096, 8192, 512, 0},
		{"zero window yields zero", 0, 1024, 512, 0},
		{"negative window is clamped, not panicked", -1, 1024, 512, 0},
		{"negative reserves are clamped", 10000, -5, -5, 8500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AvailableInput(c.window, c.reservedOutput, c.reservedSystem); got != c.want {
				t.Errorf("AvailableInput(%d,%d,%d) = %d, want %d", c.window, c.reservedOutput, c.reservedSystem, got, c.want)
			}
		})
	}

	t.Run("never negative for any ordering of reserves", func(t *testing.T) {
		for _, w := range []int{0, 1, 512, 4096, 8192} {
			for _, out := range []int{0, 4096, 64000} {
				if got := AvailableInput(w, out, 512); got < 0 {
					t.Fatalf("AvailableInput(%d,%d,512) = %d — must floor at zero", w, out, got)
				}
			}
		}
	})

	t.Run("uses the approximate safety tier", func(t *testing.T) {
		// A caller that under-counts must get the wider margin; pinning this
		// stops a future edit from silently choosing the exact tier.
		window := 100000
		approx := AvailableInput(window, 0, 0)
		exact := gollm.NewBudget(window, 0, 0, true).Available()
		if approx >= exact {
			t.Errorf("approximate tier (%d) must reserve MORE than the exact tier (%d)", approx, exact)
		}
	})
}

func TestShare(t *testing.T) {
	cases := []struct {
		name      string
		available int
		share     float64
		want      int
	}{
		{"ordinary", 10000, 0.20, 2000},
		{"zero available", 0, 0.20, 0},
		{"negative available", -100, 0.20, 0},
		{"zero share spends nothing", 10000, 0, 0},
		{"negative share spends nothing, not everything", 10000, -0.5, 0},
		{"share above one is not clamped here — the caller bounds it", 10000, 1.5, 15000},
		{"rounds down", 10, 0.25, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Share(c.available, c.share); got != c.want {
				t.Errorf("Share(%d, %v) = %d, want %d", c.available, c.share, got, c.want)
			}
		})
	}
}

func TestSaturatedShare(t *testing.T) {
	const base = 0.20

	t.Run("a fresh run gets the base share", func(t *testing.T) {
		if got := SaturatedShare(base, 1.0, true); got != base {
			t.Errorf("ratio 1.0 (everything was new) = %v, want the base %v", got, base)
		}
	})

	t.Run("a fully saturated run gets twice the base", func(t *testing.T) {
		if got := SaturatedShare(base, 0, true); got != 2*base {
			t.Errorf("ratio 0 (nothing was new) = %v, want %v", got, 2*base)
		}
	})

	t.Run("the production trend scales monotonically", func(t *testing.T) {
		// The ratios from the issue: 1.00 -> 0.18 -> 0.20 -> 0.10. More
		// saturation must never buy less prior context.
		prev := 0.0
		for _, ratio := range []float64{1.00, 0.20, 0.18, 0.10} {
			got := SaturatedShare(base, ratio, true)
			if got < prev {
				t.Fatalf("ratio %v gave %v, less than the previous (less saturated) %v", ratio, got, prev)
			}
			prev = got
		}
	})

	t.Run("no recorded convergence is not a ratio of zero", func(t *testing.T) {
		if got := SaturatedShare(base, 0, false); got != base {
			t.Errorf("have=false = %v, want the base %v — an early run must not get the maximum", got, base)
		}
	})

	t.Run("bounded to [base, 2*base] for out-of-range input", func(t *testing.T) {
		for _, ratio := range []float64{-5, -0.0001, 1.0001, 42} {
			got := SaturatedShare(base, ratio, true)
			if got < base || got > 2*base {
				t.Errorf("ratio %v = %v, outside [%v, %v]", ratio, got, base, 2*base)
			}
		}
	})

	t.Run("a zero or negative base spends nothing", func(t *testing.T) {
		for _, b := range []float64{0, -1} {
			if got := SaturatedShare(b, 0, true); got != 0 {
				t.Errorf("base %v = %v, want 0", b, got)
			}
		}
	})
}

func TestFindingLine(t *testing.T) {
	full := commonmodels.LedgerFinding{
		Area: "churn", Name: "High EU churn", Severity: "high",
		Status: "confirmed", SeenCount: 3, KeyMetric: "affected=300",
		Description: "Churn in the EU segment is elevated against the trailing quarter.",
	}

	t.Run("renders every clause", func(t *testing.T) {
		got := FindingLine(full, MinFindingDescChars)
		for _, want := range []string{"[churn]", "**High EU churn**", "high", "confirmed", "seen 3×", "affected=300", "Churn in the EU segment"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in: %s", want, got)
			}
		}
	})

	t.Run("omits what there is nothing to say about", func(t *testing.T) {
		got := FindingLine(commonmodels.LedgerFinding{Name: "Bare", Severity: "low", SeenCount: 1}, MinFindingDescChars)
		if !strings.Contains(got, "[general]") {
			t.Errorf("an unset area must read general, got: %s", got)
		}
		if strings.Contains(got, "seen") {
			t.Errorf("a single sighting must not render a counter, got: %s", got)
		}
		if strings.Contains(got, "—") || strings.HasSuffix(got, ". ") {
			t.Errorf("no metric and no description must leave no dangling punctuation, got: %s", got)
		}
	})

	t.Run("carries no trailing newline", func(t *testing.T) {
		if strings.HasSuffix(FindingLine(full, MinFindingDescChars), "\n") {
			t.Error("FindingLine must not append a newline — callers join")
		}
	})

	t.Run("truncates on runes, not bytes", func(t *testing.T) {
		// The byte-slicing renderers this replaces would cut mid-rune on
		// Turkish text and put U+FFFD in the prompt. 10 two-byte runes with a
		// width of 5 is the smallest case that proves it.
		f := full
		f.Description = strings.Repeat("ş", 10)
		got := FindingLine(f, 5)
		if strings.ContainsRune(got, '�') {
			t.Errorf("truncation split a rune: %q", got)
		}
		if !strings.Contains(got, strings.Repeat("ş", 5)+"…") {
			t.Errorf("want 5 runes then the cut marker, got: %s", got)
		}
		if strings.Contains(got, strings.Repeat("ş", 6)) {
			t.Errorf("truncated past the width: %s", got)
		}
	})

	t.Run("a width of zero drops the description entirely", func(t *testing.T) {
		got := FindingLine(full, 0)
		if strings.Contains(got, "Churn") {
			t.Errorf("width 0 must render no description, got: %s", got)
		}
		if !strings.Contains(got, "affected=300") {
			t.Errorf("width 0 must still render the metric, got: %s", got)
		}
	})

	t.Run("a description shorter than the width is not marked as cut", func(t *testing.T) {
		if got := FindingLine(full, MaxFindingDescChars); strings.Contains(got, "…") {
			t.Errorf("an untruncated description must carry no cut marker: %s", got)
		}
	})
}

// fixture builds n findings with descriptions long enough to be truncated at
// any width this package will choose.
func fixture(n int) []commonmodels.LedgerFinding {
	out := make([]commonmodels.LedgerFinding, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, commonmodels.LedgerFinding{
			ID:          "f" + strings.Repeat("x", i%3),
			Area:        "area",
			Name:        "Finding number " + strings.Repeat("n", i%5),
			Severity:    "high",
			Status:      "confirmed",
			SeenCount:   2,
			KeyMetric:   "affected=1000",
			Description: strings.Repeat("a sentence about the warehouse that keeps going. ", 60),
		})
	}
	return out
}

func TestFitFindings(t *testing.T) {
	t.Run("a large budget keeps everything at the full width", func(t *testing.T) {
		in := fixture(420)
		kept, desc := FitFindings(in, 1_000_000, MinFindingDescChars, MaxFindingDescChars)
		if len(kept) != len(in) {
			t.Errorf("kept %d of %d on a 1M budget", len(kept), len(in))
		}
		if desc != MaxFindingDescChars {
			t.Errorf("desc width = %d, want the ceiling %d", desc, MaxFindingDescChars)
		}
	})

	t.Run("a small budget keeps a prefix in the caller's order", func(t *testing.T) {
		in := fixture(420)
		for i := range in {
			in[i].Name = "rank-" + string(rune('A'+i%26))
		}
		kept, _ := FitFindings(in, 3000, MinFindingDescChars, MaxFindingDescChars)
		if len(kept) == 0 || len(kept) >= len(in) {
			t.Fatalf("kept %d of %d — want a strict, non-empty prefix", len(kept), len(in))
		}
		for i := range kept {
			if kept[i].Name != in[i].Name {
				t.Fatalf("kept[%d] = %q, want %q — order must be the caller's", i, kept[i].Name, in[i].Name)
			}
		}
	})

	t.Run("more budget never keeps fewer findings", func(t *testing.T) {
		in := fixture(420)
		prev := -1
		for _, budget := range []int{500, 1000, 2000, 5000, 10000, 50000, 200000} {
			kept, _ := FitFindings(in, budget, MinFindingDescChars, MaxFindingDescChars)
			if len(kept) < prev {
				t.Fatalf("budget %d kept %d, fewer than the smaller budget's %d", budget, len(kept), prev)
			}
			prev = len(kept)
		}
	})

	t.Run("the rendered block never exceeds the budget", func(t *testing.T) {
		in := fixture(420)
		for _, budget := range []int{1, 50, 500, 3000, 20000} {
			kept, desc := FitFindings(in, budget, MinFindingDescChars, MaxFindingDescChars)
			spent := 0
			for _, f := range kept {
				spent += Tokens(FindingLine(f, desc))
			}
			if spent > budget {
				t.Errorf("budget %d: rendered %d tokens", budget, spent)
			}
		}
	})

	t.Run("a non-positive budget keeps nothing and still returns a usable width", func(t *testing.T) {
		for _, budget := range []int{0, -1, -10000} {
			kept, desc := FitFindings(fixture(10), budget, MinFindingDescChars, MaxFindingDescChars)
			if len(kept) != 0 {
				t.Errorf("budget %d kept %d findings", budget, len(kept))
			}
			if desc <= 0 {
				t.Errorf("budget %d returned width %d — callers must not divide by or render with zero", budget, desc)
			}
		}
	})

	t.Run("one finding bigger than the whole budget keeps nothing", func(t *testing.T) {
		kept, _ := FitFindings(fixture(1), 5, MinFindingDescChars, MaxFindingDescChars)
		if len(kept) != 0 {
			t.Errorf("kept %d — a line that does not fit must not be rendered half", len(kept))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		kept, desc := FitFindings(nil, 10000, MinFindingDescChars, MaxFindingDescChars)
		if len(kept) != 0 {
			t.Errorf("kept %d from nil", len(kept))
		}
		if desc != MinFindingDescChars {
			t.Errorf("desc = %d, want the floor %d (no divide by zero)", desc, MinFindingDescChars)
		}
	})

	t.Run("width is bounded by the caller's floor and ceiling", func(t *testing.T) {
		_, wide := FitFindings(fixture(1), 1_000_000, MinFindingDescChars, MaxFindingDescChars)
		if wide != MaxFindingDescChars {
			t.Errorf("one finding and a huge budget = %d, want the ceiling %d", wide, MaxFindingDescChars)
		}
		_, narrow := FitFindings(fixture(5000), 10000, MinFindingDescChars, MaxFindingDescChars)
		if narrow != MinFindingDescChars {
			t.Errorf("many findings and a small budget = %d, want the floor %d", narrow, MinFindingDescChars)
		}
	})

	t.Run("inverted or negative bounds are repaired, not panicked", func(t *testing.T) {
		if _, desc := FitFindings(fixture(3), 10000, 900, 100); desc != 900 {
			t.Errorf("maxDesc below minDesc must collapse to minDesc, got %d", desc)
		}
		if _, desc := FitFindings(fixture(3), 10000, -50, -10); desc != 0 {
			t.Errorf("negative bounds must clamp to zero, got %d", desc)
		}
	})

	t.Run("does not mutate or reorder the caller's slice", func(t *testing.T) {
		in := fixture(50)
		before := make([]string, len(in))
		for i := range in {
			before[i] = in[i].Name
		}
		FitFindings(in, 2000, MinFindingDescChars, MaxFindingDescChars)
		for i := range in {
			if in[i].Name != before[i] {
				t.Fatalf("input reordered at %d", i)
			}
		}
	})
}

// TestFitScalesWithTheModelWindow is the property the issue is actually about:
// the same ledger must enter a large-window prompt far more fully than a
// small-window one, and must not be forced into a window that cannot hold it.
func TestFitScalesWithTheModelWindow(t *testing.T) {
	const (
		reservedOutput = 16384
		reservedSystem = 512
		share          = 0.20
	)
	ledger := fixture(420)

	count := func(window int) int {
		budget := Share(AvailableInput(window, reservedOutput, reservedSystem), share)
		kept, _ := FitFindings(ledger, budget, MinFindingDescChars, MaxFindingDescChars)
		return len(kept)
	}

	small := count(32_000)   // a modest self-hosted model
	medium := count(131_072) // the package default
	large := count(1_000_000)

	if small >= medium || medium >= large {
		t.Fatalf("counts must grow with the window: small=%d medium=%d large=%d", small, medium, large)
	}
	if small == 0 {
		t.Errorf("a 32K window should still carry some prior context, got 0")
	}
	if large < 100 {
		t.Errorf("a 1M window carried only %d findings — the whole point is that it carries far more than the 25 this replaces", large)
	}

	// A window too small to hold any of them must carry none rather than
	// overflow: today's fixed 25 findings would be ~2.5K tokens here.
	if got := count(4_096); got != 0 {
		t.Errorf("a 4K window carried %d findings — it has no room for any", got)
	}
}

func TestFitLines(t *testing.T) {
	lines := []string{
		strings.Repeat("a", 40),  // 10 tokens
		strings.Repeat("b", 40),  // 10
		strings.Repeat("c", 400), // 100
		strings.Repeat("d", 4),   // 1
	}

	t.Run("takes a prefix and stops at the first that does not fit", func(t *testing.T) {
		// 25 tokens fits the two 10s; the 100 does not. The cheap 1-token line
		// after it must NOT be pulled forward — the order is a priority order.
		if got := FitLines(lines, 25); got != 2 {
			t.Errorf("FitLines = %d, want 2", got)
		}
	})

	t.Run("everything fits a large budget", func(t *testing.T) {
		if got := FitLines(lines, 100_000); got != len(lines) {
			t.Errorf("FitLines = %d, want %d", got, len(lines))
		}
	})

	t.Run("non-positive budget takes nothing", func(t *testing.T) {
		for _, b := range []int{0, -1} {
			if got := FitLines(lines, b); got != 0 {
				t.Errorf("FitLines(%d) = %d, want 0", b, got)
			}
		}
	})

	t.Run("a first line past the budget takes nothing", func(t *testing.T) {
		if got := FitLines([]string{strings.Repeat("x", 4000)}, 5); got != 0 {
			t.Errorf("FitLines = %d, want 0 — a line that does not fit must not be half-rendered", got)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if got := FitLines(nil, 1000); got != 0 {
			t.Errorf("FitLines(nil) = %d, want 0", got)
		}
	})

	t.Run("never exceeds the budget", func(t *testing.T) {
		for _, b := range []int{1, 5, 11, 21, 121, 122, 1000} {
			n := FitLines(lines, b)
			spent := 0
			for _, l := range lines[:n] {
				spent += Tokens(l)
			}
			if spent > b {
				t.Errorf("budget %d: took %d lines costing %d", b, n, spent)
			}
		}
	})
}
