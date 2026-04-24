package models

import (
	"encoding/json"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

func TestSchemaIndexStatusConstants_Agent(t *testing.T) {
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

func TestSchemaIndexPhaseConstants_Agent(t *testing.T) {
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

func TestBlurbLLMConfig_JSONRoundTrip_Agent(t *testing.T) {
	original := BlurbLLMConfig{
		Provider: "openai",
		Model:    "gpt-4.1-nano",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded BlurbLLMConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Provider != "openai" || decoded.Model != "gpt-4.1-nano" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestSchemaIndexProgress_BSONRoundTrip_Agent(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	original := SchemaIndexProgress{
		ProjectID:   "p",
		Phase:       SchemaIndexPhaseEmbedding,
		TablesTotal: 10,
		TablesDone:  7,
		StartedAt:   now,
		UpdatedAt:   now,
	}
	b, err := bson.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SchemaIndexProgress
	if err := bson.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.TablesDone != 7 {
		t.Errorf("TablesDone = %d", decoded.TablesDone)
	}
}

func TestProject_SchemaIndex_Agent_RoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	p := Project{
		ID:                   "p",
		Name:                 "t",
		Domain:               "gaming",
		Category:             "match3",
		SchemaIndexStatus:    SchemaIndexStatusIndexing,
		SchemaIndexUpdatedAt: &now,
		BlurbLLM: &BlurbLLMConfig{
			Provider: "bedrock",
			Model:    "qwen.qwen3-32b-v1:0",
		},
		SchemaRetrieval: &SchemaRetrievalConfig{TopK: 60},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Project
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaIndexStatus != "indexing" {
		t.Errorf("status = %q", decoded.SchemaIndexStatus)
	}
	if decoded.BlurbLLM == nil || decoded.BlurbLLM.Provider != "bedrock" {
		t.Errorf("BlurbLLM = %+v", decoded.BlurbLLM)
	}
	if decoded.SchemaRetrieval == nil || decoded.SchemaRetrieval.TopK != 60 {
		t.Errorf("SchemaRetrieval = %+v", decoded.SchemaRetrieval)
	}
}

func TestProject_SchemaIndex_Agent_OmitEmpty(t *testing.T) {
	p := Project{ID: "p", Name: "t"}
	data, _ := json.Marshal(p)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)
	for _, f := range []string{
		"blurb_llm",
		"schema_retrieval",
		"schema_index_status",
		"schema_index_error",
		"schema_index_updated_at",
	} {
		if _, ok := raw[f]; ok {
			t.Errorf("%q should be omitted", f)
		}
	}
}
