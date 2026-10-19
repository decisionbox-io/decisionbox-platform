package discovery

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// The Discovery Ledger got findings and nothing else: no next tasks, no
// learnings, no prior-finding re-judgements, run after run (#434). The
// reflection call was healthy every time — it simply answered the shortest
// thing the contract allowed, because everything but coverage_summary was
// optional in both the schema and the prompt.
//
// These tests hold the contract the fix installs: each judgment output a run
// can honestly produce is demanded, in the schema AND in the prompt, and
// nothing is demanded from a run that has no grounds to produce it.

// requiredSet reads the schema's top-level required list as a set.
func requiredSet(t *testing.T, schema map[string]interface{}) map[string]bool {
	t.Helper()
	raw, ok := schema["required"].([]interface{})
	if !ok {
		t.Fatalf("schema required is %T, want []interface{}", schema["required"])
	}
	out := make(map[string]bool, len(raw))
	for _, v := range raw {
		name, ok := v.(string)
		if !ok {
			t.Fatalf("required entry %v is %T, want string", v, v)
		}
		out[name] = true
	}
	return out
}

// minItemsOf returns a property's minItems, or 0 when it has none.
func minItemsOf(t *testing.T, schema map[string]interface{}, name string) int {
	t.Helper()
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("schema has no properties object")
	}
	prop, ok := props[name].(map[string]interface{})
	if !ok {
		t.Fatalf("schema has no property %q", name)
	}
	n, ok := prop["minItems"].(int)
	if !ok {
		return 0
	}
	return n
}

// TestReflectionSchema_RequiredFollowsTheRun walks every evolution mode with
// and without prior findings. The required set is asserted exactly, so a field
// quietly gaining or losing its demand fails here — including
// domain_pack_deltas, which must stay optional in every mode.
func TestReflectionSchema_RequiredFollowsTheRun(t *testing.T) {
	everyMode := []agentplugin.EvolutionMode{
		agentplugin.EvolutionModeOff,
		agentplugin.EvolutionModeSuggestOnly,
		agentplugin.EvolutionModeAdminApproval,
		agentplugin.EvolutionModeAuto,
	}

	for _, mode := range everyMode {
		for _, hasPrior := range []bool{false, true} {
			name := string(mode)
			if hasPrior {
				name += "/with-prior-findings"
			} else {
				name += "/first-run"
			}
			t.Run(name, func(t *testing.T) {
				schema := reflectionResponseSchema(mode, hasPrior, true)
				got := requiredSet(t, schema)

				want := map[string]bool{"coverage_summary": true, "learnings": true}
				if hasPrior {
					want["prior_status_updates"] = true
				}
				if mode != agentplugin.EvolutionModeOff {
					want["next_tasks"] = true
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("required = %v, want %v", sortedKeys(got), sortedKeys(want))
				}

				// "Required" alone is satisfied by an empty array, which is the
				// exact answer this change exists to stop — so every demanded
				// array carries minItems, and no undemanded one does.
				for _, arr := range []string{"learnings", "prior_status_updates", "next_tasks", "domain_pack_deltas", "task_status_updates"} {
					min := minItemsOf(t, schema, arr)
					if want[arr] && min != 1 {
						t.Errorf("%s is required but minItems = %d, want 1", arr, min)
					}
					if !want[arr] && min != 0 {
						t.Errorf("%s is not required but carries minItems = %d", arr, min)
					}
				}
			})
		}
	}
}

