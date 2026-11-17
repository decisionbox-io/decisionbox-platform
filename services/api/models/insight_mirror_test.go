package models

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The agent's Insight and Recommendation are the structs a discovery is
// WRITTEN from; the ones in this package are the structs it is READ into. They
// live in different modules behind an `internal` boundary, so this guard reads
// the agent's source rather than importing the type -- the same approach
// TestAnalysisStepMirrorsEveryAgentCounter takes, and for the same mistake.
//
// The mistake is quiet. A field the agent persists and this mirror omits is
// dropped by BSON on decode: the endpoint reports the zero value however
// non-zero the database is, and nothing fails. Four whole evidence-trail
// fields were unreachable outside the agent that way, each with a carefully
// chosen JSON name that no client could ever see.
//
// Shaped as an exhaustive partition rather than a hand-kept list of fields to
// look for, because a list cannot fail for the field nobody remembered. Every
// bson key on the agent's struct is either on the mirror or in the table below
// with the reason it is deliberately not.
const agentModelsSource = "../../agent/internal/models/discovery.go"

// mirroredInsightOmissions are the agent Insight bson keys this mirror
// deliberately does not carry.
var mirroredInsightOmissions = map[string]string{
	"sql_metadata": "the SQL an insight was derived from; no API response or dashboard surface reads it " +
		"(the standalone insight row carries its own copy for the surfaces that do)",
}

// mirroredRecommendationOmissions is the same table for Recommendation.
var mirroredRecommendationOmissions = map[string]string{
	"created_at": "a per-recommendation timestamp; every API surface dates a recommendation by its " +
		"discovery's own created_at, which is on the parent document",
}

// agentStructBSONKeys reads the bson keys one struct in the agent's models
// declares, in declaration order. Keys tagged "-" are skipped: those are
// fields the agent deliberately does not persist, so they cannot be dropped on
// decode.
func agentStructBSONKeys(t *testing.T, src []byte, name, source string) []string {
	t.Helper()
	block := regexp.MustCompile(`(?s)\ntype ` + name + ` struct \{(.*?)\n\}\n`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("could not find the agent's %s in %s", name, source)
	}
	field := regexp.MustCompile("(?m)^\\s*\\w+\\s+[^`\n]+`[^`]*bson:\"([^\",]+)")
	var out []string
	for _, m := range field.FindAllSubmatch(block[1], -1) {
		key := string(m[1])
		if key == "-" {
			continue
		}
		out = append(out, key)
	}
	if len(out) == 0 {
		t.Fatalf("found no bson-tagged fields on the agent's %s; the regex has stopped matching", name)
	}
	return out
}

// mirrorBSONKeys reads the bson keys a mirror struct in THIS package declares.
func mirrorBSONKeys(rt reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < rt.NumField(); i++ {
		key := strings.Split(rt.Field(i).Tag.Get("bson"), ",")[0]
		if key != "" && key != "-" {
			out[key] = true
		}
	}
	return out
}

// TestInsightAndRecommendationMirrorEveryPersistedAgentField is the guard that
// makes the next omission a test failure instead of a silent BSON drop.
func TestInsightAndRecommendationMirrorEveryPersistedAgentField(t *testing.T) {
	src, err := os.ReadFile(agentModelsSource)
	if err != nil {
		t.Skipf("agent source not readable (%v); this guard only runs in the monorepo", err)
	}

	for _, tc := range []struct {
		name      string
		mirror    reflect.Type
		omissions map[string]string
	}{
		{"Insight", reflect.TypeOf(Insight{}), mirroredInsightOmissions},
		{"Recommendation", reflect.TypeOf(Recommendation{}), mirroredRecommendationOmissions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := agentStructBSONKeys(t, src, tc.name, agentModelsSource)
			have := mirrorBSONKeys(tc.mirror)

			var missing []string
			for _, key := range want {
				if have[key] {
					continue
				}
				if _, excused := tc.omissions[key]; excused {
					continue
				}
				missing = append(missing, key)
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				t.Errorf("the agent persists these %s fields and this mirror does not declare them, so BSON "+
					"drops them on decode and every client reads the zero value: %s.\nDecide which it is: add "+
					"the field here, or record why it is not needed in the omissions table above.",
					tc.name, strings.Join(missing, ", "))
			}

			// The reverse direction. An excuse left behind after the field was
			// renamed upstream would let the NEW name through unnoticed.
			declared := map[string]bool{}
			for _, key := range want {
				declared[key] = true
			}
			for key := range tc.omissions {
				if !declared[key] {
					t.Errorf("the omissions table names %s.%q, which the agent no longer persists — drop the entry", tc.name, key)
				}
			}
		})
	}
}

// TestTheMirrorsCarryTheScoringSubDocument is the specific case the guard
// above generalises, spelled out so the failure names the field. The
// dashboard's whole legacy/scored distinction is "is `scoring` present", so
// this mirror dropping it would render every scored document as legacy.
func TestTheMirrorsCarryTheScoringSubDocument(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mirror reflect.Type
	}{
		{"Insight", reflect.TypeOf(Insight{})},
		{"Recommendation", reflect.TypeOf(Recommendation{})},
	} {
		field, ok := tc.mirror.FieldByName("Scoring")
		if !ok {
			t.Errorf("%s has no Scoring field", tc.name)
			continue
		}
		if got := strings.Split(field.Tag.Get("bson"), ",")[0]; got != "scoring" {
			t.Errorf("%s.Scoring bson key = %q, want %q", tc.name, got, "scoring")
		}
		// The wire name is the same on all four persisted copies, so the
		// dashboard reads one key across both endpoints it gets these from.
		if got := strings.Split(field.Tag.Get("json"), ",")[0]; got != "evidence_scoring" {
			t.Errorf("%s.Scoring json name = %q, want %q", tc.name, got, "evidence_scoring")
		}
		// A pointer, because nil is the legacy marker.
		if field.Type.Kind() != reflect.Ptr {
			t.Errorf("%s.Scoring is %s, not a pointer; nil is what marks a document written before the scorer existed",
				tc.name, field.Type.Kind())
		}
	}
}
