//go:build integration

package database

import (
	"context"
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// ProjectRepository.Update issues {"$set": project}, and every field a
// handler can CLEAR is tagged omitempty — so resolving one to empty omits the
// key and leaves the stored value in place. The documented clear paths
// (blurb_llm on an empty-provider request; removing a provider entry or a
// layer override) therefore silently do nothing.
//
// These tests were written red against that behaviour before $unset existed.
func TestInteg_ProjectUpdate_ClearsBlurbLLM(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	p := &models.Project{
		Name:   "clear-blurb",
		Domain: "retail",
		Status: "active",
	}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.BlurbLLM = &models.BlurbLLMConfig{Provider: "litellm", Model: "qwen3-32b"}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p.BlurbLLM = nil
	if err := repo.Update(ctx, p.ID, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.BlurbLLM != nil {
		t.Fatalf("blurb_llm = %+v after a clear; the stored override survived the update, so the table-description model reverts behind the user's back", got.BlurbLLM)
	}

	var raw bson.M
	if err := testDB.Collection("projects").FindOne(ctx, bson.M{"name": "clear-blurb"}).Decode(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if _, present := raw["blurb_llm"]; present {
		t.Error("blurb_llm key is still present in the document")
	}
}

func TestInteg_ProjectUpdate_ClearsProviderEntriesAndLayerOverrides(t *testing.T) {
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	p := &models.Project{Name: "clear-layers", Domain: "retail", Status: "active"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.LLMProviders = []llmlayer.ProviderEntry{
		{ID: llmlayer.DefaultProviderID, Provider: "bedrock"},
		{ID: "deepinfra", Provider: "litellm"},
	}
	p.LayerModels = map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer:      {ProviderID: llmlayer.DefaultProviderID, Model: "opus"},
		llmlayer.LayerExploration:  {ProviderID: "deepinfra", Model: "deepseek"},
	}
	p.LLMConfigVersion = llmlayer.ConfigVersion
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A project reset to "no per-layer configuration at all" — what the
	// managed-AI path does when a tenant moves to gateway routing, and what a
	// support operation does to re-run the migration.
	p.LLMProviders = nil
	p.LayerModels = nil
	if err := repo.Update(ctx, p.ID, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(got.LLMProviders) != 0 {
		t.Errorf("llm_providers = %+v after a clear", got.LLMProviders)
	}
	if len(got.LayerModels) != 0 {
		t.Errorf("layer_models = %+v after a clear", got.LayerModels)
	}
}

func TestInteg_ProjectUpdate_OmissionIsNotAReset(t *testing.T) {
	// The other half of the semantics: $unset must fire only for a field the
	// handler RESOLVED to empty. Every Update call passes the merged
	// `existing` struct, so a field the client did not send is still
	// populated here and must survive.
	ctx := context.Background()
	repo := NewProjectRepository(testDB)

	p := &models.Project{Name: "omission-not-reset", Domain: "retail", Status: "active"}
	p.LLM = models.LLMConfig{Provider: "bedrock", Model: "opus"}
	p.BlurbLLM = &models.BlurbLLMConfig{Provider: "litellm", Model: "qwen3-32b"}
	p.LLMProviders = []llmlayer.ProviderEntry{{ID: llmlayer.DefaultProviderID, Provider: "bedrock"}}
	p.LayerModels = map[string]llmlayer.LayerModel{llmlayer.DefaultLayer: {Model: "opus"}}
	p.LLMConfigVersion = llmlayer.ConfigVersion
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	p.Name = "omission-not-reset-renamed"
	if err := repo.Update(ctx, p.ID, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.BlurbLLM == nil || got.BlurbLLM.Model != "qwen3-32b" {
		t.Errorf("blurb_llm = %+v, want the stored override preserved by an unrelated edit", got.BlurbLLM)
	}
	if len(got.LLMProviders) != 1 || len(got.LayerModels) != 1 {
		t.Errorf("provider entries / layer rows were reset by an unrelated edit: %+v / %+v", got.LLMProviders, got.LayerModels)
	}
	if got.LLMConfigVersion != llmlayer.ConfigVersion {
		t.Errorf("llm_config_version = %d, want %d — a rename must not re-gate the project", got.LLMConfigVersion, llmlayer.ConfigVersion)
	}
}
