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
		out[i] = append(unit(0), 0)
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

// A pair between the two floors is PROPOSED — that is what the adjudication
// floor is for — but the community fallback must not fold it. Folding it with
// no model in the loop would fold pairs the fold floor exists to exclude: a
// finding losing its verdict and its place in Phase 5 on a rule the deployment
// never opted into.
func TestConsolidateArea_CommunityRuleLeavesASubFoldFloorPairAlone(t *testing.T) {
	withProvider(t, nil)

	a := insight("a", "stock", "idle units across every segment", "the whole total", 100)
	b := insight("b", "stock", "the north segment holds its share of the idle units", "a slice of the same total", 90)
	// 32 degrees apart is about 0.848: above the adjudication floor, below the
	// fold floor.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 32,
	}}}

	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 1 {
		t.Fatalf("clusters = %d, want 1: the adjudication floor must still PROPOSE the pair", st.Clusters)
	}
	if st.Folded != 0 {
		t.Fatalf("folded = %d, want 0: the community rule must not fold below the fold floor", st.Folded)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q, want empty", current[1].DuplicateOf)
	}
}

// The pair the adjudication floor exists to carry: close enough to be worth a
// judgement, not close enough for a threshold to settle. The provider folds it
// and the fold is recorded.
func TestConsolidateArea_ProviderFoldsASubFoldFloorPair(t *testing.T) {
	a := insight("a", "stock", "idle units across every segment", "the whole total", 100)
	b := insight("b", "stock", "the north segment holds its share of the idle units", "a slice", 90)

	stub := &stubProvider{fn: func(c []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a",
			Duplicates:  map[string]float64{"b": 0.79},
			Reason:      "b states one row of a's table",
		}}, nil
	}}
	withProvider(t, stub)

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 32,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 1 || st.ByCosine != 1 {
		t.Fatalf("stats = %+v, want one similarity fold", st)
	}
	if current[1].DuplicateOf != "a" {
		t.Errorf("b.duplicate_of = %q, want a", current[1].DuplicateOf)
	}
	if st.Decider != "stub-consolidator" {
		t.Errorf("decider = %q, want the provider's name", st.Decider)
	}
	if len(stub.gotFirst) != 1 || len(stub.gotFirst[0].Candidates) != 2 {
		t.Fatalf("the provider was shown %+v, want one cluster of two", stub.gotFirst)
	}
	if stub.gotFirst[0].Proposal != agentplugin.ProposalCosine {
		t.Errorf("proposal = %q, want cosine", stub.gotFirst[0].Proposal)
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

// Without vectors the pass proposes nothing, and that is a deliberate
// consequence of handing the judgement to the model: similarity is how a pair
// reaches it, and there is no longer a text scan that works without one.
//
// It degrades to a no-op rather than to a guess, which is the safe direction —
// nothing is folded, so nothing is hidden. An embedding outage costs the run
// its consolidation, not its findings.
func TestConsolidateArea_WithoutEmbeddingsProposesNothing(t *testing.T) {
	a := insight("a", "stock", "idle units across every segment", "the whole total", 100)
	b := insight("b", "stock", "the north segment holds its share", "slice", 90)
	verdict := func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		t.Error("the provider must not be called when nothing was proposed")
		return nil, nil
	}

	t.Run("no embedding provider at all", func(t *testing.T) {
		withProvider(t, &stubProvider{fn: verdict})
		o := &Orchestrator{projectID: "p"}
		current := []models.Insight{a, b}
		st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))
		if st.Clusters != 0 || st.Folded != 0 {
			t.Fatalf("stats = %+v, want nothing proposed and nothing folded", st)
		}
		if current[1].DuplicateOf != "" {
			t.Errorf("b.duplicate_of = %q, want empty", current[1].DuplicateOf)
		}
	})

	t.Run("embedding provider fails", func(t *testing.T) {
		withProvider(t, &stubProvider{fn: verdict})
		emb := &failingEmbedder{}
		o := &Orchestrator{projectID: "p", embeddingProvider: emb}
		current := []models.Insight{a, b}
		st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))
		if st.Clusters != 0 || st.Folded != 0 {
			t.Fatalf("stats = %+v, want nothing proposed and nothing folded", st)
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
			{ID: "c", Name: "kept by an adjudicator", StandsOnAdjudication: true},
		},
	})
	if len(out) != 3 {
		t.Fatalf("out = %d", len(out))
	}
	// The refusal the mid-run pass recorded on the finding has to reach the
	// standalone copy too: the end-of-run check reads it from there, and
	// without it that check folds a finding an adjudicator said must stand.
	if !out[2].StandsOnAdjudication {
		t.Error("the standalone copy lost the adjudicator's refusal")
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
	repointRestoredMarks(insights, restoredIDsFor(map[string]string{"stored-a": "fresh-a"}), "stock")

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
		nil, nil, newRestoredIDs())

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
	ids := newRestoredIDs()
	// repl restores first, carrying a mark into an insight of stock's — which
	// restores after it, so the mark is still the stored id at this point.
	repl := []models.Insight{
		{ID: "fresh-b", AnalysisArea: "repl", DuplicateOf: "stored-a", SimilarityScore: 0.93},
	}
	restoreOneArea(ids, "repl", map[string]string{"fresh-b": "stored-b"}, repl)
	// stock restores next: fresh-a is the canonical repl was waiting for, and
	// two of its own insights point at a row no attempt of this run holds.
	stock := []models.Insight{
		{ID: "fresh-a", AnalysisArea: "stock"},
		{ID: "fresh-c", AnalysisArea: "stock", DuplicateOf: "never-existed", SimilarityScore: 0.91,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
		{ID: "fresh-d", AnalysisArea: "stock", DuplicateOf: "never-existed", SimilarityScore: 0.9,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusConfirmed}},
	}
	restoreOneArea(ids, "stock", map[string]string{
		"fresh-a": "stored-a", "fresh-c": "stored-c", "fresh-d": "stored-d",
	}, stock)

	allInsights := append(append([]models.Insight{}, repl...), stock...)
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

	repointed, cleared := settleRunMarks(analysisLog, allInsights, ids)

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

