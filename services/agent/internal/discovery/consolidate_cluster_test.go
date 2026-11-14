package discovery

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// unit returns the 2-D unit vector at the given angle, so a test can state the
// cosine between two documents exactly rather than hoping a fixture produces
// it. cos(25°) = 0.906, cos(50°) = 0.643 — the non-transitive triple.
func unit(deg float64) []float64 {
	r := deg * math.Pi / 180
	return []float64{math.Cos(r), math.Sin(r)}
}

func subj(id string, deg float64, text, magnitude string) foldSubject {
	return foldSubject{id: id, docType: "insight", vec: unit(deg), text: text, magnitude: magnitude}
}

func TestCosineFold_Threshold(t *testing.T) {
	later := subj("b", 25, "b text", "affected=1")
	earlier := subj("a", 0, "a text", "affected=1")

	if _, ok := cosineFold(later, earlier, 0.88); !ok {
		t.Error("0.906 must fold at a 0.88 floor")
	}
	if _, ok := cosineFold(later, earlier, 0.95); ok {
		t.Error("0.906 must not fold at a 0.95 floor")
	}
	if _, ok := cosineFold(subj("b", 50, "b", "affected=1"), earlier, 0.88); ok {
		t.Error("0.643 must not fold at a 0.88 floor")
	}
}

func TestCosineFold_TypesNeverCross(t *testing.T) {
	later := subj("b", 0, "same", "affected=1")
	earlier := subj("a", 0, "same", "affected=1")
	earlier.docType = "recommendation"
	if _, ok := cosineFold(later, earlier, 0.88); ok {
		t.Error("an insight must never fold into a recommendation, whatever the score")
	}
}

// Identical text, different magnitude: the score is 1.0 and carries no
// information at all, so the guard is what stops 100 affected entities being
// hidden behind 1,000.
func TestCosineFold_IdenticalTextDifferentMagnitude(t *testing.T) {
	a := subj("a", 0, "identical prose", "affected=100")
	b := subj("b", 0, "identical prose", "affected=1000")
	if _, ok := cosineFold(b, a, 0.88); ok {
		t.Error("identical text with a different magnitude must not fold")
	}
	same := subj("c", 0, "identical prose", "affected=100")
	if _, ok := cosineFold(same, a, 0.88); !ok {
		t.Error("identical text with the SAME magnitude must still fold")
	}
}

// Cosine is not transitive, so matching the middle of a chain says nothing
// about the head of it. 0° / 25° / 50°: neighbours at 0.906, ends at 0.643.
func TestCosineFoldTarget_ChainIsRecheckedAgainstTheCanonical(t *testing.T) {
	a := subj("a", 0, "a", "affected=1")
	b := subj("b", 25, "b", "affected=1")
	c := subj("c", 50, "c", "affected=1")
	hand := map[string]foldSubject{"a": a, "b": b, "c": c}
	byID := func(id string) (foldSubject, bool) { s, ok := hand[id]; return s, ok }

	canonical := map[string]string{}
	target, _, ok := cosineFoldTarget(b, a, 0.88, canonical, byID, "test: ")
	if !ok || target != "a" {
		t.Fatalf("b into a: target=%q ok=%v", target, ok)
	}
	canonical["b"] = "a"

	// c matches b at 0.906 but a at only 0.643, and b is folded — so c is a
	// duplicate of nothing a reader can see.
	if _, _, ok := cosineFoldTarget(c, b, 0.88, canonical, byID, "test: "); ok {
		t.Error("c must not be folded into a on the strength of its score against b")
	}
}

// The guard has to hold against the CANONICAL too, or it is simply routed
// around: c fails against a directly, matches a paraphrase of a, and is folded
// into a anyway.
func TestCosineFoldTarget_ChainCannotRouteAroundTheMagnitudeGuard(t *testing.T) {
	a := subj("a", 0, "identical prose", "affected=100")
	b := subj("b", 1, "a paraphrase", "affected=100")
	c := subj("c", 2, "identical prose", "affected=9999")
	hand := map[string]foldSubject{"a": a, "b": b, "c": c}
	byID := func(id string) (foldSubject, bool) { s, ok := hand[id]; return s, ok }

	canonical := map[string]string{"b": "a"}
	if _, _, ok := cosineFoldTarget(c, b, 0.88, canonical, byID, "test: "); ok {
		t.Error("c states a's prose with a different magnitude; folding it into a via b routes around the guard")
	}
}

// A canonical that is not among the documents in hand belongs to an earlier
// run, whose vector this pass does not have. The only evidence available is
// the stored pass, which already said no.
func TestCosineFoldTarget_CanonicalOutsideTheRunIsNotFolded(t *testing.T) {
	a := subj("a", 0, "a", "affected=1")
	b := subj("b", 1, "b", "affected=1")
	byID := func(string) (foldSubject, bool) { return foldSubject{}, false }
	if _, _, ok := cosineFoldTarget(b, a, 0.88, map[string]string{"a": "from-an-earlier-run"}, byID, "test: "); ok {
		t.Error("must not fold into a canonical whose vector is not in hand")
	}
}

