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

// TestRenderPriorStatusField_OnlyDemandsWhatThereIsToJudge. An early run has
// no prior findings, and a model told to produce a verdict anyway has no id to
// attach it to but one it invents — so the demand appears only alongside a
// non-empty PRIOR FINDINGS list.
func TestRenderPriorStatusField_OnlyDemandsWhatThereIsToJudge(t *testing.T) {
	first := renderPriorStatusField(false)
	if first != reflectionPriorStatusFieldBase {
		t.Errorf("a run with no prior findings must get the bullet unchanged, got %q", first)
	}

	withPrior := renderPriorStatusField(true)
	if !strings.HasPrefix(withPrior, reflectionPriorStatusFieldBase) {
		t.Error("the demand must be added to the bullet, not replace it")
	}
	if !strings.Contains(withPrior, "at least one") {
		t.Errorf("a run with prior findings must be asked to re-judge at least one, got %q", withPrior)
	}
	// The grounding rule is what keeps the demand honest — it must survive.
	for _, keep := range []string{"Do NOT mark a finding `resolved` just because it did not reappear", "grounded evidence for `confirmed`"} {
		if !strings.Contains(withPrior, keep) {
			t.Errorf("bullet lost %q: %s", keep, withPrior)
		}
	}
}

// TestBuildReflectionPrompt_FirstRunCarriesNoPriorDemand is the prompt half of
// the same rule, through the real render: a first run's prompt says there are
// no prior findings, so it must not also ask for one to be re-judged.
func TestBuildReflectionPrompt_FirstRunCarriesNoPriorDemand(t *testing.T) {
	o, result, _, tasks, pol := reflectionPromptFixture()

	got := o.buildReflectionPrompt(result, nil, tasks, pol, nil)

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
		prior        []commonmodels.LedgerFinding
		wantRequired []string
	}{
		{
			name:         "evolution off, first run",
			mode:         agentplugin.EvolutionModeOff,
			wantRequired: []string{"coverage_summary", "learnings"},
		},
		{
			name:         "suggest_only with a ledger behind it",
			mode:         agentplugin.EvolutionModeSuggestOnly,
			prior:        []commonmodels.LedgerFinding{{ID: "f-1", Name: "Dead stock", Status: "confirmed"}},
			wantRequired: []string{"coverage_summary", "learnings", "next_tasks", "prior_status_updates"},
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
