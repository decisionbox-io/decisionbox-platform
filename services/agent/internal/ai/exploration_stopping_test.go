package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// feed drives a rule through a sequence of observations, written as the
// engine sees them: 'r' a step that repeated earlier work, 'n' a novel step,
// '?' a step novelty could not be judged on.
func feed(s *stopRule, seq string) {
	for _, c := range seq {
		switch c {
		case 'r':
			s.observe(true, true)
		case 'n':
			s.observe(false, true)
		case '?':
			s.observe(false, false)
		default:
			panic("unknown observation " + string(c))
		}
	}
}

// TestStopRule_FloorRunIsUnchanged is the regression guard for every SQL
// deployment: a run that does not use the novelty rule must behave exactly as
// the step floor always has, whatever it observed along the way.
func TestStopRule_FloorRunIsUnchanged(t *testing.T) {
	for _, seq := range []string{"", "rrrrrr", "nnn", "??????"} {
		s := &stopRule{minSteps: 5}
		feed(s, seq)
		if ok, reason := s.acceptDone(4); ok || reason != "floor" {
			t.Errorf("seq %q: done at step 4 under a floor of 5 = (%v, %q), want refused by the floor", seq, ok, reason)
		}
		if ok, _ := s.acceptDone(5); !ok {
			t.Errorf("seq %q: done at step 5 under a floor of 5 was refused", seq)
		}
	}
}

// TestStopRule_NoFloorAcceptsImmediately pins today's default. With no floor
// configured and no novelty rule, the engine has always taken the model at
// its word — this is the behaviour the cube rule must not inherit.
func TestStopRule_NoFloorAcceptsImmediately(t *testing.T) {
	s := &stopRule{}
	if ok, _ := s.acceptDone(1); !ok {
		t.Error("a run with no floor and no novelty rule should accept a completion at step 1")
	}
}

// TestStopRule_NoveltyRefusesAnEarlyCompletionWithNoFloor is the hole the
// rule exists to close. min-steps defaults to zero, so a cube run that
// deferred to the floor when it had not yet judged enough steps would accept
// a completion signalled on step one — reintroducing exactly the early
// termination the floor was added to prevent.
func TestStopRule_NoveltyRefusesAnEarlyCompletionWithNoFloor(t *testing.T) {
	s := &stopRule{byNovelty: true}
	feed(s, "?") // the first step has nothing to be similar to
	ok, reason := s.acceptDone(1)
	if ok {
		t.Fatal("a cube run accepted a completion on step 1")
	}
	if reason != "novelty" {
		t.Errorf("refusal reason = %q, want the novelty rule to own the decision", reason)
	}
}

// TestStopRule_NoveltyAcceptsOnceTheRunRepeatsItself is the rule's whole
// purpose: the exploration stops because it stopped finding anything, at
// whatever step that happens to be.
func TestStopRule_NoveltyAcceptsOnceTheRunRepeatsItself(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 40}
	feed(s, "?nnrrr")
	if ok, reason := s.acceptDone(6); !ok {
		t.Errorf("three repeats in a row did not end the run: (%v, %q)", ok, reason)
	}
}

// TestStopRule_NoveltyIgnoresTheFloorInBothDirections is what "replace the
// floor" means. A cube run neither stops because it hit a number nor keeps
// going because it has not.
func TestStopRule_NoveltyIgnoresTheFloorInBothDirections(t *testing.T) {
	// Well past a floor of 3, but still finding new things: keep going.
	still := &stopRule{byNovelty: true, minSteps: 3}
	feed(still, "nnnnnnnnnn")
	if ok, _ := still.acceptDone(10); ok {
		t.Error("a run still turning up new ground was allowed to stop because it passed the floor")
	}
	// Well short of a floor of 40, but repeating itself: stop.
	spent := &stopRule{byNovelty: true, minSteps: 40}
	feed(spent, "nrrr")
	if ok, _ := spent.acceptDone(4); !ok {
		t.Error("a run that had run dry was forced onward because it had not reached the floor")
	}
}

