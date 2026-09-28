package discovery

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

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
				schema := reflectionResponseSchema(mode, hasPrior)
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
	on := reflectionResponseSchema(agentplugin.EvolutionModeSuggestOnly, true)
	for _, name := range []string{"learnings", "prior_status_updates", "next_tasks"} {
		if desc := descriptionOf(t, on, name); !strings.Contains(desc, "at least one") {
			t.Errorf("%s description does not demand at least one: %q", name, desc)
		}
	}

	// And the demand is not left behind on a run that must not carry it.
	off := reflectionResponseSchema(agentplugin.EvolutionModeOff, false)
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

	got := o.buildReflectionPrompt(result, nil, tasks, pol, nil, false)

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

	tests := []struct {
		name         string
		mode         agentplugin.EvolutionMode
		reSeen       bool
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
			reSeen:       true,
			prior:        []commonmodels.LedgerFinding{{ID: "f-1", Name: "Dead stock", Status: "confirmed"}},
			wantRequired: []string{"coverage_summary", "learnings", "next_tasks", "prior_status_updates"},
		},
		{
			// The findings are in the ledger, but this run touched none of
			// them — either they are its own, just consolidated, or the run
			// explored elsewhere. Absence is not proof, so there is nothing
			// honest to say and the demand must not fire.
			name:         "suggest_only, findings in the ledger but none re-seen",
			mode:         agentplugin.EvolutionModeSuggestOnly,
			reSeen:       false,
			prior:        []commonmodels.LedgerFinding{{ID: "f-1", Name: "Dead stock", Status: "confirmed"}},
			wantRequired: []string{"coverage_summary", "learnings", "next_tasks"},
		},
		{
			// The mirror case: a carried finding WAS re-seen, but the list
			// read failed so the prompt shows none. Demanding a verdict on a
			// list the model cannot see is how ids get invented.
			name:         "re-seen finding the prompt cannot show",
			mode:         agentplugin.EvolutionModeSuggestOnly,
			reSeen:       true,
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
				tc.reSeen,
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
