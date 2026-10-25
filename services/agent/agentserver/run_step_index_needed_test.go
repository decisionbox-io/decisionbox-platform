package agentserver

import "testing"

// A cost estimate runs no analysis, so it must not need the per-run step
// index or the run id it is keyed by: the API's estimate call passes no run
// id, and requiring one failed every estimate on a deployment with a vector
// store configured.
func TestRunStepIndexNeeded(t *testing.T) {
	cases := []struct {
		name         string
		estimateOnly bool
		runID        string
		hasEmbedder  bool
		wantNeeded   bool
		wantErr      bool
	}{
		{"estimate, no run id", true, "", true, false, false},
		{"estimate, no embedder", true, "", false, false, false},
		{"run", false, "r1", true, true, false},
		{"run without run id", false, "", true, true, true},
		{"run without embedder", false, "r1", false, true, true},
	}
	for _, c := range cases {
		needed, err := runStepIndexNeeded(c.estimateOnly, c.runID, c.hasEmbedder)
		if needed != c.wantNeeded || (err != nil) != c.wantErr {
			t.Errorf("%s: got (%v, %v), want needed=%v err=%v", c.name, needed, err, c.wantNeeded, c.wantErr)
		}
	}
}
