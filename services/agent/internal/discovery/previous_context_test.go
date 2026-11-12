package discovery

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	"github.com/decisionbox-io/decisionbox/libs/go-common/contextbudget"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func TestRenderHistoricalPatterns(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("empty when nothing recurring", func(t *testing.T) {
		out := renderHistoricalPatterns([]models.HistoricalPattern{
			{Name: "seen-once", SeenCount: 1, Status: "active", LastSeen: base},
		})
		if out != "" {
			t.Fatalf("a single-sighting pattern must not render as a trend, got:\n%s", out)
		}
	})

	t.Run("recurring and worsening render, once-seen excluded", func(t *testing.T) {
		out := renderHistoricalPatterns([]models.HistoricalPattern{
			{Name: "recurring-a", AnalysisArea: "churn", SeenCount: 3, Status: "recurring", LastSeen: base},
			{Name: "seen-once", SeenCount: 1, Status: "active", LastSeen: base},
			{Name: "worsening-b", SeenCount: 1, Status: "worsening", LastSeen: base},
		})
		if !strings.Contains(out, "recurring-a") {
			t.Errorf("recurring pattern missing:\n%s", out)
		}
		if !strings.Contains(out, "worsening-b") {
			t.Errorf("worsening pattern (even seen once) should render:\n%s", out)
		}
		if strings.Contains(out, "seen-once") {
			t.Errorf("single-sighting non-worsening pattern must be excluded:\n%s", out)
		}
		if !strings.Contains(out, "seen in 3 runs") {
			t.Errorf("seen-count not narrated:\n%s", out)
		}
	})

	t.Run("caps to the freshest patterns", func(t *testing.T) {
		patterns := make([]models.HistoricalPattern, 0, 20)
		for i := 0; i < 20; i++ {
			patterns = append(patterns, models.HistoricalPattern{
				Name:      "p" + string(rune('a'+i)),
				SeenCount: 2,
				Status:    "recurring",
				LastSeen:  base.Add(time.Duration(i) * time.Hour),
			})
		}
		out := renderHistoricalPatterns(patterns)
		if got := strings.Count(out, "- **"); got != maxHistoricalPatternsInPrompt {
			t.Fatalf("rendered %d patterns, want cap %d", got, maxHistoricalPatternsInPrompt)
		}
		// Freshest (latest LastSeen) must survive the cap; oldest must be dropped.
		if !strings.Contains(out, "p"+string(rune('a'+19))) {
			t.Errorf("freshest pattern should be kept:\n%s", out)
		}
		if strings.Contains(out, "**pa ") || strings.Contains(out, "**pa—") || strings.Contains(out, "**pa*") {
			t.Errorf("oldest pattern should be dropped by the cap:\n%s", out)
		}
	})
}

// --- ledger read path ---

func TestBuildPreviousContext_WithLedger(t *testing.T) {
	o := &Orchestrator{}
	pctx := &models.ProjectContext{TotalDiscoveries: 2, LastDiscoveryDate: time.Now()}
	lrc := &ledgerReadContext{
		hasLedger: true,
		coverage:  commonmodels.LedgerCoverage{ExploredTables: []string{"ds.orders"}, TotalTables: 5, Summary: "orders covered; events untouched"},
		findings: []commonmodels.LedgerFinding{
			{Area: "churn", Name: "High EU churn", Severity: "high", Status: "confirmed", KeyMetric: "affected=300", Description: "churn elevated", SeenCount: 3},
		},
		tasks:     []commonmodels.LedgerTask{{Text: "explore the events tables"}},
		descChars: 200,
	}
	out := o.buildPreviousContext(pctx, nil, nil, nil, lrc, nil)

	for _, want := range []string{
		"Investigation so far",
		"Coverage map",
		"events untouched",
		"Already established findings",
		"High EU churn",
		"affected=300",
		"seen 3×",
		"Open investigation threads",
		"explore the events tables",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ledger context missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Previously Found Insights") {
		t.Errorf("ledger present must suppress the legacy insight dump:\n%s", out)
	}
	// The heading this replaced invited re-examination and said nothing about
	// what to do when the re-examination finds no change, which made
	// re-reporting a compliant answer. It must not come back.
	if strings.Contains(out, "build on these") {
		t.Errorf("the findings heading must not invite re-examination without saying what not to do with it:\n%s", out)
	}
}