// TestReflectionSchema_DemandIsAlsoInTheDescription: the description is the
// only part of the schema a model reads as instruction. A required array whose
// description still reads as optional argues against the constraint next to
// it, which is how a small model talks itself back into [].
func TestReflectionSchema_DemandIsAlsoInTheDescription(t *testing.T) {
	on := reflectionResponseSchema(agentplugin.EvolutionModeSuggestOnly, true, true)
	for _, name := range []string{"learnings", "prior_status_updates", "next_tasks"} {
		if desc := descriptionOf(t, on, name); !strings.Contains(desc, "at least one") {
			t.Errorf("%s description does not demand at least one: %q", name, desc)
		}
	}

	// And the demand is not left behind on a run that must not carry it.
	off := reflectionResponseSchema(agentplugin.EvolutionModeOff, false, true)
	for _, name := range []string{"prior_status_updates", "next_tasks"} {
		if desc := descriptionOf(t, off, name); strings.Contains(desc, "at least one") {
			t.Errorf("%s must not demand at least one on an off/first run: %q", name, desc)
		}
	}
	if desc := descriptionOf(t, off, "domain_pack_deltas"); strings.Contains(desc, "at least one") {
		t.Errorf("domain_pack_deltas must never be demanded: %q", desc)
	}
}

func descriptionOf(t *testing.T, schema map[string]interface{}, name string) string {
	t.Helper()
	props := schema["properties"].(map[string]interface{})
	prop, ok := props[name].(map[string]interface{})
	if !ok {
		t.Fatalf("schema has no property %q", name)
	}
	desc, _ := prop["description"].(string)
	return desc
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEvolutionModeGuidance_ByMode: the schema only reaches providers with
// structured output. The prompt is what every other provider gets, so the
// demand has to live there too — and the off wording must not move, because
// off means the ledger records without self-directing.
func TestEvolutionModeGuidance_ByMode(t *testing.T) {
	const wantOff = "Domain-pack evolution is OFF for this project: return an EMPTY next_tasks array and an EMPTY domain_pack_deltas array. You may still produce coverage, learnings, prior-finding status updates, and task_status_updates that close resolved open tasks."
	if got := evolutionModeGuidance(agentplugin.EvolutionModeOff); got != wantOff {
		t.Errorf("off guidance drifted:\ngot:  %q\nwant: %q", got, wantOff)
	}

	for _, mode := range []agentplugin.EvolutionMode{
		agentplugin.EvolutionModeSuggestOnly,
		agentplugin.EvolutionModeAdminApproval,
		agentplugin.EvolutionModeAuto,
	} {
		got := evolutionModeGuidance(mode)
		if !strings.Contains(got, "AT LEAST ONE next_task") {
			t.Errorf("%s guidance must demand at least one next_task, got %q", mode, got)
		}
		if strings.Contains(got, "You may propose next_tasks") {
			t.Errorf("%s guidance still offers next_tasks as optional: %q", mode, got)
		}
		// The demand must stay grounded, or it buys tasks nobody can act on.
		if !strings.Contains(got, "grounded in this run's findings and coverage") {
			t.Errorf("%s guidance must ground the demand, got %q", mode, got)
		}
	}
}

// TestRenderPriorStatusField_OnlyDemandsWhatThereIsToJudge. A run with no
// grounded evidence about the project's past — no prior findings at all, or
// none of them surfaced again — has nothing honest to re-judge, and a model
// told to produce a verdict anyway invents one. So the demand appears only
// where the evidence does.
func TestRenderPriorStatusField_OnlyDemandsWhatThereIsToJudge(t *testing.T) {
	noEvidence := renderPriorStatusField(false)
	if noEvidence != reflectionPriorStatusFieldBase {
		t.Errorf("a run with no re-seen prior finding must get the bullet unchanged, got %q", noEvidence)
	}

	withEvidence := renderPriorStatusField(true)
	if !strings.HasPrefix(withEvidence, reflectionPriorStatusFieldBase) {
		t.Error("the demand must be added to the bullet, not replace it")
	}
	if !strings.Contains(withEvidence, "at least one") {
		t.Errorf("a run that re-saw a prior finding must be asked to re-judge at least one, got %q", withEvidence)
	}
	// The grounding rule is what keeps the demand honest — it must survive,
	// and the demand must name the evidence it rests on rather than just
	// overruling it.
	for _, keep := range []string{
		"Do NOT mark a finding `resolved` just because it did not reappear",
		"surfaced at least one of the prior findings above again",
		"IS grounded evidence",
	} {
		if !strings.Contains(withEvidence, keep) {
			t.Errorf("bullet lost %q: %s", keep, withEvidence)
		}
	}
}

// TestBuildReflectionPrompt_FirstRunCarriesNoPriorDemand is the prompt half of
// the same rule, through the real render: a first run's prompt says there are
// no prior findings, so it must not also ask for one to be re-judged.
func TestBuildReflectionPrompt_FirstRunCarriesNoPriorDemand(t *testing.T) {
	o, result, _, tasks, pol := reflectionPromptFixture()

	got := o.buildReflectionPrompt(result, nil, tasks, pol, nil, false, false)

	if !strings.Contains(got, "(no prior findings — this is an early run)") {
		t.Fatal("fixture no longer renders the empty prior-findings list")
	}
	if strings.Contains(got, "Prior findings ARE listed above") {
		t.Error("a first run must not be asked to re-judge a prior finding")
	}
	// The unconditional demands are still there.
	if !strings.Contains(got, "AT LEAST ONE next_task") {
		t.Error("evolution is on in the fixture, so the prompt must demand a next task")
	}
	if !strings.Contains(got, "**learnings**") || !strings.Contains(got, "Give **at least one**") {
		t.Error("every run must be asked for at least one learning")
	}
	if strings.Contains(got, "{{") {
		t.Error("prompt has an unsubstituted token")
	}
}

// TestGenerateReflection_SchemaIsBuiltFromTheRun is the wiring: the schema that
// actually goes on the wire has to come from this run's evolution mode and its
// prior findings, not from a constant. Asserted through a provider that really
// does support structured output, so the format survives ai.Client's gate.
func TestGenerateReflection_SchemaIsBuiltFromTheRun(t *testing.T) {
	const pn = "test-reflection-structured"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
		gollm.ProviderMeta{
			ID:                       pn,
			Name:                     "reflection structured test",
			SupportsStructuredOutput: true,
			Models:                   []gollm.ModelEntry{{ID: "mock-model", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 8000}},
		})

	oneFinding := []commonmodels.LedgerFinding{{ID: "f-1", Name: "Dead stock", Status: "confirmed"}}

	tests := []struct {
		name         string
		mode         agentplugin.EvolutionMode
		reSeen       map[string]struct{}
		prior        []commonmodels.LedgerFinding
		wantRequired []string
	}{
		{
			name:         "evolution off, first run",
			mode:         agentplugin.EvolutionModeOff,
			wantRequired: []string{"coverage_summary", "learnings"},
		},
		{
			name:         "suggest_only, a carried finding surfaced again",
			mode:         agentplugin.EvolutionModeSuggestOnly,
			reSeen:       map[string]struct{}{"f-1": {}},
			prior:        oneFinding,
			wantRequired: []string{"coverage_summary", "learnings", "next_tasks", "prior_status_updates"},
		},
		{
			// The findings are in the ledger, but this run touched none of
			// them — either they are its own, just consolidated, or the run
			// explored elsewhere. Absence is not proof, so there is nothing
			// honest to say and the demand must not fire.
			name:         "suggest_only, findings in the ledger but none re-seen",
			mode:         agentplugin.EvolutionModeSuggestOnly,
			reSeen:       nil,
			prior:        oneFinding,
			wantRequired: []string{"coverage_summary", "learnings", "next_tasks"},
		},
		{
			// The mirror case: a carried finding WAS re-seen, but the list
			// read failed so the prompt shows none. Demanding a verdict on a
			// list the model cannot see is how ids get invented.
			name:         "re-seen finding the prompt cannot show",
			mode:         agentplugin.EvolutionModeSuggestOnly,
			reSeen:       map[string]struct{}{"f-1": {}},
			prior:        nil,
			wantRequired: []string{"coverage_summary", "learnings", "next_tasks"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := testutil.NewMockLLMProvider()
			provider.DefaultResponse = &gollm.ChatResponse{
				Content: `{"coverage_summary":"covered","learnings":[{"note":"orders is daily-grain"}],"next_tasks":[{"title":"Check the seams","text":"Join orders to shipments"}]}`,
				Usage:   gollm.Usage{InputTokens: 10, OutputTokens: 20},
			}
			client, err := ai.New(provider, "mock-model")
			if err != nil {
				t.Fatalf("ai.New: %v", err)
			}
			client.SetProvenance("p", "r", pn)

			o := &Orchestrator{
				aiClient: client, projectID: "p", runID: "r", datasets: []string{"ds"},
				llmInputWindow: 200000, llmOutputCap: 4000,
				findingRepo: &fakeFindingRepo{findings: tc.prior},
			}

			if _, err := o.generateReflection(context.Background(),
				&models.DiscoveryResult{Schemas: map[string]models.TableSchema{"ds.orders": {}}},
				agentplugin.DiscoveryPolicy{EvolutionMode: tc.mode, FrontierPolicy: agentplugin.FrontierBalanced},
				consolidationResult{ReSeen: tc.reSeen},
			); err != nil {
				t.Fatalf("generateReflection: %v", err)
			}
			if len(provider.Calls) == 0 {
				t.Fatal("provider was not called")
			}

			rf := provider.Calls[0].Request.ResponseFormat
			if rf == nil {
				t.Fatal("ResponseFormat must reach a structured-output provider")
			}
			got := requiredSet(t, rf.Schema)
			want := make(map[string]bool, len(tc.wantRequired))
			for _, n := range tc.wantRequired {
				want[n] = true
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("required on the wire = %v, want %v", sortedKeys(got), tc.wantRequired)
			}
		})
	}
}

