package database

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
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
		"recommendations_expand_skipped":       2,
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
		{"recommendations_expand_skipped", entry.RecommendationsExpandSkipped, 2},
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

// agentRecommendationStepSource is where the agent declares what it persists.
// Read rather than imported: services/agent is its own module, so the compiler
// cannot be made to notice a field that was added there and not mirrored here.
const agentRecommendationStepSource = "../../agent/internal/models/discovery.go"

// recommendationLogEntryOmits names the fields this read-only view leaves out
// on purpose. The full LLM dialog stays out to keep the dashboard payload
// bounded; the agent's own tooling reads the collection directly.
//
// Anything not listed here and not present on RecommendationLogEntry is a
// field BSON silently drops, so the endpoint reports a zero however non-zero
// it was in the database.
var recommendationLogEntryOmits = map[string]string{
	"prompt":          "the rendered prompt — bounded payload",
	"response":        "the raw LLM response — bounded payload",
	"recommendations": "the recommendations themselves are served by their own endpoints",
}

// TestRecommendationLogEntry_MirrorsEveryPersistedField closes the class rather
// than its instances.
//
// Three separate review rounds found a field the agent persisted and this view
// did not name — the plan/expand counters, then the citation counters — each
// time by someone reading the two structs side by side. A test that lists the
// fields it knows about can only ever catch the fields someone remembered, so
// this one takes the inventory from the agent's own declaration: add a field to
// RecommendationStep without mirroring it, and this fails by name.
func TestRecommendationLogEntry_MirrorsEveryPersistedField(t *testing.T) {
	src, err := os.ReadFile(agentRecommendationStepSource)
	if err != nil {
		t.Fatalf("read the agent's models: %v (if the layout moved, this test's path needs updating — not skipping)", err)
	}

	stepFields, err := bsonFieldsOfStruct(string(src), "RecommendationStep")
	if err != nil {
		t.Fatalf("RecommendationStep: %v", err)
	}
	if len(stepFields) < 10 {
		t.Fatalf("found only %d fields on RecommendationStep; the parse is wrong, not the struct", len(stepFields))
	}

	mirrored := map[string]struct{}{}
	entryType := reflect.TypeOf(RecommendationLogEntry{})
	for i := 0; i < entryType.NumField(); i++ {
		if name := bsonName(entryType.Field(i).Tag.Get("bson")); name != "" {
			mirrored[name] = struct{}{}
		}
	}

	for _, f := range stepFields {
		if _, ok := mirrored[f]; ok {
			continue
		}
		if why, deliberate := recommendationLogEntryOmits[f]; deliberate {
			t.Logf("%s is deliberately omitted: %s", f, why)
			continue
		}
		t.Errorf("the agent persists %q and RecommendationLogEntry does not name it, so the endpoint reports a zero for it; mirror the field, or add it to recommendationLogEntryOmits with the reason", f)
	}

	// And the same check in the other direction: a field named here that the
	// agent never writes is a field that will always read zero.
	persisted := map[string]struct{}{}
	for _, f := range stepFields {
		persisted[f] = struct{}{}
	}
	for name := range mirrored {
		if _, ok := persisted[name]; !ok {
			t.Errorf("RecommendationLogEntry names %q, which the agent does not persist", name)
		}
	}
}

// TestRecommendationCall_MirrorsEveryPersistedField does the same for the
// per-call record nested inside the step.
func TestRecommendationCall_MirrorsEveryPersistedField(t *testing.T) {
	src, err := os.ReadFile(agentRecommendationStepSource)
	if err != nil {
		t.Fatalf("read the agent's models: %v", err)
	}
	callFields, err := bsonFieldsOfStruct(string(src), "RecommendationCall")
	if err != nil {
		t.Fatalf("RecommendationCall: %v", err)
	}
	if len(callFields) < 5 {
		t.Fatalf("found only %d fields on RecommendationCall; the parse is wrong", len(callFields))
	}

	mirrored := map[string]struct{}{}
	infoType := reflect.TypeOf(RecommendationCallInfo{})
	for i := 0; i < infoType.NumField(); i++ {
		if name := bsonName(infoType.Field(i).Tag.Get("bson")); name != "" {
			mirrored[name] = struct{}{}
		}
	}
	for _, f := range callFields {
		if _, ok := mirrored[f]; !ok {
			t.Errorf("the agent persists calls[].%q and RecommendationCallInfo does not name it", f)
		}
	}
}

// bsonFieldsOfStruct returns the bson field names declared on the named struct
// in src. Deliberately a parse of the declaration rather than a regex over the
// whole file: a tag from a neighbouring struct would make this test pass for
// the wrong reason, which is the failure mode it exists to prevent.
func bsonFieldsOfStruct(src, name string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "discovery.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	var fields []string
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != name {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		found = true
		for _, f := range st.Fields.List {
			if f.Tag == nil {
				continue
			}
			tag, err := strconv.Unquote(f.Tag.Value)
			if err != nil {
				continue
			}
			if bn := bsonName(reflect.StructTag(tag).Get("bson")); bn != "" {
				fields = append(fields, bn)
			}
		}
		return false
	})
	if !found {
		return nil, fmt.Errorf("struct %s not found", name)
	}
	return fields, nil
}

// bsonName reads the field name out of a bson tag, dropping its options and
// the "-" that means "not persisted".
func bsonName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	if name == "" || name == "-" {
		return ""
	}
	return name
}
