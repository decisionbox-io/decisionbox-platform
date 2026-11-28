package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// TestAskSessionMessage_BSONRoundTrip locks in the on-disk field shape
// that MongoDB sees. The aggregate `tokens_used` was replaced with
// split `input_tokens` / `output_tokens`. A future rename that drops
// these tags would silently lose data on legacy collections — this
// test fails first.
func TestAskSessionMessage_BSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	msg := AskSessionMessage{
		Question:     "What drives churn?",
		Answer:       "Levels 18-24",
		Model:        "claude-sonnet",
		InputTokens:  1200,
		OutputTokens: 380,
		CreatedAt:    now,
	}

	bs, err := bson.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Decode into a raw map to assert the exact BSON keys, then re-decode
	// into the typed struct to check the round-trip.
	var raw bson.M
	if err := bson.Unmarshal(bs, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["tokens_used"]; ok {
		t.Errorf("legacy bson key tokens_used must not appear; it was replaced by input_tokens/output_tokens")
	}
	if got, ok := raw["input_tokens"].(int32); !ok || int(got) != 1200 {
		t.Errorf("input_tokens bson key missing or wrong; raw=%v", raw["input_tokens"])
	}
	if got, ok := raw["output_tokens"].(int32); !ok || int(got) != 380 {
		t.Errorf("output_tokens bson key missing or wrong; raw=%v", raw["output_tokens"])
	}

	var back AskSessionMessage
	if err := bson.Unmarshal(bs, &back); err != nil {
		t.Fatalf("Unmarshal typed: %v", err)
	}
	if back.InputTokens != 1200 || back.OutputTokens != 380 {
		t.Errorf("round-trip lost tokens: got (%d, %d), want (1200, 380)", back.InputTokens, back.OutputTokens)
	}
	if back.Question != msg.Question || back.Answer != msg.Answer || back.Model != msg.Model {
		t.Errorf("round-trip lost text fields")
	}
}

// TestAskSessionMessage_JSONOmitemptyOnZero verifies the wire shape: when
// neither token field is set (e.g. a legacy row decoded from Mongo with no
// value), the JSON response omits the keys rather than emitting 0. This
// preserves the "unknown vs. zero spent" distinction the dashboard relies
// on to render "—" for legacy rows.
func TestAskSessionMessage_JSONOmitemptyOnZero(t *testing.T) {
	msg := AskSessionMessage{
		Question: "Q?",
		Answer:   "A.",
		Model:    "m",
	}
	bs, err := json.Marshal(msg)
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

// TestAskSessionMessage_JSONIncludesNonZero verifies the inverse — when
// either token field is non-zero the wire shape carries it.
func TestAskSessionMessage_JSONIncludesNonZero(t *testing.T) {
	msg := AskSessionMessage{
		Question:     "Q",
		Answer:       "A",
		Model:        "m",
		InputTokens:  500,
		OutputTokens: 120,
	}
	bs, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(bs)
	if !strings.Contains(got, `"input_tokens":500`) {
		t.Errorf("input_tokens=500 missing; got %s", got)
	}
	if !strings.Contains(got, `"output_tokens":120`) {
		t.Errorf("output_tokens=120 missing; got %s", got)
	}
}

// --- conversation surface ---

// A session written before the surface existed has no field, and every one of
// those is an Ask conversation. Reading the field directly instead of through
// EffectiveSurface would hide the entire existing corpus from Ask's history.
func TestAskSession_EffectiveSurface(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *AskSession
		want string
	}{
		{"absent", &AskSession{}, AskSurfaceAsk},
		{"explicit ask", &AskSession{Surface: AskSurfaceAsk}, AskSurfaceAsk},
		{"playbook", &AskSession{Surface: AskSurfacePlaybook}, AskSurfacePlaybook},
		// An unrecognised stored value is reported as it is, not silently
		// rebadged: a caller that filters on a known set then excludes it,
		// which is the safe direction for a value we cannot explain.
		{"unknown stored value", &AskSession{Surface: "whatever"}, "whatever"},
		{"nil receiver", nil, AskSurfaceAsk},
	} {
		if got := tc.s.EffectiveSurface(); got != tc.want {
			t.Errorf("%s: EffectiveSurface() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestKnownAskSurface(t *testing.T) {
	for _, s := range []string{"", AskSurfaceAsk, AskSurfacePlaybook} {
		if !KnownAskSurface(s) {
			t.Errorf("KnownAskSurface(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"Ask", "PLAYBOOK", "playbooks", "ask ", "insights"} {
		if KnownAskSurface(s) {
			t.Errorf("KnownAskSurface(%q) = true, want false", s)
		}
	}
}

// An absent surface must stay absent on disk: every existing row has no such
// field, and writing an explicit "ask" into new rows would mean the Ask filter
// has to keep matching both forever for no gain.
func TestAskSession_SurfaceOmittedWhenEmpty(t *testing.T) {
	bs, err := bson.Marshal(AskSession{ID: "s1", ProjectID: "p1"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw bson.M
	if err := bson.Unmarshal(bs, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["surface"]; ok {
		t.Errorf("surface present in BSON for an unset surface: %v", raw)
	}

	bs, err = bson.Marshal(AskSession{ID: "s1", ProjectID: "p1", Surface: AskSurfacePlaybook})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	raw = bson.M{}
	if err := bson.Unmarshal(bs, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if raw["surface"] != AskSurfacePlaybook {
		t.Errorf("surface = %v, want %q", raw["surface"], AskSurfacePlaybook)
	}
}