// statefulFindingRepo is a fakeFindingRepo that reads back what it wrote, the
// way the Mongo repository does. The plain fake serves a fixed list, which
// hides the ordering that matters here: RunPhaseReflection consolidates this
// run's findings into the ledger BEFORE the reflection call lists them.
type statefulFindingRepo struct {
	findings []commonmodels.LedgerFinding
}

func (r *statefulFindingRepo) List(_ context.Context, _ string) ([]commonmodels.LedgerFinding, error) {
	out := make([]commonmodels.LedgerFinding, 0, len(r.findings))
	for i := range r.findings {
		if !r.findings[i].Archived {
			out = append(out, r.findings[i])
		}
	}
	return out, nil
}

func (r *statefulFindingRepo) ListIncludingArchived(_ context.Context, _ string) ([]commonmodels.LedgerFinding, error) {
	out := make([]commonmodels.LedgerFinding, len(r.findings))
	copy(out, r.findings)
	return out, nil
}

func (r *statefulFindingRepo) Upsert(_ context.Context, f *commonmodels.LedgerFinding) error {
	for i := range r.findings {
		if r.findings[i].ID == f.ID {
			r.findings[i] = *f
			return nil
		}
	}
	r.findings = append(r.findings, *f)
	return nil
}

func (r *statefulFindingRepo) Prune(_ context.Context, _ string, _ int) error { return nil }

