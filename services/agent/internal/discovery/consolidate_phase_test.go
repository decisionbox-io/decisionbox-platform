package discovery

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// angleEmbedder returns a vector for a text from a table the test controls, so
// the cosine between any two findings is stated rather than hoped for. Texts
// with no entry get a vector orthogonal to every angle, which scores 0 against
// all of them.
type angleEmbedder struct {
	deg   map[string]float64
	calls int
	texts []string
}

func (e *angleEmbedder) Embed(_ context.Context, texts []string) ([][]float64, error) {
	e.calls++
	e.texts = append(e.texts, texts...)
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if d, ok := e.deg[t]; ok {
			out[i] = append(unit(d), 0)
			continue
		}
		out[i] = []float64{0, 0, 1}
	}
	return out, nil
}

func (e *angleEmbedder) Dimensions() int                  { return 3 }
func (e *angleEmbedder) ModelName() string                { return "angle" }
func (e *angleEmbedder) Validate(_ context.Context) error { return nil }

type failingEmbedder struct{ calls int }

func (e *failingEmbedder) Embed(_ context.Context, _ []string) ([][]float64, error) {
	e.calls++
	return nil, errors.New("embedding provider down")
}
func (e *failingEmbedder) Dimensions() int                  { return 3 }
func (e *failingEmbedder) ModelName() string                { return "failing" }
func (e *failingEmbedder) Validate(_ context.Context) error { return nil }

// insight builds one in-flight insight. The embedding text is derived the same
// way production derives it, which is what lets the angle table be keyed on it.
func insight(id, area, name, body string, affected int) models.Insight {
	return models.Insight{
		ID: id, AnalysisArea: area, Name: name, Description: body,
		Severity: "high", AffectedCount: affected,
	}
}

func embText(ins models.Insight) string {
	return commonmodels.InsightEmbeddingText(ins.Name, ins.Description, ins.AnalysisArea, ins.Severity, ins.TargetSegment)
}

// stubProvider is an in-process ConsolidationProvider for the phase tests.
type stubProvider struct {
	name     string
	fn       func(clusters []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error)
	panics   bool
	calls    int
	gotFirst []agentplugin.DuplicateCluster
}

func (s *stubProvider) Name() string {
	if s.name == "" {
		return "stub-consolidator"
	}
	return s.name
}

func (s *stubProvider) Consolidate(_ context.Context, _ string, clusters []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
	s.calls++
	if s.gotFirst == nil {
		s.gotFirst = clusters
	}
	if s.panics {
		panic("stub provider exploded")
	}
	if s.fn == nil {
		return nil, nil
	}
	return s.fn(clusters)
}

func withProvider(t *testing.T, p agentplugin.ConsolidationProvider) {
	t.Helper()
	agentplugin.ResetConsolidationForTest()
	t.Cleanup(agentplugin.ResetConsolidationForTest)
	if p != nil {
		agentplugin.RegisterConsolidationProvider(p)
	}
}

func areaInput(area string, current, earlier []models.Insight) consolidateAreaInput {
	return consolidateAreaInput{
		area:        AnalysisArea{ID: area, Name: area, Description: area + " scope"},
		prompts:     ResolvedPrompts{AnalysisAreas: map[string]string{}},
		areaByID:    map[string]AnalysisArea{area: {ID: area, Name: area, Description: area + " scope"}},
		insights:    current,
		runInsights: earlier,
	}
}

// With no provider registered the community rule applies, and it is Phase 9's
// rule: earliest member canonical, folded on the cosine pair. The only thing
// that moves is WHEN it is decided.
func TestConsolidateArea_CommunityRuleFoldsASimilarityPair(t *testing.T) {
	withProvider(t, nil)

	a := insight("a", "stock", "idle units are growing", "the total rose", 100)
	b := insight("b", "stock", "idle units keep growing", "the total rose again", 90)
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}

	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 1 || st.ByCosine != 1 {
		t.Fatalf("stats = %+v, want one cosine fold", st)
	}
	if current[0].DuplicateOf != "" {
		t.Errorf("the earliest member must stay canonical, got duplicate_of=%q", current[0].DuplicateOf)
	}
	if current[1].DuplicateOf != "a" {
		t.Errorf("b.duplicate_of = %q, want a", current[1].DuplicateOf)
	}
	if current[1].SimilarityScore < 0.88 {
		t.Errorf("b.similarity_score = %v, want the measured cosine", current[1].SimilarityScore)
	}
	if st.Decider != "community" {
		t.Errorf("decider = %q, want community", st.Decider)
	}
}

// A containment-only pair is NOT folded by the community rule. That is what
// keeps a deployment with no provider on exactly the outcome it has today:
// containment is a proposal the adjudicator decides, not a new community rule.
func TestConsolidateArea_CommunityRuleLeavesContainmentAlone(t *testing.T) {
	withProvider(t, nil)

	table := "| north | 111,222 | 33.3 |\n| south | 444,555 | 66.6 |"
	a := insight("a", "stock", "idle units across every segment", table, 100)
	b := insight("b", "stock", "the north segment holds 111,222 idle units, 33.3 of it", "a slice of the same total", 90)
	// Deliberately far apart in angle: similarity says nothing about this pair.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 70,
	}}}

	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 1 {
		t.Fatalf("clusters = %d, want 1: containment must still PROPOSE the pair", st.Clusters)
	}
	if st.Folded != 0 {
		t.Fatalf("folded = %d, want 0: the community rule must not fold a containment-only pair", st.Folded)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q, want empty", current[1].DuplicateOf)
	}
}

// The same pair, with a provider registered: the adjudicator can fold what the
// threshold cannot see. This is the case the feature exists for — measured on
// one real run, the similarity rule proposed 18 pairs and containment 16, with
// only 3 in common.
func TestConsolidateArea_ProviderFoldsAContainmentPair(t *testing.T) {
	table := "| north | 111,222 | 33.3 |\n| south | 444,555 | 66.6 |"
	a := insight("a", "stock", "idle units across every segment", table, 100)
	b := insight("b", "stock", "the north segment holds 111,222 idle units, 33.3 of it", "a slice", 90)

	stub := &stubProvider{fn: func(c []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a",
			Duplicates:  map[string]float64{"b": 0.79},
			Reason:      "b is one row of a's table",
		}}, nil
	}}
	withProvider(t, stub)

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 70,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 1 || st.ByContainment != 1 {
		t.Fatalf("stats = %+v, want one containment fold", st)
	}
	if current[1].DuplicateOf != "a" {
		t.Errorf("b.duplicate_of = %q, want a", current[1].DuplicateOf)
	}
	if st.Decider != "stub-consolidator" {
		t.Errorf("decider = %q, want the provider's name", st.Decider)
	}
	// What the provider is shown: the cluster, and the structural facts it
	// cannot infer.
	if len(stub.gotFirst) != 1 || len(stub.gotFirst[0].Candidates) != 2 {
		t.Fatalf("provider saw %+v", stub.gotFirst)
	}
	if stub.gotFirst[0].Proposal != agentplugin.ProposalContainment {
		t.Errorf("proposal = %q, want containment", stub.gotFirst[0].Proposal)
	}
	if stub.gotFirst[0].Candidates[0].AreaScope == "" {
		t.Error("a candidate must carry its area's ownership statement")
	}
	// The figures have their own field, so the body must not repeat them:
	// sending them twice is prompt budget spent saying one thing. The
	// containment test still runs over both.
	withFigures := insight("f", "stock", "headline", "prose only", 1)
	withFigures.Indicators = []string{"111,222 units"}
	if got := insightProse(&withFigures); strings.Contains(got, "111,222") {
		t.Errorf("prose = %q, want the authored body alone", got)
	}
	if got := insightBody(&withFigures); !strings.Contains(got, "111,222") {
		t.Errorf("body = %q, want the figures included for the containment test", got)
	}
}

// An earlier area's finding is a legitimate canonical — usually the right one.
func TestConsolidateArea_AnEarlierAreaCanBeTheCanonical(t *testing.T) {
	withProvider(t, nil)

	earlier := insight("e1", "replenishment", "overdue orders are rising", "the rate climbed", 500)
	cur := insight("c1", "stock", "overdue orders keep rising", "the rate climbed further", 480)
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(cur): 1,
	}}}

	current := []models.Insight{cur}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{earlier}))

	if st.Folded != 1 {
		t.Fatalf("stats = %+v, want one fold into the earlier area's finding", st)
	}
	if current[0].DuplicateOf != "e1" {
		t.Errorf("duplicate_of = %q, want e1", current[0].DuplicateOf)
	}
}

// The other direction is refused. An earlier area's finding has already been
// validated and its checkpoint is written, and a restore remints its ids — so
// a mark on it would not survive a resume. The pair is left standing and
// remembered, so the end-of-run check does not fold it the way the adjudicator
// rejected.
func TestConsolidateArea_NeverSuppressesAnEarlierArea(t *testing.T) {
	earlier := insight("e1", "replenishment", "overdue orders are rising", "the rate climbed", 500)
	cur := insight("c1", "stock", "overdue orders keep rising", "the rate climbed further", 480)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		// The adjudicator prefers the CURRENT area's phrasing.
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "c1",
			Duplicates:  map[string]float64{"e1": 0.95},
			Reason:      "the current area owns the subject",
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(cur): 1,
	}}}
	current := []models.Insight{cur}
	runInsights := []models.Insight{earlier}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, runInsights))

	if st.Folded != 0 || st.RefusedNotMine != 1 {
		t.Fatalf("stats = %+v, want zero folds and one refusal", st)
	}
	if runInsights[0].DuplicateOf != "" {
		t.Errorf("an earlier area's insight was marked: duplicate_of=%q", runInsights[0].DuplicateOf)
	}
	if current[0].DuplicateOf != "" {
		t.Errorf("the current insight must not be folded the other way either, got %q", current[0].DuplicateOf)
	}
	if !o.adjudicatedKeep("c1", "e1") {
		t.Error("the refused pair must be remembered so Phase 9 does not fold it by document order")
	}
}

// A verdict that names a document outside its own cluster is discarded: the
// candidate set is the only thing that validates it.
func TestConsolidateArea_VerdictOutsideItsClusterIsDiscarded(t *testing.T) {
	a := insight("a", "stock", "idle units are growing", "the total rose", 100)
	b := insight("b", "stock", "idle units keep growing", "again", 90)

	for _, tc := range []struct {
		name    string
		verdict agentplugin.ConsolidationVerdict
	}{
		{"canonical is a stranger", agentplugin.ConsolidationVerdict{
			CanonicalID: "not-in-this-run", Duplicates: map[string]float64{"b": 0.9}}},
		{"duplicate is a stranger", agentplugin.ConsolidationVerdict{
			CanonicalID: "a", Duplicates: map[string]float64{"also-a-stranger": 0.9}}},
		{"canonical folds into itself", agentplugin.ConsolidationVerdict{
			CanonicalID: "a", Duplicates: map[string]float64{"a": 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.verdict
			withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
				return []agentplugin.ConsolidationVerdict{v}, nil
			}})
			o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
				embText(a): 0, embText(b): 1,
			}}}
			current := []models.Insight{a, b}
			st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))
			if st.Folded != 0 {
				t.Fatalf("stats = %+v, want no fold", st)
			}
			if st.RefusedShape == 0 {
				t.Errorf("stats = %+v, want the malformed verdict counted", st)
			}
			for i := range current {
				if current[i].DuplicateOf != "" {
					t.Errorf("%s was marked %q", current[i].ID, current[i].DuplicateOf)
				}
			}
		})
	}
}

// A fold is re-verified against the canonical the mark will ACTUALLY point at.
// Here one verdict folds both b and c into a: b resembles a (0.906) and c does
// not (0.643), even though c resembles b. Folding c into a would hide it behind
// a row it does not resemble, under its neighbour's score.
func TestConsolidateArea_RecheckRefusesAFoldAgainstTheResolvedCanonical(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 300)
	b := insight("b", "stock", "claim b", "body b", 200)
	c := insight("c", "stock", "claim c", "body c", 100)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a",
			Duplicates:  map[string]float64{"b": 0.906, "c": 0.906},
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50,
	}}}
	current := []models.Insight{a, b, c}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if current[1].DuplicateOf != "a" {
		t.Errorf("b.duplicate_of = %q, want a", current[1].DuplicateOf)
	}
	if current[2].DuplicateOf != "" {
		t.Errorf("c.duplicate_of = %q; c resembles b but not a, so it is a duplicate of nothing visible", current[2].DuplicateOf)
	}
	if st.RefusedRecheck != 1 {
		t.Errorf("stats = %+v, want exactly one re-check refusal", st)
	}
}

