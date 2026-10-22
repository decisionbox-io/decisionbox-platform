package discovery

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

func validationCfgForTest() verifier.Config {
	cfg := verifier.DefaultConfig()
	cfg.Verifier = verifier.ModeConfig{MaxRounds: 8, TokenCap: 30000, MaxOutputTokens: 4000}
	cfg.Refuter = verifier.ModeConfig{MaxRounds: 6, TokenCap: 20000, MaxOutputTokens: 3000}
	cfg.RefuterEnabled = true
	return cfg
}

// Every exploration step re-sends the system prompt and the whole history,
// so input grows with the square of the step count. Counting the system
// prompt once (the previous estimator) under-stated a 60-step run's input
// several times over.
func TestEstimateRunTokens_ExplorationResendsTheConversation(t *testing.T) {
	in := runTokenInputs{MaxSteps: 10, SystemPromptTokens: 1000, Validation: validationCfgForTest()}
	e := estimateRunTokens(in)
	growth := avgExplorationUserMessagePerStep + avgExplorationOutputPerStep
	if want := 10*1000 + growth*10*9/2; e.ExplorationInput != want {
		t.Errorf("exploration input = %d, want %d", e.ExplorationInput, want)
	}
	if e.ExplorationCachedInput != 0 {
		t.Errorf("no caching requested, cached = %d", e.ExplorationCachedInput)
	}
	in.MaxSteps = 20
	if twice := estimateRunTokens(in).ExplorationInput; twice <= 2*e.ExplorationInput {
		t.Errorf("doubling steps must more than double input: %d vs %d", twice, e.ExplorationInput)
	}
}

func TestEstimateRunTokens_PromptCacheShare(t *testing.T) {
	e := estimateRunTokens(runTokenInputs{MaxSteps: 10, SystemPromptTokens: 1000, PromptCache: true, Validation: validationCfgForTest()})
	growth := avgExplorationUserMessagePerStep + avgExplorationOutputPerStep
	if want := 9*1000 + growth*9*8/2; e.ExplorationCachedInput != want {
		t.Errorf("cached input = %d, want %d", e.ExplorationCachedInput, want)
	}
	if e.ExplorationCachedInput >= e.ExplorationInput {
		t.Error("cached input must be a part of total input")
	}
}

// Validation is sized from the verifier/refuter token caps (tens of
// thousands of tokens per document), not a few hundred.
func TestEstimateRunTokens_ValidationFromTokenCaps(t *testing.T) {
	e := estimateRunTokens(runTokenInputs{MaxSteps: 1, NumAreas: 3, Validation: validationCfgForTest()})
	docs := 3 * estimatedInsightsPerArea
	if e.ValidationInput != docs*(26000+17000) || e.ValidationOutput != docs*(4000+3000) {
		t.Errorf("validation = %d in / %d out", e.ValidationInput, e.ValidationOutput)
	}
	noRefuter := validationCfgForTest()
	noRefuter.RefuterEnabled = false
	e = estimateRunTokens(runTokenInputs{MaxSteps: 1, NumAreas: 3, Validation: noRefuter})
	if e.ValidationInput != docs*26000 || e.ValidationOutput != docs*4000 {
		t.Errorf("verifier only = %d in / %d out", e.ValidationInput, e.ValidationOutput)
	}
}

func TestRunTokenEstimate_CostPricesCacheReads(t *testing.T) {
	pricing := gollm.TokenPricing{InputPerMillion: 5, OutputPerMillion: 25, CacheReadPerMillion: 0.5, CacheWritePerMillion: 6.25}
	base := runTokenInputs{MaxSteps: 60, NumAreas: 9, SystemPromptTokens: 8000, Validation: validationCfgForTest()}
	plain := estimateRunTokens(base)
	base.PromptCache = true
	cached := estimateRunTokens(base)
	if plain.cost(pricing) <= cached.cost(pricing) {
		t.Errorf("caching must lower the estimate: plain $%.2f, cached $%.2f", plain.cost(pricing), cached.cost(pricing))
	}
	// Without caching every input token is billed at the base rate.
	e := runTokenEstimate{ExplorationInput: 1_000_000, AnalysisInput: 1_000_000, ExplorationOutput: 1_000_000}
	if got := e.cost(pricing); got != 5+5+25 {
		t.Errorf("uncached cost = %v, want 35", got)
	}
	e.ExplorationCachedInput = 600_000
	if got, want := e.cost(pricing), 5+0.6*0.5+0.4*6.25+25; got-want > 1e-9 || want-got > 1e-9 {
		t.Errorf("cached cost = %v, want %v", got, want)
	}
}