// TestRunPhaseReflection_PriorDemandNeedsEvidence holds the two traps in the
// middle of this fix, both through the real phase.
//
// The reflection phase consolidates this run's insights into the ledger and
// only then lists findings for the prompt, so on any run that produced
// anything the list is non-empty. Keying the prior-finding demand off that
// list would order a brand-new project to re-judge findings it created
// seconds earlier — and, on a project with real history whose run touched
// none of it, would demand a verdict the prompt's own rule forbids. Either
// way the model's only route to compliance is to invent one, and an invented
// `resolved` moves a real finding to the front of the prune queue.
//
// The demand therefore rides on evidence: a finding the ledger carried in
// that THIS run surfaced again.
func TestRunPhaseReflection_PriorDemandNeedsEvidence(t *testing.T) {
	const pn = "test-reflection-firstrun"
	gollm.RegisterWithMeta(pn, func(_ gollm.ProviderConfig) (gollm.Provider, error) { return nil, nil },
		gollm.ProviderMeta{
			ID:                       pn,
			Name:                     "reflection first-run test",
			SupportsStructuredOutput: true,
			Models:                   []gollm.ModelEntry{{ID: "mock-model", Wire: gollm.WireOpenAICompat, MaxOutputTokens: 8000}},
		})

	run := func(t *testing.T, seeded []commonmodels.LedgerFinding, insight models.Insight) *gollm.ResponseFormat {
		t.Helper()
		t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
		agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeSuggestOnly})
		t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

		provider := testutil.NewMockLLMProvider()
		provider.DefaultResponse = &gollm.ChatResponse{
			Content: `{"coverage_summary":"orders covered","learnings":[{"note":"status 4 means closed"}],"next_tasks":[{"title":"Explore events","text":"explore the events tables"}]}`,
			Usage:   gollm.Usage{InputTokens: 10, OutputTokens: 20},
		}
		client, err := ai.New(provider, "mock-model")
		if err != nil {
			t.Fatalf("ai.New: %v", err)
		}
		client.SetProvenance("proj-1", "run-1", pn)

		o := &Orchestrator{
			reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
			llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
			ledgerRepo:  &fakeLedgerRepo{},
			findingRepo: &statefulFindingRepo{findings: seeded},
			taskRepo:    &fakeTaskRepo{},
		}
		o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{
			ID: "disc-1", ProjectID: "proj-1",
			Schemas:  map[string]models.TableSchema{"ds.orders": {}, "ds.events": {}},
			Insights: []models.Insight{insight},
		})

		if len(provider.Calls) == 0 {
			t.Fatal("the reflection LLM was not called")
		}
		rf := provider.Calls[0].Request.ResponseFormat
		if rf == nil {
			t.Fatal("ResponseFormat must reach a structured-output provider")
		}
		return rf
	}

	newChurn := models.Insight{AnalysisArea: "churn", Name: "High churn", Severity: "high", AffectedCount: 40}
	carried := func() []commonmodels.LedgerFinding {
		return []commonmodels.LedgerFinding{{
			ID: "f-old", ProjectID: "proj-1", Area: "inventory", Name: "Dead stock",
			Status: "confirmed", SeenCount: 2,
			NormalizedKey: commonmodels.NormalizedFindingKey("inventory", "Dead stock"),
		}}
	}

	t.Run("first run, ledger empty before it", func(t *testing.T) {
		rf := run(t, nil, newChurn)
		if requiredSet(t, rf.Schema)["prior_status_updates"] {
			t.Error("a first run must not be required to re-judge a prior finding — the only findings in the ledger are its own")
		}
		// The demands that do apply are unaffected.
		for _, name := range []string{"coverage_summary", "learnings", "next_tasks"} {
			if !requiredSet(t, rf.Schema)[name] {
				t.Errorf("%s must still be required on a first run", name)
			}
		}
	})

	t.Run("a carried finding this run never touched", func(t *testing.T) {
		rf := run(t, carried(), newChurn)
		if requiredSet(t, rf.Schema)["prior_status_updates"] {
			t.Error("history alone is not evidence: a run that re-saw none of it has nothing honest to re-judge")
		}
	})

	t.Run("a carried finding this run surfaced again", func(t *testing.T) {
		rf := run(t, carried(), models.Insight{
			AnalysisArea: "inventory", Name: "Dead stock", Severity: "high", AffectedCount: 44,
		})
		if !requiredSet(t, rf.Schema)["prior_status_updates"] {
			t.Error("a carried finding surfaced again IS grounded evidence — re-judging at least one must be required")
		}
	})
}