// restoredIDsFor builds the bookkeeping a resume would have produced from a
// stored-to-fresh mapping, with no marks recorded.
func restoredIDsFor(freshByStored map[string]string) *restoredIDs {
	ids := newRestoredIDs()
	for stored, fresh := range freshByStored {
		ids.register(map[string]string{fresh: stored}, "test")
	}
	return ids
}

// restoreOneArea is what a restore does to the run's id bookkeeping: remint,
// register, then re-point whatever can be resolved so far. The tests that
// care about marks go through it rather than hand-writing a mark, because the
// stored target it records on the way past is what settleRunMarks decides
// from.
//
// `storedByFresh` is this area's remap as reidentifyRestoredArea would have
// produced it, fresh id to the id the checkpoint holds.
func restoreOneArea(ids *restoredIDs, areaID string, storedByFresh map[string]string, insights []models.Insight) {
	ids.register(storedByFresh, areaID)
	repointRestoredMarks(insights, ids, areaID)
}

// An insight id is the MODEL's when the model supplies one — the parse mints a
// UUID only for an insight that arrived without an `id` — so two restored
// areas can hand back the same stored id. Last-writer-wins pointed a mark at
// whichever area restored second, which is a reader sent to an unrelated
// finding while the one they asked for stays hidden. The entry is poisoned
// instead: the fold is lost and both findings stay visible, which is the
// recoverable direction.
func TestRestoredIDs_ASharedStoredIDResolvesNowhere(t *testing.T) {
	ids := newRestoredIDs()
	ids.register(map[string]string{"fresh-1": "shared", "fresh-x": "its-own"}, "stock")
	ids.register(map[string]string{"fresh-2": "shared"}, "repl")

	if _, ok := ids.resolve("shared"); ok {
		t.Error("a stored id two areas claim must resolve nowhere")
	}
	// A stored id only one area claims is untouched.
	if fresh, ok := ids.resolve("its-own"); !ok || fresh != "fresh-x" {
		t.Errorf("resolve(its-own) = %q, %v; want fresh-x, true", fresh, ok)
	}
	// Re-registering the SAME mapping is not a collision — an area restored
	// twice in one loop would otherwise poison its own ids.
	ids.register(map[string]string{"fresh-x": "its-own"}, "stock")
	if fresh, ok := ids.resolve("its-own"); !ok || fresh != "fresh-x" {
		t.Errorf("an identical re-registration poisoned the entry: %q, %v", fresh, ok)
	}
	// A third claimant does not un-poison one.
	ids.register(map[string]string{"fresh-3": "shared"}, "margin")
	if _, ok := ids.resolve("shared"); ok {
		t.Error("a third claim un-poisoned the entry")
	}

	// And nothing resolves through a poisoned entry: not the restore-time
	// re-point, which leaves the mark for the end-of-run settle...
	insights := []models.Insight{
		{ID: "fresh-2", DuplicateOf: "shared", SimilarityScore: 0.93,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	repointRestoredMarks(insights, ids, "repl")
	if insights[0].DuplicateOf != "shared" {
		t.Errorf("mark = %q; a poisoned entry must not re-point a mark", insights[0].DuplicateOf)
	}
	// ...nor the settle itself, which clears it rather than pointing a
	// reader at the sentinel.
	repointed, cleared := settleRunMarks(nil, insights, ids)
	if insights[0].DuplicateOf != "" || insights[0].Validation != nil {
		t.Errorf("settled mark = {%q, %+v}, want cleared", insights[0].DuplicateOf, insights[0].Validation)
	}
	if repointed != 0 || cleared != 1 {
		t.Errorf("repointed=%d cleared=%d, want 0 and 1", repointed, cleared)
	}
}

// A restored insight's mark names an id from a PREVIOUS attempt, and an
// insight id is the model's when it supplies one — so an area this attempt
// analysed afresh can hand back that very id for an unrelated finding. A
// mark out of a checkpoint is therefore decided from the stored target it
// arrived with, and a live row carrying that same string is not evidence of
// anything.
func TestSettleRunMarks_ARestoredMarkWillNotRideACoincidence(t *testing.T) {
	ids := newRestoredIDs()
	// One restored area, whose insight restates a canonical from a previous
	// attempt. That canonical's area is NOT restored this time: this attempt
	// re-analysed it, and it handed back the same id for a different finding.
	restored := []models.Insight{
		{ID: "fresh-s", AnalysisArea: "stock", DuplicateOf: "reused-id", SimilarityScore: 0.91,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	restoreOneArea(ids, "stock", map[string]string{"fresh-s": "stored-s"}, restored)

	allInsights := append([]models.Insight{}, restored...)
	allInsights = append(allInsights,
		// The freshly analysed area that reused the id, and one of its own
		// insights marked by THIS attempt's pass against an id it was
		// holding. The second is trusted; the first is a coincidence.
		models.Insight{ID: "reused-id", AnalysisArea: "margin", Name: "an unrelated finding"},
		models.Insight{ID: "fresh-live", AnalysisArea: "margin", DuplicateOf: "reused-id", SimilarityScore: 0.95},
	)

	repointed, cleared := settleRunMarks(nil, allInsights, ids)

	if allInsights[0].DuplicateOf != "" || allInsights[0].SimilarityScore != 0 {
		t.Errorf("a restored mark onto a reused id = {%q, %v}, want cleared", allInsights[0].DuplicateOf, allInsights[0].SimilarityScore)
	}
	if allInsights[0].Validation != nil {
		t.Errorf("the skip verdict outlived its mark: %+v", allInsights[0].Validation)
	}
	if allInsights[2].DuplicateOf != "reused-id" || allInsights[2].SimilarityScore != 0.95 {
		t.Errorf("this attempt's own mark = {%q, %v}, want it left alone", allInsights[2].DuplicateOf, allInsights[2].SimilarityScore)
	}
	if repointed != 0 || cleared != 1 {
		t.Errorf("repointed=%d cleared=%d, want 0 and 1", repointed, cleared)
	}
}

// The restore-time translation reads a mapping that is still being written —
// a restore happens in the same pass over the areas as a fresh analysis — so
// it can resolve a mark through an entry a LATER area then poisons. Deciding
// the mark from what it says afterwards keeps the wrong canonical: the id is
// live and this attempt minted it, it just belongs to an unrelated finding.
func TestSettleRunMarks_ATranslationIsNotTrustedOverItsArrivalTarget(t *testing.T) {
	ids := newRestoredIDs()
	// Area A restores first and happens to carry the id that area B's
	// canonical also has.
	restoreOneArea(ids, "margin", map[string]string{"fresh-a": "collides"}, []models.Insight{
		{ID: "fresh-a", AnalysisArea: "margin", Name: "an unrelated finding"},
	})
	// Area C restores next, restating a canonical it knows as "collides" —
	// which at this moment resolves to A's insight.
	restatement := []models.Insight{
		{ID: "fresh-c", AnalysisArea: "stock", DuplicateOf: "collides", SimilarityScore: 0.94,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	restoreOneArea(ids, "stock", map[string]string{"fresh-c": "stored-c"}, restatement)
	if restatement[0].DuplicateOf != "fresh-a" {
		t.Fatalf("mark = %q; the fixture needs the mid-loop translation to have happened", restatement[0].DuplicateOf)
	}
	// And NOW area B restores, revealing that "collides" was claimed twice.
	restoreOneArea(ids, "repl", map[string]string{"fresh-b": "collides"}, []models.Insight{
		{ID: "fresh-b", AnalysisArea: "repl", Name: "the canonical c actually restates"},
	})

	allInsights := []models.Insight{
		{ID: "fresh-a", AnalysisArea: "margin", Name: "an unrelated finding"},
		restatement[0],
		{ID: "fresh-b", AnalysisArea: "repl", Name: "the canonical c actually restates"},
	}

	if _, cleared := settleRunMarks(nil, allInsights, ids); cleared != 1 {
		t.Errorf("cleared=%d, want 1: the translation resolved through an entry that is now ambiguous", cleared)
	}
	if allInsights[1].DuplicateOf != "" || allInsights[1].Validation != nil {
		t.Errorf("mark = {%q, %+v}; the finding is suppressed behind an unrelated canonical",
			allInsights[1].DuplicateOf, allInsights[1].Validation)
	}
}

// The mirror image: a mark whose canonical restores AFTER its own area is
// left untranslated on purpose, and its stored target can collide with a live
// id from a freshly analysed area. Clearing on that collision throws away a
// fold the final mapping can resolve perfectly well — and a restatement that
// loses its mark also loses its skip verdict and reaches the recommender
// unvalidated.
func TestSettleRunMarks_AnUntranslatedMarkIsResolvedBeforeItIsJudged(t *testing.T) {
	ids := newRestoredIDs()
	// C restores first, restating a canonical whose area has not registered.
	restatement := []models.Insight{
		{ID: "fresh-c", AnalysisArea: "stock", DuplicateOf: "canonical-id", SimilarityScore: 0.94,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	restoreOneArea(ids, "stock", map[string]string{"fresh-c": "stored-c"}, restatement)
	if restatement[0].DuplicateOf != "canonical-id" {
		t.Fatalf("mark = %q; the fixture needs the mark left untranslated", restatement[0].DuplicateOf)
	}
	// Then its canonical's area restores, minting a fresh id for it.
	restoreOneArea(ids, "repl", map[string]string{"fresh-canonical": "canonical-id"}, []models.Insight{
		{ID: "fresh-canonical", AnalysisArea: "repl", Name: "the canonical"},
	})

	allInsights := []models.Insight{
		restatement[0],
		{ID: "fresh-canonical", AnalysisArea: "repl", Name: "the canonical"},
		// A freshly analysed area that happens to carry the stored id as a
		// live one. It must not get a say.
		{ID: "canonical-id", AnalysisArea: "margin", Name: "an unrelated finding"},
	}

	repointed, cleared := settleRunMarks(nil, allInsights, ids)
	if allInsights[0].DuplicateOf != "fresh-canonical" {
		t.Errorf("mark = %q, want fresh-canonical: the final mapping resolves it", allInsights[0].DuplicateOf)
	}
	if allInsights[0].Validation == nil || allInsights[0].Validation.Combined != valmodels.StatusSkippedDuplicate {
		t.Errorf("the skip verdict was dropped from a mark that is still good: %+v", allInsights[0].Validation)
	}
	if repointed != 1 || cleared != 0 {
		t.Errorf("repointed=%d cleared=%d, want 1 and 0", repointed, cleared)
	}
}

// The case that cost the first version of the trust rule its teeth: a stored
// id claimed by TWO restored areas. Poisoning the mapping was right, but the
// provenance was read back out of that mapping's values — so poisoning took
// both areas' insights out of it, and a stale mark from either rode a live-id
// coincidence straight into a reader's hidden finding.
func TestSettleRunMarks_ACollisionDoesNotLaunderARestoredMark(t *testing.T) {
	ids := newRestoredIDs()
	// Two restored areas, both of which handed back "dup-7" for an insight of
	// their own, so the entry is poisoned. The stock one's MARK names a
	// previous attempt's canonical, which a freshly analysed area has reused
	// for something unrelated.
	marked := []models.Insight{
		{ID: "fresh-stock", AnalysisArea: "stock", DuplicateOf: "old-canonical", SimilarityScore: 0.9,
			Validation: &valmodels.InsightValidation{Combined: valmodels.StatusSkippedDuplicate}},
	}
	restoreOneArea(ids, "stock", map[string]string{"fresh-stock": "dup-7"}, marked)
	restoreOneArea(ids, "repl", map[string]string{"fresh-repl": "dup-7"}, nil)

	allInsights := []models.Insight{
		marked[0],
		{ID: "old-canonical", AnalysisArea: "margin", Name: "an unrelated finding this attempt analysed"},
	}

	if _, cleared := settleRunMarks(nil, allInsights, ids); cleared != 1 {
		t.Errorf("cleared=%d, want 1: a collision is not provenance", cleared)
	}
	if allInsights[0].DuplicateOf != "" || allInsights[0].Validation != nil {
		t.Errorf("mark = {%q, %+v}; the finding is hidden behind an unrelated canonical",
			allInsights[0].DuplicateOf, allInsights[0].Validation)
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

// A pair between the two floors is a pair the MODEL was asked about. Letting
// the community fallback satisfy its re-check at that floor folds pairs the
// community rule would never have proposed — a finding losing its verdict and
// its place in Phase 5 on a rule the deployment never opted into.
func TestReverifyFold_ASubFoldFloorPairHoldsOnlyWhenAdjudicated(t *testing.T) {
	// 32 degrees apart is a cosine of about 0.848: above the adjudication
	// floor, below the fold floor.
	canonical := doc(0, "a", 0, true, "the whole total", "")
	dup := doc(1, "b", 32, true, "the north segment", "")

	if _, held := reverifyFold(dup, canonical, 0.88, 0.80, true); !held {
		t.Error("an adjudicated fold must hold at the floor it was proposed at")
	}
	if _, held := reverifyFold(dup, canonical, 0.88, 0.80, false); held {
		t.Error("the community rule must not fold below the fold floor")
	}
	// A pair above the fold floor holds either way.
	near := doc(2, "c", 1, true, "claim c", "")
	if _, held := reverifyFold(near, canonical, 0.88, 0.80, false); !held {
		t.Error("the community rule must still fold a similarity pair")
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

// {a,b,c} then {b,c,d}, as two overlapping stars: each document is inside the
// floor of its neighbours and outside its neighbours-but-one. The first cluster
// gets no verdict, so the community fallback decides it — and the pair it would
// fold, b and c, is exactly the pair the provider's verdict for the SECOND
// cluster keeps apart. The first cluster is processed first, so the keep has to
// be known before it.
func TestConsolidateArea_AProviderKeepBeatsAnEarlierFallbackFold(t *testing.T) {
	a := insight("a", "stock", "the whole total", "body a", 400)
	b := insight("b", "stock", "the north segment", "body b", 300)
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

	// 27 degrees between neighbours is about 0.891 — above the fold floor, so
	// the fallback could act on it. 54 degrees is 0.588, outside both floors.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 27, embText(c): 54, embText(d): 81,
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

// Both of a verdict's folds refused by the re-check, and the donors still
// protected from each other afterwards.
//
// The shape that reaches it: one star seeded on b, which is inside the
// adjudication floor of a, c and d while a is outside it of both c and d. The
// verdict names a as the survivor and folds c and d into it, so each fold is
// re-checked against a pair that was never proposed and both are refused. c and
// d are a similarity pair above the FOLD floor, so without the keep the
// fallback would then fold one into the other — which is the thing being
// protected against, with nothing folded to revert.
func TestConsolidateArea_RefusedMergeDonorsAreProtectedWithNothingToRevert(t *testing.T) {
	a := insight("a", "stock", "the totals across every segment", "body a", 400)
	b := insight("b", "stock", "the middling claim", "body b", 300)
	c := insight("c", "stock", "claim c carries the caveat", "body c", 200)
	d := insight("d", "stock", "claim d carries the caveat", "body d", 100)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID: "a",
			Duplicates:  map[string]float64{"c": 0.9, "d": 0.9},
		}}, nil
	}})

	// 33-34 degrees is 0.829-0.839: proposed, not foldable. 66-67 degrees is
	// 0.39-0.41: outside both floors. c and d are 1 degree apart.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 33, embText(c): 66, embText(d): 67,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 1 {
		t.Fatalf("clusters = %d (%+v), want one star of four (fixture broken)", st.Clusters, st)
	}
	if st.RefusedRecheck != 2 {
		t.Fatalf("stats = %+v, want both proposed folds refused by the re-check (fixture broken)", st)
	}
	if st.Folded != 0 {
		t.Fatalf("stats = %+v, want nothing folded", st)
	}
	if current[3].DuplicateOf != "" {
		t.Errorf("d.duplicate_of = %q; it was folded sideways into c, losing the caveat the rewrite was for", current[3].DuplicateOf)
	}
	if !o.adjudicatedKeep("c", "d") {
		t.Error("the refused donors were not protected against each other")
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

// The magnitude guard gates BOTH acceptance paths. sameClaim refuses a fold
// where the two texts are IDENTICAL and the figures differ — 100 affected
// entities hidden behind 1,000 — and the adjudicated path must not reach its
// acceptance without passing it.
func TestReverifyFold_TheMagnitudeGuardGatesTheAdjudicatedPathToo(t *testing.T) {
	canonical := doc(0, "a", 0, true, "the whole total", "")
	dup := doc(1, "b", 32, true, "the north segment", "")

	// Different texts: the adjudicated path decides, and it holds.
	if _, held := reverifyFold(dup, canonical, 0.88, 0.80, true); !held {
		t.Fatal("an ordinary adjudicated fold must hold (fixture broken)")
	}

	// Identical text, different magnitudes: the guard's own case. The pair is
	// still above the adjudication floor, so the path would accept it alone.
	same := dup
	same.text = canonical.text
	same.magnitude = "affected=1000"
	canonical.magnitude = "affected=100"
	if _, held := reverifyFold(same, canonical, 0.88, 0.80, true); held {
		t.Error("identical text with a different magnitude must not fold, adjudicated or not")
	}
	// And the same pair with EQUAL magnitudes still folds, so the guard has
	// not simply switched the path off.
	same.magnitude = canonical.magnitude
	if _, held := reverifyFold(same, canonical, 0.88, 0.80, true); !held {
		t.Error("equal magnitudes must still fold")
	}
}

// The community rule reads the FOLD floor, and the two floors are independent.
// An operator who raises the adjudication floor above it leaves pairs that are
// foldable and never proposed — and the pass used to return before the
// community rule ran, so those pairs went on spending the validation budget it
// exists to protect.
func TestConsolidateArea_CommunityFoldsRunWhenNothingReachedAdjudication(t *testing.T) {
	t.Setenv("DISCOVERY_CONSOLIDATION_ADJUDICATION_MINSCORE", "0.99")
	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		t.Error("the provider must not be called when nothing was proposed")
		return nil, nil
	}})

	a := insight("a", "stock", "the whole total", "body a", 200)
	b := insight("b", "stock", "the whole total restated", "body b", 100)
	// 20 degrees is about 0.940: above the fold floor, below this adjudication
	// floor.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 20,
	}}}

	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 0 {
		t.Fatalf("clusters = %d, want 0: the pair is below the adjudication floor (fixture broken)", st.Clusters)
	}
	if st.Folded != 1 {
		t.Fatalf("stats = %+v, want the community rule to have folded the pair anyway", st)
	}
	if current[1].DuplicateOf != "a" {
		t.Errorf("b.duplicate_of = %q, want a", current[1].DuplicateOf)
	}
	if st.Decider != "community" {
		t.Errorf("decider = %q, want community: it is what decided the fold", st.Decider)
	}
}

// When the adjudication cap bites, the slots go where a verdict changes the
// outcome. A cluster holding a pair above the FOLD floor is folded by the
// community rule if the provider never sees it — possibly against what the
// provider would have said — while a cluster entirely below that floor only
// stands to gain a fold.
func TestConsolidateArea_TheCapSpendsItsSlotsOnClustersTheCommunityRuleCouldFold(t *testing.T) {
	t.Setenv("DISCOVERY_CONSOLIDATION_MAX_CLUSTERS", "1")

	a := insight("a", "stock", "claim a", "body a", 400)
	b := insight("b", "stock", "claim b", "body b", 300)
	c := insight("c", "stock", "claim c", "body c", 200)
	d := insight("d", "stock", "claim d", "body d", 100)

	var shown [][]string
	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		for _, cluster := range cl {
			ids := make([]string, 0, len(cluster.Candidates))
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			slices.Sort(ids)
			shown = append(shown, ids)
		}
		return nil, nil
	}})

	// a/b are 33 degrees apart (0.839: proposed, below the fold floor) and
	// come first in document order. c/d are 5 degrees apart (0.996: the
	// community rule would fold them unasked).
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 33, embText(c): 120, embText(d): 125,
	}}}
	current := []models.Insight{a, b, c, d}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Clusters != 2 || st.OverBound != 1 {
		t.Fatalf("stats = %+v, want two clusters with one over the bound (fixture broken)", st)
	}
	if len(shown) != 1 {
		t.Fatalf("the provider was shown %v, want exactly one cluster", shown)
	}
	if !slices.Equal(shown[0], []string{"c", "d"}) {
		t.Errorf("the provider was shown %v, want the pair the community rule could fold", shown[0])
	}
}