func TestClaimFigures(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"three digits qualify", "margin loss of 142 units", []string{"142"}},
		{"one and two digits do not", "5% of 42 stores, 3x worse", nil},
		{"grouped digits count across separators", "12,450 orders worth 1.234,5", []string{"1.234,5", "12,450"}},
		{"trailing punctuation is trimmed", "affected 1,250.", []string{"1,250"}},
		{"duplicates collapse", "142 and 142 again", []string{"142"}},
		{"percentages with three digits qualify", "fell 23.4% over 180 days", []string{"180", "23.4"}},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := claimFigures(tc.in)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("claimFigures(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// Containment is the test for the commonest duplicate shape these findings
// take: a headline that IS one row of another finding's table. The surrounding
// prose differs, so cosine scores it 0.71-0.87 and never folds it.
func TestContainedIn(t *testing.T) {
	table := strings.Join([]string{
		"| segment | idle units | share |",
		"|---|---|---|",
		"| north | 111,222 | 33.3 |",
		"| south | 444,555 | 66.6 |",
	}, "\n")

	if !containedIn("The north segment holds 111,222 idle units, 33.3 of the total", table) {
		t.Error("a headline whose figures are all one row of the table must be contained")
	}
	if containedIn("The south segment holds 444,555 idle units, 12.5 of the total", table) {
		t.Error("a headline with a figure the table does not carry must not be contained")
	}
}

// One shared figure is not containment. A single three-digit number between
// two findings about the same warehouse is coincidence often enough to matter
// — a row count, a year, a store code — and the cost of a wrong fold here is a
// finding that loses its verdict.
func TestContainedIn_NeedsMoreThanOneFigure(t *testing.T) {
	if containedIn("stores affected: 142", "we counted 142 of them, across 7 regions") {
		t.Error("a single shared figure must not be read as containment")
	}
	if containedIn("no figures at all", "142 and 980 and 3,100") {
		t.Error("a headline with no distinctive figures must not be contained in anything")
	}
	if containedIn("142 units over 980 days", "") {
		t.Error("nothing is contained in an empty body")
	}
}

func doc(idx int, id string, deg float64, current bool, name, body string) consolidationDoc {
	return consolidationDoc{
		foldSubject: foldSubject{id: id, docType: "insight", vec: unit(deg), text: id + " text", magnitude: "affected=1"},
		index:       idx, current: current, slot: idx, name: name, body: body,
	}
}

// Clusters are STARS over direct relations, never connected components.
// Transitive closure over containment — A contains B, B contains C, therefore
// one cluster — proposed suppressing 23 of 55 insights on real run output where
// the direct relations yield 9.
//
// A chain of four documents each 25° from the next is the case that tells the
// two apart: closure makes one cluster of four, where the ends are 0.259 apart
// and have nothing to do with each other. Stars make two overlapping clusters
// of three, each one a seed plus the documents that actually resemble it.
func TestBuildClusters_StarsNotComponents(t *testing.T) {
	docs := []consolidationDoc{
		doc(0, "a", 0, true, "a", ""),
		doc(1, "b", 25, true, "b", ""),
		doc(2, "c", 50, true, "c", ""),
		doc(3, "d", 75, true, "d", ""),
	}
	clusters := buildClusters(docs, proposePairs(docs, 0.88))
	if len(clusters) != 2 {
		t.Fatalf("clusters = %d (%v), want 2", len(clusters), clusters)
	}
	for _, c := range clusters {
		if len(c.members) != 3 {
			t.Fatalf("cluster %v has %d members, want 3: a chain of four must not collapse into one set", c.members, len(c.members))
		}
		// Every member must resemble the seed directly. The seed is the only
		// document the others were measured against.
		lo, hi := c.members[0], c.members[len(c.members)-1]
		if s, _ := cosineSimilarity(docs[lo].vec, docs[hi].vec); s >= 0.88 {
			t.Fatalf("cluster %v: the ends score %.3f, so this is not a star around a seed", c.members, s)
		}
	}
}

// A cluster with nothing from the area being consolidated cannot be acted on,
// so adjudicating it would spend a provider call on an answer the caller must
// refuse.
func TestBuildClusters_DropsClustersWithNothingMarkable(t *testing.T) {
	docs := []consolidationDoc{
		doc(0, "a", 0, false, "a", ""),
		doc(1, "b", 1, false, "b", ""),
	}
	if got := buildClusters(docs, proposePairs(docs, 0.88)); len(got) != 0 {
		t.Fatalf("clusters = %v, want none: neither member can be marked", got)
	}
}

func TestBuildClusters_SubsetsAreNotAdjudicatedTwice(t *testing.T) {
	// Three documents all within 2° of each other: one star of three, and the
	// two-member stars around each member are subsets of it.
	docs := []consolidationDoc{
		doc(0, "a", 0, true, "a", ""),
		doc(1, "b", 1, true, "b", ""),
		doc(2, "c", 2, true, "c", ""),
	}
	clusters := buildClusters(docs, proposePairs(docs, 0.88))
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d (%v), want 1", len(clusters), clusters)
	}
	if !reflect.DeepEqual(clusters[0].members, []int{0, 1, 2}) {
		t.Errorf("members = %v, want [0 1 2]", clusters[0].members)
	}
}