// TestReSeenPriorIsOnThePage_RespectsTheCap. The prompt shows at most
// maxPriorFindingsInPrompt findings, and every finding a run touches — the new
// ones it just created and the carried one it surfaced again — shares the same
// LastSeen, so the tie-break decides who makes the page. A run that created
// enough new findings can push its one re-sighting off it. Demanding a
// re-judgement then puts the model in front of a page of ids it has no
// evidence about, which is precisely the invented verdict the gate exists to
// prevent: it must be membership of the rendered page, not a count.
func TestReSeenPriorIsOnThePage_RespectsTheCap(t *testing.T) {
	now := time.Now()
	carried := commonmodels.LedgerFinding{ID: "f-carried", Name: "Dead stock", LastSeen: now}

	// Every finding this run touched carries the same LastSeen. The carried
	// one is last in list order, so the stable sort leaves it last.
	crowded := make([]commonmodels.LedgerFinding, 0, maxPriorFindingsInPrompt+1)
	for i := 0; i < maxPriorFindingsInPrompt; i++ {
		crowded = append(crowded, commonmodels.LedgerFinding{
			ID: fmt.Sprintf("f-new-%d", i), Name: "Fresh finding", LastSeen: now,
		})
	}
	crowded = append(crowded, carried)

	reSeen := map[string]struct{}{"f-carried": {}}

	if reSeenPriorIsOnThePage(crowded, reSeen) {
		t.Error("the re-seen finding was pushed off the page; no re-judgement may be demanded")
	}
	// The rendered prompt and the gate must agree on what "the page" is.
	if strings.Contains(renderPriorFindings(crowded), "id=f-carried") {
		t.Error("the fixture no longer exercises the cap — the carried finding is still rendered")
	}

	// It fits once the crowd does not fill the page.
	roomy := append([]commonmodels.LedgerFinding{}, crowded[:maxPriorFindingsInPrompt-1]...)
	roomy = append(roomy, carried)
	if !reSeenPriorIsOnThePage(roomy, reSeen) {
		t.Error("a re-seen finding the prompt does show must license the demand")
	}

	// And the degenerate inputs behave: nothing re-seen, nothing shown.
	if reSeenPriorIsOnThePage(roomy, nil) {
		t.Error("no re-seen finding means no demand")
	}
	if reSeenPriorIsOnThePage(nil, reSeen) {
		t.Error("an empty prior list shows nothing, so nothing can be demanded of it")
	}
}

