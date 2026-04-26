package models

import (
	"encoding/json"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestSchemaIndexStatusConstants(t *testing.T) {
	// Order matters for UI tab labels & migrations — if any of these
	// renames happen, it's a breaking data-model change, not a refactor.
	cases := map[string]string{
		SchemaIndexStatusPendingIndexing: "pending_indexing",
		SchemaIndexStatusIndexing:        "indexing",
		SchemaIndexStatusReady:           "ready",
		SchemaIndexStatusFailed:          "failed",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("status constant = %q, want %q", got, want)
		}
	}
}

func TestSchemaIndexPhaseConstants(t *testing.T) {
	cases := map[string]string{
		SchemaIndexPhaseListingTables:    "listing_tables",
		SchemaIndexPhaseDescribingTables: "describing_tables",
		SchemaIndexPhaseEmbedding:        "embedding",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("phase constant = %q, want %q", got, want)
		}
	}
}

func TestBlurbLLMConfig_JSONRoundTrip(t *testing.T) {
	original := BlurbLLMConfig{
		Provider: "bedrock",
		Model:    "qwen.qwen3-32b-v1:0",
		Config: map[string]string{
			"region": "us-east-1",
		},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BlurbLLMConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Provider != "bedrock" {
		t.Errorf("Provider = %q", decoded.Provider)
	}
	if decoded.Model != "qwen.qwen3-32b-v1:0" {
		t.Errorf("Model = %q", decoded.Model)
	}
	if decoded.Config["region"] != "us-east-1" {
		t.Errorf("Config[region] = %q", decoded.Config["region"])
	}
}

func TestBlurbLLMConfig_OmitEmptyConfig(t *testing.T) {
	b := BlurbLLMConfig{Provider: "openai", Model: "gpt-4.1-nano"}
	data, _ := json.Marshal(b)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["config"]; ok {
		t.Error("nil Config should be omitted")
	}
}

func TestSchemaIndexProgress_JSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := SchemaIndexProgress{
		ProjectID:   "proj-1",
		RunID:       "idx-run-1",
		Phase:       SchemaIndexPhaseDescribingTables,
		TablesTotal: 2000,
		TablesDone:  342,
		StartedAt:   now,
		UpdatedAt:   now,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SchemaIndexProgress
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q", decoded.ProjectID)
	}
	if decoded.Phase != "describing_tables" {
		t.Errorf("Phase = %q", decoded.Phase)
	}
	if decoded.TablesTotal != 2000 || decoded.TablesDone != 342 {
		t.Errorf("TablesTotal=%d TablesDone=%d", decoded.TablesTotal, decoded.TablesDone)
	}
}

func TestSchemaIndexProgress_BSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := SchemaIndexProgress{
		ProjectID:    "proj-1",
		Phase:        SchemaIndexPhaseEmbedding,
		TablesTotal:  40,
		TablesDone:   40,
		StartedAt:    now,
		UpdatedAt:    now,
		ErrorMessage: "",
	}
	b, err := bson.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SchemaIndexProgress
	if err := bson.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Phase != "embedding" {
		t.Errorf("Phase = %q", decoded.Phase)
	}
	if decoded.TablesDone != 40 {
		t.Errorf("TablesDone = %d", decoded.TablesDone)
	}

	// error_message is omitempty — round-trip via raw BSON should not carry it.
	raw := bson.Raw(b)
	if _, err := raw.LookupErr("error_message"); err == nil {
		t.Error("empty error_message should be omitted from BSON")
	}
}

func TestSchemaIndexProgress_OmitEmptyRunID(t *testing.T) {
	p := SchemaIndexProgress{ProjectID: "proj-1", Phase: SchemaIndexPhaseListingTables}
	data, _ := json.Marshal(p)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["run_id"]; ok {
		t.Error("empty RunID should be omitted")
	}
}

func TestProject_SchemaIndexFields_RoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := Project{
		ID:                "proj-1",
		Name:              "t",
		Domain:            "gaming",
		Category:          "match3",
		SchemaIndexStatus: SchemaIndexStatusReady,
		SchemaIndexError:  "",
		SchemaIndexUpdatedAt: &now,
		BlurbLLM: &BlurbLLMConfig{
			Provider: "bedrock",
			Model:    "qwen.qwen3-32b-v1:0",
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Project
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaIndexStatus != "ready" {
		t.Errorf("SchemaIndexStatus = %q", decoded.SchemaIndexStatus)
	}
	if decoded.SchemaIndexUpdatedAt == nil || !decoded.SchemaIndexUpdatedAt.Equal(now) {
		t.Errorf("SchemaIndexUpdatedAt = %v", decoded.SchemaIndexUpdatedAt)
	}
	if decoded.BlurbLLM == nil || decoded.BlurbLLM.Model != "qwen.qwen3-32b-v1:0" {
		t.Errorf("BlurbLLM = %+v", decoded.BlurbLLM)
	}
}

func TestProject_SchemaIndex_OmitEmpty(t *testing.T) {
	p := Project{ID: "p", Name: "t"}
	data, _ := json.Marshal(p)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)

	for _, f := range []string{
		"blurb_llm",
		"schema_index_status",
		"schema_index_error",
		"schema_index_updated_at",
	} {
		if _, ok := raw[f]; ok {
			t.Errorf("%q should be omitted when zero", f)
		}
	}
}

func TestProject_SchemaIndex_Failed_HasError(t *testing.T) {
	p := Project{
		ID:                "p",
		Name:              "t",
		SchemaIndexStatus: SchemaIndexStatusFailed,
		SchemaIndexError:  "embedding provider unreachable",
	}
	data, _ := json.Marshal(p)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)
	if raw["schema_index_status"] != "failed" {
		t.Errorf("schema_index_status = %v", raw["schema_index_status"])
	}
	if raw["schema_index_error"] != "embedding provider unreachable" {
		t.Errorf("schema_index_error = %v", raw["schema_index_error"])
	}
}
