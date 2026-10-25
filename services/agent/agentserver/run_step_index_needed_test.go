package agentserver

import "testing"

// The per-run step index is keyed by the run id and ranked by the embedder, so
// a run that is missing either must fail rather than silently fall back to
// keyword-only step selection.
func TestRunStepIndexNeeded(t *testing.T) {
	cases := []struct {
		name        string
		runID       string
		hasEmbedder bool
		wantNeeded  bool
		wantErr     bool
	}{
		{"run", "r1", true, true, false},
		{"run without run id", "", true, true, true},
		{"run without embedder", "r1", false, true, true},
	}
	for _, c := range cases {
		needed, err := runStepIndexNeeded(c.runID, c.hasEmbedder)
		if needed != c.wantNeeded || (err != nil) != c.wantErr {
			t.Errorf("%s: got (%v, %v), want needed=%v err=%v", c.name, needed, err, c.wantNeeded, c.wantErr)
		}
	}
}