// The two tests find largely different duplicates, so which one fired is
// reported rather than collapsed: a containment-only cluster is one the
// similarity threshold cannot see at all.
func TestProposePairs_ReportsWhichTestFired(t *testing.T) {
	table := "| x | 111,222 | 33.3 |"
	docs := []consolidationDoc{
		// Far apart in angle, so cosine says nothing; contained by figures.
		doc(0, "a", 0, false, "a headline", table),
		doc(1, "b", 80, true, "b carries 111,222 and 33.3", ""),
		// Close in angle, no shared figures.
		doc(2, "c", 1, true, "c headline", ""),
	}
	rel := proposePairs(docs, 0.88)
	if got := rel[newPairKey(0, 1)]; got != agentplugin.ProposalContainment {
		t.Errorf("a-b proposal = %q, want containment", got)
	}
	if got := rel[newPairKey(0, 2)]; got != agentplugin.ProposalCosine {
		t.Errorf("a-c proposal = %q, want cosine", got)
	}
}

func TestResolveMarkClosure(t *testing.T) {
	exists := func(ids ...string) func(string) bool {
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return func(id string) bool { return set[id] }
	}

	t.Run("a chain resolves to its end", func(t *testing.T) {
		got, err := resolveMarkClosure(map[string]string{"c": "b", "b": "a"}, exists("a", "b", "c"))
		if err != nil {
			t.Fatalf("closure: %v", err)
		}
		if got["c"] != "a" || got["b"] != "a" {
			t.Errorf("closure = %v, want both pointing at a", got)
		}
	})

	t.Run("a mutual cycle is reported, not broken", func(t *testing.T) {
		_, err := resolveMarkClosure(map[string]string{"a": "b", "b": "a"}, exists("a", "b"))
		if !errors.Is(err, errBrokenMark) {
			t.Fatalf("err = %v, want errBrokenMark: a cycle means neither document has a visible canonical", err)
		}
	})

	t.Run("a longer cycle is reported", func(t *testing.T) {
		_, err := resolveMarkClosure(map[string]string{"a": "b", "b": "c", "c": "a"}, exists("a", "b", "c"))
		if !errors.Is(err, errBrokenMark) {
			t.Fatalf("err = %v, want errBrokenMark", err)
		}
	})

	t.Run("a self mark is reported", func(t *testing.T) {
		_, err := resolveMarkClosure(map[string]string{"a": "a"}, exists("a"))
		if !errors.Is(err, errBrokenMark) {
			t.Fatalf("err = %v, want errBrokenMark", err)
		}
	})

	t.Run("a cross-run canonical ends the chain", func(t *testing.T) {
		got, err := resolveMarkClosure(map[string]string{"b": "from-an-earlier-run"}, exists("a", "b"))
		if err != nil {
			t.Fatalf("a target outside the run is not broken: %v", err)
		}
		if got["b"] != "from-an-earlier-run" {
			t.Errorf("closure = %v", got)
		}
	})

	t.Run("a mark pointing at nothing is reported", func(t *testing.T) {
		_, err := resolveMarkClosure(map[string]string{"b": ""}, exists("a", "b"))
		if !errors.Is(err, errBrokenMark) {
			t.Fatalf("err = %v, want errBrokenMark", err)
		}
	})
}

// Containment compares WHOLE tokens, through the same parser, not substrings.
// `strings.Contains` accepted a headline's 123 and 456 as contained in a body
// stating 1123 and 2456 — two different measurements reading as the same one,
// and on a pair below the similarity floor the fold costs a distinct finding
// its verdict and its place in recommendation generation on nothing at all.
func TestContainedIn_ComparesWholeTokens(t *testing.T) {
	if containedIn("the figures are 123 and 456", "we measured 1123 and 2456") {
		t.Error("a headline's figures must not match longer numbers that merely contain them")
	}
	// The same figures as whole tokens are contained.
	if !containedIn("the figures are 123 and 456", "we measured 123 and 456 across the set") {
		t.Error("whole-token matches must still be contained")
	}
	// A decimal is a different token from its integer part.
	if containedIn("the figures are 123 and 456", "we measured 123.45 and 456.78") {
		t.Error("123 must not match 123.45: they are different measurements")
	}
	// And a grouped figure is a different token from its ungrouped spelling,
	// which the substring rule also could not match — stated so the limit is
	// recorded rather than discovered.
	if containedIn("the figures are 1,234 and 5,678", "we measured 1234 and 5678") {
		t.Error("the comparison is textual: a grouped figure is not matched against its ungrouped spelling")
	}
}
