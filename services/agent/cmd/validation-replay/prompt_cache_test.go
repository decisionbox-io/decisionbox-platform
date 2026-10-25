package main

import (
	"testing"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Replay exists to mirror a production run, so it must resolve prompt caching
// identically to the agent. It has its own minimal project document, so the
// rule is duplicated — this pins the two together over every combination that
// matters, in both directions (the field can turn caching off over a legacy
// "true" and on over a legacy "false").
func TestReplayPromptCacheMatchesAgentModel(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		name  string
		field *bool
		conf  map[string]string
	}{
		{"unset", nil, nil},
		{"legacy false", nil, map[string]string{gollm.PromptCacheKey: "false"}},
		{"legacy FALSE", nil, map[string]string{gollm.PromptCacheKey: "FALSE"}},
		{"legacy true", nil, map[string]string{gollm.PromptCacheKey: "true"}},
		{"field on over legacy false", &yes, map[string]string{gollm.PromptCacheKey: "false"}},
		{"field off over legacy true", &no, map[string]string{gollm.PromptCacheKey: "true"}},
		{"field off, no legacy", &no, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			replay := &ProjectDoc{PromptCacheEnabled: c.field}
			replay.LLM.Config = c.conf

			agent := &models.Project{PromptCacheEnabled: c.field}
			agent.LLM.Config = c.conf

			got, want := replayPromptCacheEnabled(replay), agent.EffectivePromptCacheEnabled()
			if got != want {
				t.Errorf("replay resolved %v but the agent resolves %v — a replay would not mirror the run", got, want)
			}
		})
	}
	if !replayPromptCacheEnabled(nil) {
		t.Error("a nil project should resolve to caching on")
	}
}
