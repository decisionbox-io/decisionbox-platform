package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// TestSearchHistory_BSONRoundTrip locks in the on-disk shape. The pre-1.0
// `tokens_used` aggregate was replaced with `input_tokens`/`output_tokens`
// — this test fails first on a regression.
func TestSearchHistory_BSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	h := SearchHistory{
		ID:           "sh-1",
		UserID:       "u-1",
		ProjectID:    "p-1",
		Query:        "what drives churn?",
		Type:         "ask",
		ResultsCount: 7,
		LLMModel:     "claude-sonnet",
		InputTokens:  900,
		OutputTokens: 220,
		CreatedAt:    now,
	}
	bs, err := bson.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw bson.M
	if err := bson.Unmarshal(bs, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["tokens_used"]; ok {
		t.Errorf("legacy bson key tokens_used must not appear; it was replaced by input_tokens/output_tokens")
	}
	if got, ok := raw["input_tokens"].(int32); !ok || int(got) != 900 {
		t.Errorf("input_tokens key wrong; raw=%v", raw["input_tokens"])
	}
	if got, ok := raw["output_tokens"].(int32); !ok || int(got) != 220 {
		t.Errorf("output_tokens key wrong; raw=%v", raw["output_tokens"])
	}

	var back SearchHistory
	if err := bson.Unmarshal(bs, &back); err != nil {
		t.Fatalf("Unmarshal typed: %v", err)
	}
	if back.InputTokens != 900 || back.OutputTokens != 220 {
		t.Errorf("tokens round-trip lost: (%d, %d), want (900, 220)", back.InputTokens, back.OutputTokens)
	}
}

// TestSearchHistory_JSONOmitemptyOnZero — search-type rows (never make an
// LLM call) and legacy rows render the token fields absent, not as 0. This
// is what the dashboard relies on to distinguish "unknown" from "spent 0".
func TestSearchHistory_JSONOmitemptyOnZero(t *testing.T) {
	h := SearchHistory{
		ID:           "sh-2",
		Type:         "search",
		ResultsCount: 3,
	}
	bs, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(bs)
	if strings.Contains(got, `"input_tokens"`) {
		t.Errorf("input_tokens must be omitted when zero; got %s", got)
	}
	if strings.Contains(got, `"output_tokens"`) {
		t.Errorf("output_tokens must be omitted when zero; got %s", got)
	}
	if strings.Contains(got, `"tokens_used"`) {
		t.Errorf("tokens_used must not appear in JSON output; got %s", got)
	}
}