// And "could fold" means could WRITE. A pair of two earlier-area findings can
// sit well above the fold floor and still not be foldable — only the area being
// consolidated has insights this pass can mark — so it must not take the slot
// from a current pair that really would fold unasked.
func TestConsolidateArea_TheCapIgnoresAStrongPairOfTwoEarlierFindings(t *testing.T) {
	t.Setenv("DISCOVERY_CONSOLIDATION_MAX_CLUSTERS", "1")

	// Cluster one holds a current finding, so it survives clustering, but its
	// only pair above the FOLD floor is between the two earlier findings —
	// which this pass cannot mark. Cluster two is a current pair above that
	// floor, and is the one a verdict would change.
	anchor := insight("anchor", "stock", "claim anchor", "body", 400)
	pairA := insight("pairA", "stock", "claim pairA", "body", 300)
	pairB := insight("pairB", "stock", "claim pairB", "body", 200)
	old1 := insight("old1", "prior", "claim old1", "body", 150)
	old2 := insight("old2", "prior", "claim old2", "body", 100)

	var shown [][]string
	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		for _, cluster := range cl {
			ids := make([]string, 0, len(cluster.Candidates))
			for _, cand := range cluster.Candidates {
				ids = append(ids, cand.ID)
			}
			slices.Sort(ids)
			shown = append(shown, ids)
		}
		return nil, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(anchor): 0, embText(old1): 33, embText(old2): 38,
		embText(pairA): 120, embText(pairB): 125,
	}}}
	current := []models.Insight{anchor, pairA, pairB}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, []models.Insight{old1, old2}))

	if st.Clusters != 2 || st.OverBound != 1 {
		t.Fatalf("stats = %+v, want two clusters with one over the bound (fixture broken)", st)
	}
	if len(shown) != 1 {
		t.Fatalf("the provider was shown %v, want exactly one cluster", shown)
	}
	if !slices.Equal(shown[0], []string{"pairA", "pairB"}) {
		t.Errorf("the provider was shown %v, want the pair this area can actually fold", shown[0])
	}
}

