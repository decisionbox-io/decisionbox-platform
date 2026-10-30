package models

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// agentDiscoverySource is the agent's own models. AnalysisStep below is a
// hand-maintained mirror of the agent's AnalysisStep, and the two live in
// different modules behind an `internal` boundary, so this reads the source
// rather than importing the type -- the same approach as
// TestRecommendationLogEntryMirrorsEveryAgentCounter, which exists for the same
// mistake on the recommendation row.
const agentDiscoverySource = "../../agent/internal/models/discovery.go"

// TestAnalysisStepMirrorsEveryAgentCounter is the guard the recommendation row
// already had and this one did not.
//
// A counter the agent persists on the analysis-step row and this struct omits is
// not a cosmetic gap: BSON drops the field on decode, so the endpoint reports
// zero however non-zero the database is, and the run reads as clean. The
// existing test for this row enumerates four repair counters by hand, so it
// cannot fail for a counter nobody remembered -- which is precisely the failure
// mode. Removing the figures_value_unreadable mirror broke no test at all before
// this one existed.
//
// Scoped to counters: this mirror is meant to be a subset and has no business
// carrying the insight list or the prompt. But a counter the agent went to the
// trouble of persisting exists to be read, and this struct is what reads it.
func TestAnalysisStepMirrorsEveryAgentCounter(t *testing.T) {
	src, err := os.ReadFile(agentDiscoverySource)
	if err != nil {
		t.Skipf("agent source not readable (%v); this guard only runs in the monorepo", err)
	}

	block := regexp.MustCompile(`(?s)type AnalysisStep struct \{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("could not find the agent's AnalysisStep in %s", agentDiscoverySource)
	}

	// An int field with a bson tag is a counter. Strings, slices and structs are the
	// payload this mirror is allowed to leave out.
	field := regexp.MustCompile("(?m)^\\s*(\\w+)\\s+int\\s+`bson:\"([a-z0-9_]+)")
	var want []string
	for _, m := range field.FindAllSubmatch(block[1], -1) {
		want = append(want, string(m[2]))
	}
	if len(want) == 0 {
		t.Fatal("found no int counters in the agent's AnalysisStep; the regex has stopped matching")
	}

	have := map[string]bool{}
	rt := reflect.TypeOf(AnalysisStep{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("bson"), ",")[0]
		if tag != "" {
			have[tag] = true
		}
	}

	var missing []string
	for _, w := range want {
		if !have[w] {
			missing = append(missing, w)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the agent persists these analysis-step counters and this mirror does not declare them, "+
			"so BSON drops them on decode and the endpoint reports zero: %s", strings.Join(missing, ", "))
	}
}
