package ai

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The exploration action's free-text key is "rationale". Some models
// (Claude Opus 5.5, Sonnet 5.5) refuse — stop_reason "refusal", no
// content — any request that asks them for a JSON field named "thinking",
// so no engine-authored message may ask for one.

func TestParseAction_AcceptsRationaleKey(t *testing.T) {
	a, err := ParseAction(`{"rationale": "check volumes first", "query": "SELECT 1"}`, nil)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if a.Action != "query_data" || a.Thinking != "check volumes first" {
		t.Errorf("action = %q, thinking = %q", a.Action, a.Thinking)
	}
}

// Packs written before the rename still teach "thinking"; their replies
// must keep parsing.
func TestParseAction_AcceptsLegacyThinkingKey(t *testing.T) {
	a, err := ParseAction(`{"thinking": "legacy", "query": "SELECT 1"}`, nil)
	if err != nil {
		t.Fatalf("ParseAction: %v", err)
	}
	if a.Thinking != "legacy" {
		t.Errorf("thinking = %q", a.Thinking)
	}
}

func TestEngineMessages_NeverAskForThinkingKey(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{MaxSteps: 7, MinSteps: 5})
	msgs := map[string]string{
		"repair nudge (no JSON)":     explorationRepairNudge(errors.New("no action JSON object found in response"), false, false),
		"repair nudge (correlation)": explorationRepairNudge(errors.New("no query, lookup_schema"), true, true),
		"initial message":            engine.buildInitialMessage(ExplorationContext{}),
	}
	for _, reason := range []string{"productive", "unproven", "min_steps"} {
		nudge, _ := engine.rejectionFor(reason, 3)
		msgs["completion rejection ("+reason+")"] = nudge
	}
	corr := newCorrelationEngine(staticLookup(confirmedPairing()))
	msgs["correlation hint"] = corr.executeGetCorrelations(context.Background(), &ExplorationAction{}, &models.ExplorationStep{})
	for name, m := range msgs {
		if strings.Contains(m, `"thinking"`) {
			t.Errorf("%s asks for a \"thinking\" key:\n%s", name, m)
		}
	}
}

// The engine, not the pack, owns the action format: the opening message
// states it, so a pack whose exploration prompt never describes it (a
// generated or hand-written custom pack) does not leave every model
// guessing and paying a reformat round-trip per guess.
func TestBuildInitialMessage_StatesTheActionFormat(t *testing.T) {
	engine := NewExplorationEngine(ExplorationEngineOptions{MaxSteps: 7})
	msg := engine.buildInitialMessage(ExplorationContext{})
	for _, want := range []string{`"rationale"`, `"query"`, `"lookup_schema"`, `"search_tables"`, `"done"`, "EXACTLY ONE JSON object"} {
		if !strings.Contains(msg, want) {
			t.Errorf("initial message missing %s:\n%s", want, msg)
		}
	}
}

func TestNormalizeLegacyActionKey(t *testing.T) {
	in := "Respond with:\n{\n  \"thinking\": \"why\",\n  \"query\": \"SELECT 1\"\n}\nKeep thinking about churn. {\"thinking\":\"x\"}"
	got := NormalizeLegacyActionKey(in)
	if strings.Contains(got, `"thinking"`) {
		t.Errorf("legacy key left in place:\n%s", got)
	}
	if !strings.Contains(got, "\"rationale\": \"why\"") || !strings.Contains(got, `{"rationale":"x"}`) {
		t.Errorf("keys not renamed in place:\n%s", got)
	}
	if !strings.Contains(got, "Keep thinking about churn.") {
		t.Errorf("prose must be left alone:\n%s", got)
	}
}

// On a multi-datasource run the action format must carry datasource_id and
// must not prescribe SQL — a secondary datasource may speak another query
// language, and a query without datasource_id goes to the primary.
func TestActionMenu_MultiDatasourceIsRoutingAware(t *testing.T) {
	engine := newCorrelationEngine(nil)
	for name, msg := range map[string]string{
		"initial message": engine.buildInitialMessage(ExplorationContext{}),
		"repair nudge":    explorationRepairNudge(errors.New("no action JSON object found in response"), engine.correlationsOffered(), true),
	} {
		if !strings.Contains(msg, `"datasource_id"`) {
			t.Errorf("%s: multi-datasource menu must show datasource_id:\n%s", name, msg)
		}
		if strings.Contains(msg, "SELECT") {
			t.Errorf("%s: multi-datasource menu must not prescribe SQL:\n%s", name, msg)
		}
	}
	single := NewExplorationEngine(ExplorationEngineOptions{MaxSteps: 7})
	if msg := single.buildInitialMessage(ExplorationContext{}); strings.Contains(msg, "datasource_id") {
		t.Errorf("single-datasource menu should stay unchanged:\n%s", msg)
	}
}