// A verdict that asks for the survivor's description to be rewritten does not
// get its folds. The rewrite is requested precisely when a folded member
// carries a figure or a caveat the survivor does not, and nothing rewrites a
// survivor — so folding without it would suppress exactly those findings
// behind text that does not state them.
func TestConsolidateArea_AVerdictAskingForARewriteKeepsItsFindingsStanding(t *testing.T) {
	a := insight("a", "stock", "the whole total", "every segment", 200)
	b := insight("b", "stock", "the north segment", "one slice, with a caveat", 100)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "a",
			Duplicates:        map[string]float64{"b": 0.9},
			MergedDescription: "the whole total, carrying b's caveat",
			Reason:            "b restates a row of a, and carries a caveat a omits",
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 1,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.RefusedUnmergeable != 1 || st.Folded != 0 {
		t.Fatalf("stats = %+v, want the folds left standing", st)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q; it carries a caveat the survivor does not state", current[1].DuplicateOf)
	}
	if strings.Contains(current[0].Description, "carrying") {
		t.Errorf("a.description = %q; the rewrite was applied", current[0].Description)
	}
	// And the pair is remembered, so the fallback does not fold it on
	// document order instead — a is first, and the two are 1 degree apart.
	if !o.adjudicatedKeep("a", "b") {
		t.Error("the pair was not recorded as adjudicated; the community rule will fold it anyway")
	}
}

