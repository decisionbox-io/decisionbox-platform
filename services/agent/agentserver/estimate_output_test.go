package agentserver

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The API reads the estimate from the agent's stdout by taking the last line
// that is a complete JSON object (stderr log lines are interleaved on
// Kubernetes). An indented object spans many lines, none of which parses on
// its own, so every estimate failed with "no JSON found in agent output".
func TestWriteEstimate_OneLine(t *testing.T) {
	var buf bytes.Buffer
	est := &models.CostEstimate{TotalUSD: 1.5, LLM: models.LLMCostEstimate{Provider: "bedrock", CostUSD: 1.5}}
	if err := writeEstimate(&buf, est); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("estimate must be exactly one line, got %q", out)
	}
	var back models.CostEstimate
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &back); err != nil || back.TotalUSD != 1.5 {
		t.Fatalf("line is not the estimate: %v (%q)", err, out)
	}
}
