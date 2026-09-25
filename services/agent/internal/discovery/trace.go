package discovery

import (
	"os"
	"strconv"
	"strings"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Evidence tracing. Off unless DISCOVERY_TRACE is truthy.
//
// A false claim in a shipped insight is a statement about a chain: a query ran,
// some rows came back, a digest of those rows went into a prompt, a sentence came
// out, and something either checked it or did not. Every link of that chain is
// already persisted -- ExplorationStep carries the SQL and the rows,
// AnalysisStep carries the prompt and the response, the insight carries its
// declared claims and their verdicts. What is missing is the ability to read the
// chain as it happens, in one place, without reconstructing it from four Mongo
// collections afterwards.
//
// These events are that reading. They add no behaviour: nothing branches on
// them, nothing is stored, and with the env unset not one of them is built. The
// one non-obvious event is `exposure` -- how much of a step's result the
// analysis prompt actually showed, which is the difference between an insight
// that counted a population and one that counted a window onto it. That number
// exists nowhere else, because the digest is rendered into a prompt string and
// the ratio it represents is never computed.
//
// Kept in its own file so it can be removed in one delete.

const traceEnv = "DISCOVERY_TRACE"

// traceOn is resolved once. A run cannot start tracing halfway through, and
// re-reading the environment per step would put a syscall in a hot loop.
var traceOn = traceEnabled(os.Getenv(traceEnv))

// traceEnabled reads the gate. Anything unparseable is off, including the empty
// string, so an operator who has never heard of this variable gets a run with no
// trace output and no extra work done. Split out from the var so the default can
// be asserted rather than assumed.
func traceEnabled(v string) bool {
	on, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && on
}

// oneLine flattens SQL for a log field. Newlines in a structured log value are
// legal but make the line unreadable in a terminal, and these get grepped.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// traceExplorationStep records one executed exploration step: what was asked,
// how many rows came back, and whether the source said anything about their
// fidelity. Logged after exploration completes rather than from the engine's
// OnStep callback, because Quality and CompactResult are only attached to the
// finished step and those are the two fields that explain a later false claim.
func traceExplorationStep(s models.ExplorationStep) {
	if !traceOn {
		return
	}
	f := applog.Fields{
		"trace":     "query",
		"step":      s.Step,
		"action":    s.Action,
		"rows":      s.RowCount,
		"exec_ms":   s.ExecutionTimeMs,
		"purpose":   clip(s.QueryPurpose, 200),
		"sql":       clip(oneLine(s.EffectiveQuery()), 1200),
		"fixed":     s.Fixed,
		"fix_tries": s.FixAttempts,
	}
	// The proposal, only when it differs from what ran. A trace that showed one
	// SQL string could not distinguish "the model wrote this" from "this
	// answered", which is the distinction a false figure turns on.
	if s.QueryExecuted != "" {
		f["sql_proposed"] = clip(oneLine(s.Query), 1200)
	}
	if s.Error != "" {
		f["error"] = clip(oneLine(s.Error), 300)
	}
	if len(s.Quality) > 0 {
		var cav []string
		for _, c := range s.Quality {
			cav = append(cav, c.String())
		}
		f["quality_caveats"] = cav
	}
	if s.CompactResult != nil {
		f["digest_shows"] = digestRowsShown(s.CompactResult)
		f["digest_inline"] = len(s.CompactResult.AllRows) > 0
	}
	applog.WithFields(f).Info("trace: exploration query")
}

// digestRowsShown is how many of a step's rows the digest reproduces verbatim:
// every row when the result was small enough to inline, otherwise the head and
// tail windows. The remainder reaches the model only as per-column statistics.
func digestRowsShown(c *gomodels.CompactResult) int {
	if c == nil {
		return 0
	}
	if n := len(c.AllRows); n > 0 {
		return n
	}
	return len(c.HeadRows) + len(c.TailRows)
}

// traceExposure records, for one step feeding one analysis area, how much of
// that step's result the prompt actually showed.
//
// This is the measurement that matters for a population claim. A step that
// returned 150 rows and shows 20 of them can support "the top 20 are ..." and
// cannot support "there are 20 of them", and the digest reads the same either
// way. shown < rows is not a defect -- it is the compaction working as designed
// -- but it is the precondition for the defect, so it is worth being able to
// list before reading a single insight.
func traceExposure(areaID string, steps []models.ExplorationStep) {
	if !traceOn {
		return
	}
	for _, s := range steps {
		if s.Action != "query_data" || s.CompactResult == nil {
			continue
		}
		shown := digestRowsShown(s.CompactResult)
		f := applog.Fields{
			"trace":   "exposure",
			"area":    areaID,
			"step":    s.Step,
			"rows":    s.RowCount,
			"shown":   shown,
			"full":    shown >= s.RowCount,
			"purpose": clip(s.QueryPurpose, 160),
		}
		if len(s.Quality) > 0 {
			var cav []string
			for _, c := range s.Quality {
				cav = append(cav, string(c.Kind))
			}
			f["quality_caveats"] = cav
		}
		applog.WithFields(f).Info("trace: analysis evidence exposure")
	}
}

// traceClaims records every quantifier claim an insight declared and what Go
// concluded about it. attachQuantifierVerdicts already logs the failures at
// Warn; this logs the whole set, because the interesting ratio is how many
// claims were declared at all against how many sentences the insight makes.
func traceClaims(areaID string, ins models.Insight) {
	if !traceOn {
		return
	}
	for i, c := range ins.QuantifierClaims {
		f := applog.Fields{
			"trace":   "claim",
			"area":    areaID,
			"insight": clip(ins.Name, 120),
			"idx":     i,
			"kind":    c.Kind,
			"step":    c.Step,
			"subject": clip(c.Subject, 160),
			"scope":   clip(c.Scope, 160),
			"claim":   clip(c.Claim, 240),
			"verdict": "not-evaluated",
		}
		if i < len(ins.QuantifierVerdicts) {
			v := ins.QuantifierVerdicts[i]
			f["verdict"] = v.Status
			f["reason"] = clip(v.Reason, 300)
		}
		applog.WithFields(f).Info("trace: declared quantifier claim")
	}
}

// traceInsight records one shipped insight's provenance in a single line: the
// steps it cited, how many claims it declared, what happened to them, and
// whether a repair rewrote it. This is the row a reader starts from when a
// sentence turns out to be false -- it names the steps whose SQL to re-run.
func traceInsight(areaID string, ins models.Insight) {
	if !traceOn {
		return
	}
	var holds, fails, undecidable int
	for _, v := range ins.QuantifierVerdicts {
		switch v.Status {
		case QuantifierHolds:
			holds++
		case QuantifierFails:
			fails++
		default:
			undecidable++
		}
	}
	f := applog.Fields{
		"trace":        "insight",
		"area":         areaID,
		"id":           ins.ID,
		"name":         clip(ins.Name, 200),
		"source_steps": ins.SourceSteps,
		"severity":     ins.Severity,
		"confidence":   ins.Confidence,
		"claims":       len(ins.QuantifierClaims),
		"holds":        holds,
		"fails":        fails,
		"undecidable":  undecidable,
		"indicators":   len(ins.Indicators),
	}
	if len(ins.Quality) > 0 {
		var cav []string
		for _, c := range ins.Quality {
			cav = append(cav, string(c.Kind))
		}
		f["quality_caveats"] = cav
	}
	if ins.Repair != nil {
		f["repair_outcome"] = ins.Repair.Outcome
		f["repair_rounds"] = ins.Repair.Rounds
		f["repair_fixed"] = ins.Repair.Fixed
		f["repair_dropped"] = ins.Repair.Dropped
		f["repair_unrepaired"] = ins.Repair.Unrepaired
	}
	applog.WithFields(f).Info("trace: shipped insight")
}