// TestStopRule_ANovelStepBreaksTheStreak covers the case that decides whether
// this is a marginal-value rule or a counter: two repeats then a discovery
// means the run is still productive, and the count starts again.
func TestStopRule_ANovelStepBreaksTheStreak(t *testing.T) {
	s := &stopRule{byNovelty: true}
	feed(s, "nrrnr")
	if ok, _ := s.acceptDone(5); ok {
		t.Error("a novel step between repeats did not reset the streak")
	}
}

// TestStopRule_UnjudgeableStepsAreNotRepeats is the failure this rule could
// most easily have had. Three steps novelty could not be measured on are not
// three repetitions, and reading them as such would end runs on a silent
// measurement failure — with a full set of insights that were never sought.
func TestStopRule_UnjudgeableStepsAreNotRepeats(t *testing.T) {
	s := &stopRule{byNovelty: true}
	feed(s, "nnn???")
	if ok, _ := s.acceptDone(6); ok {
		t.Error("steps that could not be judged were counted as repetitions")
	}
}

// TestStopRule_AnUnmeasurableRunFallsBackToTheFloor is the other side of that
// coin. If novelty is never measurable — a vector store that is down — the
// rule must stand down rather than refuse every completion until the runaway
// cap, which would turn a degraded index into a maximum-length run against a
// source metered per request.
func TestStopRule_AnUnmeasurableRunFallsBackToTheFloor(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 5}
	feed(s, "???")
	if ok, reason := s.acceptDone(4); ok || reason != "floor" {
		t.Errorf("with novelty unmeasurable, step 4 under a floor of 5 = (%v, %q), want the floor to decide", ok, reason)
	}
	if ok, _ := s.acceptDone(5); !ok {
		t.Error("with novelty unmeasurable, the floor should still let the run finish")
	}
}

// TestStopRule_OneJudgedStepKeepsTheRuleArmed pins the boundary between the
// two cases above: the machinery having worked once proves it is there, so
// later unmeasurable steps must not be read as it being broken.
func TestStopRule_OneJudgedStepKeepsTheRuleArmed(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 1}
	feed(s, "n????????")
	if ok, reason := s.acceptDone(9); ok || reason != "novelty" {
		t.Errorf("after a successful judgement the rule handed back to the floor: (%v, %q)", ok, reason)
	}
}

// countingIndexer scripts Nearest so the engine's novelty plumbing can be
// exercised without a vector store.
type countingIndexer struct {
	scores []float64
	found  []bool
	err    error
	calls  int
	upsert int
}

func (c *countingIndexer) Upsert(context.Context, models.ExplorationStep) error {
	c.upsert++
	return nil
}

func (c *countingIndexer) Nearest(context.Context, models.ExplorationStep) (float64, bool, error) {
	i := c.calls
	c.calls++
	if c.err != nil {
		return 0, false, c.err
	}
	if i >= len(c.scores) {
		return 0, false, nil
	}
	return c.scores[i], c.found[i], nil
}

// TestRepeatsEarlierWork_ThresholdAndFailureModes covers the engine's reading
// of one novelty measurement. The failure modes matter more than the happy
// path: every one of them must come back unjudgeable, because "could not
// tell" read as "not similar" keeps a spent run going and read as "similar"
// ends a productive one.
func TestRepeatsEarlierWork_ThresholdAndFailureModes(t *testing.T) {
	step := models.ExplorationStep{Step: 4, Query: "SELECT 1", QueryPurpose: "count"}

	cases := map[string]struct {
		engine              *ExplorationEngine
		repeated, judgeable bool
	}{
		"clearly the same request": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{0.97}, found: []bool{true}}},
			repeated: true, judgeable: true,
		},
		"a different question": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{0.40}, found: []bool{true}}},
			repeated: false, judgeable: true,
		},
		"exactly at the threshold counts as a repeat": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{repeatSimilarityThreshold}, found: []bool{true}}},
			repeated: true, judgeable: true,
		},
		"nothing indexed yet": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{scores: []float64{0}, found: []bool{false}}},
			repeated: false, judgeable: false,
		},
		"the index failed": {
			engine:   &ExplorationEngine{stepIndexer: &countingIndexer{err: errors.New("qdrant unreachable")}},
			repeated: false, judgeable: false,
		},
		"no index configured": {
			engine:   &ExplorationEngine{},
			repeated: false, judgeable: false,
		},
	}
	for name, tc := range cases {
		repeated, judgeable := tc.engine.repeatsEarlierWork(context.Background(), step)
		if repeated != tc.repeated || judgeable != tc.judgeable {
			t.Errorf("%s: got (repeated=%v, judgeable=%v), want (%v, %v)", name, repeated, judgeable, tc.repeated, tc.judgeable)
		}
	}
}

