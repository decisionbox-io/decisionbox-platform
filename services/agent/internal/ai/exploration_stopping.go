package ai

// When exploration is allowed to stop.
//
// A reasoning model left to its own judgement declares completion early, so
// the engine has always rejected a "done" signal below a fixed step floor.
// That floor is a proxy: on a warehouse, one step is roughly one table or one
// join, so counting steps approximates counting coverage.
//
// The proxy does not survive a cube. There are no tables to work through —
// a query is a choice of metrics and dimensions, and the number of choices is
// combinatorial, so sixty trivial slices and fifteen revealing ones reach any
// given count equally well. Raising the floor buys nothing but quota spend
// against a source metered per request; lowering it restores the early
// termination the floor exists to prevent. The number is not the problem, and
// no value of it is right.
//
// So on a run that can query a cube, the floor is replaced by what it was
// standing in for: whether the exploration is still turning up anything new.
// A step's novelty is its distance from the steps already taken, which the
// run-scoped step index can answer because it is already embedding every step
// for the analysis phase. While recent steps are still novel, a "done" signal
// is rejected exactly as the floor rejected it; once a run of them says the
// same thing as earlier work, the exploration has finished whether or not it
// has taken a particular number of steps.
//
// The maximum step count is untouched and remains the runaway cap.

