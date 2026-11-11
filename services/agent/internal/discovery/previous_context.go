package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	"github.com/decisionbox-io/decisionbox/libs/go-common/contextbudget"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
)

// ledgerReadContext is the compounding-discovery read context assembled at run
// start: the accumulated findings (ranked, with substance, and budgeted against
// the model window), the coverage map, the convergence history, and the open
// next-task queue. hasLedger is false for legacy projects / builds with nothing
// in the ledger, so the caller falls back to the older capped insight dump.
type ledgerReadContext struct {
	findings []commonmodels.LedgerFinding
	tasks    []commonmodels.LedgerTask
	coverage commonmodels.LedgerCoverage
	// convergence is the per-run share-of-new history. Read because it is the
	// measurement of whether this investigation is still finding anything, and
	// it steers both how much prior context the run is given and what the
	// prompt tells it to do about saturation.
	convergence []commonmodels.ConvergencePoint
	// descChars is the description width the findings are rendered at, chosen
	// with them by the budget: the two are one decision and must not be made
	// in two places.
	descChars int
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

	lrc := &ledgerReadContext{}

	// Read before the findings are budgeted, not after: the convergence history
	// is an input to how much of them this run is shown.
	if o.ledgerRepo != nil {
		if ledger, err := o.ledgerRepo.Get(ctx, o.projectID); err == nil && ledger != nil {
			lrc.coverage = ledger.Coverage
			lrc.convergence = ledger.Convergence
		}
	}

	// Rank (severity × recency × liked × recurrence × status). Resolved/refuted
	// findings are done; drop them from the block the run builds on.
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

	// How many of them fit is a question about this run's models, not a
	// constant. It was 25 — on a project carrying 420 findings that is 6% of
	// what is known, and the run re-reported most of the rest as new.
	ratio, haveRatio := latestMarginalRatio(lrc.convergence)
	budget := o.priorContextBudget(ratio, haveRatio)
	lrc.findings, lrc.descChars = contextbudget.FitFindings(
		active, budget, contextbudget.MinFindingDescChars, contextbudget.MaxFindingDescChars)

	// The ids this run is being shown, so the per-area retrieval can spend its
	// own budget on findings this block does not already carry.
	o.baseContextFindingIDs = make([]string, 0, len(lrc.findings))
	for i := range lrc.findings {
		o.baseContextFindingIDs = append(o.baseContextFindingIDs, lrc.findings[i].ID)
	}

	// Logged because it is the only place the decision is visible afterwards:
	// a prompt is not persisted, so "the run was shown 12 of 420 findings" is
	// otherwise indistinguishable from "the project has 12 findings".
	applog.WithFields(applog.Fields{
		"project_id":      o.projectID,
		"candidates":      len(active),
		"total_findings":  len(all),
		"shown":           len(lrc.findings),
		"budget_tokens":   budget,
		"available_input": o.baseContextAvailableInput(),
		"desc_chars":      lrc.descChars,
		"marginal_ratio":  ratio,
		"have_ratio":      haveRatio,
	}).Info("Discovery Ledger: prior-context budget")

	// Open tasks — governed by evolution mode (off ⇒ no self-directed threads).
	pol, _ := agentplugin.ResolveDiscoveryPolicy(ctx, o.projectID)
	if pol.EvolutionMode != agentplugin.EvolutionModeOff && o.taskRepo != nil {
		if tasks, err := o.taskRepo.List(ctx, o.projectID, commonmodels.LedgerTaskStatusOpen); err == nil {
			lrc.tasks = tasks
			// The threads this run is actually being shown, remembered for the
			// reflection phase to record as pursued. Reading the queue again
			// at the end of the run would be reading a DIFFERENT queue: the
			// previous run's reflection can close one of these and open
			// another while this one is exploring, and the run would then be
			// recorded as having pursued a thread it was never told about and
			// not the one it was. Capped here the way the prompt renderer
			// caps it, because beyond the cap the run never saw them.
			shown := tasks
			if len(shown) > maxLedgerTasksInPrompt {
				shown = shown[:maxLedgerTasksInPrompt]
			}
			o.shownTaskIDs = make([]string, 0, len(shown))
			for i := range shown {
				o.shownTaskIDs = append(o.shownTaskIDs, shown[i].ID)
			}
		}
	}

	// TotalCatalogItems counts because it is recorded even when the reflection
	// LLM produced nothing, precisely so the next run can be told the cube is
	// not exhausted. Leaving it out of this check would discard the ledger in
	// exactly that case and make renderCoverage's cube-total branch
	// unreachable — the write would exist for a read that never happens.
	lrc.hasLedger = len(lrc.findings) > 0 || len(lrc.tasks) > 0 ||
		strings.TrimSpace(lrc.coverage.Summary) != "" || len(lrc.coverage.ExploredTables) > 0 ||
		len(lrc.coverage.ExploredCatalogItems) > 0 || lrc.coverage.TotalCatalogItems > 0
	if !lrc.hasLedger {
		return nil
	}
	return lrc
}

