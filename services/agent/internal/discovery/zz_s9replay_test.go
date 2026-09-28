package discovery

// Throwaway harness for the s9 three-arm experiment. Replays ONE frozen exploration
// corpus's analysis phase with today's code, swapping a single prompt region per arm,
// and writes the shipped insights out for the mechanical metric to score.
//
// How a prompt is rebuilt. The stored prompt is
//     <evidence + pack prose>  ++  quantifierContract  ++  analysisRules
// because buildAnalysisAreaPrompt appends the contract and then hands the whole
// string to discipline.AppendAnalysisRules. Only the first part is corpus-specific,
// so the harness keeps that byte-for-byte and regenerates the two platform-enforced
// blocks from today's code -- which is exactly what "arm A is today's prompt" means.
//
// An earlier version of this file spliced `prefix + quantifierContract` and dropped
// the 3.3KB discipline block from every arm. It would still have compared A against
// B fairly, but the baseline would not have been today's pipeline and the prose would
// have been shaped by a prompt production never sends.
//
// Every prompt is hashed into prompts.json beside the output, so "prefix identical,
// one region swapped" is a checkable claim rather than an assertion in a comment.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	_ "github.com/decisionbox-io/decisionbox/providers/llm/bedrock"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/discipline"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

const (
	contractMarker   = "## Declaring quantifier claims"
	disciplineMarker = "## Insight-writing discipline"
)

type frozenArea struct {
	AreaID string `json:"area_id"`
	Prompt string `json:"prompt"`
}

// corpusPrefix strips the two platform-enforced tail blocks from a stored prompt,
// leaving the corpus-specific part. Fails loudly rather than guessing: both markers
// must appear exactly once and in order.
func corpusPrefix(t *testing.T, stored string) string {
	t.Helper()
	if n := strings.Count(stored, contractMarker); n != 1 {
		t.Fatalf("contract marker appears %d times, want 1", n)
	}
	if n := strings.Count(stored, disciplineMarker); n != 1 {
		t.Fatalf("discipline marker appears %d times, want 1", n)
	}
	i := strings.Index(stored, contractMarker)
	if j := strings.Index(stored, disciplineMarker); j < i {
		t.Fatalf("discipline block precedes the contract; prompt layout is not what this harness assumes")
	}
	return stored[:i]
}

// armPrompt rebuilds one area's prompt for the named arm.
func armPrompt(t *testing.T, stored, arm string) string {
	t.Helper()
	prefix := corpusPrefix(t, stored)
	if arm != "A" {
		// Arm B/C (Fix T): the pack's example insight emits the body before the
		// title, so the model writes the description first and the headline last.
		swapped, n := swapNameAndDescription(prefix)
		if n != 1 {
			t.Fatalf("arm %s: swapped %d name/description pairs, want exactly 1", arm, n)
		}
		prefix = swapped
	}
	// Exactly what buildAnalysisAreaPrompt does with its tail today.
	return discipline.AppendAnalysisRules(prefix + "\n\n" + quantifierContract)
}

