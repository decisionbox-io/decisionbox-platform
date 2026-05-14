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
		ProjectID:    "p",
		Phase:        SchemaIndexPhaseEmbedding,
		TablesTotal:  10,
		TablesDone:   7,
		StartedAt:    now,
		UpdatedAt:    now,
		InputTokens:  4200,
		OutputTokens: 950,
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
	// Input/output token totals must round-trip.
	if decoded.InputTokens != 4200 || decoded.OutputTokens != 950 {
		t.Errorf("tokens lost in round-trip: got (%d, %d), want (4200, 950)", decoded.InputTokens, decoded.OutputTokens)
	}
}

func TestSchemaIndexProgress_JSONOmitemptyOnZero_Agent(t *testing.T) {
	// Legacy rows (built before tokens were tracked) and rows decoded after
	// Reset must render the token fields as absent rather than 0 — the
	// dashboard relies on this to distinguish "unknown" from "zero spent".
	p := SchemaIndexProgress{ProjectID: "p", Phase: SchemaIndexPhaseListingTables}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["input_tokens"]; ok {
		t.Errorf("input_tokens should be omitted when zero; raw=%v", raw)
	}
	if _, ok := raw["output_tokens"]; ok {
		t.Errorf("output_tokens should be omitted when zero; raw=%v", raw)
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
}

func TestProject_SchemaIndex_Agent_OmitEmpty(t *testing.T) {
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
			t.Errorf("%q should be omitted", f)
		}
	}
}