// A rewrite verdict's donors are protected from each other, not only from the
// canonical. The adjudicator said each of them carries detail the survivor does
// not; folding one into another suppresses that detail just as surely.
func TestConsolidateArea_ARewriteVerdictsDonorsAreKeptFromEachOther(t *testing.T) {
	a := insight("a", "stock", "the whole total", "every segment", 300)
	b := insight("b", "stock", "the north segment", "a slice, with a caveat", 200)
	c := insight("c", "stock", "the north segment again", "a slice, with another caveat", 100)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "a",
			Duplicates:        map[string]float64{"b": 0.9, "c": 0.9},
			MergedDescription: "the whole total, carrying both caveats",
		}}, nil
	}})

	// b and c are 1 degree apart, so the community rule would fold one into
	// the other unasked.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 20, embText(c): 21,
	}}}
	current := []models.Insight{a, b, c}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.Folded != 0 {
		t.Fatalf("stats = %+v, want nothing folded", st)
	}
	if !o.adjudicatedKeep("b", "c") {
		t.Error("the two donors were not kept from each other")
	}
	if current[1].DuplicateOf != "" || current[2].DuplicateOf != "" {
		t.Errorf("b/c = (%q, %q); a donor was folded into the other, losing its caveat",
			current[1].DuplicateOf, current[2].DuplicateOf)
	}
}