// renderCoverage renders the coverage-map block: explored vs. frontier + the
// reflection phase's natural-language summary.
//
// The table clause is the one this block has always carried and is unchanged.
// The cube clause exists because the table clause, alone, is a false statement
// on a project that has a cube: "0 still on the frontier" is derived from a
// count of tables, and a cube contributes none — so the run that just explored
// one reads back as having nothing left to look at, every time, cumulatively.
// The clause is deliberately not a second fraction. A cube's slices are
// combinatorial, so a ratio over its catalog would trade one wrong completion
// signal for another.
func renderCoverage(cov commonmodels.LedgerCoverage) string {
	summary := strings.TrimSpace(cov.Summary)
	explored := len(cov.ExploredTables)
	if summary == "" && explored == 0 && cov.TotalCatalogItems == 0 && len(cov.ExploredCatalogItems) == 0 {
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
	if items := len(cov.ExploredCatalogItems); cov.TotalCatalogItems > 0 || items > 0 {
		fmt.Fprintf(&sb, "That count is tables only. This project also has cube-shaped datasources, which have none: %s. A cube has no frontier to tile — its slices are combinatorial — so it is never finished and never absent from the frontier, whatever the table count above says. ",
			renderCubeExplored(items, cov.TotalCatalogItems))
	}
	if summary != "" {
		sb.WriteString(summary)
	}
	sb.WriteString("\n\n")
	return sb.String()
}

// renderCubeExplored states what has been sliced on the cube side, as a record
// rather than as progress toward a total.
func renderCubeExplored(explored, total int) string {
	switch {
	case explored == 0 && total > 0:
		return fmt.Sprintf("none of their %d metrics and dimensions are recorded as queried yet", total)
	case explored == 0:
		return "nothing is recorded as queried on them yet"
	case total > 0:
		return fmt.Sprintf("%d of their %d metrics and dimensions have been queried so far", explored, total)
	default:
		return fmt.Sprintf("%d of their metrics and dimensions have been queried so far", explored)
	}
}

// renderLedgerFindings renders the ranked findings-with-substance block. Input
// is already ranked, and already selected and sized by the loader's budget —
// descChars is the width that budget chose, not a second decision.
func renderLedgerFindings(findings []commonmodels.LedgerFinding, descChars int) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("### Key findings so far (build on these — drill deeper or check whether they changed)\n\n")
	for _, f := range findings {
		sb.WriteString(contextbudget.FindingLine(f, descChars))
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
	// Each thread carries its id so this phase's own narrative can reference
	// one — the reflection prompt has always listed them with ids, and without
	// them here the exploration phase could pursue a thread but not name it.
	for _, t := range tasks {
		fmt.Fprintf(&sb, "- [%s] %s\n", t.ID, strings.TrimSpace(t.Text))
	}
	sb.WriteString("\n")
	return sb.String()
}