// --- The two provenance fields (enterprise#389) ---
//
// Only two of the investigation graph's eight edges ask the model for anything
// new, and both follow the rule the rest of this file exists to enforce: demand
// an output only on a run that can honestly produce it. The #434 lesson is that
// anything optional comes back empty run after run; the other half of it is
// that anything demanded without grounding comes back invented.

// TestReflectionSchema_OriginFindingIDsDemandedOnlyWithIDsToCopy: a run whose
// prompt lists no finding id — no priors, and no findings of its own — has
// nothing to attribute a thread to.
func TestReflectionSchema_OriginFindingIDsDemandedOnlyWithIDsToCopy(t *testing.T) {
	cases := []struct {
		name          string
		hasFindingIDs bool
		wantRequired  bool
		wantMinItems  int
	}{
		{"prompt carries finding ids", true, true, 1},
		{"prompt carries none", false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := reflectionResponseSchema(agentplugin.EvolutionModeAuto, true, tc.hasFindingIDs)
			props := schema["properties"].(map[string]interface{})
			item := schemaItemProps(t, props, "next_tasks")
			if _, ok := item["origin_finding_ids"]; !ok {
				t.Fatal("origin_finding_ids must always be declared, demanded or not")
			}
			required := requiredSetOfItem(t, props, "next_tasks")
			if got := required["origin_finding_ids"]; got != tc.wantRequired {
				t.Errorf("required = %v, want %v", got, tc.wantRequired)
			}
			min, _ := item["origin_finding_ids"].(map[string]interface{})["minItems"].(int)
			if min != tc.wantMinItems {
				t.Errorf("minItems = %d, want %d", min, tc.wantMinItems)
			}
		})
	}
}