// Two overlapping clusters can propose the same fold with only one of them
// asking for a rewrite, and the outcome used to depend on which was read
// first: the rewrite verdict first blocked the fold, the rewrite verdict
// second left the mark standing. The request is settled before any mark is
// written now, so either order gives the same answer.
func TestConsolidateArea_ARewriteRequestBeatsAnotherClustersFoldInEitherOrder(t *testing.T) {
	for _, rewriteOn := range []string{"x", "y"} {
		t.Run("rewrite on the cluster holding "+rewriteOn, func(t *testing.T) {
			a := insight("a", "stock", "claim a", "body a", 400)
			b := insight("b", "stock", "claim b", "body b", 300)
			x := insight("x", "stock", "claim x", "body x", 200)
			y := insight("y", "stock", "claim y", "body y", 100)

			withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
				out := make([]agentplugin.ConsolidationVerdict, len(cl))
				for i, cluster := range cl {
					ids := make([]string, 0, len(cluster.Candidates))
					for _, cand := range cluster.Candidates {
						ids = append(ids, cand.ID)
					}
					out[i] = agentplugin.ConsolidationVerdict{
						CanonicalID: "a", Duplicates: map[string]float64{"b": 0.95},
					}
					if slices.Contains(ids, rewriteOn) {
						out[i].MergedDescription = "a, carrying b's caveat"
					}
				}
				return out, nil
			}})

			// a and b are a strong pair; x hangs off a only and y off b only,
			// so star(a) and star(b) are two clusters and BOTH hold (a, b).
			o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
				embText(a): 0, embText(b): 10, embText(x): -30, embText(y): 42,
			}}}
			current := []models.Insight{a, b, x, y}
			st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

			if st.Clusters != 2 {
				t.Fatalf("clusters = %d (%+v), want the two overlapping stars (fixture broken)", st.Clusters, st)
			}
			if current[1].DuplicateOf != "" {
				t.Errorf("b.duplicate_of = %q (%+v); a rewrite was asked for, so the fold must not stand",
					current[1].DuplicateOf, st)
			}
			if !o.adjudicatedKeep("a", "b") {
				t.Error("the pair a rewrite was asked for was not recorded as kept")
			}
		})
	}
}

