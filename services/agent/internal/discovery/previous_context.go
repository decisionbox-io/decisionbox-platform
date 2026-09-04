package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// maxLedgerFindingsInPrompt caps how many ledger findings the base context
// carries. The enterprise per-area RAG provider surfaces the long tail per
// analysis area; this block is the global, highest-ranked slice.
const maxLedgerFindingsInPrompt = 25

// ledgerReadContext is the compounding-discovery read context assembled at run
// start: the accumulated findings (ranked, with substance), the coverage map,
// and the open next-task queue. hasLedger is false for legacy projects /
// community builds with nothing in the ledger, so the caller falls back to the
// older capped insight dump.
type ledgerReadContext struct {
	findings  []commonmodels.LedgerFinding
	tasks     []commonmodels.LedgerTask
	coverage  commonmodels.LedgerCoverage
	hasLedger bool
}

// loadLedgerReadContext reads the project's ledger and builds the read context.
// Best-effort: any error yields a nil context (the caller degrades to the legacy
// path). The open next-task queue is suppressed when evolution mode is off
// (self-directed threads are the governed part; coverage + findings are not).
func (o *Orchestrator) loadLedgerReadContext(ctx context.Context) *ledgerReadContext {
	if o.findingRepo == nil {
		return nil
	}

	all, err := o.findingRepo.List(ctx, o.projectID)
	if err != nil {
		applog.WithError(err).Warn("Failed to load ledger findings for read context")
		return nil
	}

	// Rank (severity × recency × liked × recurrence × status) and keep the top
	// slice — replacing the arbitrary first-30 truncation. Resolved/refuted
	// findings are done; drop them from the "build on these" block.
	now := time.Now()
	active := make([]commonmodels.LedgerFinding, 0, len(all))
	for _, f := range all {
		if f.Status == commonmodels.LedgerFindingStatusResolved || f.Status == commonmodels.LedgerFindingStatusRefuted {
			continue
		}
		active = append(active, f)
	}
	sort.SliceStable(active, func(i, j int) bool {
		return active[i].Rank(now) > active[j].Rank(now)
	})
	if len(active) > maxLedgerFindingsInPrompt {
		active = active[:maxLedgerFindingsInPrompt]
	}

	lrc := &ledgerReadContext{findings: active}

	if o.ledgerRepo != nil {
		if ledger, err := o.ledgerRepo.Get(ctx, o.projectID); err == nil && ledger != nil {
			lrc.coverage = ledger.Coverage
		}
	}

	// Open tasks — governed by evolution mode (off ⇒ no self-directed threads).
	pol, _ := agentplugin.ResolveDiscoveryPolicy(ctx, o.projectID)
	if pol.EvolutionMode != agentplugin.EvolutionModeOff && o.taskRepo != nil {
		if tasks, err := o.taskRepo.List(ctx, o.projectID, commonmodels.LedgerTaskStatusOpen); err == nil {
			lrc.tasks = tasks
		}
	}

	lrc.hasLedger = len(lrc.findings) > 0 || len(lrc.tasks) > 0 ||
		strings.TrimSpace(lrc.coverage.Summary) != "" || len(lrc.coverage.ExploredTables) > 0
	if !lrc.hasLedger {
		return nil
	}
	return lrc
}

// renderCoverage renders the coverage-map block: explored vs. frontier + the
// reflection phase's natural-language summary.
func renderCoverage(cov commonmodels.LedgerCoverage) string {
	summary := strings.TrimSpace(cov.Summary)
	explored := len(cov.ExploredTables)
	if summary == "" && explored == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Coverage map\n")
	if cov.TotalTables > 0 {
		frontier := cov.TotalTables - explored
		if frontier < 0 {
			frontier = 0
		}
		fmt.Fprintf(&sb, "Explored %d of %d catalog tables (%d still on the frontier). ", explored, cov.TotalTables, frontier)
	} else if explored > 0 {
		fmt.Fprintf(&sb, "Explored %d tables so far. ", explored)
	}
	if summary != "" {
		sb.WriteString(summary)
	}
	sb.WriteString("\n\n")
	return sb.String()
}

// renderLedgerFindings renders the ranked findings-with-substance block. Input is
// already ranked + capped by the loader.
func renderLedgerFindings(findings []commonmodels.LedgerFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Key findings so far (build on these — drill deeper or check whether they changed)\n\n")
	for _, f := range findings {
		area := f.Area
		if area == "" {
			area = "general"
		}
		fmt.Fprintf(&sb, "- [%s] **%s** (%s", area, f.Name, f.Severity)
		if f.Status != "" {
			fmt.Fprintf(&sb, ", %s", f.Status)
		}
		if f.SeenCount > 1 {
			fmt.Fprintf(&sb, ", seen %d×", f.SeenCount)
		}
		sb.WriteString(")")
		if f.KeyMetric != "" {
			fmt.Fprintf(&sb, " — %s", f.KeyMetric)
		}
		if d := strings.TrimSpace(truncate(f.Description, 200)); d != "" {
			fmt.Fprintf(&sb, ". %s", d)
		}
		sb.WriteByte('\n')
	}
	sb.WriteString("\n")
	return sb.String()
}

// renderLedgerTasks renders the open next-task queue.
func renderLedgerTasks(tasks []commonmodels.LedgerTask) string {
	if len(tasks) == 0 {
		return ""
	}
	if len(tasks) > maxLedgerTasksInPrompt {
		tasks = tasks[:maxLedgerTasksInPrompt]
	}
	var sb strings.Builder
	sb.WriteString("### Open investigation threads (pursue these this run)\n")
	sb.WriteString("Earlier runs flagged these to follow up. Prioritise them alongside exploring the frontier.\n\n")
	for _, t := range tasks {
		fmt.Fprintf(&sb, "- %s\n", strings.TrimSpace(t.Text))
	}
	sb.WriteString("\n")
	return sb.String()
}
