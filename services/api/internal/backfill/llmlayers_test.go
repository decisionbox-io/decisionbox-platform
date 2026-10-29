package backfill

import (
	"testing"

	llmlayer "github.com/decisionbox-io/decisionbox/libs/go-common/llmlayer"
)

// The selection predicate is the whole safety story of this backfill: anything
// it says yes to gets its AI configuration rewritten. The cases that matter
// are the half-stamped ones, because those are the states a crash or an older
// generation leaves behind and the ones a naive "is the version set?" check
// gets wrong.
func TestNeedsLLMLayerStampCoversTheHalfStampedStates(t *testing.T) {
	rows := map[string]llmlayer.LayerModel{
		llmlayer.DefaultLayer: {ProviderID: llmlayer.DefaultProviderID, Model: "decisionbox-analysis-model"},
	}

	for _, tc := range []struct {
		name  string
		state projectLayerState
		want  bool
		why   string
	}{
		{
			name:  "never stamped",
			state: projectLayerState{},
			want:  true,
			why:   "the project this backfill exists for: no rows, no version",
		},
		{
			name:  "fully stamped at the current generation",
			state: projectLayerState{LayerModels: rows, LLMConfigVersion: llmlayer.ConfigVersion},
			want:  false,
			why:   "already done — re-running must be a no-op, or an operator cannot re-run safely",
		},
		{
			name:  "rows but no version",
			state: projectLayerState{LayerModels: rows},
			want:  true,
			why:   "the gate reads the version, so rows alone still present as unconfigured",
		},
		{
			name:  "version but no rows",
			state: projectLayerState{LLMConfigVersion: llmlayer.ConfigVersion},
			want:  true,
			why:   "a version with nothing to resolve: reads as configured and routes nowhere",
		},
		{
			name:  "stamped by an older generation",
			state: projectLayerState{LayerModels: rows, LLMConfigVersion: llmlayer.ConfigVersion - 1},
			want:  true,
			why:   "a generation bump has to re-stamp, which '== 0' would miss",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsLLMLayerStamp(tc.state); got != tc.want {
				t.Errorf("needsLLMLayerStamp() = %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}
