package main

import (
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

// The replay judges with the project's own LLM, so it needs the project's
// API key the way the agent gets it. Without it every call to an API-key
// provider (LiteLLM, OpenAI-compatible hosts) failed with 401.
func TestLLMProviderConfig_CarriesTheCredential(t *testing.T) {
	project := &ProjectDoc{}
	project.LLM.Provider = "litellm"
	project.LLM.Model = "some/model"
	project.LLM.Config = map[string]string{"base_url": "https://example.test/v1/openai"}

	cfg := llmProviderConfig(project, cliFlags{}, "sk-test")
	if cfg["credentials_json"] != "sk-test" {
		t.Errorf("credentials_json = %q, want the resolved key", cfg["credentials_json"])
	}
	if cfg["base_url"] != "https://example.test/v1/openai" || cfg["model"] != "some/model" {
		t.Errorf("project settings lost: %v", cfg)
	}
	if _, set := llmProviderConfig(project, cliFlags{}, "")["credentials_json"]; set {
		t.Error("no credential resolved: the key must be absent, not empty")
	}
}

// A replay whose every LLM call failed used no tokens; it must not pass for
// a run of "unverifiable" verdicts.
func TestNoLLMTokensUsed(t *testing.T) {
	failed := TargetResult{Verifier: &valmodels.StructuredVerdict{}, Refuter: &valmodels.StructuredVerdict{}}
	worked := TargetResult{Verifier: &valmodels.StructuredVerdict{LLMTokensIn: 900, LLMTokensOut: 80}}
	if !noLLMTokensUsed([]TargetResult{failed, failed}) {
		t.Error("every call failed: want true")
	}
	if noLLMTokensUsed([]TargetResult{failed, worked}) {
		t.Error("one target used the model: want false")
	}
	if noLLMTokensUsed(nil) {
		t.Error("nothing replayed: want false")
	}
}