// A finding an adjudicator said must stand is not folded by anything else —
// not another verdict naming it, not a redirected canonical, not the community
// fallback. The decision rides on the finding, so it holds wherever a mark is
// written.
func TestConsolidateArea_AFindingThatMustStandIsNotFoldedByTheFallback(t *testing.T) {
	a := insight("a", "stock", "the whole total", "every segment", 300)
	b := insight("b", "stock", "the north segment", "a slice, with a caveat", 200)
	c := insight("c", "stock", "the north segment restated", "a slice", 100)

	withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		out := make([]agentplugin.ConsolidationVerdict, len(cl))
		for i := range cl {
			// One verdict, asking for b's caveat to be carried onto a.
			out[i] = agentplugin.ConsolidationVerdict{
				CanonicalID:       "a",
				Duplicates:        map[string]float64{"b": 0.95},
				MergedDescription: "a, carrying b's caveat",
			}
		}
		return out, nil
	}})

	// b and c are 1 degree apart, so the fallback would fold b into c — a
	// different canonical from the one the verdict named.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 20, embText(c): 21,
	}}}
	current := []models.Insight{a, b, c}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if !current[1].StandsOnAdjudication {
		t.Fatalf("b was not marked as standing (%+v)", st)
	}
	if current[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q; the fallback folded a finding the adjudicator kept", current[1].DuplicateOf)
	}
	// And the flag is what does it, not the pair: dropping the pair record
	// leaves the decision standing.
	o.consolidationKept = nil
	again := []models.Insight{a, b, c}
	again[1].StandsOnAdjudication = true
	withProvider(t, nil)
	o2 := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 20, embText(c): 21,
	}}}
	st2 := o2.consolidateArea(context.Background(), areaInput("stock", again, nil))
	if again[1].DuplicateOf != "" {
		t.Errorf("b.duplicate_of = %q on a resumed attempt with no pair records (%+v)", again[1].DuplicateOf, st2)
	}
}

