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
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// ledgerReadContext is the compounding-discovery read context assembled at run
// start: the accumulated findings (ranked, with substance, and budgeted against
// the model window), the coverage map, the convergence history, and the open
// next-task queue. hasLedger is false for legacy projects / builds with nothing
// in the ledger, so the caller falls back to the older capped insight dump.
type ledgerReadContext struct {
	// candidates are every active finding, ranked, before a budget has chosen
	// which of them fit. Held because the choosing happens in a second step:
	// how many fit depends on what the rest of the prompt costs, and part of
	// that cost is the rest of this very block.
	candidates []commonmodels.LedgerFinding
	findings   []commonmodels.LedgerFinding
	tasks      []commonmodels.LedgerTask
	coverage   commonmodels.LedgerCoverage
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
	// totalFindings is the ledger's size including the retired ones, for the
	// log line that is the only lasting record of what the run was shown.
	totalFindings int
}

// fitPriorFindings chooses which of the ranked candidates this run is shown,
// and how fully each is rendered.
//
// Separate from the read because how many fit is a question about this run's
// models and this run's prompt, not a constant — it was 25, which on a project
// carrying 420 findings is 6% of what is known, and the run re-reported most of
// the rest as new. fixedPromptTokens is everything else the prompt will carry,
// including the other sections of this same block: five consecutive review
// findings on this change were a piece of that prompt nothing was counting, and
// the last of them was this block's own open-threads queue.
func (o *Orchestrator) fitPriorFindings(lrc *ledgerReadContext, fixedPromptTokens, room int) {
	if lrc == nil {
		return
	}
	novelty, haveNovelty := latestRunNovelty(lrc.convergence)
	available := o.baseContextAvailableInput()
	budget := priorContextBudget(available, room, novelty, haveNovelty)
	// The allowance is for the whole block; the fit is over its lines.
	lines := budget - ledgerBlockOverhead()
	if lines < 0 {
		lines = 0
	}
	lrc.findings, lrc.descChars = contextbudget.FitFindings(
		lrc.candidates, lines, contextbudget.MinFindingDescChars, contextbudget.MaxFindingDescChars)

	// Logged because it is the only place the decision is visible afterwards:
	// a prompt is not persisted, so "the run was shown 12 of 420 findings" is
	// otherwise indistinguishable from "the project has 12 findings".
	applog.WithFields(applog.Fields{
		"project_id":      o.projectID,
		"candidates":      len(lrc.candidates),
		"total_findings":  lrc.totalFindings,
		"shown":           len(lrc.findings),
		"budget_tokens":   budget,
		"available_input": available,
		"fixed_prompt":    fixedPromptTokens,
		"desc_chars":      lrc.descChars,
		"run_novelty":     novelty,
		"have_novelty":    haveNovelty,
	}).Info("Discovery Ledger: prior-context budget")
}

// previousContextToken is the slot a base-context template offers the
// prior-knowledge block. A project may supply its own base_context, and one
// without this token renders no prior findings at all.
const previousContextToken = "{{PREVIOUS_CONTEXT}}"

// priorFindingIDs is the ids a RENDERED prompt actually carries, which is what
// per-area retrieval is told to skip so it can spend its own budget elsewhere.
//
// Derived from the template rather than from the fit, because the two can
// disagree: buildBaseContext substitutes previousContextToken, so a project
// whose own base_context omits it gets a prompt with no findings in it — and
// excluding findings from retrieval on behalf of a prompt that does not contain
// them hides prior knowledge the model never saw, which is the one failure
// worse than retrieving something twice. Exactly the mistake refitAgainst
// guards against when a phase resizes the block; this is the same rule at the
// other end.
func priorFindingIDs(template string, findings []commonmodels.LedgerFinding) []string {
	if !strings.Contains(template, previousContextToken) {
		return nil
	}
	ids := make([]string, 0, len(findings))
	for i := range findings {
		ids = append(ids, findings[i].ID)
	}
	return ids
}