func TestLoadLedgerReadContext_RanksAndExcludesResolved(t *testing.T) {
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeAuto})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	now := time.Now()
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "crit", Area: "fraud", Name: "Card testing", Severity: "critical", Status: "confirmed", LastSeen: now},
		{ID: "low", Area: "misc", Name: "Minor blip", Severity: "low", Status: "confirmed", LastSeen: now},
		{ID: "done", Area: "old", Name: "Fixed thing", Severity: "critical", Status: commonmodels.LedgerFindingStatusResolved, LastSeen: now},
	}}
	tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{{Text: "t1", Status: "open"}}}
	o := &Orchestrator{projectID: "p1", findingRepo: fr, ledgerRepo: &fakeLedgerRepo{}, taskRepo: tr}

	lrc := o.loadAndFitLedger(context.Background(), 0)
	if lrc == nil || !lrc.hasLedger {
		t.Fatal("expected a ledger read context")
	}
	if len(lrc.findings) != 2 {
		t.Fatalf("resolved finding should be excluded, got %d findings", len(lrc.findings))
	}
	if lrc.findings[0].ID != "crit" {
		t.Errorf("critical finding should rank first, got %q", lrc.findings[0].ID)
	}
	if len(lrc.tasks) != 1 {
		t.Errorf("auto mode should surface open tasks, got %d", len(lrc.tasks))
	}
}

// TestLoadLedgerReadContext_RemembersOnlyWhatThePromptShows: the read context
// is where the run is handed its threads, so it is where the record of which
// threads it was handed belongs. Capped the way the prompt renderer caps it —
// beyond the cap the run was never told about them, and recording a thread as
// pursued by a run that never saw it is the kind of fabricated edge this whole
// record exists to avoid.
func TestLoadLedgerReadContext_RemembersOnlyWhatThePromptShows(t *testing.T) {
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeAuto})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	many := make([]commonmodels.LedgerTask, maxLedgerTasksInPrompt+5)
	for i := range many {
		many[i] = commonmodels.LedgerTask{ID: "t-" + strconv.Itoa(i), Text: "thread", Status: "open"}
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "a", Area: "x", Name: "finding", Severity: "high", Status: "confirmed", LastSeen: time.Now()},
	}}
	o := &Orchestrator{
		projectID: "p1", findingRepo: fr, ledgerRepo: &fakeLedgerRepo{},
		taskRepo: &fakeTaskRepo{existing: many},
	}

	o.loadAndFitLedger(context.Background(), 0)

	if len(o.shownTaskIDs) != maxLedgerTasksInPrompt {
		t.Errorf("want the prompt's cap (%d) remembered, got %d",
			maxLedgerTasksInPrompt, len(o.shownTaskIDs))
	}
	if o.shownTaskIDs[0] != "t-0" {
		t.Errorf("the same slice the renderer takes, in the same order, got %q", o.shownTaskIDs[0])
	}
}

func TestLoadLedgerReadContext_OffModeSuppressesTasks(t *testing.T) {
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "a", Area: "x", Name: "finding", Severity: "high", Status: "confirmed", LastSeen: time.Now()},
	}}
	tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{{Text: "t1", Status: "open"}}}
	o := &Orchestrator{projectID: "p1", findingRepo: fr, ledgerRepo: &fakeLedgerRepo{}, taskRepo: tr}

	lrc := o.loadAndFitLedger(context.Background(), 0)
	if lrc == nil {
		t.Fatal("expected a ledger read context (findings present)")
	}
	if len(lrc.tasks) != 0 {
		t.Errorf("off mode must suppress the next-task queue, got %d", len(lrc.tasks))
	}
	if len(lrc.findings) != 1 {
		t.Errorf("findings are unconditional, got %d", len(lrc.findings))
	}
}