// A rewrite verdict's donor is protected BEFORE any fold is written. The
// clusters are overlapping stars read in order, so another cluster's verdict
// can fold the donor into its own canonical first — and a protection recorded
// only when the writer reaches the rewrite then lands on a finding that is
// already suppressed, already out of validation and already out of the
// recommendations.
func TestConsolidateArea_ARewriteDonorIsProtectedBeforeAnyFoldIsWritten(t *testing.T) {
	for _, rewriteOn := range []string{"x", "y"} {
		t.Run("rewrite on the cluster holding "+rewriteOn, func(t *testing.T) {
			a := insight("a", "stock", "claim a", "body a", 400)
			b := insight("b", "stock", "claim b", "body b", 300)
			x := insight("x", "stock", "claim x", "body x", 200)
			y := insight("y", "stock", "claim y", "body y", 100)

			withProvider(t, &stubProvider{fn: func(cl []agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
				out := make([]agentplugin.ConsolidationVerdict, len(cl))
				for i, cluster := range cl {
					ids := make([]string, 0, len(cluster.Candidates))
					for _, cand := range cluster.Candidates {
						ids = append(ids, cand.ID)
					}
					if slices.Contains(ids, rewriteOn) {
						// This verdict says b carries detail its own
						// canonical does not state: b must stand.
						out[i] = agentplugin.ConsolidationVerdict{
							CanonicalID:       rewriteOn,
							Duplicates:        map[string]float64{"b": 0.95},
							MergedDescription: rewriteOn + ", carrying b's caveat",
						}
						continue
					}
					// And this one folds b into a, plainly, with no rewrite.
					out[i] = agentplugin.ConsolidationVerdict{
						CanonicalID: "a", Duplicates: map[string]float64{"b": 0.95},
					}
				}
				return out, nil
			}})

			// star(a) = {a, b, x} and star(b) = {a, b, y}: two overlapping
			// clusters, both holding b, with a different canonical in each.
			// Whichever is read first, b has to come out visible.
			o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
				embText(a): 0, embText(b): 10, embText(x): -30, embText(y): 42,
			}}}
			current := []models.Insight{a, b, x, y}
			st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

			if st.Clusters != 2 {
				t.Fatalf("clusters = %d (%+v), want the two overlapping stars (fixture broken)", st.Clusters, st)
			}
			if !current[1].StandsOnAdjudication {
				t.Fatalf("b was not recorded as standing (%+v)", st)
			}
			if current[1].DuplicateOf != "" {
				t.Errorf("b.duplicate_of = %q (%+v); an adjudicator said b must stand and another cluster's verdict folded it anyway",
					current[1].DuplicateOf, st)
			}
		})
	}
}

// The community simulation must not model a fold it cannot write. A protected
// finding stays visible, which makes it a perfectly good survivor for a later
// document — but recording a fold OF it pointed the chain at a canonical
// nobody is being sent to, and the next document was then measured against
// THAT, left visible, and spent a validation check on a duplicate.
func TestConsolidateArea_TheSimulationDoesNotFoldAProtectedFinding(t *testing.T) {
	withProvider(t, nil)

	a := insight("a", "stock", "the whole total", "every segment", 300)
	b := insight("b", "stock", "the north segment", "a slice, with a caveat", 200)
	c := insight("c", "stock", "the north segment restated", "a slice", 100)

	// a-b and b-c both clear the fold floor at 20 degrees; a-c, 40 apart,
	// does not. So c's only available survivor is b.
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 20, embText(c): 40,
	}}}
	current := []models.Insight{a, b, c}
	// b arrives protected: an adjudicator said so in an earlier area's pass,
	// and the finding itself is where that decision lives.
	current[1].StandsOnAdjudication = true
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if current[1].DuplicateOf != "" {
		t.Fatalf("b.duplicate_of = %q; a protected finding was folded (%+v)", current[1].DuplicateOf, st)
	}
	if current[2].DuplicateOf != "b" {
		t.Errorf("c.duplicate_of = %q, want b — the visible finding it restates (%+v)", current[2].DuplicateOf, st)
	}
}

// A verdict the writer discards on its shape must leave nothing behind it.
// One naming A as its canonical and then listing A among its own duplicates
// registered a keep for the A-B pair the writer then refused, and that keep
// went on to block the community rule from folding B into A at a score this
// product folds at with no model in the loop at all.
func TestConsolidateArea_AMalformedRewriteLeavesNoKeepBehind(t *testing.T) {
	a := insight("a", "stock", "claim a", "body a", 300)
	b := insight("b", "stock", "claim b", "body b", 200)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "a",
			Duplicates:        map[string]float64{"a": 1, "b": 0.95},
			MergedDescription: "a, carrying b's caveat",
		}}, nil
	}})

	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(a): 0, embText(b): 10,
	}}}
	current := []models.Insight{a, b}
	st := o.consolidateArea(context.Background(), areaInput("stock", current, nil))

	if st.RefusedShape == 0 {
		t.Fatalf("stats = %+v, want the malformed verdict discarded", st)
	}
	if o.adjudicatedKeep("a", "b") {
		t.Error("a verdict the writer discarded still registered a keep for its pair")
	}
	if current[1].StandsOnAdjudication {
		t.Error("a verdict the writer discarded still protected its donor")
	}
	if current[1].DuplicateOf != "a" {
		t.Errorf("b.duplicate_of = %q, want a — the community rule's own decision on a pair no verdict survived (%+v)",
			current[1].DuplicateOf, st)
	}
}
