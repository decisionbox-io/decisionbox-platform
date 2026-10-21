package database

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

// TestRecommendationLogEntry_DecodesThePersistedFieldNames pins the BSON field
// names the agent writes onto RecommendationStep against the names this
// read-only view decodes.
//
// The failure it guards is silent in both directions: a field this struct does
// not name, or names with the wrong key, is dropped by the decoder and the
// endpoint reports a zero however non-zero it was in the database. The keys
// below are written as literals on purpose — they are the wire contract, and
// deriving them from the struct's own tags would assert nothing.
func TestRecommendationLogEntry_DecodesThePersistedFieldNames(t *testing.T) {
	doc := bson.M{
		"insight_count":                        5,
		"tokens_in":                            1200,
		"tokens_out":                           800,
		"duration_ms":                          int64(9000),
		"status":                               "recommendation_truncated",
		"recommendations_dropped":              6,
		"recommendations_dropped_parse":        2,
		"recommendations_dropped_missing_ids":  1,
		"recommendations_dropped_unknown_id":   3,
		"recommendation_parse_retries":         1,
		"recommendations_planned":              4,
		"recommendations_expanded":             3,
		"recommendations_expand_failed":        1,
		"recommendation_plan_retries":          1,
		"recommendations_single_call_fallback": true,
		"calls": bson.A{
			bson.M{
				"kind":           "expand",
				"index":          1,
				"attempt":        1,
				"stop_reason":    "length",
				"truncated":      true,
				"degenerate":     false,
				"tokens_in":      600,
				"tokens_out":     4096,
				"duration_ms":    int64(5000),
				"response_chars": 9000,
				"error":          "",
			},
		},
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var entry RecommendationLogEntry
	if err := bson.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	checks := []struct {
		name      string
		got, want interface{}
	}{
		{"insight_count", entry.InsightCount, 5},
		{"tokens_in", entry.TokensIn, 1200},
		{"tokens_out", entry.TokensOut, 800},
		{"duration_ms", entry.DurationMs, int64(9000)},
		{"status", entry.Status, "recommendation_truncated"},
		{"recommendations_dropped", entry.RecommendationsDropped, 6},
		{"recommendations_dropped_parse", entry.RecommendationsDroppedParse, 2},
		{"recommendations_dropped_missing_ids", entry.RecommendationsDroppedMissingIDs, 1},
		{"recommendations_dropped_unknown_id", entry.RecommendationsDroppedUnknownID, 3},
		{"recommendation_parse_retries", entry.RecommendationParseRetries, 1},
		{"recommendations_planned", entry.RecommendationsPlanned, 4},
		{"recommendations_expanded", entry.RecommendationsExpanded, 3},
		{"recommendations_expand_failed", entry.RecommendationsExpandFailed, 1},
		{"recommendation_plan_retries", entry.RecommendationPlanRetries, 1},
		{"recommendations_single_call_fallback", entry.RecommendationsSingleCallFallback, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s decoded as %v, want %v", c.name, c.got, c.want)
		}
	}

	if len(entry.Calls) != 1 {
		t.Fatalf("calls decoded as %d entries, want 1", len(entry.Calls))
	}
	call := entry.Calls[0]
	callChecks := []struct {
		name      string
		got, want interface{}
	}{
		{"calls.kind", call.Kind, "expand"},
		{"calls.index", call.Index, 1},
		{"calls.attempt", call.Attempt, 1},
		{"calls.stop_reason", call.StopReason, "length"},
		{"calls.truncated", call.Truncated, true},
		{"calls.tokens_in", call.TokensIn, 600},
		{"calls.tokens_out", call.TokensOut, 4096},
		{"calls.duration_ms", call.DurationMs, int64(5000)},
		{"calls.response_chars", call.ResponseChars, 9000},
	}
	for _, c := range callChecks {
		if c.got != c.want {
			t.Errorf("%s decoded as %v, want %v", c.name, c.got, c.want)
		}
	}
}
