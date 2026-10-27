package discovery

import (
	"strings"
	"testing"
)

// What this file pins: the insight schema reaches the model on the call that writes the
// insights, the switch that turns that off exists, and every string the schema describes is
// bounded except the one field measured to have no bound available.

func TestTheFirstAnalysisCallCarriesTheSchemaByDefault(t *testing.T) {
	// Default on. The field descriptions are the schema's half of the numbers contract, and
	// a description on a call no model receives is not a contract -- which is what the
	// measurement found: across 189 recorded analysis steps exactly one ever took the
	// retry path the schema used to be attached to.
	t.Setenv(analysisStructuredOutputEnv, "")
	if !analysisStructuredOutputEnabled() {
		t.Error("the first analysis call does not carry the insight schema by default, so every " +
			"field description in it is text no model sees on the call that writes insights")
	}
}

func TestTheSchemaOnTheFirstAnalysisCallCanBeTurnedOff(t *testing.T) {
	// The escape hatch has to work, because this is the one change in this layer that
	// alters the shape of a call rather than its text, on the phase that was deliberately
	// left byte-identical for models already producing good results.
	for _, off := range []string{"false", "0", "FALSE"} {
		t.Setenv(analysisStructuredOutputEnv, off)
		if analysisStructuredOutputEnabled() {
			t.Errorf("%s=%q did not restore the plain first call; a deployment that regresses "+
				"would have to wait for an image", analysisStructuredOutputEnv, off)
		}
	}
}

// insightUnboundedByDesign is the single string the insight schema still leaves unbounded,
// and it is a measurement rather than a preference.
//
// Insight descriptions run p50 815, p95 1,146, p99 1,464, maximum 1,643 over the 562
// insights this pipeline has written. grammarRepetitionCeiling is 2,000 and a maxLength
// above it is silently clamped back to unbounded on an Ollama-backed runner, so the only
// bound that applies there is 1.22x the observed maximum -- a bound that can clip a real
// answer. No bound is the honest answer; a comfortable-looking 2,000 is not.
const insightUnboundedByDesign = "insight.insights.items.description"

func TestTheInsightSchemaBoundsEveryStringItCan(t *testing.T) {
	// The schema was unbounded throughout while it was attached on a corrective retry
	// alone, which was a recorded and reasonable risk: no grammar was applying to it. That
	// reasoning ended the moment it went on every analysis call, and this is the test that
	// records the new position -- every string bounded, one exception, named.
	unbounded := findUnboundedStrings(insightResponseSchema(), "insight")
	got := map[string]bool{}
	for _, path := range unbounded {
		got[path] = true
	}

	if !got[insightUnboundedByDesign] {
		t.Errorf("%s is now bounded. If a bound above %d was added it does nothing on an "+
			"Ollama-backed runner, and one below it can clip a description measured at 1,643 "+
			"runes; if the prompt now asks for less prose, re-measure and update the constant "+
			"and this test together", insightUnboundedByDesign, grammarRepetitionCeiling)
	}
	for path := range got {
		if path == insightUnboundedByDesign {
			continue
		}
		t.Errorf("%s is an unbounded string in a schema that is now sent on every analysis "+
			"call, so a grammar lets it run forever there; bound it with boundedString from a "+
			"measured maximum", path)
	}
}

// TestTheInsightSchemaStillDescribesWhatItAsksFor guards the mechanical risk in converting
// twenty field descriptions from an unbounded helper to bounded ones: a description lost in
// the edit is invisible, because a schema with no description is still a valid schema.
func TestTheInsightSchemaStillDescribesWhatItAsksFor(t *testing.T) {
	for path, desc := range schemaStringFields(t) {
		if !strings.HasPrefix(path, "insight.") {
			continue
		}
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description: a model is being asked for a field with nothing "+
				"said about it, and an empty description is not a schema error", path)
		}
	}
}