// A canonical that a PREVIOUS area's pass already suppressed is the variant
// that produced a mutual cycle when each case was guarded as it appeared: the
// pre-check verified that no canonical was suppressed by this proposal, and
// not that it was already suppressed. The mark must resolve to the end of the
// chain, re-verified against it.
func TestConsolidateArea_ResolvesACanonicalThatIsItselfFolded(t *testing.T) {
	e0 := insight("e0", "repl", "the canonical statement", "body", 500)
	e1 := insight("e1", "repl", "a restatement of it", "body", 450)
	e1.DuplicateOf = "e0" // folded by its own area's pass
	cur := insight("c1", "stock", "another restatement", "body", 400)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		// The adjudicator points at e1, which is itself hidden.
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "e1", Duplicates: map[string]float64{"c1": 0.99},
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(e0): 0, embText(e1): 1, embText(cur): 2,
	}}}

	current := []models.Insight{cur}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{e0, e1}))

	if st.Folded != 1 {
		t.Fatalf("stats = %+v, want one fold", st)
	}
	if current[0].DuplicateOf != "e0" {
		t.Errorf("duplicate_of = %q, want e0 — the end of the chain, not the hidden e1", current[0].DuplicateOf)
	}
}

// Nothing this pass writes may be a chain, and the closure check is the proof.
// A verdict whose canonical it also suppresses is discarded whole rather than
// half-applied.
func TestConsolidateArea_RefusesAVerdictThatSuppressesItsOwnCanonical(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 300)
	b := insight("b", "stock", "claim b", "body b", 200)
	c := insight("c", "stock", "claim c", "body c", 100)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "b",
			Duplicates:  map[string]float64{"b": 1, "c": 0.99},
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1, embText(c): 2,
	}}}
	current := []models.Insight{a, b, c}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.RefusedShape == 0 {
		t.Fatalf("stats = %+v, want the malformed verdict discarded", st)
	}
	// b must not be folded into the canonical the verdict named, which the
	// verdict itself suppressed. The community rule may then make its own
	// decision about these documents — that is the documented fallback — so
	// what this asserts is that nothing CHAINS, below.
	if current[1].DuplicateOf == "b" {
		t.Errorf("b.duplicate_of = %q, pointing at itself", current[1].DuplicateOf)
	}
	byID := map[string]string{}
	for _, ins := range current {
		byID[ins.ID] = ins.DuplicateOf
	}
	for id, target := range byID {
		if target == "" {
			continue
		}
		if byID[target] != "" {
			t.Errorf("%s points at %s, which is itself folded into %s", id, target, byID[target])
		}
		if target == id {
			t.Errorf("%s points at itself", id)
		}
	}
}

// A provider that fails, or panics, must leave the run on the community rule —
// never worse than today, and never half-applied.
func TestConsolidateArea_ProviderFailureFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		stub *stubProvider
	}{
		{"error", &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
			return []agentplugin.ConsolidationVerdict{{CanonicalID: "b", Duplicates: map[string]float64{"a": 1}}},
				errors.New("provider down")
		}}},
		{"panic", &stubProvider{panics: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := insight("a", "stock", "idle units are growing", "the total rose", 100)
			b := insight("b", "stock", "idle units keep growing", "again", 90)
			withProvider(t, tc.stub)
			o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
				embText(a): 0, embText(b): 1,
			}}}
			current := []models.Insight{a, b}
			st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

			if st.Decider != "community" {
				t.Errorf("decider = %q, want community", st.Decider)
			}
			// The community rule still applies: earliest member canonical.
			if current[0].DuplicateOf != "" || current[1].DuplicateOf != "a" {
				t.Errorf("marks = (%q, %q), want the community outcome", current[0].DuplicateOf, current[1].DuplicateOf)
			}
		})
	}
}

// The detail a folded member added survives on the survivor — a fold decides
// which caveats live, so it is not quality-neutral.
func TestConsolidateArea_MergedDescriptionLandsOnACurrentCanonical(t *testing.T) {
	a := insight("a", "stock", "participants spend more", "they spend 2.4x more", 100)
	b := insight("b", "stock", "participants spend much more", "they spend 2.4x more; causality cannot be separated", 90)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "a",
			Duplicates:        map[string]float64{"b": 0.99},
			MergedDescription: "They spend 2.4x more. **Causality cannot be separated.**",
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}
	current := []models.Insight{a, b}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if !strings.Contains(current[0].Description, "Causality cannot be separated") {
		t.Errorf("canonical description = %q; the folded member's caveat was discarded", current[0].Description)
	}
	if current[0].DescriptionMd == "" {
		t.Error("the markdown rendition must be set too, or the dashboard renders the pre-merge text")
	}
}

// An earlier area's text is already recorded in that area's checkpoint and
// cannot be rewritten here. A verdict that asks for one is saying the survivor
// is missing something the folded members carry — so applying the fold without
// the rewrite would be exactly how a fold loses a caveat. The folds are given
// back and both findings stand.
func TestConsolidateArea_RefusesAFoldWhoseSurvivorCannotBeRewritten(t *testing.T) {
	earlier := insight("e1", "replenishment", "overdue orders are rising", "the rate climbed", 500)
	cur := insight("c1", "stock", "overdue orders keep rising", "the rate climbed further; seasonality not separated", 480)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "e1",
			Duplicates:        map[string]float64{"c1": 0.99},
			MergedDescription: "the rate climbed; seasonality not separated",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(cur): 1,
	}}}
	current := []models.Insight{cur}
	runInsights := []models.Insight{earlier}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, runInsights))

	if st.Folded != 0 || st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the cluster given back", st)
	}
	if current[0].DuplicateOf != "" {
		t.Errorf("c1 was folded anyway, losing its caveat: duplicate_of=%q", current[0].DuplicateOf)
	}
	if runInsights[0].Description == "the rate climbed; seasonality not separated" {
		t.Error("an earlier area's description was rewritten")
	}
}

// A fold into an earlier area WITHOUT a rewrite is still applied: nothing is
// being lost, and that is the common and useful case.
func TestConsolidateArea_FoldsIntoAnEarlierAreaWhenNoRewriteIsNeeded(t *testing.T) {
	earlier := insight("e1", "replenishment", "overdue orders are rising", "the rate climbed", 500)
	cur := insight("c1", "stock", "overdue orders keep rising", "the rate climbed further", 480)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "e1", Duplicates: map[string]float64{"c1": 0.99},
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(cur): 1,
	}}}
	current := []models.Insight{cur}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{earlier})); st.Folded != 1 {
		t.Fatalf("stats = %+v, want the fold applied", st)
	}
	if current[0].DuplicateOf != "e1" {
		t.Errorf("duplicate_of = %q, want e1", current[0].DuplicateOf)
	}
}

// Two overlapping stars can legitimately fold different members into the same
// survivor, and each merged description REPLACES its text — so applying the
// second erases the detail the first preserved while both folds stand. The
// first rewrite wins; the second is dropped, because it was written against
// text this pass has already changed.
func TestConsolidateArea_SecondRewriteOfOneSurvivorIsDropped(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i := range cl {
			out[i] = agentplugin.ConsolidationVerdict{
				CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99, "c": 0.99},
				MergedDescription: fmt.Sprintf("rewrite number %d, carrying b's figure", i+1),
			}
		}
		return out, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1, embText(c): 2,
	}}}
	current := []models.Insight{a, b, c}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded != 2 {
		t.Fatalf("stats = %+v, want both folds", st)
	}
	if !strings.Contains(current[0].Description, "rewrite number 1") {
		t.Errorf("a.description = %q, want the first rewrite kept", current[0].Description)
	}
}

// A member the PROVIDER left out of Duplicates is an explicit judgement that
// it states a different claim. Without recording it the end-of-run check folds
// the pair anyway on document order, overriding the one decision this feature
// exists to take.
func TestConsolidateArea_RecordsThePairsTheProviderLeftStanding(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 300)
	b := insight("b", "stock", "claim b", "body b", 200)
	c := insight("c", "stock", "claim c", "body c", 100)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		// b folds into a; c is a different finding.
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99},
			Reason: "c states a different claim",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1, embText(c): 2,
	}}}
	current := []models.Insight{a, b, c}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 1 {
		t.Fatalf("stats = %+v, want one fold", st)
	}
	if st.Kept == 0 {
		t.Fatalf("stats = %+v, want the standing pairs recorded", st)
	}
	for _, pair := range [][2]string{{"c", "a"}, {"c", "b"}} {
		if !o.adjudicatedKeep(pair[0], pair[1]) {
			t.Errorf("pair %v was not recorded; Phase 9 will fold it on document order", pair)
		}
	}
	// And the pair the provider DID fold is not in the keep set.
	if o.adjudicatedKeep("a", "b") {
		t.Error("a folded pair must not also be recorded as standing")
	}
}

// The community fallback's non-folds ARE the end-of-run rule, so recording
// them would change the community outcome rather than preserve it.
func TestConsolidateArea_TheCommunityRuleRecordsNoKeeps(t *testing.T) {
	withProvider(t, nil)

	a := insight("a", "stock", "claim a", "body a", 300)
	b := insight("b", "stock", "claim b", "body b", 200)
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 60,
	}}}
	// Far apart in angle and sharing figures, so containment proposes the
	// pair and the community rule declines to fold it.
	a.Description = "| north | 111,222 | 33.3 |"
	b.Name = "the north segment holds 111,222 idle units, 33.3 of it"
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Kept != 0 {
		t.Errorf("stats = %+v, want no keeps recorded by the community rule", st)
	}
	if o.adjudicatedKeep("a", "b") {
		t.Error("the community rule recorded a keep; the end-of-run check is now frozen out of its own decision")
	}
}

// With no embedding provider the pass still runs: containment needs no model
// at all, and it is the test that finds the duplicate shape similarity cannot
// see. The same holds when the provider errors.
func TestConsolidateArea_RunsWithoutEmbeddings(t *testing.T) {
	table := "| north | 111,222 | 33.3 |"
	a := insight("a", "stock", "idle units across every segment", table, 100)
	b := insight("b", "stock", "the north segment holds 111,222 idle units, 33.3 of it", "slice", 90)
	verdict := func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0},
		}}, nil
	}

	t.Run("no provider at all", func(t *testing.T) {
		withProvider(t, &stubProvider{fn: verdict})
		o := &Orchestrator{projectID: "p"}
		current := []models.Insight{a, b}
		if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded != 1 {
			t.Fatalf("stats = %+v, want the containment fold without any embeddings", st)
		}
		if current[1].SimilarityScore != 0 {
			t.Errorf("score = %v, want 0 when there is no vector to measure", current[1].SimilarityScore)
		}
	})

	t.Run("embedding provider fails", func(t *testing.T) {
		withProvider(t, &stubProvider{fn: verdict})
		emb := &failingEmbedder{}
		o := &Orchestrator{projectID: "p", embeddingProvider: emb}
		current := []models.Insight{a, b}
		if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded != 1 {
			t.Fatalf("stats = %+v, want the containment fold despite the embedding failure", st)
		}
		if emb.calls == 0 {
			t.Error("the pass never tried to embed")
		}
	})
}

