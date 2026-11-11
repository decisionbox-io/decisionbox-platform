package discovery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/contextbudget"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

// ledgerFixture builds n active findings shaped like a real mature project's:
// substantive descriptions, mixed severities, all still active.
func ledgerFixture(n int) []commonmodels.LedgerFinding {
	now := time.Now()
	sev := []string{"critical", "high", "medium", "low"}
	out := make([]commonmodels.LedgerFinding, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, commonmodels.LedgerFinding{
			ID:        fmt.Sprintf("f-%03d", i),
			ProjectID: "p1",
			Area:      fmt.Sprintf("area-%d", i%6),
			Name:      fmt.Sprintf("Finding %03d", i),
			Severity:  sev[i%len(sev)],
			Status:    commonmodels.LedgerFindingStatusConfirmed,
			SeenCount: 1 + i%4,
			KeyMetric: fmt.Sprintf("affected=%d", 100*i),
			Description: "A substantive description of what this finding established, with " +
				"enough prose to be worth truncating: " + strings.Repeat("detail about the warehouse. ", 12),
			LastSeen:  now.Add(-time.Duration(i) * time.Hour),
			FirstSeen: now.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}
	return out
}

// windowedOrchestrator is a run whose every layer shares one window/cap.
func windowedOrchestrator(findings []commonmodels.LedgerFinding, ledger *commonmodels.DiscoveryLedger, window, outputCap int) *Orchestrator {
	return &Orchestrator{
		projectID:      "p1",
		findingRepo:    &fakeFindingRepo{findings: findings},
		ledgerRepo:     &fakeLedgerRepo{ledger: ledger},
		llmProvider:    "bedrock",
		llmModel:       "test-model",
		llmInputWindow: window,
		llmOutputCap:   outputCap,
	}
}

func convergence(ratios ...float64) *commonmodels.DiscoveryLedger {
	l := &commonmodels.DiscoveryLedger{ProjectID: "p1"}
	for i, r := range ratios {
		l.Convergence = append(l.Convergence, commonmodels.ConvergencePoint{
			RunID:         fmt.Sprintf("run-%d", i),
			TotalFindings: 50,
			NewFindings:   int(r * 50),
			MarginalRatio: r,
			Date:          time.Now().Add(-time.Duration(len(ratios)-i) * 24 * time.Hour),
		})
	}
	return l
}

// TestPriorContextScalesWithTheModelWindow is acceptance criterion 1: the
// number of prior findings entering a prompt is derived from the effective
// window, with no constant on the path, and a small and a large window produce
// materially different counts.
func TestPriorContextScalesWithTheModelWindow(t *testing.T) {
	const total = 420
	findings := ledgerFixture(total)

	shown := func(window, outputCap int) int {
		o := windowedOrchestrator(findings, convergence(1.00), window, outputCap)
		lrc := o.loadLedgerReadContext(context.Background())
		if lrc == nil {
			return 0
		}
		return len(lrc.findings)
	}

	small := shown(32_000, 8_192)
	medium := shown(131_072, 16_384)
	large := shown(1_000_000, 32_000)

	t.Logf("findings shown: 32K=%d 131K=%d 1M=%d (of %d)", small, medium, large, total)

	if !(small < medium && medium < large) {
		t.Fatalf("counts must grow with the window: 32K=%d 131K=%d 1M=%d", small, medium, large)
	}
	if small == 0 {
		t.Error("a 32K-window model should still be told something about the project")
	}
	// The constant this replaces was 25. A large-window model must be shown
	// substantially more than that, which is the whole point of the issue.
	if large <= 25*4 {
		t.Errorf("a 1M-window model was shown %d findings — materially more than the 25 this replaces means a multiple, not a handful", large)
	}
	if large > total {
		t.Errorf("shown %d of %d findings — cannot show more than exist", large, total)
	}
}

// TestPriorContextDoesNotOverflowASmallWindow is acceptance criterion 2: on a
// window too small for the fixed 25 findings this replaces, the block degrades
// instead of pushing the prompt into a context-overflow 400.
//
// Two separate claims, because only one of them is about the block alone. The
// block must never cost more than the window leaves free — that holds at every
// size. And where the old constant did not fit, the new budget must be
// strictly cheaper than it; the old 25-finding block is rendered here for the
// comparison rather than asserted from memory.
func TestPriorContextDoesNotOverflowASmallWindow(t *testing.T) {
	findings := ledgerFixture(420)
	oldCost := contextbudget.Tokens(renderLedgerFindings(findings[:25], 200))

	type result struct{ window, avail, shown, cost int }
	var results []result

	for _, tc := range []struct{ window, outputCap int }{
		{16_000, 4_096},
		{8_000, 2_048},
		{6_000, 1_024},
		{4_096, 1_024},
		{2_048, 512},
		{1_024, 256},
	} {
		t.Run(fmt.Sprintf("window-%d", tc.window), func(t *testing.T) {
			o := windowedOrchestrator(findings, convergence(0.10), tc.window, tc.outputCap)
			lrc := o.loadLedgerReadContext(context.Background())

			shown, rendered := 0, ""
			if lrc != nil {
				shown = len(lrc.findings)
				rendered = renderLedgerFindings(lrc.findings, lrc.descChars)
			}
			avail := o.baseContextAvailableInput()
			cost := contextbudget.Tokens(rendered)
			results = append(results, result{tc.window, avail, shown, cost})

			t.Logf("window=%d available=%d shown=%d cost=%d (the fixed 25 findings cost %d)",
				tc.window, avail, shown, cost, oldCost)

			if cost > avail {
				t.Errorf("block costs %d tokens with %d available — this is the overflow the budget exists to prevent", cost, avail)
			}
			if oldCost > avail && cost >= oldCost {
				t.Errorf("the window leaves %d free, the old constant needed %d, and the new block costs %d — it must be strictly cheaper where the constant did not fit", avail, oldCost, cost)
			}
			// Deliberately NOT asserted: that a small window is shown fewer
			// than the 25 the constant carried. A 16K model with a saturated
			// ledger is shown more than 25, and that is correct — the
			// requirement is that a small window must not FAIL, and the share
			// is bounded at twice the operator's floor, so "more than before"
			// on a window that can afford it is the budget working. What must
			// hold is the ceiling above and the monotonicity below.
		})
	}

	// Monotonic: a smaller window must never be shown more.
	for i := 1; i < len(results); i++ {
		if results[i].shown > results[i-1].shown {
			t.Errorf("window %d showed %d findings, more than the larger window %d's %d",
				results[i].window, results[i].shown, results[i-1].window, results[i-1].shown)
		}
	}
	// And the smallest window must have degraded all the way, not merely a bit:
	// a 1K window has no room for prior context at all.
	if last := results[len(results)-1]; last.shown != 0 {
		t.Errorf("a %d-token window still carried %d findings", last.window, last.shown)
	}
}

// TestPriorContextBudgetIsSizedForTheTightestLayer pins the per-layer rule: the
// base context is one string prepended to exploration, every analysis area and
// recommendations, so a run whose analysis layer is a small model must size it
// for that model and not for the big exploration one.
func TestPriorContextBudgetIsSizedForTheTightestLayer(t *testing.T) {
	big := layerLLM(llmlayer.DefaultLayer, llmlayer.DefaultProviderID, "bedrock", "opus", 1_000_000, 32_000)
	small := layerLLM(llmlayer.LayerAnalysis, "cheap", "litellm", "qwen3-32b", 32_000, 8_192)
	// A layer that does NOT receive the base context must not affect it.
	tiny := layerLLM(llmlayer.LayerSQLRepair, "tiny", "litellm", "qwen-0.5b", 4_096, 512)

	withLayers := func(layers map[string]*ai.LayerLLM) int {
		o := &Orchestrator{
			projectID:   "p1",
			findingRepo: &fakeFindingRepo{findings: ledgerFixture(420)},
			ledgerRepo:  &fakeLedgerRepo{ledger: convergence(1.00)},
			aiClient:    big.Client,
			layers:      &fakeLayerSet{byLayer: layers, fallback: big},
		}
		lrc := o.loadLedgerReadContext(context.Background())
		if lrc == nil {
			return 0
		}
		return len(lrc.findings)
	}

	allBig := withLayers(map[string]*ai.LayerLLM{llmlayer.DefaultLayer: big})
	smallAnalysis := withLayers(map[string]*ai.LayerLLM{llmlayer.DefaultLayer: big, llmlayer.LayerAnalysis: small})
	tinyUnrelated := withLayers(map[string]*ai.LayerLLM{llmlayer.DefaultLayer: big, llmlayer.LayerSQLRepair: tiny})

	t.Logf("all-big=%d small-analysis=%d tiny-unrelated-layer=%d", allBig, smallAnalysis, tinyUnrelated)

	if smallAnalysis >= allBig {
		t.Errorf("a 32K analysis layer showed %d findings, no fewer than the all-1M run's %d — the shared block must be sized for the tightest consumer", smallAnalysis, allBig)
	}
	if tinyUnrelated != allBig {
		t.Errorf("a 4K sql_repair layer changed the block from %d to %d findings — that layer never receives the base context", allBig, tinyUnrelated)
	}
}

// TestPriorContextGrowsWithSaturation is the MarginalRatio consumer: the
// signal the system already computes, and which no decision read, now decides
// how much of what is known the run is told.
func TestPriorContextGrowsWithSaturation(t *testing.T) {
	findings := ledgerFixture(420)

	shown := func(ledger *commonmodels.DiscoveryLedger) int {
		o := windowedOrchestrator(findings, ledger, 131_072, 16_384)
		lrc := o.loadLedgerReadContext(context.Background())
		if lrc == nil {
			return 0
		}
		return len(lrc.findings)
	}

	fresh := shown(convergence(1.00))
	// The production trend from the issue.
	saturated := shown(convergence(1.00, 0.18, 0.20, 0.10))
	none := shown(&commonmodels.DiscoveryLedger{ProjectID: "p1"})

	t.Logf("fresh(ratio 1.00)=%d saturated(ratio 0.10)=%d no-history=%d", fresh, saturated, none)

	if saturated <= fresh {
		t.Errorf("a saturated project was shown %d findings, no more than a fresh one's %d — not knowing what is known is what produced the repetition", saturated, fresh)
	}
	if none != fresh {
		t.Errorf("a project with no convergence history was shown %d, want the base share's %d — an early run must not get the saturated budget", none, fresh)
	}
}

func TestLatestMarginalRatio(t *testing.T) {
	t.Run("empty history has no ratio", func(t *testing.T) {
		if _, have := latestMarginalRatio(nil); have {
			t.Error("no history must report no ratio")
		}
	})

	t.Run("takes the most recent point", func(t *testing.T) {
		got, have := latestMarginalRatio(convergence(1.0, 0.5, 0.1).Convergence)
		if !have || got != 0.1 {
			t.Errorf("got %v (have=%v), want the last point's 0.1", got, have)
		}
	})

	t.Run("skips a run that recorded no findings at all", func(t *testing.T) {
		// A run that found nothing divides by a zero total and stores a ratio
		// of 0. Reading that as "nothing was new" would hand the largest
		// prior-context budget to a project whose last run failed.
		history := append(convergence(0.9).Convergence, commonmodels.ConvergencePoint{
			RunID: "empty-run", TotalFindings: 0, NewFindings: 0, MarginalRatio: 0,
		})
		got, have := latestMarginalRatio(history)
		if !have || got != 0.9 {
			t.Errorf("got %v (have=%v), want the last MEASURED 0.9 — an empty run is not saturation", got, have)
		}
	})

	t.Run("a history of only empty runs has no ratio", func(t *testing.T) {
		history := []commonmodels.ConvergencePoint{{RunID: "a"}, {RunID: "b"}}
		if _, have := latestMarginalRatio(history); have {
			t.Error("points that measured nothing must not count as a measurement")
		}
	})

	t.Run("a genuine zero is a measurement", func(t *testing.T) {
		history := []commonmodels.ConvergencePoint{{RunID: "a", TotalFindings: 40, NewFindings: 0, MarginalRatio: 0}}
		got, have := latestMarginalRatio(history)
		if !have || got != 0 {
			t.Errorf("got %v (have=%v) — a run that found 40 findings, none of them new, measured a real zero", got, have)
		}
	})
}

// TestBaseContextFindingIDsAreExactlyWhatWasShown pins the ExcludeIDs source.
// An over-broad list would suppress real per-area retrieval for findings the
// run was never actually told about.
func TestBaseContextFindingIDsAreExactlyWhatWasShown(t *testing.T) {
	o := windowedOrchestrator(ledgerFixture(420), convergence(0.20), 64_000, 8_192)
	lrc := o.loadLedgerReadContext(context.Background())
	if lrc == nil || len(lrc.findings) == 0 {
		t.Fatal("expected a populated read context")
	}
	if len(o.baseContextFindingIDs) != len(lrc.findings) {
		t.Fatalf("recorded %d ids for %d shown findings", len(o.baseContextFindingIDs), len(lrc.findings))
	}
	for i := range lrc.findings {
		if o.baseContextFindingIDs[i] != lrc.findings[i].ID {
			t.Errorf("id[%d] = %q, want %q", i, o.baseContextFindingIDs[i], lrc.findings[i].ID)
		}
	}
	rendered := renderLedgerFindings(lrc.findings, lrc.descChars)
	for _, id := range []string{"f-000"} {
		if !strings.Contains(rendered, "Finding "+strings.TrimPrefix(id, "f-")) {
			t.Errorf("the top-ranked finding %q is recorded as shown but is not in the rendered block", id)
		}
	}
}

func TestPriorContextShareEnvOverride(t *testing.T) {
	findings := ledgerFixture(420)
	shown := func() int {
		o := windowedOrchestrator(findings, convergence(1.00), 131_072, 16_384)
		lrc := o.loadLedgerReadContext(context.Background())
		if lrc == nil {
			return 0
		}
		return len(lrc.findings)
	}

	base := shown()

	t.Setenv(priorContextShareEnv, "0.05")
	lowered := shown()
	if lowered >= base {
		t.Errorf("a 0.05 share showed %d findings, no fewer than the default's %d", lowered, base)
	}

	t.Setenv(priorContextShareEnv, "0.40")
	raised := shown()
	if raised <= base {
		t.Errorf("a 0.40 share showed %d findings, no more than the default's %d", raised, base)
	}

	// An operator typo must fall back to the default, not to zero context.
	t.Setenv(priorContextShareEnv, "a fifth")
	if got := shown(); got != base {
		t.Errorf("an unparseable share showed %d findings, want the default's %d", got, base)
	}

	// A share of zero is an explicit opt-out and must be honoured as one.
	t.Setenv(priorContextShareEnv, "0")
	if got := shown(); got != 0 {
		t.Errorf("a zero share showed %d findings, want none", got)
	}
}

func TestContextProviderBudget(t *testing.T) {
	o := windowedOrchestrator(nil, nil, 131_072, 16_384)
	ctx := context.Background()

	t.Run("a short prompt leaves a budget", func(t *testing.T) {
		if got := o.contextProviderBudget(ctx, llmlayer.LayerAnalysis, "a short prompt"); got <= 0 {
			t.Errorf("budget = %d, want a positive budget on a 131K window", got)
		}
	})

	t.Run("shrinks as the prompt grows", func(t *testing.T) {
		small := o.contextProviderBudget(ctx, llmlayer.LayerAnalysis, strings.Repeat("x", 4_000))
		large := o.contextProviderBudget(ctx, llmlayer.LayerAnalysis, strings.Repeat("x", 200_000))
		if large >= small {
			t.Errorf("a 200K-char prompt got %d, no less than a 4K-char prompt's %d", large, small)
		}
	})

	t.Run("zero when the prompt already fills the window", func(t *testing.T) {
		// Providers must go quiet rather than push a prompt that is already at
		// the limit over it.
		if got := o.contextProviderBudget(ctx, llmlayer.LayerAnalysis, strings.Repeat("x", 2_000_000)); got != 0 {
			t.Errorf("budget = %d, want 0 for a prompt past the window", got)
		}
	})

	t.Run("sized from the layer's own model", func(t *testing.T) {
		big := layerLLM(llmlayer.DefaultLayer, llmlayer.DefaultProviderID, "bedrock", "opus", 1_000_000, 32_000)
		small := layerLLM(llmlayer.LayerAnalysis, "cheap", "litellm", "qwen3-32b", 32_000, 8_192)
		po := &Orchestrator{
			aiClient: big.Client,
			layers: &fakeLayerSet{
				byLayer:  map[string]*ai.LayerLLM{llmlayer.DefaultLayer: big, llmlayer.LayerAnalysis: small},
				fallback: big,
			},
		}
		prompt := strings.Repeat("x", 10_000)
		analysis := po.contextProviderBudget(ctx, llmlayer.LayerAnalysis, prompt)
		exploration := po.contextProviderBudget(ctx, llmlayer.LayerExploration, prompt)
		if analysis >= exploration {
			t.Errorf("the 32K analysis layer got %d, no less than the 1M exploration layer's %d", analysis, exploration)
		}
	})
}

func TestContextRetrievalTimeout(t *testing.T) {
	if got := contextRetrievalTimeout(); got != defaultContextRetrievalTimeout {
		t.Errorf("unset = %v, want %v", got, defaultContextRetrievalTimeout)
	}
	t.Setenv(contextRetrievalTimeoutEnv, "45s")
	if got := contextRetrievalTimeout(); got != 45*time.Second {
		t.Errorf("45s = %v", got)
	}
	// A zero or negative bound would make every provider section expire
	// instantly, which the surrounding code degrades to silence.
	for _, bad := range []string{"0", "-5s", "soon"} {
		t.Setenv(contextRetrievalTimeoutEnv, bad)
		if got := contextRetrievalTimeout(); got != defaultContextRetrievalTimeout {
			t.Errorf("%q = %v, want the default %v", bad, got, defaultContextRetrievalTimeout)
		}
	}
}
