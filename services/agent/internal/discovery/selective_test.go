package discovery

import (
	"reflect"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- resolvePrompts (reads from project prompts only) ---

func TestResolvePrompts_NilProjectPrompts(t *testing.T) {
	o := &Orchestrator{projectPrompts: nil}

	prompts, areas := o.resolvePrompts()

	if prompts.Exploration != "" {
		t.Error("should return empty prompts when projectPrompts is nil")
	}
	if len(areas) != 0 {
		t.Error("should return no areas when projectPrompts is nil")
	}
}

func TestResolvePrompts_BasicExtraction(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			Exploration:     "explore prompt",
			Recommendations: "recommend prompt",
			BaseContext:     "base context",
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"churn": {
					Name:     "Churn Risks",
					Keywords: []string{"churn", "retention"},
					Prompt:   "analyze churn",
					IsBase:   true,
					Enabled:  true,
					Priority: 1,
				},
			},
		},
	}

	prompts, areas := o.resolvePrompts()

	if prompts.Exploration != "explore prompt" {
		t.Errorf("exploration = %q", prompts.Exploration)
	}
	if prompts.Recommendations != "recommend prompt" {
		t.Errorf("recommendations = %q", prompts.Recommendations)
	}
	if prompts.BaseContext != "base context" {
		t.Errorf("base_context = %q", prompts.BaseContext)
	}
	if prompts.AnalysisAreas["churn"] != "analyze churn" {
		t.Errorf("churn prompt = %q", prompts.AnalysisAreas["churn"])
	}
	if len(areas) != 1 || areas[0].ID != "churn" {
		t.Errorf("areas = %v, want [churn]", areas)
	}
}

func TestResolvePrompts_DisabledArea(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"churn":      {Name: "Churn", Enabled: true, Prompt: "c", Priority: 1},
				"engagement": {Name: "Engagement", Enabled: false, Prompt: "e", Priority: 2},
			},
		},
	}

	prompts, areas := o.resolvePrompts()

	if _, ok := prompts.AnalysisAreas["engagement"]; ok {
		t.Error("disabled area should be excluded from prompts")
	}
	if len(areas) != 1 || areas[0].ID != "churn" {
		t.Errorf("areas = %v, want only churn", areas)
	}
}

func TestResolvePrompts_CustomArea(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"churn":  {Name: "Churn", Enabled: true, Prompt: "c", IsBase: true, Priority: 1},
				"whales": {Name: "Whale Analysis", Enabled: true, Prompt: "find whales", IsCustom: true, Keywords: []string{"whale"}, Priority: 10},
			},
		},
	}

	prompts, areas := o.resolvePrompts()

	if prompts.AnalysisAreas["whales"] != "find whales" {
		t.Error("custom area prompt should be included")
	}
	found := false
	for _, a := range areas {
		if a.ID == "whales" {
			found = true
		}
	}
	if !found {
		t.Error("custom area should appear in areas list")
	}
}

func TestResolvePrompts_MultipleAreas(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"churn":        {Name: "Churn", Enabled: true, Prompt: "c", IsBase: true, Priority: 1},
				"engagement":   {Name: "Engagement", Enabled: true, Prompt: "e", IsBase: true, Priority: 2},
				"monetization": {Name: "Monetization", Enabled: true, Prompt: "m", IsBase: true, Priority: 3},
				"levels":       {Name: "Levels", Enabled: true, Prompt: "l", IsBase: false, Priority: 4},
			},
		},
	}

	prompts, areas := o.resolvePrompts()

	if len(prompts.AnalysisAreas) != 4 {
		t.Errorf("prompt areas = %d, want 4", len(prompts.AnalysisAreas))
	}
	if len(areas) != 4 {
		t.Errorf("areas = %d, want 4", len(areas))
	}
}

// --- DiscoveryResult run type ---

func TestDiscoveryResult_RunType(t *testing.T) {
	result := models.DiscoveryResult{
		RunType:        "partial",
		AreasRequested: []string{"churn", "levels"},
	}

	if result.RunType != "partial" {
		t.Error("RunType should be partial")
	}
	if len(result.AreasRequested) != 2 {
		t.Error("AreasRequested should have 2 areas")
	}
}

func TestDiscoveryResult_FullRun(t *testing.T) {
	result := models.DiscoveryResult{RunType: "full"}

	if result.RunType != "full" {
		t.Error("RunType should be full")
	}
	if result.AreasRequested != nil {
		t.Error("AreasRequested should be nil for full run")
	}
}

// --- area order ---

// areaIDs is the order resolvePrompts handed back, which is the order the run
// works through the areas and therefore the order the validation budget is
// spent in.
func areaIDs(areas []AnalysisArea) []string {
	out := make([]string, 0, len(areas))
	for _, a := range areas {
		out = append(out, a.ID)
	}
	return out
}

// The areas come out of a map, and a Go map is ranged in a randomised order, so
// the assertion is repeated: a single pass can agree with the wanted order by
// chance. Before the sort this failed within a handful of iterations.
func TestResolvePrompts_AreasComeBackInPriorityOrder(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"zebra":    {Name: "Zebra", Prompt: "p", Enabled: true, Priority: 1},
				"apple":    {Name: "Apple", Prompt: "p", Enabled: true, Priority: 1},
				"middle":   {Name: "Middle", Prompt: "p", Enabled: true, Priority: 2},
				"tail":     {Name: "Tail", Prompt: "p", Enabled: true, Priority: 3},
				"unranked": {Name: "Unranked", Prompt: "p", Enabled: true},
			},
		},
	}
	want := []string{"apple", "zebra", "middle", "tail", "unranked"}

	for i := 0; i < 50; i++ {
		_, areas := o.resolvePrompts()
		if got := areaIDs(areas); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: area order = %v, want %v", i, got, want)
		}
	}
}

// An unranked area sorts last rather than first: every pack the product ships
// numbers its areas from one, so a zero is an area nobody ranked, and it must
// not displace one somebody did.
func TestResolvePrompts_UnrankedAreasSortAfterRankedOnes(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"aaa-unranked": {Name: "A", Prompt: "p", Enabled: true},
				"zzz-ranked":   {Name: "Z", Prompt: "p", Enabled: true, Priority: 9},
			},
		},
	}
	for i := 0; i < 50; i++ {
		_, areas := o.resolvePrompts()
		got := areaIDs(areas)
		if !reflect.DeepEqual(got, []string{"zzz-ranked", "aaa-unranked"}) {
			t.Fatalf("iteration %d: area order = %v, want the ranked area first", i, got)
		}
	}
}

// A disabled area is still absent, and the ones that remain are still ordered.
func TestResolvePrompts_DisabledAreasAreExcludedAndTheRestStayOrdered(t *testing.T) {
	o := &Orchestrator{
		projectPrompts: &models.ProjectPrompts{
			AnalysisAreas: map[string]models.AnalysisAreaConfig{
				"first":  {Name: "First", Prompt: "p", Enabled: true, Priority: 1},
				"off":    {Name: "Off", Prompt: "p", Enabled: false, Priority: 2},
				"second": {Name: "Second", Prompt: "p", Enabled: true, Priority: 3},
			},
		},
	}
	for i := 0; i < 50; i++ {
		prompts, areas := o.resolvePrompts()
		if got := areaIDs(areas); !reflect.DeepEqual(got, []string{"first", "second"}) {
			t.Fatalf("iteration %d: area order = %v, want the two enabled areas in priority order", i, got)
		}
		if _, ok := prompts.AnalysisAreas["off"]; ok {
			t.Error("a disabled area must not reach the resolved prompts")
		}
	}
}
