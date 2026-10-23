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

func TestRunTokenEstimate_CachingLowersExplorationCost(t *testing.T) {
	pricing := gollm.TokenPricing{InputPerMillion: 5, OutputPerMillion: 25, CacheReadPerMillion: 0.5, CacheWritePerMillion: 6.25}
	base := runTokenInputs{MaxSteps: 60, NumAreas: 9, SystemPromptTokens: 8000, Validation: validationCfgForTest()}
	plain := estimateRunTokens(base).costs(pricing)
	base.PromptCache = true
	cached := estimateRunTokens(base).costs(pricing)
	if cached.Exploration >= plain.Exploration {
		t.Errorf("caching must lower exploration: plain $%.2f, cached $%.2f", plain.Exploration, cached.Exploration)
	}
	// Caching is exploration-only: the other phases are priced the same.
	if cached.Analysis != plain.Analysis || cached.Validation != plain.Validation || cached.Recommendations != plain.Recommendations {
		t.Errorf("caching moved non-exploration phases: plain %+v cached %+v", plain, cached)
	}
}

// Each phase is priced on its own, request by request; the breakdown must
// not spread exploration's cache discount over other phases.
func TestRunTokenEstimate_PhaseCostsArePricedPerPhase(t *testing.T) {
	pricing := gollm.TokenPricing{InputPerMillion: 1, OutputPerMillion: 10, CacheReadPerMillion: 0.1, CacheWritePerMillion: 2}
	e := estimateRunTokens(runTokenInputs{MaxSteps: 3, NumAreas: 1, SystemPromptTokens: 1000, PromptCache: true,
		BaseContextTokens: 100, AvgAreaPromptTokens: 100, Validation: validationCfgForTest()})
	c := e.costs(pricing)
	g := avgExplorationUserMessagePerStep + avgExplorationOutputPerStep
	out := float64(avgExplorationOutputPerStep) * 10
	// Step 1 writes its whole prompt; steps 2 and 3 read the previous prompt
	// and write the new tail.
	wantExplore := (1000*2.0 + out +
		float64(1000)*0.1 + float64(g)*2 + out +
		float64(1000+g)*0.1 + float64(g)*2 + out) / 1e6
	if diff := c.Exploration - wantExplore; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("exploration = %v, want %v", c.Exploration, wantExplore)
	}
	wantValidation := (float64(e.ValidationInput)*1 + float64(e.ValidationOutput)*10) / 1e6
	if diff := c.Validation - wantValidation; diff > 1e-12 || diff < -1e-12 {
		t.Errorf("validation = %v, want %v (base rate, no cache discount)", c.Validation, wantValidation)
	}
	if got := c.total(); got != c.Exploration+c.Analysis+c.Validation+c.Recommendations {
		t.Errorf("total %v is not the sum of the phases", got)
	}
}

// A long-prompt tier multiplies every rate of a request whose prompt is
// over the threshold, per request — as Claude Haiku 5.5 bills prompts over
// 100K tokens at 5x. A long exploration crosses it through history growth.
func TestRunTokenEstimate_LongContextTierPerRequest(t *testing.T) {
	flat := gollm.TokenPricing{InputPerMillion: 1, OutputPerMillion: 10}
	tiered := flat
	tiered.LongContextThreshold = 1000 + avgExplorationUserMessagePerStep + avgExplorationOutputPerStep // step 2's prompt
	tiered.LongContextMultiplier = 5
	e := estimateRunTokens(runTokenInputs{MaxSteps: 3, NumAreas: 1, SystemPromptTokens: 1000, Validation: validationCfgForTest()})
	f, tc := e.costs(flat), e.costs(tiered)
	g := avgExplorationUserMessagePerStep + avgExplorationOutputPerStep
	out := float64(avgExplorationOutputPerStep) * 10
	step3 := (float64(1000+2*g)*1 + out) / 1e6 // only step 3 is over the threshold
	if diff := tc.Exploration - (f.Exploration + 4*step3); diff > 1e-12 || diff < -1e-12 {
		t.Errorf("tiered exploration = %v, want %v", tc.Exploration, f.Exploration+4*step3)
	}
	// Validation requests are bounded by the mode caps (30K / 20K), below
	// a 100K threshold, so they stay on the base tier.
	tiered.LongContextThreshold = 100000
	if got := e.costs(tiered).Validation; got != f.Validation {
		t.Errorf("validation under the threshold must not be tiered: %v vs %v", got, f.Validation)
	}
}