// loadAndFitLedger is the two production steps in one call, for the tests that
// are about what the budget chooses rather than about when it is chosen.
//
// Production splits them because the findings have to be budgeted against the
// rest of the prompt — including the rest of their own block — and that is not
// rendered until the ledger has been read.
func (o *Orchestrator) loadAndFitLedger(ctx context.Context, fixedPromptTokens int) *ledgerReadContext {
	lrc := o.loadLedgerReadContext(ctx)
	// The room a single-model project has: one window, so the per-layer
	// minimum is that window's allowance less the fixed prompt and the
	// picker's floor — which is what the old single-number form computed.
	room := o.baseContextAvailableInput() - fixedPromptTokens - minPickerBudgetTokens
	o.fitPriorFindings(lrc, fixedPromptTokens, room)
	// Production captures the exclusion ids AFTER rendering the base context,
	// off the template that rendered it — a template with no
	// {{PREVIOUS_CONTEXT}} slot carries no findings and must claim none.
	// Mirrored here, with the smallest template that has the slot, so the
	// tests exercise that step instead of a shortcut past it. The slot-less
	// case is TestPriorFindingIDsFollowTheTemplate.
	if lrc != nil {
		o.baseContextFindingIDs = priorFindingIDs(previousContextToken, lrc.findings)
	}
	return lrc
}

// TestLedgerBlockFitsItsWholeAllowance. FitFindings is handed a budget for
// LINES, and the rendered block is heading + no-restate instruction + lines.
// Handing it the whole block allowance overran that allowance by the heading's
// cost — the same error as pricing a line without the newline that follows it,
// one level up, and the enterprise renderer had already been fixed for it.
func TestLedgerBlockFitsItsWholeAllowance(t *testing.T) {
	o := &Orchestrator{
		projectID: "p1", llmProvider: "bedrock", llmModel: "m",
		llmInputWindow: 32_000, llmOutputCap: 8_192,
		findingRepo: &fakeFindingRepo{findings: ledgerFixture(420)},
	}
	if o := ledgerBlockOverhead(); o <= 0 {
		t.Fatalf("the block overhead measured %d — the subtraction would be a no-op", o)
	}

	for _, fixed := range []int{0, 2_000, 8_000, 16_000} {
		lrc := o.loadAndFitLedger(context.Background(), fixed)
		if lrc == nil {
			t.Fatal("no read context")
		}
		available := o.baseContextAvailableInput()
		allowance := priorContextBudget(available, available-fixed-minPickerBudgetTokens, 0, false)
		block := renderLedgerFindings(lrc.findings, lrc.descChars)
		if got := contextbudget.Tokens(block); got > allowance {
			t.Errorf("fixed=%d: the rendered block is %d tokens against an allowance of %d (%d over)",
				fixed, got, allowance, got-allowance)
		}
	}
}

// --- what THIS run has already found (enterprise#542) ---

func runInsights(n int) []models.Insight {
	out := make([]models.Insight, 0, n)
	areas := []string{"pricing-margin-dynamics", "inventory-stock-health", "store-sales-performance"}
	for i := 0; i < n; i++ {
		out = append(out, models.Insight{
			ID:            "ins-" + strconv.Itoa(i),
			AnalysisArea:  areas[i%len(areas)],
			Name:          "Finding " + strconv.Itoa(i) + " about the warehouse",
			Description:   strings.Repeat("substance nobody needs to re-read. ", 40),
			Severity:      "high",
			AffectedCount: 100 + i,
		})
	}
	return out
}

