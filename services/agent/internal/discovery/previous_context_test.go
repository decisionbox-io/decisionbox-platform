package discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
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
		tasks: []commonmodels.LedgerTask{{Text: "explore the events tables"}},
	}
	out := o.buildPreviousContext(pctx, nil, nil, nil, lrc)

	for _, want := range []string{
		"Investigation so far",
		"Coverage map",
		"events untouched",
		"Key findings so far",
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

	lrc := o.loadLedgerReadContext(context.Background())
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

func TestLoadLedgerReadContext_OffModeSuppressesTasks(t *testing.T) {
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "a", Area: "x", Name: "finding", Severity: "high", Status: "confirmed", LastSeen: time.Now()},
	}}
	tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{{Text: "t1", Status: "open"}}}
	o := &Orchestrator{projectID: "p1", findingRepo: fr, ledgerRepo: &fakeLedgerRepo{}, taskRepo: tr}

	lrc := o.loadLedgerReadContext(context.Background())
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