// A run embeds each insight exactly once however many times it is compared:
// the pass fills the cache and Phase 9 reads it, so consolidation does not
// change the embedding spend.
func TestConsolidateArea_EmbeddingIsPaidOncePerText(t *testing.T) {
	withProvider(t, nil)

	a := insight("a", "stock", "claim a", "body a", 100)
	b := insight("b", "stock", "claim b", "body b", 90)
	emb := &angleEmbedder{deg: map[string]float64{embText(a): 0, embText(b): 40}}
	o := &Orchestrator{projectID: "p", embeddingProvider: emb}

	current := []models.Insight{a, b}
	o.consolidateArea(context.Background(), areaInput("stock", current, nil))
	firstBatch := len(emb.texts)
	if firstBatch != 2 {
		t.Fatalf("the pass embedded %d texts, want 2", firstBatch)
	}

	// A second area whose candidates include the first area's findings must
	// not re-embed them.
	c := insight("c", "repl", "claim c", "body c", 80)
	emb.deg[embText(c)] = 80
	currentB := []models.Insight{c}
	o.consolidateArea(context.Background(), areaInput("repl", currentB, current))
	if got := len(emb.texts) - firstBatch; got != 1 {
		t.Errorf("the second area embedded %d texts, want 1 (its own); the earlier area's vectors are cached", got)
	}

	// And Phase 9 serves the same texts from the cache.
	vectors, err := o.embedWithCache(context.Background(), []string{embText(a), embText(b), embText(c)}, "angle")
	if err != nil {
		t.Fatalf("embedWithCache: %v", err)
	}
	if len(emb.texts) != firstBatch+1 {
		t.Errorf("Phase 9 re-embedded %d texts that the pass had already paid for", len(emb.texts)-firstBatch-1)
	}
	for i, v := range vectors {
		if len(v) == 0 {
			t.Errorf("vectors[%d] is empty", i)
		}
	}
}

// A text the cache does not hold is still embedded, which is what makes the
// cache correct after a description merge: the merged text is a different
// document and must get its own vector.
func TestEmbedWithCache_MissesAreEmbeddedAndDeduplicated(t *testing.T) {
	emb := &angleEmbedder{deg: map[string]float64{}}
	o := &Orchestrator{embeddingProvider: emb}
	o.cacheInsightVector("cached", []float64{1, 0, 0})

	vectors, err := o.embedWithCache(context.Background(), []string{"cached", "fresh", "fresh"}, "angle")
	if err != nil {
		t.Fatalf("embedWithCache: %v", err)
	}
	if len(vectors) != 3 {
		t.Fatalf("vectors = %d, want 3", len(vectors))
	}
	if len(emb.texts) != 1 || emb.texts[0] != "fresh" {
		t.Errorf("embedded %v, want exactly one call for the one uncached text", emb.texts)
	}
	if vectors[0][0] != 1 {
		t.Errorf("the cached vector was not served: %v", vectors[0])
	}
	if vectors[1] == nil || vectors[2] == nil {
		t.Error("a repeated text must be filled at both positions")
	}
}

// A marked duplicate consumes no validation budget, and the insight after it
// is validated with the budget it did not spend. This is the whole point: the
// 30 checks land on the run's distinct findings instead of its restatements.
func TestValidateInsights_AMarkedDuplicateCostsNoBudget(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 2

	// i5 and i4 are the two most-affected, so they would take the whole
	// budget. Mark i5 a duplicate and i3 should be reached instead.
	for i := range insights {
		if insights[i].ID == "i5" {
			insights[i].DuplicateOf = "i4"
		}
	}

	results, validated, paused := p.validateInsights(context.Background(), insights, steps, "a", 0, nil)
	if paused {
		t.Fatal("paused = true with no pause seam")
	}
	if validated != 2 {
		t.Fatalf("validated = %d, want 2: the duplicate must not be charged", validated)
	}

	byID := make(map[string]valmodels.Status, len(results))
	for _, r := range results {
		byID[r.InsightID] = r.Combined
	}
	if byID["i5"] != valmodels.StatusSkippedDuplicate {
		t.Errorf("i5 = %q, want skipped_duplicate", byID["i5"])
	}
	// The two checks went to the next two by affected count.
	for _, id := range []string{"i4", "i3"} {
		if byID[id] == valmodels.StatusSkippedBudgetCap || byID[id] == valmodels.StatusSkippedDuplicate {
			t.Errorf("%s = %q, want a real verdict", id, byID[id])
		}
	}
	if byID["i2"] != valmodels.StatusSkippedBudgetCap {
		t.Errorf("i2 = %q, want skipped_budget_cap", byID["i2"])
	}
	// Every insight still accounted for, exactly once.
	if len(results) != len(insights) {
		t.Errorf("results = %d, want %d: the counts must reconcile", len(results), len(insights))
	}
	for i := range insights {
		if insights[i].Validation == nil {
			t.Errorf("%s carries no stamped validation", insights[i].ID)
		}
	}
}

// Excluded from Phase 5 on the MARK, not on the verdict. A draft-restored
// insight can arrive carrying `confirmed` from the previous attempt and be
// marked a duplicate on this one — and filtering on the verdict alone would
// also mean suppressing a duplicate's verdict PROMOTED it, since a nil verdict
// fails open.
func TestFilterEligibleInsights_AMarkedDuplicateIsExcludedWhateverItsVerdict(t *testing.T) {
	eligible := map[valmodels.Status]bool{valmodels.StatusConfirmed: true, valmodels.StatusSupported: true}
	all := []models.Insight{
		{ID: "clean", Validation: &valmodels.InsightValidation{Combined: valmodels.StatusConfirmed}},
		{ID: "dup-confirmed", DuplicateOf: "clean",
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusConfirmed}},
		{ID: "dup-no-verdict", DuplicateOf: "clean"},
		{ID: "dup-skipped", DuplicateOf: "clean",
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
		{ID: "no-verdict-at-all"},
	}
	got := filterEligibleInsights(all, eligible)

	var ids []string
	for _, ins := range got {
		ids = append(ids, ins.ID)
	}
	want := []string{"clean", "no-verdict-at-all"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("eligible = %v, want %v", ids, want)
	}
}

// Phase 9 does not re-decide a document the mid-run pass already marked, and
// a document matching one of those marked documents resolves to the FINAL
// canonical rather than to a hidden row.
func TestMarkDuplicates_DoesNotRedecideAConsolidatedDocument(t *testing.T) {
	store := &mockEmbedIndexStore{}
	o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}

	vec := []float64{1, 0, 0}
	candidates := []dedupCandidate{
		{id: "a", docType: "insight", vec: vec, text: "a", magnitude: "affected=1"},
		// Already folded into a by the mid-run pass.
		{id: "b", docType: "insight", vec: vec, text: "b", magnitude: "affected=1", markedBy: "a"},
		{id: "c", docType: "insight", vec: vec, text: "c", magnitude: "affected=1"},
	}
	o.markDuplicates(context.Background(), candidates, nil)

	marks := make(map[string]string, len(store.dupUpdates))
	for _, u := range store.dupUpdates {
		marks[u.ID] = u.DupOf
	}
	if _, remarked := marks["b"]; remarked {
		t.Errorf("b was re-decided by Phase 9: %v", marks)
	}
	if marks["c"] != "a" {
		t.Errorf("c.duplicate_of = %q, want a — the final canonical, not the suppressed b", marks["c"])
	}
}

// A pair the consolidation pass adjudicated and left standing must not be
// folded by Phase 9's own rule, which would apply exactly the direction the
// adjudicator rejected: document order.
func TestMarkDuplicates_HonoursAnAdjudicatedKeep(t *testing.T) {
	store := &mockEmbedIndexStore{}
	o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
	o.keepPair("a", "b")

	vec := []float64{1, 0, 0}
	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", vec: vec, text: "a", magnitude: "affected=1"},
		{id: "b", docType: "insight", vec: vec, text: "b", magnitude: "affected=1"},
	}, nil)

	if len(store.dupUpdates) != 0 {
		t.Errorf("Phase 9 folded an adjudicated pair: %+v", store.dupUpdates)
	}
}

// The standalone copy is what the API filters on, so a mid-run mark has to
// reach it. A mark on the embedded copy alone would hide a finding on the
// dashboard and return it from the API.
func TestDenormalizeInsights_CarriesTheConsolidationMarks(t *testing.T) {
	o := &Orchestrator{projectID: "p"}
	out := o.denormalizeInsights(&models.DiscoveryResult{
		ID: "d1", ProjectID: "p",
		Insights: []models.Insight{
			{ID: "a", Name: "canonical"},
			{ID: "b", Name: "restatement", DuplicateOf: "a", SimilarityScore: 0.93},
		},
	})
	if len(out) != 2 {
		t.Fatalf("out = %d", len(out))
	}
	if out[1].DuplicateOf != "a" || out[1].SimilarityScore != 0.93 {
		t.Errorf("standalone copy = {%q, %v}, want {a, 0.93}", out[1].DuplicateOf, out[1].SimilarityScore)
	}
	if out[0].DuplicateOf != "" {
		t.Errorf("the canonical was marked: %q", out[0].DuplicateOf)
	}
}

// A restore remints every insight id, so a mark carried over verbatim names a
// row this attempt does not hold. What CAN be resolved at restore time is
// re-pointed immediately; what cannot is left alone, because the area order
// comes out of a map and a mark's target may simply not have restored yet.
// settleRunMarks is what decides the rest.
func TestRepointRestoredMarks(t *testing.T) {
	insights := []models.Insight{
		{ID: "fresh-a", Name: "canonical"},
		{ID: "fresh-b", Name: "restatement", DuplicateOf: "stored-a", SimilarityScore: 0.93},
		{ID: "fresh-c", Name: "orphan", DuplicateOf: "gone", SimilarityScore: 0.91,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	repointRestoredMarks(insights, map[string]string{"stored-a": "fresh-a"}, "stock")

	if insights[1].DuplicateOf != "fresh-a" {
		t.Errorf("resolvable mark = %q, want fresh-a", insights[1].DuplicateOf)
	}
	if insights[1].SimilarityScore != 0.93 {
		t.Errorf("a re-pointed mark must keep its score, got %v", insights[1].SimilarityScore)
	}
	// NOT cleared here: the target may belong to an area that has not
	// restored yet, and the order is not predictable.
	if insights[2].DuplicateOf != "gone" {
		t.Errorf("unresolved mark = %q; it must be left for settleRunMarks, not cleared on a partial mapping", insights[2].DuplicateOf)
	}
	if insights[0].DuplicateOf != "" {
		t.Errorf("an unmarked insight was touched: %q", insights[0].DuplicateOf)
	}
}

// A resumed area restores the marks it was consolidated with rather than
// re-deriving them, and calls no provider: the marks live in the area draft,
// which is written on the next line after the pass.
func TestRestoreArea_RestoresTheConsolidationMarksWithoutAProvider(t *testing.T) {
	stub := &stubProvider{}
	withProvider(t, stub)

	stored := models.AnalysisStep{
		AreaID: "stock",
		Insights: []models.Insight{
			{ID: "stored-a", Name: "canonical"},
			{ID: "stored-b", Name: "restatement", DuplicateOf: "stored-a", SimilarityScore: 0.93},
		},
	}
	_, insights, _ := restoreArea(
		models.AnalysisAreaCheckpoint{Step: stored, InsightsValidated: 1},
		nil, nil, map[string]string{})

	if len(insights) != 2 {
		t.Fatalf("restored %d insights", len(insights))
	}
	if insights[1].DuplicateOf != insights[0].ID {
		t.Errorf("restored mark = %q, want the reminted canonical id %q", insights[1].DuplicateOf, insights[0].ID)
	}
	if insights[1].SimilarityScore != 0.93 {
		t.Errorf("restored score = %v, want 0.93", insights[1].SimilarityScore)
	}
	if stub.calls != 0 {
		t.Errorf("the provider was called %d times for a restored area", stub.calls)
	}
}

// The area's own ownership statement reaches the adjudicator, including the
// boundary block a project's prompt declares — ownership predicts the right
// canonical better than document order does.
func TestAreaScopeText(t *testing.T) {
	area := AnalysisArea{ID: "stock", Description: "stock health across the chain"}
	prompt := strings.Join([]string{
		"# Stock health",
		"Investigate the following.",
		"## Not This Area",
		"Order fulfilment belongs to replenishment.",
		"Campaign margin belongs to campaigns.",
		"## Output",
		"Return JSON.",
	}, "\n")

	got := areaScopeText(area, prompt)
	if !strings.Contains(got, "stock health across the chain") {
		t.Errorf("scope = %q, missing the area description", got)
	}
	if !strings.Contains(got, "Order fulfilment belongs to replenishment.") {
		t.Errorf("scope = %q, missing the declared boundary", got)
	}
	if strings.Contains(got, "Return JSON") {
		t.Errorf("scope = %q, ran past the boundary block into the next heading", got)
	}

	if got := areaScopeText(area, "no boundary block here"); got != "stock health across the chain" {
		t.Errorf("scope with no block = %q", got)
	}
}

// A merged description must not land on a canonical that nothing was folded
// into. Gating it on the run-wide fold counter applied one cluster's rewrite
// to the next cluster's canonical as soon as any earlier cluster had folded
// something.
func TestConsolidateArea_MergedDescriptionNeedsAFoldInItsOwnCluster(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b restated", "body a again", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 2)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			switch {
			case slices.Contains(ids, "a") && slices.Contains(ids, "b"):
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99},
					MergedDescription: "a, with b's detail",
				}
			case slices.Contains(ids, "c") && slices.Contains(ids, "d"):
				// Nothing folded, but a rewrite offered anyway.
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "c", MergedDescription: "c, rewritten for no reason",
				}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1, embText(c): 60, embText(d): 61,
	}}}
	current := []models.Insight{a, b, c, d}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded != 1 {
		t.Fatalf("stats = %+v, want exactly one fold", st)
	}
	if !strings.Contains(current[0].Description, "b's detail") {
		t.Errorf("a.description = %q; the cluster that DID fold must get its merge", current[0].Description)
	}
	if strings.Contains(current[2].Description, "rewritten") {
		t.Errorf("c.description = %q; nothing was folded into c, so its text must be untouched", current[2].Description)
	}
}

