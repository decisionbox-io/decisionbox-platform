package models

import (
	"bytes"
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

// The agent's SchemaIndexRun must marshal to the same bson shape the API reads.
func TestSchemaIndexRun_RoundTrip_Agent(t *testing.T) {
	orig := SchemaIndexRun{
		ProjectID: "p1", DatasourceID: "default", RunID: "r1", Kind: SchemaIndexRunKindTables,
		ObjectsIndexed: 7, BlurbsGenerated: 7, Status: SchemaIndexStatusFailed, Error: "boom",
		PhaseDurations: map[string]int64{"describing_tables": 1200},
		StartedAt:      time.Now().UTC().Truncate(time.Millisecond),
		FinishedAt:     time.Now().UTC().Truncate(time.Millisecond),
	}
	b, err := bson.Marshal(orig)
	if err != nil {
		t.Fatalf("bson marshal: %v", err)
	}
	var got SchemaIndexRun
	if err := bson.Unmarshal(b, &got); err != nil {
		t.Fatalf("bson unmarshal: %v", err)
	}
	if got.Status != SchemaIndexStatusFailed || got.Error != "boom" {
		t.Errorf("failure fields lost: %+v", got)
	}
	if got.ObjectsIndexed != 7 || got.PhaseDurations["describing_tables"] != 1200 {
		t.Errorf("fields lost: %+v", got)
	}

	// JSON is the wire form the dashboard consumes.
	j, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("json marshal: %v", err)
	}
	if !bytes.Contains(j, []byte(`"objects_indexed":7`)) {
		t.Errorf("json missing objects_indexed: %s", j)
	}
}
