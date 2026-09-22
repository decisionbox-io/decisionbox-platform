package askserve

import (
	"testing"
	"time"
)

// finalizeUpdate decides which telemetry survives to the stored record, and
// every field in it is conditional — a zero value is omitted rather than
// written. That makes "the field is absent" and "the field was zero" the same
// state, so a bug that drops a field looks exactly like a turn with nothing to
// report. Nothing covered it before: the persistence path needs Mongo, and the
// loop tests use a fake store, so the mapping from TurnFinal to the stored
// document was untested end to end.

func TestFinalizeUpdate_WritesRoutingTelemetry(t *testing.T) {
	now := time.Now()
	set := finalizeUpdate(TurnFinal{
		Status:              "done",
		Disposition:         "answer",
		Answer:              "42",
		Model:               "test-model",
		InputTokens:         10,
		OutputTokens:        5,
		RoutedDatasourceIDs: []string{"wh_b"},
		RoutingReason:       "about CRM users",
		RoutingConfidence:   0.9,
		RoutingCandidateIDs: []string{"wh_a", "wh_b"},
		RoutingChosenIDs:    []string{"wh_b"},
	}, now)

	assertStrings(t, set, "routed_datasource_ids", []string{"wh_b"})
	assertStrings(t, set, "routing_candidate_ids", []string{"wh_a", "wh_b"})
	assertStrings(t, set, "routing_chosen_ids", []string{"wh_b"})

	if got := set["routing_reason"]; got != "about CRM users" {
		t.Errorf("routing_reason = %v", got)
	}
	if got := set["routing_confidence"]; got != 0.9 {
		t.Errorf("routing_confidence = %v", got)
	}
	if _, ok := set["routing_clarify"]; ok {
		t.Error("routing_clarify must be omitted when the turn did not clarify")
	}
	for _, k := range []string{"status", "disposition", "answer", "model", "updated_at", "completed_at"} {
		if _, ok := set[k]; !ok {
			t.Errorf("%s is always written, but is missing", k)
		}
	}
}

// TestFinalizeUpdate_OmitsEmptyTelemetry pins that a turn with no routing
// decision writes no routing fields at all, rather than empty ones — the
// fields mean "the router ran and saw this".
func TestFinalizeUpdate_OmitsEmptyTelemetry(t *testing.T) {
	set := finalizeUpdate(TurnFinal{Status: "done", Disposition: "answer"}, time.Now())

	for _, k := range []string{
		"routed_datasource_ids", "routing_reason", "routing_confidence",
		"routing_candidate_ids", "routing_chosen_ids", "routing_clarify",
		"error", "input_tokens", "output_tokens",
	} {
		if v, ok := set[k]; ok {
			t.Errorf("%s = %v, want omitted when unset", k, v)
		}
	}
}

// TestFinalizeUpdate_ClarifyRecordsBallotWithoutVerdict pins the shape of the
// case the ballot exists for: the router weighed datasources and picked none.
func TestFinalizeUpdate_ClarifyRecordsBallotWithoutVerdict(t *testing.T) {
	set := finalizeUpdate(TurnFinal{
		Status:              "done",
		Disposition:         "clarify",
		RoutingCandidateIDs: []string{"wh_a", "wh_b"},
		RoutingClarify:      true,
	}, time.Now())

	assertStrings(t, set, "routing_candidate_ids", []string{"wh_a", "wh_b"})
	if _, ok := set["routing_chosen_ids"]; ok {
		t.Error("routing_chosen_ids must be omitted when the router chose nothing")
	}
	if set["routing_clarify"] != true {
		t.Errorf("routing_clarify = %v, want true", set["routing_clarify"])
	}
}

func TestFinalizeUpdate_WritesErrorAndTokens(t *testing.T) {
	set := finalizeUpdate(TurnFinal{
		Status: "failed", Error: "boom", InputTokens: 3, OutputTokens: 4,
	}, time.Now())

	if set["error"] != "boom" {
		t.Errorf("error = %v, want %q", set["error"], "boom")
	}
	if set["input_tokens"] != 3 || set["output_tokens"] != 4 {
		t.Errorf("tokens = %v / %v, want 3 / 4", set["input_tokens"], set["output_tokens"])
	}
}

func assertStrings(t *testing.T, set map[string]interface{}, key string, want []string) {
	t.Helper()
	got, ok := set[key].([]string)
	if !ok {
		t.Errorf("%s = %v (%T), want []string", key, set[key], set[key])
		return
	}
	if len(got) != len(want) {
		t.Errorf("%s = %v, want %v", key, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s = %v, want %v", key, got, want)
			return
		}
	}
}