// Which test proposed a cluster is read from the relations INSIDE it.
//
// Two overlapping maximal stars are what tells the two readings apart. Here
// a-b and b-c are containment and only c-d is a similarity pair, so the
// {a,b,c} cluster was proposed by containment ALONE — but c also sits in
// {b,c,d}, and reading every relation that touches a member reported {a,b,c}
// as "both". That field is the one thing that says whether the similarity
// threshold could have seen this duplicate at all, so a containment-only
// cluster reported as "both" is the measurement reading as its own opposite.
func TestBuildClusters_ProposalComesFromTheClustersOwnRelations(t *testing.T) {
	// Angles: only c and d are inside the 0.88 floor of each other.
	docs := []consolidationDoc{
		doc(0, "a", 0, true, "111,222 and 333,444", ""),
		doc(1, "b", 30, true, "555,666 and 777,888", "ref 111,222 and 333,444"),
		doc(2, "c", 60, true, "claim c", "ref 555,666 and 777,888"),
		doc(3, "d", 61, true, "claim d", ""),
	}
	rel := proposePairs(docs, 0.88)
	if got := rel[newPairKey(0, 1)]; got != agentplugin.ProposalContainment {
		t.Fatalf("a-b = %q, want containment (fixture broken)", got)
	}
	if got := rel[newPairKey(1, 2)]; got != agentplugin.ProposalContainment {
		t.Fatalf("b-c = %q, want containment (fixture broken)", got)
	}
	if got := rel[newPairKey(2, 3)]; got != agentplugin.ProposalCosine {
		t.Fatalf("c-d = %q, want cosine (fixture broken)", got)
	}
	if len(rel) != 3 {
		t.Fatalf("relations = %v, want exactly the three (fixture broken)", rel)
	}

	byMembers := map[string]agentplugin.Proposal{}
	for _, cl := range buildClusters(docs, rel) {
		byMembers[fmt.Sprint(cl.members)] = cl.proposal
	}
	if len(byMembers) != 2 {
		t.Fatalf("clusters = %v, want the two overlapping stars", byMembers)
	}
	if got := byMembers["[0 1 2]"]; got != agentplugin.ProposalContainment {
		t.Errorf("{a,b,c} proposal = %q, want containment — c's similarity pair with d is not in this cluster", got)
	}
	if got := byMembers["[1 2 3]"]; got != agentplugin.ProposalBoth {
		t.Errorf("{b,c,d} proposal = %q, want both", got)
	}
}

// The switch an operator needs when they want the previous behaviour back.
// The similarity floor is a threshold and cannot express "do not consolidate",
// and the containment test does not read it at all.
func TestConsolidateArea_CanBeTurnedOff(t *testing.T) {
	withProvider(t, nil)
	t.Setenv(consolidationEnabledEnv, "false")

	a := insight("a", "stock", "idle units are growing", "the total rose", 100)
	b := insight("b", "stock", "idle units keep growing", "again", 90)
	emb := &angleEmbedder{deg: map[string]float64{embText(a): 0, embText(b): 1}}
	o := &Orchestrator{projectID: "p", embeddingProvider: emb}

	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 0 || st.Folded != 0 {
		t.Errorf("stats = %+v, want nothing done", st)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b was marked %q with the pass switched off", current[1].DuplicateOf)
	}
	if emb.calls != 0 {
		t.Errorf("the pass embedded %d batches with the switch off", emb.calls)
	}
	if st.persisted() != nil {
		t.Error("a pass that did nothing must record nothing on the run log")
	}
}

// settleRunMarks is the authority, run once the whole loop has finished. The
// case it exists for: the run's analysis areas come out of a MAP, so an area
// that folded into another can restore BEFORE it, and clearing on the spot
// threw away a good mark whose target had not registered yet.
func TestSettleRunMarks(t *testing.T) {
	// b restored first, carrying a mark into a — which restores after it.
	allInsights := []models.Insight{
		{ID: "fresh-b", AnalysisArea: "repl", DuplicateOf: "stored-a", SimilarityScore: 0.93},
		{ID: "fresh-a", AnalysisArea: "stock"},
		{ID: "fresh-c", AnalysisArea: "stock", DuplicateOf: "never-existed", SimilarityScore: 0.91,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
		{ID: "fresh-d", AnalysisArea: "stock", DuplicateOf: "never-existed", SimilarityScore: 0.9,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusConfirmed}},
	}
	// The step copies, which are a separate copy by the time the loop has
	// appended them: settling one and not the other would leave the result and
	// the record disagreeing about which findings are visible.
	analysisLog := []models.AnalysisStep{
		{AreaID: "repl", Insights: []models.Insight{
			{ID: "fresh-b", DuplicateOf: "stored-a", SimilarityScore: 0.93},
		}},
		{AreaID: "stock", Insights: []models.Insight{
			{ID: "fresh-a"},
			{ID: "fresh-c", DuplicateOf: "never-existed", SimilarityScore: 0.91},
		}},
	}

	repointed, cleared := settleRunMarks(analysisLog, allInsights, map[string]string{"stored-a": "fresh-a"})

	if allInsights[0].DuplicateOf != "fresh-a" {
		t.Errorf("mark = %q, want fresh-a: its target registered after its own area restored", allInsights[0].DuplicateOf)
	}
	if allInsights[0].SimilarityScore != 0.93 {
		t.Errorf("a re-pointed mark must keep its score, got %v", allInsights[0].SimilarityScore)
	}
	if analysisLog[0].Insights[0].DuplicateOf != "fresh-a" {
		t.Errorf("the step copy was not settled: %q", analysisLog[0].Insights[0].DuplicateOf)
	}

	// A mark into a row this run genuinely does not hold is cleared, and the
	// verdict that existed only because of it goes with it: a visible finding
	// stamped "Restated" is a restatement of nothing.
	if allInsights[2].DuplicateOf != "" || allInsights[2].SimilarityScore != 0 {
		t.Errorf("unresolvable mark = {%q, %v}, want cleared", allInsights[2].DuplicateOf, allInsights[2].SimilarityScore)
	}
	if allInsights[2].Validation != nil {
		t.Errorf("validation = %+v, want cleared with the mark", allInsights[2].Validation)
	}
	if analysisLog[1].Insights[1].DuplicateOf != "" {
		t.Errorf("the step copy's unresolvable mark was not cleared: %q", analysisLog[1].Insights[1].DuplicateOf)
	}
	// A verdict the verifier actually produced is NOT touched.
	if allInsights[3].Validation == nil || allInsights[3].Validation.Combined != valmodels.StatusConfirmed {
		t.Errorf("a real verdict was cleared: %+v", allInsights[3].Validation)
	}
	if repointed != 2 || cleared != 3 {
		t.Errorf("repointed=%d cleared=%d, want 2 and 3 across both copies", repointed, cleared)
	}
	// An unmarked insight is never touched.
	if allInsights[1].DuplicateOf != "" {
		t.Errorf("an unmarked insight was given a mark: %q", allInsights[1].DuplicateOf)
	}
}

// An insight id is the MODEL's when the model supplies one — the parse mints a
// UUID only for an insight that arrived without an `id` — so two restored
// areas can hand back the same stored id. Last-writer-wins pointed a mark at
// whichever area restored second, which is a reader sent to an unrelated
// finding while the one they asked for stays hidden. The entry is poisoned
// instead: the fold is lost and both findings stay visible, which is the
// recoverable direction.
func TestRegisterRestoredIDs_ASharedStoredIDResolvesNowhere(t *testing.T) {
	freshByStored := map[string]string{}
	registerRestoredIDs(freshByStored, map[string]string{"fresh-1": "shared", "fresh-x": "its-own"}, "stock")
	registerRestoredIDs(freshByStored, map[string]string{"fresh-2": "shared"}, "repl")

	if got := freshByStored["shared"]; got != ambiguousStoredID {
		t.Errorf("freshByStored[shared] = %q, want the entry poisoned", got)
	}
	// A stored id only one area claims is untouched.
	if got := freshByStored["its-own"]; got != "fresh-x" {
		t.Errorf("freshByStored[its-own] = %q, want fresh-x", got)
	}
	// Re-registering the SAME mapping is not a collision — an area restored
	// twice in one loop would otherwise poison its own ids.
	registerRestoredIDs(freshByStored, map[string]string{"fresh-x": "its-own"}, "stock")
	if got := freshByStored["its-own"]; got != "fresh-x" {
		t.Errorf("an identical re-registration poisoned the entry: %q", got)
	}
	// A third claimant does not un-poison one.
	registerRestoredIDs(freshByStored, map[string]string{"fresh-3": "shared"}, "margin")
	if got := freshByStored["shared"]; got != ambiguousStoredID {
		t.Errorf("freshByStored[shared] = %q after a third claim, want it still poisoned", got)
	}

	// And nothing resolves through a poisoned entry: not the restore-time
	// re-point, which leaves the mark for the end-of-run settle...
	insights := []models.Insight{
		{ID: "fresh-2", DuplicateOf: "shared", SimilarityScore: 0.93,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	repointRestoredMarks(insights, freshByStored, "repl")
	if insights[0].DuplicateOf != "shared" {
		t.Errorf("mark = %q; a poisoned entry must not re-point a mark", insights[0].DuplicateOf)
	}
	// ...nor the settle itself, which clears it rather than pointing a
	// reader at the sentinel.
	repointed, cleared := settleRunMarks(nil, insights, freshByStored)
	if insights[0].DuplicateOf != "" || insights[0].Validation != nil {
		t.Errorf("settled mark = {%q, %+v}, want cleared", insights[0].DuplicateOf, insights[0].Validation)
	}
	if repointed != 0 || cleared != 1 {
		t.Errorf("repointed=%d cleared=%d, want 0 and 1", repointed, cleared)
	}
}

// A restored insight's mark names an id from a PREVIOUS attempt, and an
// insight id is the model's when it supplies one — so an area this attempt
// analysed afresh can hand back that very id for an unrelated finding.
// Matching a live id is therefore evidence for a mark THIS attempt's
// consolidation pass wrote, and a coincidence for one that came out of a
// checkpoint.
func TestSettleRunMarks_ARestoredMarkWillNotRideACoincidence(t *testing.T) {
	// What this attempt minted for the areas it RESTORED, which is the only
	// provenance available: the values are those ids.
	freshByStored := map[string]string{
		"stored-a": "fresh-a", "stored-r": "fresh-r", "stored-s": "fresh-s",
	}
	allInsights := []models.Insight{
		// Restored, its mark already translated at restore time to another
		// restored insight. Provenance; kept.
		{ID: "fresh-r", AnalysisArea: "repl", DuplicateOf: "fresh-a", SimilarityScore: 0.93},
		{ID: "fresh-a", AnalysisArea: "repl"},
		// Restored, its mark still naming a previous attempt's id — which a
		// freshly analysed area has handed back for a different finding, so
		// it matches a live id. Cleared: the finding is visible again.
		{ID: "fresh-s", AnalysisArea: "stock", DuplicateOf: "reused-id", SimilarityScore: 0.91,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
		{ID: "reused-id", AnalysisArea: "margin", Name: "an unrelated finding"},
		// Freshly analysed and marked by this attempt's pass against an id it
		// is holding. Trusted, and the mark is into the same row.
		{ID: "fresh-live", AnalysisArea: "margin", DuplicateOf: "reused-id", SimilarityScore: 0.95},
	}

	repointed, cleared := settleRunMarks(nil, allInsights, freshByStored)

	if allInsights[0].DuplicateOf != "fresh-a" {
		t.Errorf("a restored mark into a restored target = %q, want fresh-a kept", allInsights[0].DuplicateOf)
	}
	if allInsights[2].DuplicateOf != "" || allInsights[2].SimilarityScore != 0 {
		t.Errorf("a restored mark onto a reused id = {%q, %v}, want cleared", allInsights[2].DuplicateOf, allInsights[2].SimilarityScore)
	}
	if allInsights[2].Validation != nil {
		t.Errorf("the skip verdict outlived its mark: %+v", allInsights[2].Validation)
	}
	if allInsights[4].DuplicateOf != "reused-id" || allInsights[4].SimilarityScore != 0.95 {
		t.Errorf("this attempt's own mark = {%q, %v}, want it left alone", allInsights[4].DuplicateOf, allInsights[4].SimilarityScore)
	}
	if repointed != 0 || cleared != 1 {
		t.Errorf("repointed=%d cleared=%d, want 0 and 1", repointed, cleared)
	}
}

// A document another finding is already folded INTO cannot be suppressed by
// the end-of-run check. A mid-run mark B -> A is already written and
// persisted, so folding A into an earlier run's X would leave B pointing at a
// hidden row — and re-pointing B at X is not possible there, because X belongs
// to another run and its vector is not in hand to re-verify B against.
func TestMarkDuplicates_DoesNotFoldADocumentThatIsAlreadyACanonical(t *testing.T) {
	store := &mockEmbedIndexStore{}
	vs := &mockVectorStore{dupes: []vectorstore.SearchResult{{ID: "from-an-earlier-run", Score: 0.97}}}
	o := &Orchestrator{embedIndexStore: store, vectorStore: vs}

	vec := []float64{1, 0, 0}
	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", vec: vec, text: "a", magnitude: "affected=1"},
		{id: "b", docType: "insight", vec: vec, text: "b", magnitude: "affected=1", markedBy: "a"},
	}, nil)

	for _, u := range store.dupUpdates {
		if u.ID == "a" {
			t.Errorf("a is b's canonical and was folded into %q; b now points at a hidden row", u.DupOf)
		}
	}
}

// A cluster whose folds are given back because the survivor cannot be
// rewritten must leave no trace in the chain map. Left in, a later cluster
// resolving its canonical through that map points a real fold at the wrong
// document, and the pair the branch just refused is read as folded — so no
// keep is recorded and the end-of-run check folds it anyway.
func TestConsolidateArea_AGivenBackFoldLeavesNoTrace(t *testing.T) {
	earlier := insight("e1", "repl", "overdue orders are rising", "the rate climbed", 500)
	c1 := insight("c1", "stock", "overdue orders keep rising", "the rate climbed; seasonality not separated", 480)
	c2 := insight("c2", "stock", "overdue orders still rising", "the rate climbed again", 470)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			if slices.Contains(ids, "e1") {
				// Needs a rewrite of an earlier area's text: refused.
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "e1", Duplicates: map[string]float64{"c1": 0.99},
					MergedDescription: "the rate climbed; seasonality not separated",
				}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(c1): 1, embText(c2): 2,
	}}}
	current := []models.Insight{c1, c2}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{earlier}))

	if st.Folded != 0 || st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the cluster given back", st)
	}
	for i := range current {
		if current[i].DuplicateOf != "" {
			t.Errorf("%s = %q, want visible", current[i].ID, current[i].DuplicateOf)
		}
	}
	// The refused pair is recorded, so the end-of-run check does not fold it
	// without the rewrite this branch declined to make.
	if !o.adjudicatedKeep("c1", "e1") {
		t.Error("the refused pair was not recorded; Phase 9 will fold it on document order")
	}
}