// TestReflectionSchema_ResolvedByOnlyAlongsideDone: a `dropped` is a proven
// dead end. Demanding the findings that answered it would be demanding an
// answer to a question that has none — so the requirement is a two-branch
// condition on the item, not a blanket `required`.
func TestReflectionSchema_ResolvedByOnlyAlongsideDone(t *testing.T) {
	props := reflectionResponseSchema(agentplugin.EvolutionModeAuto, true, true)["properties"].(map[string]interface{})
	prop := props["task_status_updates"].(map[string]interface{})
	item := prop["items"].(map[string]interface{})

	// Never unconditionally required — that would hit `dropped` too.
	if required := requiredSetOfItem(t, props, "task_status_updates"); required["resolved_by_finding_ids"] {
		t.Error("resolved_by_finding_ids must not be unconditionally required: a dropped task has nothing to point at")
	}
	// A reason, by contrast, is always answerable: a close always has one.
	if required := requiredSetOfItem(t, props, "task_status_updates"); !required["reason"] {
		t.Error("reason should be required on every close")
	}

	branches, ok := item["oneOf"].([]interface{})
	if !ok || len(branches) != 2 {
		t.Fatalf("expected a two-branch condition on the item, got %#v", item["oneOf"])
	}
	var sawDoneDemand, sawDroppedExemption bool
	for _, b := range branches {
		branch := b.(map[string]interface{})
		bp := branch["properties"].(map[string]interface{})
		status := bp["status"].(map[string]interface{})["const"]
		switch status {
		case "done":
			req, _ := branch["required"].([]interface{})
			for _, r := range req {
				if r == "resolved_by_finding_ids" {
					sawDoneDemand = true
				}
			}
			arr, _ := bp["resolved_by_finding_ids"].(map[string]interface{})
			if arr["minItems"] != 1 {
				t.Error("the done branch must demand a non-empty array, not just the key")
			}
		case "dropped":
			if _, demanded := branch["required"]; demanded {
				t.Error("the dropped branch must demand nothing")
			}
			sawDroppedExemption = true
		}
	}
	if !sawDoneDemand || !sawDroppedExemption {
		t.Errorf("both branches must be present: done-demands=%v dropped-exempt=%v", sawDoneDemand, sawDroppedExemption)
	}
}

// TestReflectionSchema_NoConditionWithoutIDsToCopy: with no finding ids in the
// prompt there is nothing to point at on a `done` either, so the branch is left
// out entirely rather than demanding an id the model would have to invent.
func TestReflectionSchema_NoConditionWithoutIDsToCopy(t *testing.T) {
	props := reflectionResponseSchema(agentplugin.EvolutionModeAuto, true, false)["properties"].(map[string]interface{})
	item := props["task_status_updates"].(map[string]interface{})["items"].(map[string]interface{})
	if _, ok := item["oneOf"]; ok {
		t.Error("a run with no finding ids must not carry the done/dropped condition")
	}
}

// requiredSetOfItem returns the item-level `required` list of an array property
// as a set.
func requiredSetOfItem(t *testing.T, props map[string]interface{}, name string) map[string]bool {
	t.Helper()
	prop, ok := props[name].(map[string]interface{})
	if !ok {
		t.Fatalf("schema has no property %q", name)
	}
	item, ok := prop["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("property %q has no items shape", name)
	}
	out := map[string]bool{}
	req, _ := item["required"].([]interface{})
	for _, r := range req {
		if s, ok := r.(string); ok {
			out[s] = true
		}
	}
	return out
}