// TestRejectionFor_TellsTheModelSomethingItCanAct on. A nudge that just says
// "keep going" is how a model produces three more near-identical slices and
// signals done again, so the novelty refusal has to ask for the one thing
// that ends the run: an unasked question.
func TestRejectionFor_TellsTheModelSomethingItCanActOn(t *testing.T) {
	e := &ExplorationEngine{minSteps: 12, stop: stopRule{consecutiveRepeats: 3}}

	nudge, recorded := e.rejectionFor("novelty", 4)
	for _, want := range []string{"already answered", "not looked at"} {
		if !strings.Contains(nudge, want) {
			t.Errorf("novelty nudge does not mention %q: %s", want, nudge)
		}
	}
	if strings.Contains(nudge, "minimum") {
		t.Error("the novelty nudge asks for a step count, which this run does not have")
	}
	if !strings.Contains(recorded, "repeated earlier work") {
		t.Errorf("recorded reason %q does not say why the run continued", recorded)
	}

	nudge, recorded = e.rejectionFor("floor", 4)
	if !strings.Contains(nudge, "minimum 12") {
		t.Errorf("floor nudge does not name the floor: %s", nudge)
	}
	if !strings.Contains(recorded, "4 < 12") {
		t.Errorf("recorded floor reason %q does not show the shortfall", recorded)
	}
}

// TestNoveltySubject_OnlyADataQueryCounts is the fix for a rule that would
// otherwise end a run on its own bookkeeping. lookup_schema and search_tables
// stamp a CONSTANT purpose and carry no query, so every one of them embeds as
// the same text and scores as a perfect repeat of the last — three in a row
// would accept a completion in a run where no data query had repeated, or
// where none had been run at all.
func TestNoveltySubject_OnlyADataQueryCounts(t *testing.T) {
	cases := map[string]struct {
		step models.ExplorationStep
		want bool
	}{
		"a SQL query": {
			step: models.ExplorationStep{Action: "query_data", Query: "SELECT 1", QueryPurpose: "count"},
			want: true,
		},
		"a structured request against a cube, rendered as text": {
			step: models.ExplorationStep{Action: "query_data", Query: `{"metrics":["sessions"],"dimensions":["channel"]}`, QueryPurpose: "sessions by channel"},
			want: true,
		},
		"a schema lookup, whose purpose is a constant": {
			step: models.ExplorationStep{Action: "lookup_schema", QueryPurpose: "lookup_schema"},
			want: false,
		},
		"a table search, likewise": {
			step: models.ExplorationStep{Action: "search_tables", QueryPurpose: "search_tables"},
			want: false,
		},
		"a query that is only whitespace": {
			step: models.ExplorationStep{Action: "query_data", Query: "   \n", QueryPurpose: "lookup_schema"},
			want: false,
		},
	}
	for name, tc := range cases {
		if got := noveltySubject(tc.step); got != tc.want {
			t.Errorf("%s: noveltySubject = %v, want %v", name, got, tc.want)
		}
	}
}

// TestStopRule_SkippedStepsDoNotDisarmTheRule pins the consequence of
// skipping non-subjects rather than counting them unmeasurable: a run that
// opens with several schema lookups must still be judged by novelty once it
// starts querying, not handed back to the floor because of them.
func TestStopRule_SkippedStepsDoNotDisarmTheRule(t *testing.T) {
	s := &stopRule{byNovelty: true, minSteps: 2}
	// Three schema lookups are not observed at all — the engine skips them —
	// so the rule sees only the query steps that followed.
	feed(s, "nrrr")
	if ok, _ := s.acceptDone(7); !ok {
		t.Error("a run that opened with schema lookups was not judged on its queries")
	}
}