// A fold given back must give back the bucket it was counted in, or Folded
// reads 0 beside a non-zero by_containment and the one number the log line
// exists to make legible does not add up.
func TestConsolidateArea_AGivenBackFoldGivesBackItsBucket(t *testing.T) {
	earlier := insight("e1", "repl", "overdue orders are rising", "the rate climbed", 500)
	cur := insight("c1", "stock", "overdue orders keep rising", "the rate climbed; seasonality not separated", 480)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "e1", Duplicates: map[string]float64{"c1": 0.99},
			MergedDescription: "needs a rewrite of an earlier area's text",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(cur): 1,
	}}}
	st := o.consolidateArea(context.Background(), areaInput("stock", []models.Insight{cur}, []models.Insight{earlier}))

	if st.Folded != 0 {
		t.Fatalf("stats = %+v, want no folds", st)
	}
	if st.ByCosine+st.ByContainment+st.ByBoth != st.Folded {
		t.Errorf("stats = %+v; the per-test buckets must sum to Folded", st)
	}
}

// A second rewrite of the same survivor means the second cluster's folds go
// back too, not just its rewrite. Keeping the folds would suppress the second
// cluster's members without the detail the rewrite was asked for — the caveat
// loss this pass exists to stop making, arriving through the back door.
//
// Two overlapping maximal stars sharing one survivor is the shape: a chain of
// four documents 25° apart gives {a,b,c} and {b,c,d}, and c is a legitimate
// canonical in both.
func TestConsolidateArea_ASecondRewriteGivesBackItsClustersFolds(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b carries a figure", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d carries the caveat", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			switch {
			case slices.Contains(ids, "a"):
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "c", Duplicates: map[string]float64{"b": 0.906},
					MergedDescription: "c, carrying b's figure",
				}
			case slices.Contains(ids, "d"):
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "c", Duplicates: map[string]float64{"d": 0.906},
					MergedDescription: "c, carrying d's caveat",
				}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50, embText(d): 75,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 2 {
		t.Fatalf("clusters = %d (%+v), want the two overlapping stars", st.Clusters, st)
	}
	byID := map[string]models.Insight{}
	for _, ins := range current {
		byID[ins.ID] = ins
	}
	if byID["b"].DuplicateOf != "c" {
		t.Errorf("b.duplicate_of = %q, want c — the first cluster's fold and rewrite stand", byID["b"].DuplicateOf)
	}
	if byID["d"].DuplicateOf != "" {
		t.Errorf("d.duplicate_of = %q; its rewrite could not be applied, so its fold must have been given back", byID["d"].DuplicateOf)
	}
	if st.RefusedUnmergeable != 1 {
		t.Errorf("stats = %+v, want one cluster given back", st)
	}
	if st.Folded != 1 || st.ByCosine != 1 {
		t.Errorf("stats = %+v, want exactly the one fold that kept its rewrite", st)
	}
	// d's caveat is not hidden behind text that does not mention it.
	if !strings.Contains(byID["c"].Description, "b's figure") {
		t.Errorf("c.description = %q, want the first rewrite", byID["c"].Description)
	}
}

// A merge carries the folded members' evidence with it. The verifier builds
// its bundle from the survivor's SourceSteps, so a merged claim whose step the
// survivor does not cite would be checked against evidence that does not
// contain it — and attachSourceQuality would miss the caveat that step
// carries.
func TestConsolidateArea_AMergeCarriesTheEvidenceWithIt(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 300)
	a.SourceSteps = []int{1, 3}
	b := insight("b", "stock", "claim b", "body b", 200)
	b.SourceSteps = []int{7, 3}

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99},
			MergedDescription: "a, carrying b's figure from step 7",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}
	current := []models.Insight{a, b}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if got := fmt.Sprint(current[0].SourceSteps); got != "[1 3 7]" {
		t.Errorf("canonical source steps = %s, want [1 3 7]: deduplicated, ordered, and carrying step 7", got)
	}
	// A fold with NO rewrite states nothing new, so it must not widen the
	// survivor's provenance.
	if got := fmt.Sprint(mergeSourceSteps([]int{1}, current, nil)); got != "[1]" {
		t.Errorf("mergeSourceSteps with nothing folded = %s, want [1]", got)
	}
}

// A mark into a restored area is STORED by the id the run's checkpoints hold,
// not the one this attempt minted — otherwise a third attempt, which can only
// translate the original id, resolves it against nothing and clears a fold
// that was perfectly good.
func TestWithCheckpointStableMarks(t *testing.T) {
	step := models.AnalysisStep{AreaID: "stock", Insights: []models.Insight{
		{ID: "live-b", DuplicateOf: "live-a", SimilarityScore: 0.93},
		{ID: "live-c"},
		{ID: "live-d", DuplicateOf: "live-c"},
	}}
	stored := withCheckpointStableMarks(step, map[string]string{"live-a": "checkpoint-a"})

	if stored.Insights[0].DuplicateOf != "checkpoint-a" {
		t.Errorf("stored mark = %q, want the checkpoint's id", stored.Insights[0].DuplicateOf)
	}
	// An intra-area target is not in the map, so it is stored as it stands —
	// which IS the id the checkpoint holds for it.
	if stored.Insights[2].DuplicateOf != "live-c" {
		t.Errorf("intra-area mark = %q, want live-c", stored.Insights[2].DuplicateOf)
	}
	// And the LIVE slice is untouched: the in-memory mark must keep naming
	// the live id, or the dashboard renders a suppression pointing at an id no
	// row carries.
	if step.Insights[0].DuplicateOf != "live-a" {
		t.Errorf("the live mark was rewritten to %q", step.Insights[0].DuplicateOf)
	}
	// Nothing to translate: the same step back, no copy.
	if got := withCheckpointStableMarks(step, nil); got.Insights[0].DuplicateOf != "live-a" {
		t.Errorf("with no mapping the step must come back unchanged, got %q", got.Insights[0].DuplicateOf)
	}
}

// Each call's prompt is bounded by the layer's input window, but the NUMBER of
// calls grows with the cluster count — so without a bound a pathological area
// could spend several times what the analysis call that produced it did. The
// clusters past the bound are not dropped: they get the community rule, which
// is the decision the product makes today.
func TestConsolidateArea_BoundsWhatGoesToTheAdjudicator(t *testing.T) {
	t.Setenv(consolidationMaxClustersEnv, "1")

	// Two DISJOINT pairs, so the cluster past the bound is independent of the
	// one the adjudicator saw and its community decision is observable.
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b restated", "body a again", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d restated", "body c again", 100)

	stub := &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			out[i] = agentplugin.ConsolidationVerdict{CanonicalID: cluster.Candidates[0].ID}
		}
		return out, nil
	}}
	withProvider(t, stub)

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1, embText(c): 90, embText(d): 91,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 2 {
		t.Fatalf("clusters = %d, want the two disjoint pairs (fixture broken)", st.Clusters)
	}
	if st.OverBound != 1 {
		t.Errorf("stats = %+v, want one cluster past the bound", st)
	}
	if len(stub.gotFirst) != 1 {
		t.Errorf("the adjudicator saw %d clusters, want 1", len(stub.gotFirst))
	}
	// The cluster past the bound is not dropped: it gets the community rule,
	// which folds the later member of a similarity pair into the earlier.
	if current[3].DuplicateOf != "c" {
		t.Errorf("d.duplicate_of = %q, want c — the cluster past the bound must still be decided", current[3].DuplicateOf)
	}
	// And the adjudicated cluster's own verdict stands: the stub named a as
	// canonical and folded nothing, so b is left visible.
	if current[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q; the adjudicator folded nothing in that cluster", current[1].DuplicateOf)
	}
}

// A non-positive bound means no bound, which is a deliberate operator choice:
// the pass is already bounded per call by the layer's input window.
func TestConsolidationMaxClusters_ZeroMeansUnbounded(t *testing.T) {
	t.Setenv(consolidationMaxClustersEnv, "0")
	if got := consolidationMaxClusters(); got > 0 {
		t.Errorf("bound = %d, want non-positive for an explicit 0", got)
	}
	t.Setenv(consolidationMaxClustersEnv, "")
	if got := consolidationMaxClusters(); got != defaultConsolidationMaxClusters {
		t.Errorf("bound = %d, want the default %d", got, defaultConsolidationMaxClusters)
	}
}