// loadLedgerReadContext reads the project's ledger and builds the read context,
// up to but NOT including the choice of which findings fit — that is
// fitPriorFindings, and it runs once the rest of the prompt, this block's own
// other sections included, has been rendered and measured.
//
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
	lrc.candidates = active
	lrc.totalFindings = len(all)
	lrc.descChars = contextbudget.MinFindingDescChars

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
	//
	// Counted over the CANDIDATES, not over what the budget chose to show. The
	// two differ exactly when the budget carried nothing — an operator share of
	// zero, or a window with no room — and in that case the project still has a
	// ledger, so the caller must not fall through to the pre-ledger insight
	// dump. Reading it off the selection made the documented opt-out inject an
	// unbudgeted block instead of nothing, and handed the smallest models the
	// very list the budget exists to keep out of them.
	lrc.hasLedger = len(active) > 0 || len(lrc.tasks) > 0 ||
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

// noRestateInstruction is the contract that comes with the established-findings
// list: these are already recorded and already in front of the user, so
// reporting one again is not a finding.
//
// It is rendered WITH the list rather than appended to the static discipline
// rules, and that placement is the point. The discipline blocks are
// unconditional, and a rule that points at a section the prompt may not carry
// invites the model to satisfy it against whatever it can see instead — the
// same reason the reflection prompt renders its re-judgement demand only when
// there are priors to re-judge. Here the rule and the thing it governs cannot
// be separated: no findings, no instruction.
//
// The wording has to permit the two things a run SHOULD do with a known
// finding — report a magnitude that moved, and drill past it — or it would
// suppress the trend reporting the ledger exists to produce. The heading it
// replaced did the opposite: "build on these — drill deeper or check whether
// they changed" invites re-examination and says nothing about the result of a
// re-examination that found no change, so re-reporting was compliant.
const noRestateInstruction = "These findings are ALREADY RECORDED in this project's ledger and are already in front of the user. " +
	"Do NOT emit an insight that restates one of them — a restatement is not a finding, however differently it is worded or whatever new figure it carries. " +
	"If you re-examine one and its magnitude has MOVED, report the change and say which finding it updates. " +
	"If you re-examine one and it is unchanged, report nothing about it. " +
	"If you can go deeper than one — a cause, a segment, a mechanism it does not already name — that is a new finding and is what this run is for.\n" +
	"A `confirmed` finding seen several times is settled: spend the run elsewhere. " +
	"`monitoring` and `changed` findings are the ones worth re-checking.\n"

// renderLedgerFindings renders the ranked findings-with-substance block. Input
// is already ranked, and already selected and sized by the loader's budget —
// descChars is the width that budget chose, not a second decision.
// ledgerFindingsHeading introduces the block. Named because its cost has to be
// subtracted from the block's allowance before the lines are fitted.
const ledgerFindingsHeading = "### Already established findings — do NOT re-report these\n"

// ledgerBlockOverhead is what the block costs besides its finding lines: this
// heading, the no-restate instruction, and the blank lines around them.
//
// Subtracted from the allowance before the lines are fitted, because
// contextbudget.FitFindings is given a budget for LINES. Handing it the whole
// block allowance overran that allowance by exactly this much — the same error
// as pricing a line without the newline that follows it, one level up.
func ledgerBlockOverhead() int {
	return contextbudget.Tokens(ledgerFindingsHeading + noRestateInstruction + "\n\n")
}

func renderLedgerFindings(findings []commonmodels.LedgerFinding, descChars int) string {
	if len(findings) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(ledgerFindingsHeading)
	sb.WriteString(noRestateInstruction)
	sb.WriteString("\n")
	for _, f := range findings {
		sb.WriteString(contextbudget.FindingLine(f, descChars))
		sb.WriteByte('\n')
	}
	sb.WriteString("\n")
	return sb.String()
}

