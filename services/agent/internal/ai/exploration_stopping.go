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

	// unjudgedStepsBeforeFallback is how many steps the rule will fail to
	// assess before deciding it cannot assess anything, and handing the
	// decision back to the step floor.
	//
	// Novelty is unmeasurable for real reasons that are not failures — an
	// empty index on the first step, an action carrying no query — so a
	// single miss must not disarm the rule. Three consecutive-or-not misses
	// with nothing ever judged is a different thing: the vector store is
	// unavailable, and a rule that cannot see must not be the one deciding
	// when a run ends. Without this the run would reject every completion
	// until the runaway cap, turning a degraded index into a maximum-length
	// run against a metered source.
	unjudgedStepsBeforeFallback = 3
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

	// unjudged counts steps the rule tried and failed to assess. Read only
	// together with judged: the pair distinguishes "this run is young" from
	// "novelty is not measurable here", which need opposite answers.
	unjudged int
}

// measurable reports whether novelty is worth consulting at all.
//
// One judged step is enough to prove the machinery works. Nothing judged
// after several attempts is the signature of an index that is not answering,
// and the rule stands down rather than deciding a run it cannot see.
func (s *stopRule) measurable() bool {
	return s.judged > 0 || s.unjudged < unjudgedStepsBeforeFallback
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
		s.unjudged++
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
	if s.byNovelty && s.measurable() {
		// Enough judged steps to hold an opinion, and the opinion is that
		// the run has stopped finding new ground.
		if s.judged >= repeatsBeforeDone && s.consecutiveRepeats >= repeatsBeforeDone {
			return true, ""
		}
		// Otherwise the run is still turning things up, or has not yet done
		// enough for the question to have an answer. Both are refusals, and
		// deliberately NOT deferred to the floor: the floor defaults to zero,
		// so deferring would accept a completion signalled on step one — the
		// early termination the floor was added to prevent, reintroduced by
		// the rule meant to replace it.
		return false, "novelty"
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