// Containment is a PROPOSAL the adjudicator rules on, never a community rule.
// Letting the community fallback satisfy its re-check by containment folds
// pairs the community rule would never have folded — a finding losing its
// verdict and its place in Phase 5 on a rule the deployment never opted into.
func TestReverifyFold_ContainmentIsOnlyForAnAdjudicatedCluster(t *testing.T) {
	table := "| north | 111,222 | 33.3 |"
	dup := doc(1, "b", 70, true, "the north segment holds 111,222 units, 33.3 of it", "")
	canonical := doc(0, "a", 0, true, "the whole total", table)

	if _, held := reverifyFold(dup, canonical, 0.88, true); !held {
		t.Error("an adjudicated fold must be allowed to hold on containment")
	}
	if _, held := reverifyFold(dup, canonical, 0.88, false); held {
		t.Error("the community rule must not fold on containment; it is a proposal, not a rule")
	}
	// A cosine pair holds either way.
	near := doc(2, "c", 1, true, "claim c", "")
	if _, held := reverifyFold(near, canonical, 0.88, false); !held {
		t.Error("the community rule must still fold a similarity pair")
	}
}

// A survivor whose prose carries rendered evidence figures cannot be
// rewritten: the figure template holds the text as authored with the
// references intact, and free-text replacement severs that link — so a
// repaired insight's figures are all withheld from the recommender, and on an
// unrepaired one a recommendation could restate a number the prose no longer
// shows.
func TestConsolidateArea_RefusesARewriteOfAFigureBearingSurvivor(t *testing.T) {
	a := insight("a", "stock", "claim a", "the rate is {{f1}}", 300)
	a.Figures = []models.Figure{{ID: "f1", Value: 142, Unit: "units"}}
	b := insight("b", "stock", "claim b", "the rate again; and a caveat", 200)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99},
			MergedDescription: "the rate is 142 units; and a caveat",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 0 || st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the cluster given back", st)
	}
	if current[0].Description != "the rate is {{f1}}" {
		t.Errorf("a.description = %q; the figure references were overwritten", current[0].Description)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b was folded anyway, losing the caveat: %q", current[1].DuplicateOf)
	}

	// A survivor with NO declared figures is still rewritten: nothing links
	// its prose to a number another document may restate.
	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "x", Duplicates: map[string]float64{"y": 0.99},
			MergedDescription: "x, with y's caveat",
		}}, nil
	}})
	x := insight("x", "stock", "claim x", "body x", 300)
	y := insight("y", "stock", "claim y", "body x again; and a caveat", 200)
	o2 := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(x): 0, embText(y): 1,
	}}}
	plain := []models.Insight{x, y}
	if st := o2.consolidateArea(context.Background(), areaInput("stock", plain, nil)); st.Folded != 1 {
		t.Fatalf("stats = %+v, want the fold and the rewrite applied", st)
	}
	if !strings.Contains(plain[0].Description, "y's caveat") {
		t.Errorf("x.description = %q, want the rewrite", plain[0].Description)
	}
}

// A fallback verdict for one cluster must not override a provider keep
// recorded for another: the clusters are overlapping stars, so the same pair
// can appear in a cluster the provider answered and one it declined — and the
// end-of-run check cannot correct it, because it skips a document that already
// carries a mark.
func TestConsolidateArea_AFallbackDoesNotOverrideAProviderKeep(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			// Answer only the cluster holding a: b and c are distinct.
			if slices.Contains(ids, "a") {
				out[i] = agentplugin.ConsolidationVerdict{CanonicalID: "a", Reason: "b and c are different findings"}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50, embText(d): 75,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if !o.adjudicatedKeep("b", "c") {
		t.Fatalf("the provider's keep was not recorded: %+v", st)
	}
	if current[2].DuplicateOf != "" {
		t.Errorf("c.duplicate_of = %q; a fallback verdict overrode the provider's keep", current[2].DuplicateOf)
	}
	// Asserted as an OUTCOME, not as a refusal counter: the community pass
	// consults the keeps while it builds its chain, so a kept pair is never
	// proposed in the first place rather than proposed and refused.
	if current[1].DuplicateOf == "c" || current[2].DuplicateOf == "b" {
		t.Errorf("b/c = (%q, %q); the provider kept them apart", current[1].DuplicateOf, current[2].DuplicateOf)
	}
}

// The keeps are registered BEFORE any mark is written, and that ordering is
// the fix. The clusters are overlapping stars processed in order, so a cluster
// the provider DECLINED can be processed first and its community fallback
// folds a pair a provider verdict for a later, overlapping cluster explicitly
// keeps — and discovering the keep afterwards is useless, because the mark is
// already written and the end-of-run check will not revisit a marked document.
func TestConsolidateArea_AProviderKeepBeatsAnEarlierFallbackFold(t *testing.T) {
	// {a,b,c} then {b,c,d}, with a attached to b by CONTAINMENT only — so the
	// fallback for the first cluster would fold c into b, which is exactly the
	// pair the provider's verdict for the second cluster keeps apart. The
	// first cluster is processed first, so the keep has to be known before it.
	table := "| north | 111,222 | 33.3 |"
	a := insight("a", "stock", "the whole total", table, 400)
	b := insight("b", "stock", "the north segment holds 111,222 units, 33.3 of it", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			if slices.Contains(ids, "d") {
				out[i] = agentplugin.ConsolidationVerdict{CanonicalID: "b", Reason: "all three are distinct"}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 150, embText(b): 0, embText(c): 25, embText(d): 50,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 2 {
		t.Fatalf("clusters = %d (%+v), want the two overlapping stars (fixture broken)", st.Clusters, st)
	}
	if !o.adjudicatedKeep("b", "c") {
		t.Fatalf("the provider's keep was not registered: %+v", st)
	}
	byID := map[string]models.Insight{}
	for _, ins := range current {
		byID[ins.ID] = ins
	}
	if byID["c"].DuplicateOf != "" {
		t.Errorf("c.duplicate_of = %q; the fallback for the first cluster folded a pair the provider kept", byID["c"].DuplicateOf)
	}
	if byID["b"].DuplicateOf == "c" {
		t.Errorf("b.duplicate_of = %q; the provider kept b and c apart", byID["b"].DuplicateOf)
	}
	_ = st
}

// Two overlapping verdicts can disagree about one pair — folded in one, simply
// unnamed in the other. An explicit fold beats an omission: the fold is a
// positive judgement, while a member a verdict did not mention may just be a
// member it did not mention. Without that precedence the second cluster's
// silence vetoed the first cluster's decision.
func TestConsolidateArea_AnExplicitFoldBeatsAnOmission(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			switch {
			case slices.Contains(ids, "a"):
				// Explicitly folds b into c.
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "c", Duplicates: map[string]float64{"b": 0.906},
				}
			case slices.Contains(ids, "d"):
				// Names c as canonical and never mentions b.
				out[i] = agentplugin.ConsolidationVerdict{CanonicalID: "c", Reason: "d is distinct"}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50, embText(d): 75,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if current[1].DuplicateOf != "c" {
		t.Errorf("b.duplicate_of = %q, want c — the explicit fold must not be vetoed by the other cluster's silence", current[1].DuplicateOf)
	}
	if st.Folded != 1 {
		t.Errorf("stats = %+v, want the one explicit fold", st)
	}
}

// A rewrite may incorporate detail from a member this pass refused to fold, so
// applying it would credit the survivor with a claim whose evidence it does
// not cite — and the provenance union cannot carry what was never folded. The
// whole cluster goes back rather than half of it.
func TestConsolidateArea_ARefusedDonorGivesBackTheWholeCluster(t *testing.T) {
	earlier := insight("e1", "repl", "the broad claim", "body e1", 500)
	c1 := insight("c1", "stock", "the broad claim restated", "body e1 again", 400)
	c2 := insight("c2", "stock", "a narrower slice", "body c2", 300)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		// e1 cannot be suppressed (earlier area); c2 can. The rewrite claims
		// to carry both donors' detail.
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "c1",
			Duplicates:        map[string]float64{"e1": 0.95, "c2": 0.95},
			MergedDescription: "c1, carrying e1's and c2's detail",
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(c1): 1, embText(c2): 2,
	}}}
	current := []models.Insight{c1, c2}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{earlier}))

	if st.Folded != 0 || st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the whole cluster given back", st)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("c2.duplicate_of = %q; it was suppressed behind a rewrite that credits a donor nobody folded", current[1].DuplicateOf)
	}
	if strings.Contains(current[0].Description, "carrying") {
		t.Errorf("c1.description = %q, want the rewrite refused", current[0].Description)
	}
}

// The community rule is ONE pass in document order, not a per-cluster
// decision. Per cluster carried state that depended on which overlapping star
// was processed first, and it also had to MATCH what the end-of-run check
// would have made — the promise to a community deployment is that only the
// moment of the decision moves.
func TestCommunityFolds_IsTheEndOfRunRuleInDocumentOrder(t *testing.T) {
	// a, b, c pairwise similar; d far from all of them.
	docs := []consolidationDoc{
		doc(0, "a", 0, true, "claim a", ""),
		doc(1, "b", 1, true, "claim b", ""),
		doc(2, "c", 2, true, "claim c", ""),
		doc(3, "d", 90, true, "claim d", ""),
	}
	o := &Orchestrator{}
	folds := o.communityFolds(docs, 0.88, nil, nil)

	if folds["b"].target != "a" || folds["c"].target != "a" {
		t.Errorf("folds = %+v; both later members must point at the earliest, not at each other", folds)
	}
	if _, ok := folds["a"]; ok {
		t.Error("the earliest member must not be folded")
	}
	if _, ok := folds["d"]; ok {
		t.Error("an unrelated finding must not be folded")
	}

	// The non-transitive chain: 0/25/50 scores 0.906 between neighbours and
	// 0.643 end to end, so the last must NOT be folded into the first.
	chain := []consolidationDoc{
		doc(0, "x", 0, true, "claim x", ""),
		doc(1, "y", 25, true, "claim y", ""),
		doc(2, "z", 50, true, "claim z", ""),
	}
	cf := o.communityFolds(chain, 0.88, nil, nil)
	if cf["y"].target != "x" {
		t.Errorf("y folded into %q, want x", cf["y"].target)
	}
	if f, ok := cf["z"]; ok {
		t.Errorf("z folded into %q; it resembles y but not x, so it is a duplicate of nothing visible", f.target)
	}

	// A document already folded by a previous area's pass is not re-decided,
	// and a match against it resolves to the canonical a reader can see.
	seeded := []consolidationDoc{
		doc(0, "p", 0, true, "claim p", ""),
		doc(1, "q", 1, true, "claim q", ""),
		doc(2, "r", 2, true, "claim r", ""),
	}
	seeded[1].duplicateOf = "p"
	sf := o.communityFolds(seeded, 0.88, map[string]string{"q": "p"}, nil)
	if _, ok := sf["q"]; ok {
		t.Error("an already-folded document must not be re-decided")
	}
	if sf["r"].target != "p" {
		t.Errorf("r folded into %q, want p — the canonical a reader can see", sf["r"].target)
	}
}

// Two overlapping containment stars whose middle members are all similar to
// each other: per cluster, the first star made one of them a canonical and the
// second then refused to fold it into the earlier member it actually restates,
// leaving two redundant findings standing and spending the budget on both.
func TestConsolidateArea_OverlappingStarsStillFoldInDocumentOrder(t *testing.T) {
	withProvider(t, nil)

	// b, c, d are mutually similar. a and e are broad findings that contain
	// them by figure, which is what joins them into overlapping stars.
	table := "| north | 111,222 | 33.3 |\n| south | 444,555 | 66.6 |"
	a := insight("a", "stock", "the whole total", table, 500)
	b := insight("b", "stock", "the north segment holds 111,222 units, 33.3 of it", "slice b", 400)
	c := insight("c", "stock", "north restated: 111,222 units at 33.3", "slice c", 300)
	d := insight("d", "stock", "north once more: 111,222 units, 33.3", "slice d", 200)
	e := insight("e", "stock", "the whole total again", table, 100)

	oc := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 90, embText(c): 91, embText(d): 92, embText(e): 180,
	}}}
	current := []models.Insight{a, b, c, d, e}
	st := oc.consolidateArea(context.Background(), areaInput("stock", current, nil))

	byID := map[string]models.Insight{}
	for _, ins := range current {
		byID[ins.ID] = ins
	}
	// b is the earliest of the three similar findings, so c and d fold into
	// it — whichever stars they happened to land in.
	if byID["c"].DuplicateOf != "b" {
		t.Errorf("c.duplicate_of = %q, want b", byID["c"].DuplicateOf)
	}
	if byID["d"].DuplicateOf != "b" {
		t.Errorf("d.duplicate_of = %q, want b", byID["d"].DuplicateOf)
	}
	if byID["b"].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q; it is the earliest of the three", byID["b"].DuplicateOf)
	}
	// The community rule folds on similarity only, so the broad findings
	// stand: containment is a proposal an adjudicator rules on.
	for _, id := range []string{"a", "e"} {
		if byID[id].DuplicateOf != "" {
			t.Errorf("%s.duplicate_of = %q; the community rule must not fold a containment relation", id, byID[id].DuplicateOf)
		}
	}
	if st.Folded != 2 {
		t.Errorf("stats = %+v, want two folds", st)
	}
}