// saturatedNovelty is where the guidance flips from "keep walking this ground"
// to "find new ground": below half, the MAJORITY of what the last run reported
// was already in the ledger. A boundary at the majority is the one place on
// this axis that is not an arbitrary pick, which is why it is here and not in
// an env var — it is a property of the sentence, not of a deployment.
const saturatedNovelty = 0.5

// maxAreasPerSaturationLine caps each of the two area lists so a project with
// a large playbook cannot turn a steering note into a wall of area ids.
const maxAreasPerSaturationLine = 12

// renderSaturation narrates what the ledger already measures about whether this
// investigation is still finding anything, and what to do about it.
//
// Every number here is read, not computed: the per-run novelty and growth
// figures from the convergence history, and Coverage.AreaDepth's count of runs
// that produced findings per area. All of it has been recorded every run since
// the ledger existed and none of it reached a prompt or a decision — the system
// measured the repetition it was producing and told nobody.
//
// The two ratios say different things and are rendered as different sentences,
// which is the whole reason this function is careful. RunNovelty is the share of
// a run's OWN output that was new — the repetition rate, and the only one of the
// two that can honestly be stated as "already known". MarginalRatio divides by
// the whole ledger, so it is a growth rate: a run that adds ten entirely new
// findings to a hundred-finding ledger scores 0.09 there having repeated
// nothing. Printing that as "91% already known" would tell the model something
// false about its own project.
//
// Empty for a project with nothing measured yet, so an early run reads no
// steering at all rather than steering derived from one data point.
func renderSaturation(history []commonmodels.ConvergencePoint, areaDepth map[string]int, areas []AnalysisArea) string {
	novelty, haveNovelty := latestRunNovelty(history)
	growth := renderLedgerGrowth(history)
	deep, barren := areasByDepth(areaDepth, areas)
	if !haveNovelty && growth == "" && len(deep) == 0 && len(barren) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### How much this investigation is still finding\n")

	if haveNovelty {
		fmt.Fprintf(&sb, "Of what the last run reported, **%.0f%% was already in this ledger**. ", (1-novelty)*100)
		if novelty < saturatedNovelty {
			sb.WriteString("Most of what it said, this project already had. Re-walking the same ground produces restatements, not findings: prefer territory the coverage map above shows as untouched, or a different angle on covered ground — a different grain, a segment you have not cut by, a join you have not tried — over re-examining what is listed below.\n")
		} else {
			sb.WriteString("This investigation is still productive. Keep tiling the frontier and drilling the seams that are paying off.\n")
		}
	}
	if growth != "" {
		sb.WriteString(growth)
	}

	if len(deep) > 0 {
		fmt.Fprintf(&sb, "Areas that have produced findings in several runs (diminishing returns): %s.\n", strings.Join(deep, ", "))
	}
	if len(barren) > 0 {
		fmt.Fprintf(&sb, "Areas with no findings recorded yet (open ground): %s.\n", strings.Join(barren, ", "))
	}
	sb.WriteString("\n")
	return sb.String()
}

// renderLedgerGrowth states how fast the ledger itself has been growing, which
// is what MarginalRatio measures — named and framed as growth, never as a
// repetition rate.
//
// Still worth telling the model: a ledger that has stopped growing is a tiled
// warehouse, whoever is or is not repeating themselves. Runs that recorded
// nothing are left out for the same reason latestRunNovelty skips unmeasured
// points — their zero is a division by zero, and printing it would show a
// collapse that never happened.
func renderLedgerGrowth(history []commonmodels.ConvergencePoint) string {
	measured := make([]commonmodels.ConvergencePoint, 0, len(history))
	for _, p := range history {
		if p.TotalFindings > 0 {
			measured = append(measured, p)
		}
	}
	if len(measured) < 2 {
		return ""
	}
	if len(measured) > maxMarginalTrendPoints {
		measured = measured[len(measured)-maxMarginalTrendPoints:]
	}
	shares := make([]string, 0, len(measured))
	for _, p := range measured {
		shares = append(shares, fmt.Sprintf("%.0f%%", p.MarginalRatio*100))
	}
	return "Each recent run's new findings as a share of everything the ledger then held, oldest first: " +
		strings.Join(shares, " → ") + ". A falling share means the ledger is still growing but more slowly.\n"
}

