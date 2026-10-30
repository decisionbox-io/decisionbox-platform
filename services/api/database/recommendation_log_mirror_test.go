package database

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// agentRecommendationStepSource is the agent's own RecommendationStep. The API
// read model below is a hand-maintained mirror of it, and the two live in
// different modules behind an `internal` boundary, so this test reads the
// source rather than importing the type.
const agentRecommendationStepSource = "../../agent/internal/models/discovery.go"

// TestRecommendationLogEntryMirrorsEveryAgentCounter is a class test for a
// mistake that has already been made twice in this struct.
//
// RecommendationLogEntry decodes the recommendation-phase row the agent
// writes, and it is a hand-written subset of the agent's RecommendationStep.
// A field the agent persists and this struct omits is not a cosmetic gap: BSON
// drops it on decode, so the endpoint reports zero however non-zero it was in
// the database, and the dashboard renders a clean run. Both the parse-drop
// telemetry and the figure telemetry were added to the agent first and
// mirrored here afterwards, each time by someone noticing.
//
// Scoped to the counters, deliberately. The mirror is meant to be a subset --
// it has no business carrying the parsed recommendation list or the prompt --
// but a counter the agent went to the trouble of persisting exists to be read,
// and this struct is what reads it.
func TestRecommendationLogEntryMirrorsEveryAgentCounter(t *testing.T) {
	src, err := os.ReadFile(agentRecommendationStepSource)
	if err != nil {
		t.Fatalf("cannot read the agent's models source at %s: %v", agentRecommendationStepSource, err)
	}

	agentTags := bsonTagsOfStruct(t, string(src), "RecommendationStep")
	if len(agentTags) == 0 {
		t.Fatalf("found no bson tags on the agent's RecommendationStep -- has the struct been "+
			"renamed or moved out of %s? This test is reading the wrong thing, not passing",
			agentRecommendationStepSource)
	}

	mirrored := map[string]bool{}
	entry := reflect.TypeOf(RecommendationLogEntry{})
	for i := 0; i < entry.NumField(); i++ {
		mirrored[bsonName(entry.Field(i).Tag.Get("bson"))] = true
	}

	var missing []string
	for _, tag := range agentTags {
		if !isCounterTag(tag) || mirrored[tag] {
			continue
		}
		missing = append(missing, tag)
	}
	sort.Strings(missing)
	for _, tag := range missing {
		t.Errorf("the agent persists %q on the recommendation-phase row and RecommendationLogEntry "+
			"does not declare it, so BSON drops it on decode and the endpoint reports zero "+
			"however non-zero it is in the database", tag)
	}
}

// isCounterTag matches the recommendation-phase telemetry this mirror is
// responsible for: the per-reason drop counters and the figure counters.
// Matched by prefix rather than listed, so a counter added later is covered
// without anyone remembering to extend a list -- which is the failure this
// whole test exists for.
func isCounterTag(tag string) bool {
	return strings.HasPrefix(tag, "recommendations_dropped") ||
		strings.HasPrefix(tag, "recommendation_parse_") ||
		strings.HasPrefix(tag, "figures_")
}

var (
	reStructOpen = regexp.MustCompile(`type\s+(\w+)\s+struct\s*\{`)
	reBSONTag    = regexp.MustCompile("bson:\"([^\"]+)\"")
)

// bsonTagsOfStruct returns the bson tag names declared inside one named struct
// in a Go source file, tracking brace depth so a nested struct literal cannot
// leak its tags into the answer.
func bsonTagsOfStruct(t *testing.T, src, name string) []string {
	t.Helper()

	open := reStructOpen.FindAllStringSubmatchIndex(src, -1)
	start := -1
	for _, m := range open {
		if src[m[2]:m[3]] == name {
			start = m[1]
			break
		}
	}
	if start < 0 {
		return nil
	}

	depth := 1
	end := start
	for i := start; i < len(src) && depth > 0; i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
		end = i
	}

	var out []string
	for _, m := range reBSONTag.FindAllStringSubmatch(src[start:end], -1) {
		if n := bsonName(m[1]); n != "" && n != "-" {
			out = append(out, n)
		}
	}
	return out
}

// bsonName strips the options from a bson tag: "figures_inlined,omitempty".
func bsonName(tag string) string {
	return strings.TrimSpace(strings.SplitN(tag, ",", 2)[0])
}
