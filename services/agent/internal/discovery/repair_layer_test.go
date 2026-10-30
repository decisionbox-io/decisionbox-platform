package discovery

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// What this file pins: the repair call goes to the analysis layer's model, not to the
// default one.
//
// It is here because the merge that brought per-layer models onto this branch did not
// convert this call site, and could not have: repair.go is newer than that work, so there
// was nothing to convert when it was written. The result compiled, every test passed, and
// the repair pass quietly used whatever model the default layer named -- while being handed
// an output budget computed for the analysis model, which is the part that bites. A smaller
// default model rejects the call, and the pass falls back to deleting the sentence.
//
// Nothing about that failure is visible from a build or from the rest of this suite, which
// is the whole reason it is asserted rather than left to a reader of the call site.
func TestTheRepairCallGoesToTheAnalysisLayersModel(t *testing.T) {
	queued := func(body string) *testutil.MockLLMProvider {
		p := testutil.NewMockLLMProvider()
		p.ResponseQueue = append(p.ResponseQueue, &gollm.ChatResponse{
			Content: body,
			Usage:   gollm.Usage{InputTokens: 7, OutputTokens: 11},
		})
		return p
	}

	// A repaired insight, so the pass runs to the end rather than stopping on a parse
	// error before the assertion means anything.
	const repaired = `{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"Two of the ten largest sub-categories by sales run a loss: Tables and Bookcases. Chairs leads the category on volume.",
		"severity":"high","source_steps":[4],
		"quantifier_claims":[{"claim":"Two of the ten largest sub-categories by sales run a loss","kind":"cardinality","step":4,"filter":"profit < 0","top_n":10,"top_n_column":"sales","count":2}]
	}]}`

	analysisProvider := queued(repaired)
	// The default layer answers with the same body, so a test that routes to the wrong
	// client fails on where the call went rather than on what came back. A default that
	// returned nothing would also fail, but for the wrong reason, and would still pass if
	// the queue happened to be shared.
	defaultProvider := queued(repaired)

	analysisClient, err := ai.New(analysisProvider, "analysis-model")
	if err != nil {
		t.Fatalf("analysis client: %v", err)
	}
	defaultClient, err := ai.New(defaultProvider, "default-model")
	if err != nil {
		t.Fatalf("default client: %v", err)
	}

	deflt := &ai.LayerLLM{
		Layer: llmlayer.DefaultLayer, ProviderID: llmlayer.DefaultProviderID,
		Provider: "bedrock", Model: "default-model",
		Window: 200000, OutputCap: 32000, Client: defaultClient,
	}
	analysis := &ai.LayerLLM{
		Layer: llmlayer.LayerAnalysis, ProviderID: "analysis",
		Provider: "bedrock", Model: "analysis-model",
		Window: 200000, OutputCap: 8192, Client: analysisClient,
	}
	o := &Orchestrator{
		aiClient: defaultClient,
		layers: &fakeLayerSet{
			byLayer:  map[string]*ai.LayerLLM{llmlayer.DefaultLayer: deflt, llmlayer.LayerAnalysis: analysis},
			fallback: deflt,
		},
	}

	repairOne(t, o, refutedInsight())

	if len(analysisProvider.Calls) != 1 {
		t.Errorf("the analysis layer's model took %d repair call(s), want 1", len(analysisProvider.Calls))
	}
	if len(defaultProvider.Calls) != 0 {
		t.Errorf("the default layer's model took %d repair call(s), want 0 -- the repair is budgeted "+
			"for the analysis model, so sending it elsewhere spends one model's budget on another",
			len(defaultProvider.Calls))
	}
}

// The single-model path has to keep working: with no layer set at all, o.ai falls back to
// the one client, and a repair must still happen. Without this, routing the call through a
// layer would be free to break every deployment that names one model for everything.
func TestTheRepairCallStillWorksWithNoLayerSet(t *testing.T) {
	o, provider := newRepairOrchestrator(`{"insights":[{
		"name":"Furniture drags the top ten",
		"description":"Two of the ten largest sub-categories by sales run a loss: Tables and Bookcases. Chairs leads the category on volume.",
		"severity":"high","source_steps":[4],
		"quantifier_claims":[{"claim":"Two of the ten largest sub-categories by sales run a loss","kind":"cardinality","step":4,"filter":"profit < 0","top_n":10,"top_n_column":"sales","count":2}]
	}]}`)
	if o.layers != nil {
		t.Fatal("precondition: this orchestrator must have no layer set")
	}

	repairOne(t, o, refutedInsight())

	if len(provider.Calls) != 1 {
		t.Errorf("LLM calls = %d, want 1 on the single-client path", len(provider.Calls))
	}
}
