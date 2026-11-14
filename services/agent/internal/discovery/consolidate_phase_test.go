package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
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

	if st.Folded != 0 || st.RefusedShape == 0 {
		t.Fatalf("stats = %+v, want the cluster discarded", st)
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

// Not onto an earlier area's finding: its text is already recorded in that
// area's checkpoint, and rewriting it here would leave the record and the
// result disagreeing about what the finding says.
func TestConsolidateArea_MergedDescriptionIsNotWrittenOntoAnEarlierArea(t *testing.T) {
	earlier := insight("e1", "replenishment", "overdue orders are rising", "the rate climbed", 500)
	cur := insight("c1", "stock", "overdue orders keep rising", "the rate climbed further", 480)

	withProvider(t, &stubProvider{fn: func([]agentplugin.DuplicateCluster) ([]agentplugin.ConsolidationVerdict, error) {
		return []agentplugin.ConsolidationVerdict{{
			CanonicalID:       "e1",
			Duplicates:        map[string]float64{"c1": 0.99},
			MergedDescription: "rewritten",
		}}, nil
	}})
	o := &Orchestrator{projectID: "p", embeddingProvider: &angleEmbedder{deg: map[string]float64{
		embText(earlier): 0, embText(cur): 1,
	}}}
	current := []models.Insight{cur}
	runInsights := []models.Insight{earlier}
	if st := o.consolidateArea(context.Background(), areaInput("stock", current, runInsights)); st.Folded != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if runInsights[0].Description == "rewritten" {
		t.Error("an earlier area's description was rewritten")
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
// row this attempt does not hold — which renders the finding as suppressed
// behind a canonical the reader cannot reach. Resolvable marks are re-pointed;
// unresolvable ones are dropped and the finding is visible again.
func TestRepointRestoredMarks(t *testing.T) {
	insights := []models.Insight{
		{ID: "fresh-a", Name: "canonical"},
		{ID: "fresh-b", Name: "restatement", DuplicateOf: "stored-a", SimilarityScore: 0.93},
		{ID: "fresh-c", Name: "orphan", DuplicateOf: "gone", SimilarityScore: 0.91},
	}
	repointRestoredMarks(insights, map[string]string{"stored-a": "fresh-a"}, "stock")

	if insights[1].DuplicateOf != "fresh-a" {
		t.Errorf("resolvable mark = %q, want fresh-a", insights[1].DuplicateOf)
	}
	if insights[1].SimilarityScore != 0.93 {
		t.Errorf("a re-pointed mark must keep its score, got %v", insights[1].SimilarityScore)
	}
	if insights[2].DuplicateOf != "" || insights[2].SimilarityScore != 0 {
		t.Errorf("unresolvable mark = {%q, %v}, want cleared", insights[2].DuplicateOf, insights[2].SimilarityScore)
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
