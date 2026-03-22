package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestGetDatasets_MultipleDatasets(t *testing.T) {
	w := WarehouseConfig{
		Datasets: []string{"events_prod", "features_prod"},
	}
	ds := w.GetDatasets()
	if len(ds) != 2 {
		t.Errorf("len = %d, want 2", len(ds))
	}
	if ds[0] != "events_prod" {
		t.Errorf("ds[0] = %q", ds[0])
	}
}

func TestGetDatasets_Empty(t *testing.T) {
	w := WarehouseConfig{}
	ds := w.GetDatasets()
	if ds != nil {
		t.Errorf("should return nil for empty config, got %v", ds)
	}
}

func TestGetDatasets_SingleInArray(t *testing.T) {
	w := WarehouseConfig{
		Datasets: []string{"only_one"},
	}
	ds := w.GetDatasets()
	if len(ds) != 1 || ds[0] != "only_one" {
		t.Errorf("ds = %v", ds)
	}
}

func TestProject_JSONRoundTrip(t *testing.T) {
	now := time.Now()
	lastRun := now.Add(-1 * time.Hour)
	project := Project{
		ID:          "proj-123",
		Name:        "My Match-3 Game",
		Description: "Puzzle game analytics",
		Domain:      "gaming",
		Category:    "match3",
		Warehouse: WarehouseConfig{
			Provider:  "bigquery",
			ProjectID: "gcp-project-id",
			Location:  "US",
			Datasets:  []string{"analytics_prod"},
		},
		LLM: LLMConfig{
			Provider: "claude",
			Model:    "claude-sonnet-4-20250514",
		},
		Schedule: ScheduleConfig{
			Enabled:  true,
			CronExpr: "0 6 * * 1",
			MaxSteps: 50,
		},
		Profile: map[string]interface{}{
			"game_type": "match3",
			"genre":     "puzzle",
		},
		Status:        "active",
		LastRunAt:     &lastRun,
		LastRunStatus: "completed",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	data, err := json.Marshal(project)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var parsed Project
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if parsed.ID != "proj-123" {
		t.Errorf("ID = %q, want proj-123", parsed.ID)
	}
	if parsed.Name != "My Match-3 Game" {
		t.Errorf("Name = %q", parsed.Name)
	}
	if parsed.Domain != "gaming" {
		t.Errorf("Domain = %q, want gaming", parsed.Domain)
	}
	if parsed.Category != "match3" {
		t.Errorf("Category = %q, want match3", parsed.Category)
	}
	if parsed.Warehouse.Provider != "bigquery" {
		t.Errorf("Warehouse.Provider = %q, want bigquery", parsed.Warehouse.Provider)
	}
	if parsed.LLM.Provider != "claude" {
		t.Errorf("LLM.Provider = %q, want claude", parsed.LLM.Provider)
	}
	if !parsed.Schedule.Enabled {
		t.Error("Schedule.Enabled should be true")
	}
	if parsed.LastRunAt == nil {
		t.Error("LastRunAt should not be nil")
	}
	if parsed.Status != "active" {
		t.Errorf("Status = %q, want active", parsed.Status)
	}
}

func TestProject_WarehouseConfig(t *testing.T) {
	tests := []struct {
		name   string
		config WarehouseConfig
	}{
		{
			name: "bigquery config",
			config: WarehouseConfig{
				Provider:    "bigquery",
				ProjectID:   "my-gcp-project",
				Location:    "US",
				Datasets:    []string{"analytics"},
				FilterField: "app_id",
				FilterValue: "game-123",
			},
		},
		{
			name: "redshift config",
			config: WarehouseConfig{
				Provider: "redshift",
				Datasets: []string{"public"},
				Config: map[string]string{
					"workgroup": "default",
					"database":  "analytics",
					"region":    "us-east-1",
				},
			},
		},
		{
			name: "minimal config",
			config: WarehouseConfig{
				Provider: "bigquery",
				Datasets: []string{"events"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Provider == "" {
				t.Error("Provider should not be empty")
			}
			ds := tt.config.GetDatasets()
			if len(ds) == 0 {
				t.Error("should have at least one dataset")
			}
		})
	}
}

func TestProject_LLMConfig(t *testing.T) {
	tests := []struct {
		name   string
		config LLMConfig
	}{
		{
			name: "claude config",
			config: LLMConfig{
				Provider: "claude",
				Model:    "claude-sonnet-4-20250514",
			},
		},
		{
			name: "openai config",
			config: LLMConfig{
				Provider: "openai",
				Model:    "gpt-4o",
			},
		},
		{
			name: "vertex-ai config",
			config: LLMConfig{
				Provider: "vertex-ai",
				Model:    "claude-sonnet-4-20250514",
				Config: map[string]string{
					"project_id": "my-gcp-project",
					"location":   "us-central1",
				},
			},
		},
		{
			name: "ollama config",
			config: LLMConfig{
				Provider: "ollama",
				Model:    "llama3.3",
				Config: map[string]string{
					"host": "http://localhost:11434",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.config.Provider == "" {
				t.Error("Provider should not be empty")
			}
			if tt.config.Model == "" {
				t.Error("Model should not be empty")
			}
		})
	}
}

func TestScheduleConfig_Fields(t *testing.T) {
	s := ScheduleConfig{
		Enabled:  true,
		CronExpr: "0 6 * * 1",
		MaxSteps: 50,
	}

	if !s.Enabled {
		t.Error("Enabled should be true")
	}
	if s.CronExpr != "0 6 * * 1" {
		t.Errorf("CronExpr = %q, want '0 6 * * 1'", s.CronExpr)
	}
	if s.MaxSteps != 50 {
		t.Errorf("MaxSteps = %d, want 50", s.MaxSteps)
	}
}

func TestProjectPrompts_Fields(t *testing.T) {
	prompts := ProjectPrompts{
		Exploration:     "Explore the data warehouse...",
		Recommendations: "Generate recommendations...",
		BaseContext:     "You are analyzing a match-3 game...",
		AnalysisAreas: map[string]AnalysisAreaConfig{
			"churn": {
				Name:        "Churn Risks",
				Description: "Analyze player churn",
				Keywords:    []string{"churn", "retention", "dropout"},
				Prompt:      "Analyze churn patterns...",
				IsBase:      true,
				IsCustom:    false,
				Priority:    1,
				Enabled:     true,
			},
			"whales": {
				Name:        "Whale Analysis",
				Description: "Analyze high-spending players",
				Keywords:    []string{"whale", "spend", "purchase"},
				Prompt:      "Find whale patterns...",
				IsBase:      false,
				IsCustom:    true,
				Priority:    10,
				Enabled:     true,
			},
		},
	}

	if prompts.Exploration == "" {
		t.Error("Exploration should be set")
	}
	if prompts.Recommendations == "" {
		t.Error("Recommendations should be set")
	}
	if prompts.BaseContext == "" {
		t.Error("BaseContext should be set")
	}
	if len(prompts.AnalysisAreas) != 2 {
		t.Errorf("AnalysisAreas = %d, want 2", len(prompts.AnalysisAreas))
	}

	churn := prompts.AnalysisAreas["churn"]
	if !churn.IsBase {
		t.Error("churn should be a base area")
	}
	if churn.IsCustom {
		t.Error("churn should not be custom")
	}
	if !churn.Enabled {
		t.Error("churn should be enabled")
	}

	whales := prompts.AnalysisAreas["whales"]
	if whales.IsBase {
		t.Error("whales should not be a base area")
	}
	if !whales.IsCustom {
		t.Error("whales should be custom")
	}
}

func TestWarehouseConfig_ConfigMap(t *testing.T) {
	w := WarehouseConfig{
		Provider: "redshift",
		Datasets: []string{"public"},
		Config: map[string]string{
			"workgroup":  "analytics-wg",
			"database":   "analytics_db",
			"region":     "us-east-1",
			"cluster_id": "my-cluster",
		},
	}

	if w.Config["workgroup"] != "analytics-wg" {
		t.Errorf("workgroup = %q", w.Config["workgroup"])
	}
	if w.Config["database"] != "analytics_db" {
		t.Errorf("database = %q", w.Config["database"])
	}
	if w.Config["region"] != "us-east-1" {
		t.Errorf("region = %q", w.Config["region"])
	}
}