import (
	"context"
	"fmt"
	"strings"

	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

const (
	// repeatSimilarityThreshold is the cosine score above which a step is
	// treated as saying what an earlier step already said.
	//
	// Steps are embedded as their purpose plus their query text, so two
	// slices of one cube differing only in the dimension broken out score
	// far higher against each other than either does against a genuinely
	// different question. 0.92 sits above that band: it is not "about the
	// same subject", it is "very nearly the same request".
	repeatSimilarityThreshold = 0.92

	// repeatsBeforeDone is how many consecutive repetitive steps end the
	// exploration.
	//
	// One is too eager — a model often re-asks a question a slightly better
	// way immediately after asking it badly, and that second step is where
	// the answer comes from. Three consecutive means the model has stopped
	// finding new ground rather than paused on one, and it costs at most two
	// extra steps to establish.
	//
	// It is also the least evidence the rule will conclude anything from: a
	// streak of three cannot exist before three steps have been judged, so a
	// completion signalled earlier than that is refused for want of evidence
	// rather than accepted for want of a floor.
	repeatsBeforeDone = 3

	// degradedStepsBeforeFallback is how many steps IN A ROW of a failing
	// index the rule tolerates before handing the decision back to the step
	// floor. It counts two independent signals of the same thing: steps whose
	// novelty could not be read, and steps the index refused to store.
	//
	// Consecutive, not cumulative. Measurability is a question about the
	// index's health NOW: a run that judged a step an hour ago and cannot
	// read the index any more must fall back, and one that hit a single
	// transient failure must not. A cumulative counter answers neither —
	// it only ever describes history, and history is not what the next
	// decision depends on.
	//
	// Without the fallback at all, a degraded vector store turns every run
	// into a maximum-length one against a source metered per request.
	degradedStepsBeforeFallback = 3
)

// stopRule decides whether a "done" signal from the model is accepted.
//
// The zero value is the step floor with no floor — accept immediately —
// which is what a run configured with MinSteps 0 has always done.
type stopRule struct {
	// minSteps is the floor, used when novelty cannot decide.
	minSteps int

	// byNovelty switches the run to the marginal-value rule. Set for a run
	// that can query a cube-shaped datasource.
	byNovelty bool

	// consecutiveRepeats counts executed steps in a row that repeated
	// earlier work. Reset by any step that was novel, or that could not be
	// judged.
	consecutiveRepeats int

	// judged counts steps this rule was able to assess. It separates "this
	// run is too young to have an opinion" from "this run is still finding
	// things" — two refusals that describe opposite runs.
	judged int

	// consecutiveUnjudged counts steps in a row novelty could not be
	// measured on. Reset by any successful judgement, because it stands for
	// the index's health now rather than for anything that happened earlier.
	consecutiveUnjudged int
}

// observe records what the run learned from one executed step.
//
// repeated is whether the step said what an earlier step already said;
// judgeable is false only when novelty could not be MEASURED — no index, an
// index that failed, or a query that failed and so returned nothing to judge.
// An unjudgeable step is not evidence either way and clears the streak rather
// than extending it: three silent failures must not read as three repetitions.
//
// A step with nothing to compare against is judged, not unjudgeable: it broke
// new ground by definition. That distinction is load-bearing — neighbours are
// scoped per datasource, so first queries have no neighbour routinely.
func (s *stopRule) observe(repeated, judgeable bool) {
	if !judgeable {
		s.consecutiveUnjudged++
		s.consecutiveRepeats = 0
		return
	}
	s.consecutiveUnjudged = 0
	s.judged++
	if repeated {
		s.consecutiveRepeats++
		return
	}
	s.consecutiveRepeats = 0
}

// acceptDone reports whether a completion signalled at this step stands, and
// when it does not, why not — the reason shapes the nudge sent back.
//
// step is the 1-based number of the step the signal arrived on.
//
// canMeasure is the caller's answer to "is the index in a state where novelty
// means anything" — a question about the machinery, which the rule cannot see.
// False hands the decision to the floor, which is what makes a run with a
// degraded vector store behave as it did before this rule existed rather than
// rejecting every completion until the runaway cap.
func (s *stopRule) acceptDone(step int, canMeasure bool) (bool, string) {
	if s.byNovelty && canMeasure && s.consecutiveUnjudged < degradedStepsBeforeFallback {
		// The run has stopped finding new ground. A streak that long cannot
		// exist without that many judged steps, so this subsumes the
		// evidence check below.
		if s.consecutiveRepeats >= repeatsBeforeDone {
			return true, ""
		}
		// Otherwise the run has not shown that there is nothing left. Both
		// refusals are deliberately NOT deferred to the floor: the floor
		// defaults to zero, so deferring would accept a completion signalled
		// on step one — the early termination the floor was added to prevent,
		// reintroduced by the rule meant to replace it.
		//
		// The two are recorded apart because they describe opposite runs, and
		// an operator reading a run needs to know which happened. Neither is
		// "you are repeating yourself": a run that repeats itself is accepted,
		// so saying so in a refusal is always false.
		if s.judged < repeatsBeforeDone {
			return false, "unproven"
		}
		return false, "productive"
	}
	if step < s.minSteps {
		return false, "floor"
	}
	return true, ""
}

// noveltySubject reports whether a step is the kind this rule can judge: one
// that actually asked the data a question.
//
// A lookup_schema or search_tables step is not. Those stamp a CONSTANT
// purpose ("lookup_schema", "search_tables") and carry no query, so every one
// of them embeds as the same text and scores as a perfect repeat of the last.
// Three schema lookups in a row would then end a run in which no data query
// had repeated — or in which none had been run at all.
//
// They are skipped rather than counted as unmeasurable, because they are not
// a failure to measure. The unmeasurable count exists to notice an index that
// is not answering, and a step that was never a subject says nothing about
// that.
func noveltySubject(step models.ExplorationStep) bool {
	return strings.TrimSpace(step.Query) != ""
}

// repeatsEarlierWork asks the step index whether this step said what an
// earlier step already said.
//
// The second return is whether the question could be answered at all, and only
// a genuine failure to measure comes back false: no index, or an index that
// errored. Failing that way round matters — the opposite default would let a
// broken vector store end every run after three steps, silently, with a full
// set of insights that were never looked for.
//
// A step with no NEIGHBOUR is a different thing and counts as new ground; see
// the branch below.
func (e *ExplorationEngine) repeatsEarlierWork(ctx context.Context, step models.ExplorationStep) (repeated, judgeable bool) {
	if e.stepIndexer == nil {
		return false, false
	}
	// A query that failed returned no data, so it says nothing about what
	// the source has left to give. Asking the same broken request three
	// times is a model that is stuck, not a run that is finished — and
	// treating it as a repeat would end the run having never read anything.
	//
	// Unjudgeable rather than skipped, unlike a schema action: the run DID
	// try to get data and could not, so novelty genuinely cannot be measured
	// for this step. A run where every query fails therefore stops being
	// measurable and hands the decision back to the floor, instead of
	// refusing every completion until the runaway cap.
	if step.Error != "" {
		return false, false
	}
	score, found, err := e.stepIndexer.Nearest(ctx, step)
	if err != nil {
		logger.WithFields(logger.Fields{
			"step":  step.Step,
			"error": err.Error(),
		}).Warn("novelty check failed; this step counts as neither new nor repeated")
		return false, false
	}
	if !found {
		// Nothing to compare against: this step cannot be repeating anything,
		// so it broke new ground. Ordinary rather than exceptional — the
		// neighbour search is scoped to one datasource, so a run's first
		// query against each source has none.
		//
		// An index that is storing nothing also answers this way, and that is
		// deliberately NOT untangled here: whether the machinery works is a
		// question about the machinery, answered once by
		// noveltyMeasurable at the point the decision is made. Encoding it
		// into each measurement is what made a first step and a broken write
		// path indistinguishable.
		return false, true
	}
	return score >= repeatSimilarityThreshold, true
}

// noveltyMeasurable reports whether the run-scoped index is in a state where
// an answer from it means anything.
//
// Three states, and each needs a different answer:
//
//   - Nothing offered yet. The index has had no chance to fail, and refusing
//     an immediate completion is exactly what the rule is for.
//   - Nothing ever kept. The write path has never worked, every search comes
//     back empty, and nothing it says is evidence about anything.
//   - Something kept, but the recent offers refused. The index is STALE: it
//     holds the run's early work and none of its latest, so a search reports
//     steps as new that were never stored to be recognised. A lifetime count
//     of successes cannot see this — one early success would mark the
//     machinery healthy for the rest of the run — so what is read is how many
//     of the most recent offers it turned away.
//
// Asked at the moment the decision is made rather than recorded per step, for
// the same reason: anything derived from what the machinery once did stays on
// the books after it stops being true, and the rule then never stands down.
func (e *ExplorationEngine) noveltyMeasurable() bool {
	if e.stepsIndexOffered == 0 {
		return true
	}
	if e.stepsIndexed == 0 {
		return false
	}
	return e.consecutiveIndexFailures < degradedStepsBeforeFallback
}

// rejectionFor renders the nudge sent back to the model when its completion
// is refused, plus the short reason recorded on the rejected step.
//
// The two reasons need different nudges because they ask for different
// things. The floor asks for more steps and can say how many are missing.
// The novelty rule cannot — there is no target number — so it asks for the
// only thing that will actually end the run: a question the model has not
// already asked. Telling it "keep going" without that is how a model
// produces three more near-identical slices and signals done again.
func (e *ExplorationEngine) rejectionFor(reason string, step int) (nudge, recorded string) {
	// Both novelty refusals get the same nudge, and it says nothing about
	// how the run will be allowed to end. Describing the rule would hand the
	// model a cheaper way to satisfy it than exploring: re-ask one question
	// three times and the run stops. What it asks for is the thing that is
	// actually wanted either way — ground this run has not covered.
	const keepGoing = "Not yet — this run has not covered everything it can. " +
		"Explore something no earlier step has looked at: a different metric, " +
		"a different breakdown, or a segment none of them touched. " +
		"Respond with the next query in the documented JSON format: " +
		`{"thinking": "...", "query": "..."}.`

	switch reason {
	case "productive":
		return keepGoing, fmt.Sprintf("rejected premature completion (recent steps were still finding new ground; %d of the last steps repeated earlier work)", e.stop.consecutiveRepeats)
	case "unproven":
		return keepGoing, fmt.Sprintf("rejected premature completion (only %d steps could be judged for novelty so far)", e.stop.judged)
	}
	return fmt.Sprintf(
			"You've only completed %d of the required minimum %d exploration steps. "+
				"Do not signal completion yet — there are more analysis areas to cover. "+
				"Respond with the next query in the documented JSON format: "+
				`{"thinking": "...", "query": "SELECT ..."}.`,
			step, e.minSteps,
		),
		fmt.Sprintf("rejected premature completion (%d < %d)", step, e.minSteps)
}