// Phase 9 must check the keep against the target it RESOLVED to, not only the
// candidate it matched. A chain hides the pair otherwise: with b folded into a
// and the adjudicator having kept a and c apart, matching c against b resolves
// back to a and suppresses c anyway.
func TestMarkDuplicates_HonoursAKeepAgainstTheResolvedTarget(t *testing.T) {
	store := &mockEmbedIndexStore{}
	o := &Orchestrator{embedIndexStore: store, vectorStore: &mockVectorStore{}}
	o.keepPair("a", "c")

	vec := []float64{1, 0, 0}
	o.markDuplicates(context.Background(), []dedupCandidate{
		{id: "a", docType: "insight", vec: vec, text: "a", magnitude: "affected=1"},
		{id: "b", docType: "insight", vec: vec, text: "b", magnitude: "affected=1"},
		{id: "c", docType: "insight", vec: vec, text: "c", magnitude: "affected=1"},
	}, nil)

	for _, u := range store.dupUpdates {
		if u.ID == "c" {
			t.Errorf("c was folded into %q; the keep against a was routed around through b", u.DupOf)
		}
	}
}

// An operator clicking Validate on a finding the dashboard shows as restated
// has already decided to spend the check. The run-wide duplicate skip exists
// so a RUN's fixed budget is not spent on restatements; applied to a
// single-document request it made the request a permanent no-op that
// overwrote whatever verdict the document had with the skip stamp.
func TestValidateInsights_ASingleDocumentRequestIgnoresTheDuplicateSkip(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 6
	for i := range insights {
		insights[i].DuplicateOf = "some-canonical"
	}

	// The run path skips every one of them and charges nothing.
	results, validated, _ := p.validateInsights(context.Background(), insights, steps, "a", 0, nil)
	if validated != 0 {
		t.Errorf("validated = %d in a run, want 0: the budget must not be spent on restatements", validated)
	}
	for _, r := range results {
		if r.Combined != valmodels.StatusSkippedDuplicate {
			t.Errorf("%s = %q, want skipped_duplicate", r.InsightID, r.Combined)
		}
	}

	// On request, the same documents are checked.
	p.onRequest = true
	one := []models.Insight{insights[0]}
	one[0].Validation = nil
	onRequest, validatedOnRequest, _ := p.validateInsights(context.Background(), one, steps, "a", 0, nil)
	if validatedOnRequest != 1 {
		t.Fatalf("validated = %d on request, want 1", validatedOnRequest)
	}
	if len(onRequest) != 1 || onRequest[0].Combined == valmodels.StatusSkippedDuplicate {
		t.Errorf("verdict = %+v; an explicit request must actually run the agents", onRequest[0].Combined)
	}
}

// A rewrite naming a donor this pass did not fold — because a previous area
// had already folded it — credits the survivor with detail whose evidence the
// provenance union cannot carry.
func TestConsolidateArea_AnAlreadyFoldedDonorRefusesTheRewrite(t *testing.T) {
	e0 := insight("e0", "repl", "the canonical statement", "body e0", 600)
	e1 := insight("e1", "repl", "a restatement of it", "body e1", 500)
	e1.DuplicateOf = "e0" // folded by its own area's pass
	c1 := insight("c1", "stock", "another restatement", "body c1", 400)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		// Folds e1 (already folded) and c1 into the current-area canonical,
		// with a rewrite claiming both donors' detail.
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "c1",
			Duplicates:        map[string]float64{"e1": 0.99},
			MergedDescription: "c1, carrying e1's detail",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(e0): 0, embText(e1): 1, embText(c1): 2,
	}}}
	current := []models.Insight{c1}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{e0, e1}))

	if strings.Contains(current[0].Description, "carrying") {
		t.Errorf("c1.description = %q; the rewrite credits a donor nobody folded", current[0].Description)
	}
	if st.Folded != 0 {
		t.Errorf("stats = %+v, want no fold", st)
	}
}

// The community chain is built only from folds this pass could actually WRITE.
// Simulating a fold a keep forbids made a later document's chain re-check
// resolve against a canonical the reader is NOT being sent to — so a document
// that plainly restates the visible middle finding was left standing and spent
// a validation check, before the end-of-run rule folded it there anyway.
func TestCommunityFolds_DoesNotChainThroughAForbiddenFold(t *testing.T) {
	// 0°/25°/50°: neighbours at 0.906, ends at 0.643.
	docs := []consolidationDoc{
		doc(0, "a", 0, true, "claim a", ""),
		doc(1, "b", 25, true, "claim b", ""),
		doc(2, "c", 50, true, "claim c", ""),
	}

	// With nothing kept, b folds into a and c then fails the re-check
	// against a — which is correct, because a is where the mark points.
	plain := &Orchestrator{}
	if f := plain.communityFolds(docs, 0.88, nil, nil); f["b"].target != "a" {
		t.Fatalf("folds = %+v, want b into a (fixture broken)", f)
	} else if _, ok := f["c"]; ok {
		t.Fatalf("folds = %+v; c resembles b but not a (fixture broken)", f)
	}

	// With a and b kept apart, b is NOT folded — so b stays visible, and c
	// plainly restates it.
	kept := &Orchestrator{}
	kept.keepPair("a", "b")
	folds := kept.communityFolds(docs, 0.88, nil, nil)
	if _, ok := folds["b"]; ok {
		t.Errorf("folds = %+v; a and b were kept apart", folds)
	}
	if folds["c"].target != "b" {
		t.Errorf("c folded into %q, want b — the visible finding it restates", folds["c"].target)
	}
}

// A document this pass cannot mark contributes nothing to the chain either:
// its mark would never be written, so a later document must not resolve
// through it.
func TestCommunityFolds_IgnoresDocumentsItCannotMark(t *testing.T) {
	docs := []consolidationDoc{
		doc(0, "earlier", 0, false, "claim a", ""),
		doc(1, "b", 25, true, "claim b", ""),
		doc(2, "c", 50, true, "claim c", ""),
	}
	docs[0].current = false

	o := &Orchestrator{}
	folds := o.communityFolds(docs, 0.88, nil, nil)
	if _, ok := folds["earlier"]; ok {
		t.Errorf("folds = %+v; an earlier area's finding cannot be marked by this pass", folds)
	}
	// b may still fold INTO it: an earlier area's finding is a legitimate
	// canonical.
	if folds["b"].target != "earlier" {
		t.Errorf("b folded into %q, want earlier", folds["b"].target)
	}
}

// When EVERY proposed fold is refused there is nothing to revert — and the
// members the verdict named still carry detail the survivor lacks, so they
// still have to be protected from being folded sideways into each other by
// the community pass.
func TestConsolidateArea_RefusedMergeDonorsAreProtectedWithNothingToRevert(t *testing.T) {
	// a is the survivor the verdict names. It joins the star because ITS
	// headline figures appear in b's and c's bodies — containment the other
	// way round — so neither b nor c is contained in a, and with a 90° gap
	// neither fold survives the re-check either.
	a := insight("a", "stock", "the totals are 111,222 and 444,555", "body a", 400)
	b := insight("b", "stock", "claim b", "ref 111,222 and 444,555", 300)
	c := insight("c", "stock", "claim c carries the caveat", "ref 111,222 and 444,555 again", 200)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "a",
			Duplicates:        map[string]float64{"b": 0.9, "c": 0.9},
			MergedDescription: "a, carrying b's and c's detail",
		}}, nil
	}})

	// b and c are a similarity pair; neither is near a.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 90, embText(c): 91,
	}}}
	current := []models.Insight{a, b, c}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 1 {
		t.Fatalf("clusters = %d (%+v), want one star of three (fixture broken)", st.Clusters, st)
	}
	if st.RefusedRecheck != 2 {
		t.Fatalf("stats = %+v, want both proposed folds refused by the re-check (fixture broken)", st)
	}
	if st.Folded != 0 {
		t.Fatalf("stats = %+v, want nothing folded", st)
	}
	if current[2].DuplicateOf != "" {
		t.Errorf("c.duplicate_of = %q; it was folded sideways into b, losing the caveat the rewrite was for", current[2].DuplicateOf)
	}
	if !o.adjudicatedKeep("b", "c") {
		t.Error("the refused donors were not protected against each other")
	}
	if strings.Contains(current[0].Description, "carrying") {
		t.Errorf("a.description = %q; the rewrite was applied with nothing folded", current[0].Description)
	}
}

// The community pass must see the marks the PROVIDER just wrote, not the ones
// the documents arrived with. With the provider folding b into the later c and
// declining the overlapping cluster, reading the arrival marks left the
// simulation thinking b was visible: it folded c into b, and d then failed its
// re-check against b — while c, which d plainly restates, was visible the
// whole time. d spent a validation check and could reach Phase 5 before the
// end-of-run rule folded it into c anyway.
func TestConsolidateArea_TheCommunityPassSeesTheProvidersMarks(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			// Answers {a,b,c} with b folded into the LATER c, and declines
			// {b,c,d}.
			if slices.Contains(ids, "a") {
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "c", Duplicates: map[string]float64{"b": 0.906},
				}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50, embText(d): 75,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	byID := map[string]models.Insight{}
	for _, ins := range current {
		byID[ins.ID] = ins
	}
	if byID["b"].DuplicateOf != "c" {
		t.Fatalf("b.duplicate_of = %q, want c (the provider's own decision)", byID["b"].DuplicateOf)
	}
	if byID["d"].DuplicateOf != "c" {
		t.Errorf("d.duplicate_of = %q, want c — the visible finding it restates at 0.906", byID["d"].DuplicateOf)
	}
	if byID["c"].DuplicateOf != "" {
		t.Errorf("c.duplicate_of = %q; it is the survivor the provider chose", byID["c"].DuplicateOf)
	}
	if st.Folded != 2 {
		t.Errorf("stats = %+v, want both folds", st)
	}
}

// A rewrite is written against the text of the document the verdict NAMED. If
// that document was itself folded, the mark resolves somewhere else — and
// applying the rewrite to the resolved survivor overwrites ITS caveats with a
// rewrite of someone else's prose. The named document need not even have been
// in this cluster's payload.
func TestConsolidateArea_RefusesARewriteRedirectedToAnotherCanonical(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a, with a caveat of its own", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			switch {
			case slices.Contains(ids, "a"):
				// b folds into a, with NO rewrite — so mergedCanonicals does
				// not know about a.
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "a", Duplicates: map[string]float64{"b": 0.906},
				}
			case slices.Contains(ids, "d"):
				// Names b, which is now folded into a, and offers a rewrite
				// of b's text.
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "b", Duplicates: map[string]float64{"d": 0.906},
					MergedDescription: "b's text, rewritten",
				}
			}
		}
		return out, nil
	}})

	d := insight("d", "stock", "claim d", "body d", 100)
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50, embText(d): 75,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if current[0].Description != "body a, with a caveat of its own" {
		t.Errorf("a.description = %q; it was overwritten with a rewrite of b's prose", current[0].Description)
	}
	if st.RefusedUnmergeable == 0 {
		t.Errorf("stats = %+v, want the redirected rewrite refused", st)
	}
}