// TestReflectionPrompt_ProvenanceBulletsAreConditional is the prompt half of
// the same rule — the schema is the contract, but the prompt is what teaches.
func TestReflectionPrompt_ProvenanceBulletsAreConditional(t *testing.T) {
	withIDs := renderProvenanceFields(true)
	for _, want := range []string{"origin_finding_ids", "resolved_by_finding_ids", "Never invent an id"} {
		if !strings.Contains(withIDs, want) {
			t.Errorf("a run with finding ids must be told about %q: %s", want, withIDs)
		}
	}
	withoutIDs := renderProvenanceFields(false)
	if !strings.Contains(withoutIDs, "omit both") {
		t.Errorf("a run with no finding ids must be told to omit both, got: %s", withoutIDs)
	}
}

// TestReflectionPrompt_StoredGraphFieldsNeverReachTheModel is acceptance
// criterion 6. The investigation graph is a write-side and read-side feature:
// the sightings, the status trail, the archive flag and the edge arrays all
// exist to be read by the API and the dashboard, and none of them belongs in a
// prompt. A field that leaked in here would grow every run's context in
// proportion to the project's history.
func TestReflectionPrompt_StoredGraphFieldsNeverReachTheModel(t *testing.T) {
	seen := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	prior := []commonmodels.LedgerFinding{{
		ID: "f-1", Area: "inventory", Name: "Slow movers", Status: "confirmed", SeenCount: 4,
		KeyMetric: "units=1200", LastSeen: seen,
		SourceInsightIDs: []string{"ins-old-1", "ins-old-2"},
		AnsweredTaskIDs:  []string{"t-old"},
		Sightings: []commonmodels.FindingSighting{
			{RunID: "r-old", DiscoveryID: "d-old", AffectedCount: 11, InsightID: "ins-old-1"},
		},
		StatusHistory: []commonmodels.FindingStatusChange{
			{Status: "confirmed", Reason: "SECRET-TRAIL-REASON", RunID: "r-old", Source: "reflection"},
		},
	}}
	tasks := []commonmodels.LedgerTask{{
		ID: "t-1", Kind: "next_task", Text: "check the fulfilment centres",
		OriginFindingIDs: []string{"f-origin"}, ResolvedByFindingIDs: []string{"f-answer"},
		PursuedInRunIDs: []string{"r-old"}, CloseReason: "SECRET-CLOSE-REASON",
	}}
	o := &Orchestrator{language: "English", datasets: []string{"ds"}}
	result := &models.DiscoveryResult{
		Schemas:  map[string]models.TableSchema{"ds.orders": {}},
		Insights: []models.Insight{{ID: "ins-1", AnalysisArea: "churn", Name: "Churn up", Severity: "high"}},
	}
	pol := agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeAuto, FrontierPolicy: agentplugin.FrontierBalanced}

	// Both prompts the ledger feeds.
	reflectionPrompt := o.buildReflectionPrompt(result, prior, tasks, pol, nil, true, true)
	explorationPrompt := renderLedgerFindings(prior) + renderLedgerTasks(tasks)

	leaks := []string{
		"ins-old-1", "ins-old-2", "t-old", "r-old", "d-old",
		"SECRET-TRAIL-REASON", "SECRET-CLOSE-REASON", "f-origin", "f-answer",
		"sightings", "status_history", "answered_task_ids", "pursued_in_run_ids", "archived",
	}
	for _, leak := range leaks {
		if strings.Contains(reflectionPrompt, leak) {
			t.Errorf("stored graph data %q reached the reflection prompt", leak)
		}
		if strings.Contains(explorationPrompt, leak) {
			t.Errorf("stored graph data %q reached the exploration prompt", leak)
		}
	}
	// What the exploration prompt DOES newly carry is the thread's own id, so
	// the phase can name a thread it pursued. That is one short id per open
	// task, and nothing else.
	if !strings.Contains(explorationPrompt, "[t-1]") {
		t.Error("the exploration prompt should carry each open thread's id")
	}
}
