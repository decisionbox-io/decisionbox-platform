package discovery

import (
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