// A donor's figures were rendered into the DONOR's prose against {{fN}}
// references the model never saw and cannot reproduce. Transferring the
// declarations leaves figures with no placeholder in the survivor's text;
// leaving them behind puts the number in the survivor's prose with nothing a
// recommendation can reference it by, while the donor that owns it is
// suppressed. The donor stays visible instead.
func TestConsolidateArea_RefusesARewriteThatWouldOrphanADonorsFigures(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 300)
	b := insight("b", "stock", "claim b", "the rate is {{f1}}", 200)
	b.Figures = []models.Figure{{ID: "f1", Value: 142, Unit: "units"}}

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99},
			MergedDescription: "a, and the rate is 142 units",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 0 || st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the cluster given back", st)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b was suppressed; its figure is now unreferenceable while its number is in a's prose")
	}
	if strings.Contains(current[0].Description, "142") {
		t.Errorf("a.description = %q; the rewrite states a number no figure backs", current[0].Description)
	}

	// A fold with NO rewrite is unaffected: the survivor's text is untouched,
	// so it states nothing needing a figure of its own.
	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0.99},
		}}, nil
	}})
	plain := []models.Insight{a, b}
	o2 := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}
	if st := o2.consolidateArea(context.Background(), areaInput("stock", plain, nil)); st.Folded != 1 {
		t.Errorf("stats = %+v; a fold with no rewrite must still apply", st)
	}
}

// A rewritten survivor loses its cosine projection, and keeping it was not
// conservative as an earlier comment claimed: a rewrite can REDUCE similarity
// below the floor, and the community pass would go on folding against the
// pre-merge vector — suppressing a finding that no longer restates this one.
// Containment still works, over the new body; Phase 9 embeds the merged text
// and decides on a real vector.
func TestConsolidateArea_ARewrittenSurvivorLosesItsVector(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	// c is similar to a as it stands, and is in an overlapping cluster.
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			if slices.Contains(ids, "a") {
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "b", Duplicates: map[string]float64{"a": 0.906},
					MergedDescription: "b, carrying a's detail — and now about something else entirely",
				}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 25, embText(c): 50, embText(d): 75,
	}}}
	current := []models.Insight{a, b, c, d}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, nil)); st.Folded == 0 {
		t.Fatalf("stats = %+v, want the provider's fold applied (fixture broken)", st)
	}
	if !strings.Contains(current[1].Description, "something else entirely") {
		t.Fatalf("b.description = %q, want the rewrite applied (fixture broken)", current[1].Description)
	}
	// c must not have been folded into the rewritten b on the strength of a
	// vector describing b's old text.
	if current[2].DuplicateOf == "b" {
		t.Error("c was folded into a rewritten survivor using the pre-merge vector")
	}

	// And the projection itself: a rewritten document offers no cosine answer.
	docs := []consolidationDoc{
		doc(0, "x", 0, true, "claim x", "body x"),
		doc(1, "y", 1, true, "claim y", "body y"),
	}
	docs[0].vec = nil
	if _, ok := cosineFold(docs[1].foldSubject, docs[0].foldSubject, 0.88); ok {
		t.Error("a document with no vector must offer no cosine fold")
	}
	if _, held := reverifyFold(docs[1], docs[0], 0.88, true); held {
		t.Error("the re-check must refuse a cosine fold into a document with no vector")
	}
}

// Clearing a restored mark has to clear the validation-log entry that existed
// only because of it. The step's ValidationResults are persisted by the
// split-log writer and served by the validation-results endpoint, so leaving
// the entry behind labelled a finding that is visible again, and was never
// checked, as Restated — on the one surface an operator goes to in order to
// ask what WAS checked.
func TestSettleRunMarks_ClearsTheSkipOnlyValidationLogEntry(t *testing.T) {
	allInsights := []models.Insight{
		{ID: "fresh-c", AnalysisArea: "stock", DuplicateOf: "never-existed",
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
		{ID: "fresh-d", AnalysisArea: "stock"},
	}
	analysisLog := []models.AnalysisStep{{
		AreaID: "stock",
		Insights: []models.Insight{
			{ID: "fresh-c", DuplicateOf: "never-existed",
				Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
			{ID: "fresh-d"},
		},
		ValidationResults: []models.ValidationResult{
			{InsightID: "fresh-c", Combined: valmodels.StatusSkippedDuplicate},
			{InsightID: "fresh-d", Combined: valmodels.StatusConfirmed},
		},
	}}

	settleRunMarks(analysisLog, allInsights, nil)

	ids := map[string]valmodels.Status{}
	for _, r := range analysisLog[0].ValidationResults {
		ids[r.InsightID] = r.Combined
	}
	if _, present := ids["fresh-c"]; present {
		t.Errorf("results = %v; the skip entry outlived the mark it came from", ids)
	}
	if ids["fresh-d"] != valmodels.StatusConfirmed {
		t.Errorf("results = %v; a real verdict was removed", ids)
	}
}

// A skip entry carrying agent detail is a verdict an agent actually produced,
// and it belongs in the log whatever happened to the mark.
func TestDropSkippedDuplicateResult_KeepsAnythingWithAgentDetail(t *testing.T) {
	withDetail := []models.ValidationResult{
		{InsightID: "a", Combined: valmodels.StatusSkippedDuplicate, Verifier: &valmodels.StructuredVerdict{}},
		{InsightID: "b", Combined: valmodels.StatusSkippedDuplicate},
	}
	got := dropSkippedDuplicateResult(withDetail, "a")
	if len(got) != 2 {
		t.Errorf("results = %+v; an entry with verifier detail must be kept", got)
	}
	if got := dropSkippedDuplicateResult(withDetail, ""); len(got) != 2 {
		t.Errorf("an empty id must remove nothing, got %+v", got)
	}
}

// The magnitude guard gates BOTH acceptance paths. That it did not was a hole
// this feature opened rather than inherited: sameClaim refuses a fold where
// the two texts are IDENTICAL and the figures differ — 100 affected entities
// hidden behind 1,000 — and containment reached its acceptance without passing
// it, so two otherwise identical findings were folded whenever the headline's
// figures appeared in the other's body.
func TestReverifyFold_TheMagnitudeGuardGatesContainmentToo(t *testing.T) {
	table := "| north | 111,222 | 33.3 |"
	canonical := doc(0, "a", 0, true, "the whole total", table)
	dup := doc(1, "b", 70, true, "the north segment holds 111,222 units, 33.3 of it", "")

	// Different texts: containment decides, and it holds.
	if _, held := reverifyFold(dup, canonical, 0.88, true); !held {
		t.Fatal("an ordinary containment fold must hold (fixture broken)")
	}

	// Identical text, different magnitudes: the guard's own case. The
	// headline's figures are still in the body, so containment would accept
	// it on its own.
	same := dup
	same.text = canonical.text
	same.magnitude = "affected=1000"
	canonical.magnitude = "affected=100"
	if _, held := reverifyFold(same, canonical, 0.88, true); held {
		t.Error("identical text with a different magnitude must not fold, by containment or otherwise")
	}
	// And the same pair with EQUAL magnitudes still folds, so the guard has
	// not simply switched containment off.
	same.magnitude = canonical.magnitude
	if _, held := reverifyFold(same, canonical, 0.88, true); !held {
		t.Error("identical text with the same magnitude must still fold")
	}
}

// A containment fold is justified by figures found in the survivor's body, and
// a rewrite is free text that can drop them. The closure check cannot catch it
// — it verifies where marks POINT, not whether the claim still holds — and the
// end-of-run check skips a document that already carries a mark, so the
// finding would stay suppressed and unvalidated with its claim stated nowhere.
func TestConsolidateArea_RefusesARewriteThatDropsAFoldsJustification(t *testing.T) {
	table := "| north | 111,222 | 33.3 |\n| south | 444,555 | 66.6 |"
	a := insight("a", "stock", "the whole total", table, 300)
	b := insight("b", "stock", "the north segment holds 111,222 units, 33.3 of it", "a slice", 200)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0},
			// A rewrite that keeps the south row and drops the north one —
			// the row that justified b's fold.
			MergedDescription: "the whole total\n\n| south | 444,555 | 66.6 |",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 70,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the rewrite refused", st)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q; it is suppressed behind a survivor that no longer states its claim", current[1].DuplicateOf)
	}
	if strings.Contains(current[0].Description, "south") && !strings.Contains(current[0].Description, "north") {
		t.Errorf("a.description = %q; the rewrite was applied", current[0].Description)
	}

	// A rewrite that KEEPS the row is applied, so the guard is not refusing
	// every merge on a containment fold.
	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a", Duplicates: map[string]float64{"b": 0},
			MergedDescription: table + "\n\nand a caveat b carried",
		}}, nil
	}})
	keeps := []models.Insight{a, b}
	o2 := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 70,
	}}}
	if st := o2.consolidateArea(context.Background(), areaInput("stock", keeps, nil)); st.Folded != 1 {
		t.Fatalf("stats = %+v; a rewrite that keeps the justification must be applied", st)
	}
	if !strings.Contains(keeps[0].Description, "caveat b carried") {
		t.Errorf("a.description = %q, want the rewrite", keeps[0].Description)
	}
}

// The rewrite is checked against EVERY fold into the survivor, not just the
// ones this verdict folded. The survivor can already carry a fold from an
// overlapping cluster of the same pass — and a rewrite that keeps this
// cluster's figures while dropping that earlier dependant's leaves it hidden
// behind a survivor which no longer states its claim.
func TestConsolidateArea_ARewriteIsCheckedAgainstEveryExistingFold(t *testing.T) {
	// `broad` contains both slices by figure; n and s are the slices; mid
	// joins the two stars so they overlap.
	table := "| north | 111,222 | 33.3 |\n| south | 444,555 | 66.6 |"
	broad := insight("broad", "stock", "the whole total", table, 500)
	n := insight("n", "stock", "the north segment holds 111,222 units, 33.3 of it", "slice n", 400)
	s := insight("s", "stock", "the south segment holds 444,555 units, 66.6 of it", "slice s", 300)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i, cluster := range cl {
			ids := make([]string, 0, 3)
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			// One cluster holds both slices. Fold n with no rewrite, then s
			// with a rewrite that keeps only the south row.
			if slices.Contains(ids, "n") && slices.Contains(ids, "s") {
				out[i] = agentplugin.ConsolidationVerdict{
					CanonicalID: "broad", Duplicates: map[string]float64{"n": 0, "s": 0},
					MergedDescription: "the whole total\n\n| south | 444,555 | 66.6 |",
				}
			}
		}
		return out, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(broad): 0, embText(n): 90, embText(s): 170,
	}}}
	current := []models.Insight{broad, n, s}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.RefusedUnmergeable != 1 {
		t.Fatalf("stats = %+v, want the rewrite refused: it drops the row n's fold rests on", st)
	}
	if strings.Contains(current[0].Description, "south") && !strings.Contains(current[0].Description, "north") {
		t.Errorf("broad.description = %q; the rewrite was applied", current[0].Description)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("n.duplicate_of = %q; its claim is stated nowhere in the rewritten survivor", current[1].DuplicateOf)
	}

	// A dependant THIS verdict did not fold — a previous area's, say — is
	// refused outright, whichever way its own fold was justified: the rewrite
	// was written from this cluster's payload and does not contain that
	// finding's text at all.
	docs := []consolidationDoc{
		doc(0, "broad", 0, true, "the whole total", table),
		doc(1, "earlier", 90, false, "the north segment holds 111,222 units, 33.3 of it", ""),
	}
	marks := map[string]string{"earlier": "broad"}
	kept := insightBodyOf("the whole total\n\n"+table, "", nil)
	if !rewriteBreaksAFold(kept, docs[0], docs, marks, nil, 0.88) {
		t.Error("a dependant this verdict did not fold must refuse the rewrite")
	}
	// Folded by THIS verdict, and the rewrite keeps the row its containment
	// rests on: applied.
	mine := map[string]float64{"earlier": 0}
	if rewriteBreaksAFold(kept, docs[0], docs, marks, mine, 0.88) {
		t.Error("a rewrite that keeps the justification of its own fold must not be refused")
	}
	dropped := insightBodyOf("the whole total\n\n| south | 444,555 | 66.6 |", "", nil)
	if !rewriteBreaksAFold(dropped, docs[0], docs, marks, mine, 0.88) {
		t.Error("a rewrite that drops the row its own fold rests on must be refused")
	}
	// A cosine-justified dependant of THIS verdict needs no further check:
	// the adjudicator saw its text and the rewrite is what carries it.
	near := []consolidationDoc{
		doc(0, "a", 0, true, "claim a", "body a"),
		doc(1, "b", 1, true, "claim b", "body b"),
	}
	if rewriteBreaksAFold("anything at all", near[0], near, map[string]string{"b": "a"}, map[string]float64{"b": 0.99}, 0.88) {
		t.Error("a similarity-justified fold of this verdict's own must not be re-checked against the prose")
	}
}
