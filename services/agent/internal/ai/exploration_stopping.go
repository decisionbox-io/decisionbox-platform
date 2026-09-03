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
	repeatsBeforeDone = 3
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

	// judged counts steps this rule was actually able to assess. A rule
	// that has judged nothing knows nothing, whatever its repeat count
	// says, and must not accept a completion on the strength of it.
	judged int
}

// observe records what the run learned from one executed step.
//
// repeated is whether the step said what an earlier step already said;
// judgeable is false when novelty could not be established — no index, an
// index that failed, a step with nothing to embed, or the first step of a
// run, which has nothing to be similar to. An unjudgeable step is not
// evidence either way and clears the streak rather than extending it: three
// silent failures must not read as three repetitions.
func (s *stopRule) observe(repeated, judgeable bool) {
	if !judgeable {
		s.consecutiveRepeats = 0
		return
	}
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
// Novelty decides only for a run that uses the rule AND has judged enough
// steps to hold an opinion. Everything else is the floor, which is what makes
// a cube run whose index is unavailable degrade to today's behaviour instead
// of rejecting every completion until the runaway cap.
func (s *stopRule) acceptDone(step int) (bool, string) {
	if s.byNovelty && s.judged >= repeatsBeforeDone {
		if s.consecutiveRepeats >= repeatsBeforeDone {
			return true, ""
		}
		return false, "novelty"
	}
	if step < s.minSteps {
		return false, "floor"
	}
	return true, ""
}

// repeatsEarlierWork asks the step index whether this step said what an
// earlier step already said.
//
// The second return is whether the question could be answered at all. A run
// with no index, an index that failed, a step with nothing worth embedding,
// and the first step of a run all come back unjudgeable — and an unjudgeable
// step is not evidence of exhaustion. Failing that way round matters: the
// opposite default would let a broken vector store end every run after three
// steps, silently, with a full set of insights that were never looked for.
func (e *ExplorationEngine) repeatsEarlierWork(ctx context.Context, step models.ExplorationStep) (repeated, judgeable bool) {
	if e.stepIndexer == nil {
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
		return false, false
	}
	return score >= repeatSimilarityThreshold, true
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
	if reason == "novelty" {
		return "Not yet — your recent steps have been re-asking questions this run has already answered, " +
				"so completing now would stop on repetition rather than on coverage. " +
				"Explore something this run has not looked at: a different metric, a different breakdown, " +
				"or a segment no earlier step touched. " +
				"Respond with the next query in the documented JSON format: " +
				`{"thinking": "...", "query": "..."}.`,
			fmt.Sprintf("rejected premature completion (last %d steps repeated earlier work)", e.stop.consecutiveRepeats)
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