// maxMarginalTrendPoints is how many runs the growth line names. Enough to show
// a direction; a project carries up to fifty convergence points and all of them
// would be a table, not a sentence.
const maxMarginalTrendPoints = 6

// areasByDepth splits this run's analysis areas into the ones earlier runs have
// repeatedly mined and the ones that have never produced a finding.
//
// Only areas this run actually has are named, because an area id from a
// playbook that has since changed is not something the run can act on; and area
// NAMES are used where they exist, because the ids are slugs and the rest of
// the prompt talks about areas by name.
func areasByDepth(areaDepth map[string]int, areas []AnalysisArea) (deep, barren []string) {
	// Nothing recorded means every area reads as open ground, which is a
	// sentence that distinguishes nothing. The lists are a contrast, so they
	// need something to contrast against — and a ledger CAN reach here with no
	// depth map: coverage is written even on a run whose reflection call failed
	// before it judged any area.
	if len(areas) == 0 || len(areaDepth) == 0 {
		return nil, nil
	}
	// The boundary for "several runs". Two is not a trend and four is a rule
	// nobody can satisfy on a young project; three runs having each produced a
	// finding in one area is the point at which the next one is likely to
	// restate rather than extend.
	const deepAfterRuns = 3
	for _, a := range areas {
		label := a.Name
		if strings.TrimSpace(label) == "" {
			label = a.ID
		}
		if strings.TrimSpace(label) == "" {
			continue
		}
		switch depth := areaDepth[a.ID]; {
		case depth >= deepAfterRuns:
			if len(deep) < maxAreasPerSaturationLine {
				deep = append(deep, fmt.Sprintf("%s (%d runs)", label, depth))
			}
		case depth == 0:
			if len(barren) < maxAreasPerSaturationLine {
				barren = append(barren, label)
			}
		}
	}
	return deep, barren
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

// --- what THIS run has already found (enterprise#542) ---

// runFindingsSoFar is the rendered block naming what earlier analysis areas of
// the SAME run already reported.
//
// A named type rather than a seventh string parameter on
// buildAnalysisAreaPrompt, which already takes five strings: the compiler
// cannot catch two of them being swapped, and the measurement paths have to
// carry this one too or they measure a prompt the model does not receive. That
// mistake accounted for five consecutive review findings on this change, so the
// type is the cheap guard against making it again.
type runFindingsSoFar string

// maxRunFindingsInAreaPrompt caps the lines, like maxLedgerTasksInPrompt and
// maxHistoricalPatternsInPrompt next to it: a property of a readable prompt
// rather than of a deployment. A run's whole output is the natural bound — ten
// areas of eight insights is eighty — and the budget below is what actually
// keeps it inside the window.
const maxRunFindingsInAreaPrompt = 100

// runFindingsSoFarShare is the share of an area's available input the block may
// occupy. A share rather than a token count, for the reason the rest of this
// change is about: a constant is simultaneously trivial on a large window and
// material on a small one, and on an 8K model a fixed few thousand tokens would
// be a third of everything available.
//
// Small, because the block is names and figures only and is bounded by the
// run's own output — ten areas of eight insights is eighty lines, which is
// about 1.4K tokens. The share is what stops a pathological run spending an
// area's evidence budget on its own table of contents.
const runFindingsSoFarShare = 0.05

// runFindingsSoFarHeading introduces the block.
const runFindingsSoFarHeading = "### Already found by other areas in THIS run — do NOT report these again\n"

// renderRunFindingsSoFar names what the areas before this one reported, so this
// area can go somewhere else.
//
// It exists because {{PREVIOUS_CONTEXT}} cannot answer this. That block is
// composed once at run start, before any area has run, and carries what EARLIER
// RUNS established — so an area has never had any way to know what a sibling
// area of its own run just said. Measured on a real project, every single
// duplicate insight within a run crossed an area boundary, and roughly half of
// each run's output restated another area of the same run.
//
// Names, areas and figures only. Recognition is the whole job: the model needs
// to see that a subject is taken, not to re-read the finding.
func renderRunFindingsSoFar(insights []models.Insight, availableInput int) runFindingsSoFar {
	return renderRunFindingsSoFarWithin(insights, contextbudget.Share(availableInput, runFindingsSoFarShare))
}

// runFindingsSoFarOverhead is the heading and the instruction, which the block
// pays before its first line. Counted for the reason ledgerBlockOverhead is:
// fitting the lines to the whole allowance renders a block larger than the
// allowance, and in the refit below that difference is the overflow itself.
func runFindingsSoFarOverhead() int {
	return contextbudget.Tokens(runFindingsSoFarHeading + runFindingsSoFarInstruction + "\n\n")
}

// renderRunFindingsSoFarWithin renders the block to an ABSOLUTE token budget,
// heading and instruction included.
//
// Separate from the share-of-window form because the block is sized twice: once
// before the evidence exists, as a share, and again against the prompt as it
// really stands — see refitRunFindingsSoFar.
func renderRunFindingsSoFarWithin(insights []models.Insight, budget int) runFindingsSoFar {
	if len(insights) == 0 {
		return ""
	}
	budget -= runFindingsSoFarOverhead()
	if budget <= 0 {
		// A window with nothing spare carries nothing, the same answer the
		// prior-knowledge block gives. Said once, here, rather than left for
		// the overflow retry to discover.
		return ""
	}
	if len(insights) > maxRunFindingsInAreaPrompt {
		// The most recent, which are the ones the areas nearest this one
		// produced and so the likeliest to be re-trodden.
		insights = insights[len(insights)-maxRunFindingsInAreaPrompt:]
	}
	lines := make([]string, 0, len(insights))
	for _, in := range insights {
		area := in.AnalysisArea
		if area == "" {
			area = "general"
		}
		line := fmt.Sprintf("- [%s] **%s**", area, strings.TrimSpace(in.Name))
		if in.AffectedCount > 0 {
			line += fmt.Sprintf(" (affected %d)", in.AffectedCount)
		}
		lines = append(lines, line+"\n")
	}
	n := contextbudget.FitLines(lines, budget)
	if n == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(runFindingsSoFarHeading)
	sb.WriteString(runFindingsSoFarInstruction)
	sb.WriteString("\n")
	for _, l := range lines[:n] {
		sb.WriteString(l)
	}
	sb.WriteString("\n")
	return runFindingsSoFar(sb.String())
}

// runFindingsSoFarInstruction is deliberately the same contract as
// noRestateInstruction, one axis along. A model told two different things about
// two kinds of repetition will apply neither consistently; the cross-run
// wording is also the one that has been measured to work, so this is that
// wording with its subject changed rather than a new attempt at it.
const runFindingsSoFarInstruction = "These were reported MINUTES AGO by other analysis areas of this same run, and are already recorded for it. " +
	"Do NOT emit an insight that restates one — a restatement is not a finding, however differently it is worded or whatever figure it carries. " +
	"If your evidence gives a DIFFERENT magnitude for the same thing, report that and say which one it updates. " +
	"If it gives the same thing, report nothing about it. " +
	"If you can go deeper than one — a cause, a segment, a mechanism it does not already name — that is a new finding and is what this area is for.\n" +
	"Several of these may be about subjects close to your own area: the overlap is expected, and leaving a subject to the area that already covered it is the correct answer, not a gap in your own.\n"
