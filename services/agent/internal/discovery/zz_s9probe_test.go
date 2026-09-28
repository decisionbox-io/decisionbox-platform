package discovery

// Probe: what does attempt 0 actually emit for the two areas that ship nothing?
// analyzeAreaInsights keeps only the last attempt's response, so the shape that
// failed is never stored. This calls the model once per area with the same arm-A
// prompt and writes the raw bytes out.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
)

func TestS9ProbeRawResponse(t *testing.T) {
	want := os.Getenv("S9_PROBE_AREAS")
	if want == "" {
		t.Skip("set S9_PROBE_AREAS")
	}
	corpus := os.Getenv("S9_CORPUS")
	if corpus == "" {
		corpus = "s8"
	}
	raw, err := os.ReadFile("/home/abacigil/tpch-lab/" + corpus + "/evidence/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	var areas []frozenArea
	if err := json.Unmarshal(raw, &areas); err != nil {
		t.Fatal(err)
	}
	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model": "us.anthropic.claude-opus-4-8", "region": "us-east-1", "auth_method": "iam_role",
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := ai.New(provider, "us.anthropic.claude-opus-4-8")
	if err != nil {
		t.Fatal(err)
	}
	dir := "/home/abacigil/tpch-lab/s9/probe"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, a := range areas {
		if !strings.Contains(want, a.AreaID) {
			continue
		}
		prompt := armPrompt(t, a.Prompt, "A")
		res, err := client.Chat(context.Background(), prompt, "", 64000)
		if err != nil {
			t.Fatalf("%s: %v", a.AreaID, err)
		}
		name := fmt.Sprintf("%s/%s_%s_attempt0.txt", dir, corpus, a.AreaID)
		if err := os.WriteFile(name, []byte(res.Content), 0o644); err != nil {
			t.Fatal(err)
		}
		// What the production parser makes of it, verbatim.
		cleaned := cleanJSONResponse(res.Content)
		_, _, perr := (&Orchestrator{}).parseInsights(res.Content, a.AreaID)
		t.Logf("%s/%s len=%d cleaned_len=%d parse_err=%v", corpus, a.AreaID, len(res.Content), len(cleaned), perr)
		t.Logf("  head: %q", head(res.Content, 120))
		t.Logf("  tail: %q", tail(res.Content, 200))
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
