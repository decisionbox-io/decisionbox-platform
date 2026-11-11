package discovery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	"github.com/decisionbox-io/decisionbox/libs/go-common/contextbudget"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
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

// convergence builds a history whose points carry BOTH measurements, with the
// same value, so a test can name one number and mean it. novelty() builds
// points where the two deliberately disagree.
func convergence(ratios ...float64) *commonmodels.DiscoveryLedger {
	l := &commonmodels.DiscoveryLedger{ProjectID: "p1"}
	for i, r := range ratios {
		l.Convergence = append(l.Convergence, commonmodels.ConvergencePoint{
			RunID:         fmt.Sprintf("run-%d", i),
			TotalFindings: 50,
			NewFindings:   int(r * 50),
			MarginalRatio: r,
			RunFindings:   50,
			Date:          time.Now().Add(-time.Duration(len(ratios)-i) * 24 * time.Hour),
		})
	}
	return l
}

// legacyConvergence builds points as they were written before RunFindings
// existed: a growth ratio and no per-run measurement at all.
func legacyConvergence(ratios ...float64) *commonmodels.DiscoveryLedger {
	l := convergence(ratios...)
	for i := range l.Convergence {
		l.Convergence[i].RunFindings = 0
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

	if small >= medium || medium >= large {
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

// TestLatestRunNovelty pins the distinction the P1 review finding was about:
// MarginalRatio divides by the whole ledger and is NOT a repetition rate, so
// the budget and the prompt read RunFindings/NewFindings instead.
func TestLatestRunNovelty(t *testing.T) {
	t.Run("empty history has no measurement", func(t *testing.T) {
		if _, have := latestRunNovelty(nil); have {
			t.Error("no history must report no measurement")
		}
	})

	t.Run("takes the most recent measured point", func(t *testing.T) {
		got, have := latestRunNovelty(convergence(1.0, 0.5, 0.1).Convergence)
		if !have || got != 0.1 {
			t.Errorf("got %v (have=%v), want the last point's 0.1", got, have)
		}
	})

	t.Run("a legacy point is not a measurement, and is NOT read as zero", func(t *testing.T) {
		// Reading an unmeasured point as novelty 0 would tell a project that
		// everything it found was already known, and hand it the largest
		// prior-context budget on no evidence.
		if _, have := latestRunNovelty(legacyConvergence(0.9, 0.1).Convergence); have {
			t.Error("points written before RunFindings existed must not be treated as measurements")
		}
	})

	t.Run("skips back past legacy points to the last measured one", func(t *testing.T) {
		h := convergence(0.9).Convergence
		h = append(h, legacyConvergence(0.1).Convergence...)
		got, have := latestRunNovelty(h)
		if !have || got != 0.9 {
			t.Errorf("got %v (have=%v), want the last MEASURED 0.9", got, have)
		}
	})

	t.Run("growth and novelty are different numbers", func(t *testing.T) {
		// The review's example: ten entirely new findings added to a
		// hundred-finding ledger. Growth says 9%; the run repeated NOTHING.
		p := commonmodels.ConvergencePoint{NewFindings: 10, TotalFindings: 110, MarginalRatio: 10.0 / 110.0, RunFindings: 10}
		if n, ok := p.RunNovelty(); !ok || n != 1 {
			t.Errorf("RunNovelty = %v (ok=%v), want 1 — the run reported ten findings and all ten were new", n, ok)
		}
		got, _ := latestRunNovelty([]commonmodels.ConvergencePoint{p})
		if got != 1 {
			t.Errorf("latestRunNovelty = %v, want 1, not the 0.09 growth ratio", got)
		}
	})

	t.Run("a run that repeated everything measures zero", func(t *testing.T) {
		p := commonmodels.ConvergencePoint{NewFindings: 0, TotalFindings: 400, RunFindings: 40}
		n, ok := p.RunNovelty()
		if !ok || n != 0 {
			t.Errorf("RunNovelty = %v (ok=%v), want a measured 0", n, ok)
		}
	})

	t.Run("out-of-range counts are clamped, not propagated", func(t *testing.T) {
		for _, p := range []commonmodels.ConvergencePoint{
			{NewFindings: 50, RunFindings: 10},
			{NewFindings: -5, RunFindings: 10},
		} {
			n, ok := p.RunNovelty()
			if !ok || n < 0 || n > 1 {
				t.Errorf("%+v gave %v (ok=%v), outside [0,1]", p, n, ok)
			}
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

// --- the do-not-restate contract (acceptance criterion 4) ---

// TestNoRestateInstructionRendersWithTheFindings pins the instruction to the
// list it governs: present whenever findings are, absent when they are not.
//
// A rule that outlives the section it refers to is the failure this placement
// avoids — the model satisfies it against whatever it can see instead.
func TestNoRestateInstructionRendersWithTheFindings(t *testing.T) {
	findings := ledgerFixture(3)

	withFindings := renderLedgerFindings(findings, 200)
	if !strings.Contains(withFindings, "ALREADY RECORDED") {
		t.Errorf("the findings block must carry the do-not-restate instruction:\n%s", withFindings)
	}
	for _, want := range []string{
		"Do NOT emit an insight that restates one of them",
		"magnitude has MOVED",
		"report nothing about it",
		"go deeper",
	} {
		if !strings.Contains(withFindings, want) {
			t.Errorf("instruction missing %q:\n%s", want, withFindings)
		}
	}

	if got := renderLedgerFindings(nil, 200); got != "" {
		t.Errorf("no findings must render no instruction, got:\n%s", got)
	}
}

// TestNoRestateInstructionStillPermitsTrendReporting is the other half of the
// contract. The ledger exists to mark a finding `changed` when its magnitude
// moves, and that mark comes from a run reporting the movement — so an
// instruction that forbade every mention of a known finding would suppress the
// trend reporting this feature is for.
func TestNoRestateInstructionStillPermitsTrendReporting(t *testing.T) {
	out := renderLedgerFindings(ledgerFixture(1), 200)
	if !strings.Contains(out, "report the change and say which finding it updates") {
		t.Errorf("the instruction must permit — and direct — reporting a magnitude that moved:\n%s", out)
	}
	if !strings.Contains(out, "`monitoring` and `changed` findings are the ones worth re-checking") {
		t.Errorf("the instruction must say which statuses are worth re-checking:\n%s", out)
	}
}

// TestNoRestateInstructionReachesEveryPromptThatWritesAnInsight: the block lives
// in the base context, which buildBaseContext prepends to exploration, every
// analysis area and recommendations. If it only reached one of them, the phase
// that actually emits insights could miss it.
func TestNoRestateInstructionReachesEveryPromptThatWritesAnInsight(t *testing.T) {
	o := &Orchestrator{}
	lrc := &ledgerReadContext{
		hasLedger: true,
		findings:  ledgerFixture(2),
		descChars: 200,
	}
	previous := o.buildPreviousContext(&models.ProjectContext{TotalDiscoveries: 2}, nil, nil, nil, lrc, nil)
	base := o.buildBaseContext("{{PREVIOUS_CONTEXT}}", "", previous, "English", "ds")

	if !strings.Contains(base, "ALREADY RECORDED") {
		t.Fatalf("the base context must carry the instruction — it is the one string every writer prompt is built from:\n%s", base)
	}
	// Every downstream prompt is base + its own body, so carrying it here is
	// what carries it to all three. Assert the composition rather than trust it.
	for _, body := range []string{"\n\nexploration body", "\n\nanalysis area body", "\n\nrecommendations body"} {
		if !strings.Contains(base+body, "ALREADY RECORDED") {
			t.Errorf("prompt built from base + %q lost the instruction", body)
		}
	}
}

// --- saturation steering (the MarginalRatio / AreaDepth consumer) ---

func TestRenderSaturation(t *testing.T) {
	areas := []AnalysisArea{
		{ID: "churn", Name: "Churn"},
		{ID: "fraud", Name: "Fraud"},
		{ID: "inventory", Name: "Inventory"},
	}

	t.Run("a saturated project is steered off the trodden ground", func(t *testing.T) {
		out := renderSaturation(convergence(1.00, 0.18, 0.20, 0.10).Convergence,
			map[string]int{"churn": 6, "fraud": 1}, areas)
		for _, want := range []string{
			"90% was already in this ledger",
			"Re-walking the same ground produces restatements",
			"100% → 18% → 20% → 10%",
			"the ledger is still growing but more slowly",
			"Churn (6 runs)",
			"Inventory",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "still productive") {
			t.Errorf("a 10%% novelty must not read as productive:\n%s", out)
		}
		// Fraud has produced findings once: neither mined out nor open ground,
		// so naming it in either list would be false.
		if strings.Contains(out, "Fraud") {
			t.Errorf("an area with one productive run belongs in neither list:\n%s", out)
		}
	})

	t.Run("growth is never stated as a repetition rate", func(t *testing.T) {
		// The P1 this function was rewritten for: a run that added ten
		// entirely new findings to a hundred-finding ledger has a growth ratio
		// of 9% and repeated NOTHING. Saying "91% already known" would tell
		// the model something false about its own project.
		h := []commonmodels.ConvergencePoint{
			{RunID: "a", NewFindings: 20, TotalFindings: 100, MarginalRatio: 0.20, RunFindings: 20},
			{RunID: "b", NewFindings: 10, TotalFindings: 110, MarginalRatio: 10.0 / 110.0, RunFindings: 10},
		}
		out := renderSaturation(h, nil, nil)
		if !strings.Contains(out, "0% was already in this ledger") {
			t.Errorf("a run whose every finding was new must read as 0%% already known:\n%s", out)
		}
		for _, forbidden := range []string{"91% was already", "9% was already"} {
			if strings.Contains(out, forbidden) {
				t.Errorf("the growth ratio leaked into the repetition sentence (%q):\n%s", forbidden, out)
			}
		}
		if !strings.Contains(out, "still productive") {
			t.Errorf("a fully novel run must read as productive:\n%s", out)
		}
	})

	t.Run("a productive project is told to keep going", func(t *testing.T) {
		out := renderSaturation(convergence(1.00, 0.80).Convergence, map[string]int{"churn": 1}, areas)
		if !strings.Contains(out, "still productive") {
			t.Errorf("an 80%% novelty must read as productive:\n%s", out)
		}
		if strings.Contains(out, "Re-walking the same ground") {
			t.Errorf("a productive project must not be steered off its seams:\n%s", out)
		}
	})

	t.Run("nothing measured renders nothing", func(t *testing.T) {
		if got := renderSaturation(nil, nil, areas); got != "" {
			t.Errorf("an early run must read no steering at all, got:\n%s", got)
		}
		if got := renderSaturation(nil, nil, nil); got != "" {
			t.Errorf("no history and no areas must render nothing, got:\n%s", got)
		}
	})

	t.Run("a legacy history still states growth but claims no repetition rate", func(t *testing.T) {
		// Points written before RunFindings existed cannot answer "how much was
		// already known" — so the block says what it does know and no more.
		out := renderSaturation(legacyConvergence(1.00, 0.18, 0.10).Convergence, nil, nil)
		if strings.Contains(out, "already in this ledger") {
			t.Errorf("an unmeasured history must not assert a repetition rate:\n%s", out)
		}
		if !strings.Contains(out, "100% → 18% → 10%") {
			t.Errorf("the growth series is still known and worth stating:\n%s", out)
		}
	})

	t.Run("a single measured run states its share but no growth trend", func(t *testing.T) {
		out := renderSaturation(convergence(0.40).Convergence, nil, nil)
		if !strings.Contains(out, "60% was already in this ledger") {
			t.Errorf("one measured run must still state its share:\n%s", out)
		}
		if strings.Contains(out, "→") {
			t.Errorf("one point is not a trend:\n%s", out)
		}
	})

	t.Run("an empty run is left out of the growth series", func(t *testing.T) {
		// Its ratio is a division by zero, and printing it would show a
		// collapse to 0% that never happened.
		history := convergence(0.9, 0.8).Convergence
		history = append(history, commonmodels.ConvergencePoint{
			RunID: "empty", TotalFindings: 0, MarginalRatio: 0,
		})
		out := renderSaturation(history, nil, nil)
		if strings.Contains(out, "→ 0%") {
			t.Errorf("a run that recorded nothing must not appear as a 0%% share:\n%s", out)
		}
		if !strings.Contains(out, "20% was already in this ledger") {
			t.Errorf("the headline must come from the last MEASURED run (0.8):\n%s", out)
		}
	})

	t.Run("area lists are capped and only name this run's areas", func(t *testing.T) {
		many := make([]AnalysisArea, 0, 40)
		depth := map[string]int{}
		for i := 0; i < 40; i++ {
			id := fmt.Sprintf("a%02d", i)
			many = append(many, AnalysisArea{ID: id, Name: "Area " + id})
			depth[id] = 5
		}
		// An area id the playbook no longer has must not be named: the run
		// cannot act on it.
		depth["removed-area"] = 9
		out := renderSaturation(convergence(0.1).Convergence, depth, many)
		if strings.Contains(out, "removed-area") {
			t.Errorf("an area this run does not have must not be named:\n%s", out)
		}
		if n := strings.Count(out, " runs)"); n > maxAreasPerSaturationLine {
			t.Errorf("named %d deep areas, want at most %d", n, maxAreasPerSaturationLine)
		}
	})

	t.Run("falls back to the area id when it has no name", func(t *testing.T) {
		out := renderSaturation(convergence(0.1).Convergence, map[string]int{"slug-only": 4},
			[]AnalysisArea{{ID: "slug-only"}})
		if !strings.Contains(out, "slug-only (4 runs)") {
			t.Errorf("an unnamed area must still be nameable:\n%s", out)
		}
	})

	t.Run("the growth series is capped", func(t *testing.T) {
		ratios := make([]float64, 0, 50)
		for i := 0; i < 50; i++ {
			ratios = append(ratios, 0.5)
		}
		out := renderSaturation(convergence(ratios...).Convergence, nil, nil)
		if n := strings.Count(out, "→"); n != maxMarginalTrendPoints-1 {
			t.Errorf("growth line has %d arrows, want %d — fifty points is a table, not a sentence", n, maxMarginalTrendPoints-1)
		}
	})
}

// TestSaturationBlockRendersInsideThePreviousContext pins that the block is
// actually wired into the prompt, between the coverage map it completes and the
// findings it steers.
func TestSaturationBlockRendersInsideThePreviousContext(t *testing.T) {
	o := &Orchestrator{}
	lrc := &ledgerReadContext{
		hasLedger: true,
		coverage: commonmodels.LedgerCoverage{
			ExploredTables: []string{"ds.orders"}, TotalTables: 5,
			Summary:   "orders covered",
			AreaDepth: map[string]int{"churn": 7},
		},
		convergence: convergence(1.00, 0.18, 0.10).Convergence,
		findings:    ledgerFixture(3),
		descChars:   200,
	}
	out := o.buildPreviousContext(&models.ProjectContext{TotalDiscoveries: 3}, nil, nil, nil, lrc,
		[]AnalysisArea{{ID: "churn", Name: "Churn"}, {ID: "pricing", Name: "Pricing"}})

	cov := strings.Index(out, "Coverage map")
	sat := strings.Index(out, "How much this investigation is still finding")
	fin := strings.Index(out, "Already established findings")
	if cov < 0 || sat < 0 || fin < 0 {
		t.Fatalf("missing a block: coverage=%d saturation=%d findings=%d\n%s", cov, sat, fin, out)
	}
	if cov >= sat || sat >= fin {
		t.Errorf("order must be coverage → saturation → findings, got %d/%d/%d", cov, sat, fin)
	}
	if !strings.Contains(out, "Churn (7 runs)") || !strings.Contains(out, "Pricing") {
		t.Errorf("per-area steering missing from the assembled context:\n%s", out)
	}
}

// TestAreasByDepthNeedsSomethingToContrast: with no depth recorded, every area
// is trivially "open ground", and a line naming all of them tells the run
// nothing it did not already know. A ledger does reach this state — coverage is
// written even on a run whose reflection call failed before judging any area.
func TestAreasByDepthNeedsSomethingToContrast(t *testing.T) {
	areas := []AnalysisArea{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}

	if deep, barren := areasByDepth(nil, areas); len(deep) != 0 || len(barren) != 0 {
		t.Errorf("no depth map must name no areas, got deep=%v barren=%v", deep, barren)
	}
	if deep, barren := areasByDepth(map[string]int{}, areas); len(deep) != 0 || len(barren) != 0 {
		t.Errorf("an empty depth map must name no areas, got deep=%v barren=%v", deep, barren)
	}
	// One recorded area is enough to make the contrast real.
	deep, barren := areasByDepth(map[string]int{"a": 4}, areas)
	if len(deep) != 1 || len(barren) != 1 {
		t.Errorf("one mined area and one untouched: got deep=%v barren=%v", deep, barren)
	}
}

// --- the two budgets are independent (acceptance criterion 3) ---

// fixedTopNProvider stands for a provider that returns a known handful of
// items and reads only Limit — the shape the top-K constants were written for.
type fixedTopNProvider struct {
	name  string
	calls int
	got   agentplugin.ContextProviderOpts
}

func (p *fixedTopNProvider) Name() string { return p.name }

func (p *fixedTopNProvider) Section(_ context.Context, _, _ string, opts agentplugin.ContextProviderOpts) (string, error) {
	p.calls++
	p.got = opts
	if opts.Limit <= 0 {
		return "", nil
	}
	lines := make([]string, 0, opts.Limit)
	for i := 0; i < opts.Limit; i++ {
		lines = append(lines, fmt.Sprintf("- fixed item %d", i))
	}
	return "## Fixed\n" + strings.Join(lines, "\n"), nil
}

// budgetedProvider stands for a retrieval provider that scales with the window:
// it sizes itself from TokenBudget and falls back to Limit only when no budget
// was supplied, which is the contract the opts field documents.
type budgetedProvider struct {
	name  string
	calls int
	got   agentplugin.ContextProviderOpts
}

const budgetedProviderTokensPerItem = 10

func (p *budgetedProvider) Name() string { return p.name }

func (p *budgetedProvider) Section(_ context.Context, _, _ string, opts agentplugin.ContextProviderOpts) (string, error) {
	p.calls++
	p.got = opts
	n := opts.Limit
	if opts.TokenBudget > 0 {
		n = opts.TokenBudget / budgetedProviderTokensPerItem
	}
	if n <= 0 {
		return "", nil
	}
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, fmt.Sprintf("- retrieved item %d", i))
	}
	return "## Retrieved\n" + strings.Join(lines, "\n"), nil
}

func countLines(section, prefix string) int {
	n := 0
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// TestLedgerAndKnowledgeBudgetsAreIndependent is acceptance criterion 3,
// asserted in both directions. Before this, one Limit was handed to every
// provider, so a provider built to scale past a fixed top-N was given 5 and
// competed for those 5 with a provider that wanted exactly 5.
func TestLedgerAndKnowledgeBudgetsAreIndependent(t *testing.T) {
	fixed := &fixedTopNProvider{name: "fixed-knowledge-for-test"}
	scaling := &budgetedProvider{name: "scaling-retrieval-for-test"}
	agentplugin.RegisterContextProvider(fixed)
	agentplugin.RegisterContextProvider(scaling)
	t.Cleanup(func() {
		unregisterContextProviderForTest(fixed.name)
		unregisterContextProviderForTest(scaling.name)
	})

	inject := func(topK, window int) string {
		o := &Orchestrator{
			projectID:      "p1",
			llmProvider:    "bedrock",
			llmModel:       "test-model",
			llmInputWindow: window,
			llmOutputCap:   16_384,
		}
		return o.injectKnowledgeSources(context.Background(), "PROMPT", "a query", topK, llmlayer.LayerAnalysis, nil)
	}

	t.Run("changing the top-K does not change the retrieval provider", func(t *testing.T) {
		at5 := inject(5, 131_072)
		at40 := inject(40, 131_072)
		if a, b := countLines(at5, "- retrieved"), countLines(at40, "- retrieved"); a != b {
			t.Errorf("retrieval returned %d items at topK=5 and %d at topK=40 — its budget must not be the knowledge top-K", a, b)
		}
		if a, b := countLines(at5, "- fixed"), countLines(at40, "- fixed"); a != 5 || b != 40 {
			t.Errorf("the fixed provider must still follow Limit: got %d at 5 and %d at 40", a, b)
		}
	})

	t.Run("changing the window does not change the fixed provider", func(t *testing.T) {
		small := inject(5, 32_000)
		large := inject(5, 1_000_000)
		if a, b := countLines(small, "- fixed"), countLines(large, "- fixed"); a != 5 || b != 5 {
			t.Errorf("the fixed provider returned %d and %d items — a window change must not move it", a, b)
		}
		if a, b := countLines(small, "- retrieved"), countLines(large, "- retrieved"); b <= a {
			t.Errorf("retrieval returned %d items on a 32K window and %d on a 1M one — it must scale with the window", a, b)
		}
	})

	t.Run("both sections reach the prompt", func(t *testing.T) {
		out := inject(5, 131_072)
		if !strings.Contains(out, "## Fixed") || !strings.Contains(out, "## Retrieved") {
			t.Errorf("both providers must render — they are additive, not alternatives:\n%s", out)
		}
		if !strings.HasSuffix(out, "PROMPT") {
			t.Errorf("the original prompt must survive at the end:\n%s", out)
		}
	})
}

// TestInjectedOptsCarryTheBudgetAndExclusions pins what the providers are
// actually handed, since everything above depends on it.
func TestInjectedOptsCarryTheBudgetAndExclusions(t *testing.T) {
	scaling := &budgetedProvider{name: "opts-observer-for-test"}
	agentplugin.RegisterContextProvider(scaling)
	t.Cleanup(func() { unregisterContextProviderForTest(scaling.name) })

	o := &Orchestrator{
		projectID:             "p1",
		llmProvider:           "bedrock",
		llmModel:              "test-model",
		llmInputWindow:        131_072,
		llmOutputCap:          16_384,
		baseContextFindingIDs: []string{"f-000", "f-001", "f-002"},
	}

	t.Run("budget and exclusions arrive", func(t *testing.T) {
		o.injectKnowledgeSources(context.Background(), "PROMPT", "a query", 5, llmlayer.LayerAnalysis, o.baseContextFindingIDs)
		if scaling.got.TokenBudget <= 0 {
			t.Errorf("TokenBudget = %d, want positive on a 131K window", scaling.got.TokenBudget)
		}
		if len(scaling.got.ExcludeIDs) != 3 {
			t.Fatalf("ExcludeIDs = %v, want the three findings the base context already carries", scaling.got.ExcludeIDs)
		}
		if scaling.got.MinScore != knowledgeMinScore {
			t.Errorf("MinScore = %v, want %v", scaling.got.MinScore, knowledgeMinScore)
		}
	})

	t.Run("a prompt that already fills the window offers no budget", func(t *testing.T) {
		// The provider then falls back to Limit, which is the documented
		// reading of a zero budget — not silence.
		out := o.injectKnowledgeSources(context.Background(), strings.Repeat("x", 2_000_000), "a query", 5, llmlayer.LayerAnalysis, nil)
		if scaling.got.TokenBudget != 0 {
			t.Errorf("TokenBudget = %d, want 0 for a prompt past the window", scaling.got.TokenBudget)
		}
		if n := countLines(out, "- retrieved"); n != 5 {
			t.Errorf("fell back to %d items, want the Limit's 5 — a zero budget is not an instruction to carry nothing", n)
		}
	})

	t.Run("a prompt that does not carry the base context excludes nothing", func(t *testing.T) {
		// The clarifying-questions prompt is built without the base context, so
		// excluding its findings there would suppress knowledge the model never
		// saw. The caller says what its own prompt carries; this function must
		// not assume.
		o.injectKnowledgeSources(context.Background(), "PROMPT", "a query", 5, llmlayer.LayerQuestions, nil)
		if len(scaling.got.ExcludeIDs) != 0 {
			t.Errorf("ExcludeIDs = %v, want none for a prompt without the base context", scaling.got.ExcludeIDs)
		}
	})

	t.Run("no query means no provider call at all", func(t *testing.T) {
		before := scaling.calls
		if got := o.injectKnowledgeSources(context.Background(), "PROMPT", "", 5, llmlayer.LayerAnalysis, nil); got != "PROMPT" {
			t.Errorf("an empty query must return the prompt unchanged, got %q", got)
		}
		if scaling.calls != before {
			t.Error("an empty query must not reach the providers")
		}
	})
}

// --- the reflection prompt's re-judgement list (the retirement path) ---

// TestReflectionPriorsScaleWithTheWindow is the fix for the reason nothing ever
// retired. A status only moves through prior_status_updates, and the model may
// only name ids this prompt printed — so a fixed 60-finding list made every
// finding beyond it unreachable by any verdict, for ever, and the active set
// could only grow.
func TestReflectionPriorsScaleWithTheWindow(t *testing.T) {
	const total = 420
	prior := ledgerFixture(total)
	result := &models.DiscoveryResult{
		ID: "d1", ProjectID: "p1",
		Insights: []models.Insight{{ID: "ins-1", AnalysisArea: "area-0", Name: "Something", Severity: "high"}},
	}
	pol := agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeAuto, FrontierPolicy: agentplugin.FrontierBalanced}

	shown := func(window, outputCap, reSeen int) int {
		o := windowedOrchestrator(nil, nil, window, outputCap)
		budget := o.reflectionPriorsBudget(context.Background(), result, nil, pol, nil, window, outputCap, reSeen)
		return len(priorFindingsForPrompt(append([]commonmodels.LedgerFinding{}, prior...), budget))
	}

	// This list is cheap — it carries ids, statuses and metrics, and no
	// descriptions, because the substance is in the base context the same run
	// already has. So the interesting result is that it fits whole at every
	// ordinary window size: the retirement path is open on a 32K model too, and
	// the 60-finding cap was never a budget, just a number.
	for _, tc := range []struct {
		name              string
		window, outputCap int
	}{
		{"1M", 1_000_000, 32_000},
		{"131K", 131_072, 16_384},
		{"32K", 32_000, 8_192},
	} {
		if got := shown(tc.window, tc.outputCap, 0); got != total {
			t.Errorf("%s window re-judged %d of %d findings — this list is cheap enough to fit whole here", tc.name, got, total)
		}
	}

	// It does still degrade, which is what makes it a budget rather than a
	// bigger constant.
	tiny := shown(8_192, 2_048, 0)
	tinier := shown(4_096, 1_024, 0)
	t.Logf("priors re-judgeable: 32K+=%d (all) 8K=%d 4K=%d (of %d; the deleted cap was 60)", total, tiny, tinier, total)
	if tiny >= total {
		t.Errorf("an 8K window re-judged %d of %d — it cannot hold the whole list", tiny, total)
	}
	if tinier > tiny {
		t.Errorf("a 4K window re-judged %d, more than the 8K window's %d", tinier, tiny)
	}
}

// TestReflectionPriorsReserveRoomForTheVerdicts pins the output coupling. A
// longer list can demand a longer response, and a reflection response truncated
// mid-JSON parses as nothing and discards the whole phase's judgement — the
// #403 failure. The reserve is sized from the findings the run actually SAW
// again, because those are the only ones it can honestly judge.
func TestReflectionPriorsReserveRoomForTheVerdicts(t *testing.T) {
	result := &models.DiscoveryResult{ID: "d1", ProjectID: "p1"}
	pol := agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeAuto}
	o := windowedOrchestrator(nil, nil, 131_072, 16_384)

	budget := func(reSeen int) int {
		return o.reflectionPriorsBudget(context.Background(), result, nil, pol, nil, 131_072, 16_384, reSeen)
	}

	none := budget(0)
	some := budget(50)
	many := budget(1000)

	t.Logf("priors budget: reSeen=0 -> %d, reSeen=50 -> %d, reSeen=1000 -> %d", none, some, many)

	if some > none {
		t.Errorf("re-seeing 50 findings must not RAISE the input budget: %d vs %d", some, none)
	}
	if many >= none {
		t.Errorf("a run that re-saw 1000 findings owes a large response and must get less input budget: %d vs %d", many, none)
	}
	// Below the intended-output reserve, the number of re-sightings must not
	// matter at all — otherwise every ordinary run pays for headroom nothing
	// uses.
	if budget(1) != none {
		t.Errorf("one re-sighting changed the budget from %d to %d; it is far below the intended output reserve", none, budget(1))
	}
}

// TestReflectionPriorsBudgetMeasuresTheRestOfThePrompt: the other sections vary
// by orders of magnitude between projects — a wide warehouse's catalog lists
// alone run to hundreds of names — so the budget measures them instead of
// assuming a size.
func TestReflectionPriorsBudgetMeasuresTheRestOfThePrompt(t *testing.T) {
	pol := agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeAuto}
	o := windowedOrchestrator(nil, nil, 131_072, 16_384)

	bare := &models.DiscoveryResult{ID: "d1", ProjectID: "p1"}
	wide := &models.DiscoveryResult{ID: "d1", ProjectID: "p1", Schemas: map[string]models.TableSchema{}}
	for i := 0; i < 400; i++ {
		wide.Schemas[fmt.Sprintf("dataset.table_with_a_long_name_%03d", i)] = models.TableSchema{}
	}
	manyTasks := make([]commonmodels.LedgerTask, 0, 40)
	for i := 0; i < 40; i++ {
		manyTasks = append(manyTasks, commonmodels.LedgerTask{
			ID: fmt.Sprintf("t-%d", i), Kind: "next_task",
			Text: "a thread with a reasonably long description of what to investigate next " + strings.Repeat("x", 80),
		})
	}

	plain := o.reflectionPriorsBudget(context.Background(), bare, nil, pol, nil, 131_072, 16_384, 0)
	wideCatalog := o.reflectionPriorsBudget(context.Background(), wide, nil, pol, nil, 131_072, 16_384, 0)
	withTasks := o.reflectionPriorsBudget(context.Background(), bare, manyTasks, pol, nil, 131_072, 16_384, 0)
	withCube := o.reflectionPriorsBudget(context.Background(), bare, nil, pol, []string{"a", "b", "c"}, 131_072, 16_384, 0)

	if wideCatalog >= plain {
		t.Errorf("a 400-table catalog must cost the priors budget: %d vs %d", wideCatalog, plain)
	}
	if withTasks >= plain {
		t.Errorf("40 open threads must cost the priors budget: %d vs %d", withTasks, plain)
	}
	if withCube >= plain {
		t.Errorf("a cube catalog adds two paragraphs and must cost the priors budget: %d vs %d", withCube, plain)
	}
}

// TestReflectionResolvePathIsGrounded is acceptance criterion 4's other half: a
// finding must be able to reach a terminal status, without the prompt
// licensing absence-as-proof.
func TestReflectionResolvePathIsGrounded(t *testing.T) {
	for _, demand := range []bool{false, true} {
		field := renderPriorStatusField(demand)
		if !strings.Contains(field, "`resolved`") {
			t.Errorf("demand=%v: the contract must say when a finding may be resolved:\n%s", demand, field)
		}
		for _, want := range []string{
			"THIS RUN looked at the thing it describes",
			"Name the finding from this run that establishes it",
			"Do NOT mark a finding `resolved` just because it did not reappear",
			"absence from this run's output is not proof",
		} {
			if !strings.Contains(field, want) {
				t.Errorf("demand=%v: missing %q:\n%s", demand, want, field)
			}
		}
	}
	// The permissive clause must not have displaced the prohibition.
	field := renderPriorStatusField(true)
	resolve := strings.Index(field, "Mark a finding `resolved`")
	deny := strings.Index(field, "Do NOT mark a finding `resolved`")
	if resolve < 0 || deny < 0 || deny < resolve {
		t.Errorf("the prohibition must follow the permission, so the last word on absence is that it proves nothing:\n%s", field)
	}
}

// --- the three remaining review findings ---

// TestPriorContextOptOutIsHonoured. A ledger that carried nothing — because the
// operator set the share to zero, or because the window has no room — must not
// be answered with the legacy unbudgeted insight dump. That fallback exists for
// a project with NO ledger; using it here would undo the opt-out and feed the
// smallest models exactly the block the budget keeps out of them.
func TestPriorContextOptOutIsHonoured(t *testing.T) {
	o := &Orchestrator{}
	pctx := &models.ProjectContext{TotalDiscoveries: 4, LastDiscoveryDate: time.Now()}
	prevInsights := []models.InsightSummary{
		{Name: "legacy dumped insight", AnalysisArea: "churn", Severity: "high", AffectedCount: 10, Date: "2026-01-01"},
	}

	t.Run("share of zero carries nothing at all", func(t *testing.T) {
		t.Setenv(priorContextShareEnv, "0")
		wo := windowedOrchestrator(ledgerFixture(420), convergence(0.10), 131_072, 16_384)
		lrc := wo.loadLedgerReadContext(context.Background())
		if lrc == nil {
			t.Fatal("the ledger is still present — only the budget is zero")
		}
		if len(lrc.findings) != 0 {
			t.Fatalf("budget zero carried %d findings", len(lrc.findings))
		}
		out := o.buildPreviousContext(pctx, prevInsights, nil, nil, lrc, nil)
		if strings.Contains(out, "legacy dumped insight") {
			t.Errorf("the opt-out fell through to the legacy insight dump:\n%s", out)
		}
		if strings.Contains(out, "Already established findings") {
			t.Errorf("no findings were budgeted, so no findings block may render:\n%s", out)
		}
	})

	t.Run("a window with no room carries nothing either", func(t *testing.T) {
		wo := windowedOrchestrator(ledgerFixture(420), convergence(0.10), 1_024, 256)
		lrc := wo.loadLedgerReadContext(context.Background())
		if lrc == nil || len(lrc.findings) != 0 {
			t.Fatalf("expected a present ledger carrying no findings, got %+v", lrc)
		}
		out := o.buildPreviousContext(pctx, prevInsights, nil, nil, lrc, nil)
		if strings.Contains(out, "legacy dumped insight") {
			t.Errorf("a 1K window was handed the unbudgeted legacy dump:\n%s", out)
		}
	})

	t.Run("a project with NO ledger still gets the legacy dump", func(t *testing.T) {
		// The fallback is not removed, only stopped from firing for the wrong
		// reason — a project that predates the ledger still needs it.
		out := o.buildPreviousContext(pctx, prevInsights, nil, nil, nil, nil)
		if !strings.Contains(out, "legacy dumped insight") {
			t.Errorf("a project with no ledger must still get the older block:\n%s", out)
		}
	})
}

// TestPriorContextBudgetNeverExceedsTheHeadroom. SaturatedShare is bounded to
// twice the configured base, so a base above 0.5 can scale past 1 — and a
// budget larger than the free space is not a budget. On a 32K/8K model a 0.75
// base would have allocated 26,356 tokens of 18,496 available.
func TestPriorContextBudgetNeverExceedsTheHeadroom(t *testing.T) {
	const available = 18_496
	for _, share := range []string{"0.2", "0.5", "0.75", "1.0", "2.0"} {
		t.Setenv(priorContextShareEnv, share)
		for _, novelty := range []float64{1.0, 0.5, 0.1, 0} {
			got := priorContextBudget(available, novelty, true)
			if got > available {
				t.Errorf("share %s, novelty %v: budget %d exceeds the %d available", share, novelty, got, available)
			}
			if got < 0 {
				t.Errorf("share %s, novelty %v: negative budget %d", share, novelty, got)
			}
		}
	}

	t.Run("an ordinary share is unaffected by the clamp", func(t *testing.T) {
		t.Setenv(priorContextShareEnv, "0.20")
		if got := priorContextBudget(available, 1.0, true); got != available/5 {
			t.Errorf("budget = %d, want a fifth of %d", got, available)
		}
	})

	t.Run("zero available yields zero, not a clamp artefact", func(t *testing.T) {
		if got := priorContextBudget(0, 0, true); got != 0 {
			t.Errorf("budget = %d, want 0", got)
		}
	})
}

// TestPriorContextBudgetUsesNoveltyNotGrowth is the P1, end to end: a mature
// project whose last run repeated nothing must NOT be handed the saturated
// budget just because its ledger is large.
func TestPriorContextBudgetUsesNoveltyNotGrowth(t *testing.T) {
	findings := ledgerFixture(420)

	shown := func(history []commonmodels.ConvergencePoint) int {
		o := windowedOrchestrator(findings, &commonmodels.DiscoveryLedger{ProjectID: "p1", Convergence: history}, 131_072, 16_384)
		lrc := o.loadLedgerReadContext(context.Background())
		if lrc == nil {
			return 0
		}
		return len(lrc.findings)
	}

	// Growth 9% but every finding the run reported was new: productive, so the
	// base share.
	productive := shown([]commonmodels.ConvergencePoint{
		{RunID: "r", NewFindings: 10, TotalFindings: 110, MarginalRatio: 10.0 / 110.0, RunFindings: 10},
	})
	// The same growth, but the run repeated almost everything: saturated.
	repeating := shown([]commonmodels.ConvergencePoint{
		{RunID: "r", NewFindings: 10, TotalFindings: 110, MarginalRatio: 10.0 / 110.0, RunFindings: 100},
	})
	t.Logf("same 9%% growth ratio: all-new run shown %d findings, repeating run shown %d", productive, repeating)

	if repeating <= productive {
		t.Errorf("the repeating run was shown %d findings and the all-new run %d — the two have the same growth ratio, so only novelty can tell them apart", repeating, productive)
	}
}