// swapNameAndDescription moves the example insight's "description" line ahead of its
// "name" line, which is what the model copies. Both lines end in a comma, so the
// example stays valid JSON.
func swapNameAndDescription(prompt string) (string, int) {
	lines := strings.Split(prompt, "\n")
	swaps := 0
	for i := 0; i+1 < len(lines); i++ {
		if strings.Contains(lines[i], `"name":`) && strings.Contains(lines[i+1], `"description":`) {
			lines[i], lines[i+1] = lines[i+1], lines[i]
			swaps++
			i++
		}
	}
	return strings.Join(lines, "\n"), swaps
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// decodeFrozenSteps decodes the frozen steps dump.
//
// The Mongo export wrote every timestamp without a zone offset
// ("2026-09-25T09:22:53.838000"), which time.Time refuses. s7 additionally carries
// one inside fix_history, so the fix has to be nesting-agnostic. Rather than decode
// into `any` and re-marshal -- which would round-trip every number in query_result
// through float64 -- the zone is appended textually, so only string values that are
// exactly a fractional-second timestamp change and every byte of the evidence is
// left alone.
//
// Done here instead of by rewriting the frozen file, which stays exactly as the run
// produced it.
var reZonelessTime = regexp.MustCompile(`"(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+)"`)

func decodeFrozenSteps(raw []byte) ([]models.ExplorationStep, error) {
	fixed := reZonelessTime.ReplaceAll(raw, []byte(`"${1}Z"`))
	var steps []models.ExplorationStep
	if err := json.Unmarshal(fixed, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

func TestS9Replay(t *testing.T) {
	corpus := os.Getenv("S9_CORPUS") // s7 | s8
	arm := os.Getenv("S9_ARM")       // A | B
	rep := os.Getenv("S9_REP")
	if corpus == "" || arm == "" {
		t.Skip("set S9_CORPUS and S9_ARM")
	}
	base := "/home/abacigil/tpch-lab/" + corpus + "/evidence"

	raw, err := os.ReadFile(base + "/analysis.json")
	if err != nil {
		t.Fatal(err)
	}
	var areas []frozenArea
	if err := json.Unmarshal(raw, &areas); err != nil {
		t.Fatal(err)
	}
	rawSteps, err := os.ReadFile(base + "/steps.json")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := decodeFrozenSteps(rawSteps)
	if err != nil {
		t.Fatal(err)
	}
	stepByID := map[int]*models.ExplorationStep{}
	for i := range steps {
		stepByID[steps[i].Step] = &steps[i]
	}

	provider, err := gollm.NewProvider("bedrock", gollm.ProviderConfig{
		"model": "us.anthropic.claude-opus-4-8", "region": "us-east-1", "auth_method": "iam_role",
	})
	if err != nil {
		t.Fatalf("bedrock: %v", err)
	}
	client, err := ai.New(provider, "us.anthropic.claude-opus-4-8")
	if err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{aiClient: client}

	type areaOut struct {
		AreaID       string           `json:"area_id"`
		Insights     []models.Insight `json:"insights"`
		Response     string           `json:"response"`
		TokensIn     int              `json:"tokens_in"`
		TokensOut    int              `json:"tokens_out"`
		DurationMs   int64            `json:"duration_ms"`
		ParseRetries int              `json:"parse_retries"`
		DroppedParse int              `json:"dropped_parse"`
	}
	type promptRec struct {
		AreaID    string `json:"area_id"`
		StoredLen int    `json:"stored_len"`
		PrefixLen int    `json:"prefix_len"`
		PrefixSHA string `json:"prefix_sha8"`
		PromptLen int    `json:"prompt_len"`
		PromptSHA string `json:"prompt_sha8"`
	}
	var out []areaOut
	var recs []promptRec
	for _, a := range areas {
		if strings.TrimSpace(a.Prompt) == "" {
			continue
		}
		prompt := armPrompt(t, a.Prompt, arm)
		pre := corpusPrefix(t, a.Prompt)
		recs = append(recs, promptRec{a.AreaID, len(a.Prompt), len(pre), sha(pre), len(prompt), sha(prompt)})

		outcome := o.analyzeAreaInsights(context.Background(), a.AreaID, prompt, 64000)
		if outcome.chatErr != nil {
			t.Fatalf("area %s: %v", a.AreaID, outcome.chatErr)
		}
		insights := outcome.insights
		attachQuantifierVerdicts(insights, stepByID)
		o.repairRefutedInsights(context.Background(), a.AreaID, insights, stepByID, 64000)
		attachSourceQuality(insights, stepByID)
		out = append(out, areaOut{
			AreaID: a.AreaID, Insights: insights, Response: outcome.response,
			TokensIn: outcome.tokensIn, TokensOut: outcome.tokensOut,
			DurationMs: outcome.durationMs, ParseRetries: outcome.parseRetries,
			DroppedParse: outcome.droppedParse,
		})
		t.Logf("%s/%s rep%s area=%-20s insights=%d in=%d out=%d", corpus, arm, rep, a.AreaID, len(insights), outcome.tokensIn, outcome.tokensOut)
	}

	dir := fmt.Sprintf("/home/abacigil/tpch-lab/s9/runs/%s_%s_%s", corpus, arm, rep)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(dir+"/insights.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
	pb, _ := json.MarshalIndent(recs, "", "  ")
	if err := os.WriteFile(dir+"/prompts.json", pb, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s/insights.json", dir)
}