// TestRenderRunFindingsSoFar. Every duplicate insight measured inside a run
// crossed an analysis-area boundary, and roughly half of each run's output
// restated another area of the same run. {{PREVIOUS_CONTEXT}} cannot address
// that: it is composed once at run start, before any area has run, so it
// carries what EARLIER RUNS established and nothing from this one.
func TestRenderRunFindingsSoFar(t *testing.T) {
	t.Run("nothing yet renders nothing", func(t *testing.T) {
		// The first area of a run has no siblings to be told about, and a
		// heading with nothing under it reads as "the others found nothing".
		if got := renderRunFindingsSoFar(nil, 100_000); got != "" {
			t.Errorf("got %q, want empty", got)
		}
		if got := renderRunFindingsSoFar([]models.Insight{}, 100_000); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("names, areas and figures — not descriptions", func(t *testing.T) {
		got := string(renderRunFindingsSoFar(runInsights(3), 100_000))
		for _, want := range []string{
			"Already found by other areas in THIS run",
			"[pricing-margin-dynamics]",
			"Finding 0 about the warehouse",
			"(affected 100)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
		// Recognition is the job: the model has to see a subject is taken,
		// not re-read the finding. Descriptions would multiply the cost by the
		// number of areas left to run.
		if strings.Contains(got, "substance nobody needs to re-read") {
			t.Error("the block carries descriptions; it is meant to be recognisable, not complete")
		}
	})

	t.Run("the instruction is the same contract the cross-run block states", func(t *testing.T) {
		// A model told two different things about two kinds of repetition will
		// apply neither consistently — and the cross-run wording is the one
		// that has been measured to work.
		got := string(renderRunFindingsSoFar(runInsights(2), 100_000))
		for _, want := range []string{
			"Do NOT emit an insight that restates one",
			"magnitude for the same thing",
			"report nothing about it",
			"go deeper",
			"leaving a subject to the area that already covered it is the correct answer",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q", want)
			}
		}
	})

	t.Run("the block is sized from the window, not from a constant", func(t *testing.T) {
		// A fixed token budget is simultaneously trivial on a large window and
		// material on a small one, which is the thing this whole change is
		// about. The share bounds the LINES; the heading and the instruction
		// are the block's fixed cost.
		overhead := contextbudget.Tokens(runFindingsSoFarHeading + runFindingsSoFarInstruction + "\n\n")
		for _, available := range []int{1_000, 20_000, 150_000} {
			got := renderRunFindingsSoFar(runInsights(500), available)
			if got == "" {
				continue
			}
			lines := contextbudget.Tokens(string(got)) - overhead
			if want := contextbudget.Share(available, runFindingsSoFarShare); lines > want {
				t.Errorf("available %d: lines cost %d tokens against a share of %d", available, lines, want)
			}
		}
		// More window, more of the run named.
		small := strings.Count(string(renderRunFindingsSoFar(runInsights(500), 20_000)), "\n- ")
		large := strings.Count(string(renderRunFindingsSoFar(runInsights(500), 150_000)), "\n- ")
		if large <= small {
			t.Errorf("20K window named %d lines, 150K named %d", small, large)
		}
	})

	t.Run("a window with nothing spare carries nothing", func(t *testing.T) {
		// The same answer the prior-knowledge block gives, rather than leaving
		// it for the overflow retry to discover.
		for _, available := range []int{0, -1} {
			if got := renderRunFindingsSoFar(runInsights(20), available); got != "" {
				t.Errorf("available %d rendered %q", available, got)
			}
		}
	})

	t.Run("a run bigger than the cap keeps the most recent", func(t *testing.T) {
		// The areas nearest this one are the likeliest to be re-trodden.
		got := string(renderRunFindingsSoFar(runInsights(maxRunFindingsInAreaPrompt+20), 100_000))
		if strings.Contains(got, "Finding 0 about") {
			t.Error("kept the oldest insights over the most recent")
		}
		if !strings.Contains(got, "Finding "+strconv.Itoa(maxRunFindingsInAreaPrompt+19)+" about") {
			t.Error("dropped the most recent insight")
		}
	})
}

// TestRunFindingsReachTheMeasuredPrompt is the property five consecutive review
// findings on this change were about: a section the model receives but the
// budget does not measure. The area's own prompt assembly and both of its
// measurement paths have to carry this block, or the prior-knowledge block is
// sized against a prompt that is not the one being sent.
func TestRunFindingsReachTheMeasuredPrompt(t *testing.T) {
	o := &Orchestrator{}
	soFar := renderRunFindingsSoFar(runInsights(40), 100_000)
	if soFar == "" {
		t.Fatal("fixture rendered nothing")
	}

	withBlock := o.buildAnalysisAreaPrompt("BASE", "AREA {{QUERY_RESULTS}}", "ds", 3, "[]", "", soFar)
	without := o.buildAnalysisAreaPrompt("BASE", "AREA {{QUERY_RESULTS}}", "ds", 3, "[]", "", "")

	if !strings.Contains(withBlock, "Already found by other areas in THIS run") {
		t.Fatal("the block does not reach the assembled prompt")
	}
	if strings.Contains(without, "Already found by other areas in THIS run") {
		t.Error("an empty block still rendered a heading")
	}
	// It costs what it costs, and the measurement has to see it.
	delta := contextbudget.Tokens(withBlock) - contextbudget.Tokens(without)
	if want := contextbudget.Tokens(string(soFar)); delta < want-2 {
		t.Errorf("the prompt grew by %d tokens, want about the block's %d", delta, want)
	}
}

// TestPriorFindingIDsFollowTheTemplate. Retrieval is told to skip the findings
// the prompt already carries, so that list has to come from the prompt and not
// from the budget's choice. A project can supply its own base_context, and one
// without the {{PREVIOUS_CONTEXT}} slot renders no findings at all — claiming
// them anyway makes the per-area retrieval skip the project's best-ranked
// findings on behalf of a prompt that never contained them, so the model is
// denied knowledge it was never shown. Retrieving something twice is a wasted
// budget; this loses the finding.
func TestPriorFindingIDsFollowTheTemplate(t *testing.T) {
	findings := ledgerFixture(3)

	t.Run("a template with the slot claims what it renders", func(t *testing.T) {
		ids := priorFindingIDs("Analyst instructions.\n\n{{PREVIOUS_CONTEXT}}\n", findings)
		if len(ids) != 3 {
			t.Fatalf("ids = %v, want all three the prompt carries", ids)
		}
		for i := range findings {
			if ids[i] != findings[i].ID {
				t.Errorf("ids[%d] = %q, want %q", i, ids[i], findings[i].ID)
			}
		}
	})

	t.Run("a custom template without the slot claims nothing", func(t *testing.T) {
		const custom = "You are an analyst. Use {{PROFILE}} and answer in {{LANGUAGE}}."
		if ids := priorFindingIDs(custom, findings); len(ids) != 0 {
			t.Errorf("ids = %v, want none — this prompt renders no findings", ids)
		}
	})

	t.Run("the claim matches what buildBaseContext actually rendered", func(t *testing.T) {
		// The property, rather than the two cases: whatever the template, a
		// finding is claimed only if its name is in the rendered prompt.
		o := &Orchestrator{}
		block := renderLedgerFindings(findings, 200)
		for _, template := range []string{
			"{{PREVIOUS_CONTEXT}}",
			"Instructions.\n{{PROFILE}}\n{{PREVIOUS_CONTEXT}}\nin {{LANGUAGE}}",
			"Instructions only, no history slot.",
			"{{PROFILE}} in {{LANGUAGE}}",
		} {
			rendered := o.buildBaseContext(template, "p", block, "English", "ds")
			claimed := len(priorFindingIDs(template, findings)) > 0
			present := strings.Contains(rendered, findings[0].Name)
			if claimed != present {
				t.Errorf("template %q: claimed=%v but rendered=%v", template, claimed, present)
			}
		}
	})
}
