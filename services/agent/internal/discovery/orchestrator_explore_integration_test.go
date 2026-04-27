//go:build integration

package discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discovery/blurb"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestInteg_OnDemandSchema_EndToEnd is the closest thing we have to a
// production reproduction: real Qdrant container, real schema indexer,
// real CacheSchemaProvider, real ExplorationEngine, a scripted LLM that
// drives the agent through every on-demand schema action, and a query
// executor that just refuses (so the loop terminates on max_steps once
// the schema actions are exhausted). The test asserts on the messages
// the engine emits back to the LLM — that is the contract domain-pack
// prompts depend on, and is the single place where a regression in the
// new architecture would manifest.
func TestInteg_OnDemandSchema_EndToEnd(t *testing.T) {
	// Surface the engine's structured logs in the test output so a
	// regression in action wiring or budget accounting shows up in CI.
	logger.Init("integ-test", "info")

	ctx := context.Background()
	retriever := startQdrant(t)

	const projectID = "integ-e2e-1"

	schemas := map[string]models.TableSchema{
		"sales.orders": {
			TableName: "sales.orders",
			RowCount:  1_000_000,
			Columns: []models.ColumnInfo{
				{Name: "order_id", Type: "INT64", Category: "primary_key"},
				{Name: "customer_id", Type: "INT64"},
				{Name: "total", Type: "DECIMAL", Category: "metric"},
			},
			SampleData: []map[string]interface{}{
				{"order_id": 1, "customer_id": 7, "total": 49.95},
			},
		},
		"sales.users": {
			TableName: "sales.users",
			RowCount:  50_000,
			Columns: []models.ColumnInfo{
				{Name: "user_id", Type: "INT64", Category: "primary_key"},
				{Name: "email", Type: "STRING", Nullable: true},
			},
		},
	}

	// Index for search_tables.
	emb := &stubEmbedder{dim: 3, model: "stub-embedder"}
	llmStub := &stubLLM{text: "Sales fact / dimension table for retention and AOV analysis."}
	gen, err := blurb.New(blurb.Config{LLM: llmStub, Model: "stub-blurb", Workers: 2})
	if err != nil {
		t.Fatalf("blurb.New: %v", err)
	}
	si := SchemaIndexer{
		Discovery: &stubIntegSchemaSource{schemas: schemas},
		Blurber:   gen,
		Embedder:  emb,
		Retriever: retriever,
		Progress:  &memProgress{},
	}
	if _, err := si.BuildIndex(ctx, IndexOptions{
		ProjectID:       projectID,
		RunID:           "idx-1",
		BlurbModelLabel: "stub/blurb",
		DomainBlurb:     "E-commerce sales warehouse.",
		Keywords:        []string{"sales"},
	}); err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	// Build the production CacheSchemaProvider.
	provider, err := NewCacheSchemaProvider(CacheSchemaProviderOptions{
		ProjectID: projectID,
		Schemas:   schemas,
		Retriever: retriever,
		Embedder:  emb,
	})
	if err != nil {
		t.Fatalf("NewCacheSchemaProvider: %v", err)
	}

	// Scripted LLM. Each turn returns the next canned response so we
	// drive the engine deterministically through every action.
	scripted := &scriptedLLM{
		responses: []string{
			// Turn 1: lookup an existing table — should return L1 detail.
			`{"thinking": "inspect sales.orders columns", "lookup_schema": ["sales.orders"]}`,
			// Turn 2: lookup the SAME table again — must short-circuit
			// via dedup without burning a second slot of the budget.
			`{"thinking": "lookup again to test dedup", "lookup_schema": ["sales.orders"]}`,
			// Turn 3: search_tables — must hit Qdrant and return a hit.
			`{"thinking": "search for users-shaped tables", "search_tables": "user identity table", "search_top_k": 5}`,
			// Turn 4: lookup a missing table — should land in NotFound.
			`{"thinking": "ask for a missing one", "lookup_schema": ["sales.does_not_exist"]}`,
			// Turn 5: signal done.
			`{"done": true, "summary": "explored on-demand actions"}`,
		},
	}

	client, err := ai.New(scripted, "test-model")
	if err != nil {
		t.Fatalf("ai.New: %v", err)
	}

	// Capture the messages the engine appends — these are what the LLM
	// sees on subsequent turns and what domain-pack prompts target.
	scripted.captureFollowUps = true

	engine := ai.NewExplorationEngine(ai.ExplorationEngineOptions{
		Client:            client,
		Executor:          nil, // no query path in this test
		MaxSteps:          10,
		Dataset:           "sales",
		SchemaProvider:    provider,
		MaxLookupsPerRun:  ai.DefaultMaxLookupsPerRun,
		MaxSearchesPerRun: ai.DefaultMaxSearchesPerRun,
	})

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	res, err := engine.Explore(runCtx, ai.ExplorationContext{
		ProjectID:     projectID,
		Dataset:       "sales",
		InitialPrompt: "You are an exploration agent. Respond with one JSON action per turn.",
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}

	// Pull the user-message stream the engine sent BACK to the LLM
	// after each action. responses[0] is replied to with messages[1],
	// responses[1] is replied to with messages[2], etc.
	msgs := scripted.followUps
	if len(msgs) < 4 {
		t.Fatalf("expected ≥4 follow-up messages, got %d:\n%v", len(msgs), msgs)
	}

	// --- Assertion 1: lookup_schema emitted L1 detail for the asked table.
	lookup1 := msgs[0]
	if !strings.Contains(lookup1, "sales.orders") {
		t.Errorf("first lookup follow-up missing table name:\n%s", lookup1)
	}
	if !strings.Contains(lookup1, "order_id") || !strings.Contains(lookup1, "customer_id") {
		t.Errorf("first lookup follow-up missing column names:\n%s", lookup1)
	}
	if !strings.Contains(lookup1, "1/30") {
		t.Errorf("first lookup follow-up should report budget 1/30, got:\n%s", lookup1)
	}

	// --- Assertion 2: dedup short-circuit.
	lookup2 := msgs[1]
	if !strings.Contains(strings.ToLower(lookup2), "already") {
		t.Errorf("second lookup follow-up should mention dedup ('already'), got:\n%s", lookup2)
	}

	// --- Assertion 3: search_tables hit Qdrant and returned hits.
	search := msgs[2]
	if !strings.Contains(search, "user identity table") {
		t.Errorf("search follow-up should echo query, got:\n%s", search)
	}
	// At least one of the indexed tables must appear in the hits.
	if !strings.Contains(search, "sales.orders") && !strings.Contains(search, "sales.users") {
		t.Errorf("search follow-up should list at least one indexed table, got:\n%s", search)
	}
	if !strings.Contains(search, "1/30") {
		t.Errorf("search follow-up should report budget 1/30, got:\n%s", search)
	}

	// --- Assertion 4: missing table lands in NotFound.
	notFound := msgs[3]
	if !strings.Contains(strings.ToLower(notFound), "not found") &&
		!strings.Contains(strings.ToLower(notFound), "does_not_exist") {
		t.Errorf("missing-ref follow-up should mention NotFound or the ref, got:\n%s", notFound)
	}

	// --- Assertion 5: completion recorded.
	if !res.Completed {
		t.Errorf("Explore should mark Completed=true once done is accepted, got %+v", res)
	}
	if res.TotalSteps < 5 {
		t.Errorf("expected ≥5 steps, got %d", res.TotalSteps)
	}

	// --- Assertion 6: per-step Action types are recorded correctly.
	wantActions := []string{"lookup_schema", "lookup_schema", "search_tables", "lookup_schema", "complete"}
	if len(res.Steps) != len(wantActions) {
		t.Fatalf("steps len = %d, want %d:\n%+v", len(res.Steps), len(wantActions), res.Steps)
	}
	for i, want := range wantActions {
		if res.Steps[i].Action != want {
			t.Errorf("steps[%d].Action = %q, want %q", i, res.Steps[i].Action, want)
		}
	}
}

// scriptedLLM implements gollm.Provider with a fixed response queue and
// optional capture of follow-up user messages. Pure in-memory; no
// network. Tracks every CreateMessage call so the test can pin both the
// system prompt (turn 0) and every follow-up the engine appends.
type scriptedLLM struct {
	responses        []string
	captureFollowUps bool
	followUps        []string

	turn int
}

func (s *scriptedLLM) Validate(ctx context.Context) error { return nil }

func (s *scriptedLLM) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
	if s.captureFollowUps && len(req.Messages) > 0 {
		// The last message in req.Messages on turn N is the user's
		// follow-up (the engine's reply to turn N-1's assistant
		// response). On turn 0 it's the kick-off message, which we
		// skip. After turn 0 each call accumulates one new user msg.
		if s.turn > 0 {
			last := req.Messages[len(req.Messages)-1]
			if last.Role == "user" {
				s.followUps = append(s.followUps, last.Content)
			}
		}
	}
	if s.turn >= len(s.responses) {
		// Out of script — return empty so the engine logs a parse error
		// and stops. Should not be reached if the script is sized right.
		return &gollm.ChatResponse{Content: ""}, nil
	}
	resp := &gollm.ChatResponse{
		Content: s.responses[s.turn],
		Usage:   gollm.Usage{InputTokens: 1, OutputTokens: 1},
	}
	s.turn++
	return resp, nil
}
