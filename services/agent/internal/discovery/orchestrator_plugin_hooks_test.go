package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
)

var errBoom = errors.New("provider failed")

func unregisterContextProviderForTest(name string) {
	agentplugin.UnregisterContextProviderForTest(name)
}

// TestInjectKnowledgeSources_RegistryMigrationByteEquivalent is the
// Phase 0 regression guard: replacing the direct gosources call with
// agentplugin.RenderSections must produce a byte-for-byte identical
// final prompt for the same chunk set. If this test fails, the
// orchestrator's prompt drifted during the migration.
func TestInjectKnowledgeSources_RegistryMigrationByteEquivalent(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	chunks := []gosources.Chunk{
		{
			SourceID:   "s1",
			SourceName: "handbook.pdf",
			SourceType: "pdf",
			Text:       "Retention is measured weekly.",
			Score:      0.87,
			Metadata:   map[string]string{"page": "12"},
		},
		{
			SourceID:   "s2",
			SourceName: "glossary.md",
			SourceType: "md",
			Text:       "DAU = Daily Active Users.",
			Score:      0.74,
		},
	}
	gosources.SetProviderForTest(&stubSourcesProvider{chunks: chunks})

	o := &Orchestrator{projectID: "proj_abc"}
	prompt := "## Original Prompt\nDo the analysis."
	got := o.injectKnowledgeSources(context.Background(), prompt, "the question", 8)

	// Pre-registry layout the orchestrator produced was:
	//   <FormatPromptSection output> + "\n" + <prompt>
	// That output already ends with "\n", so the concatenation has a
	// blank line between section and prompt. The new path must match.
	directSection := gosources.FormatPromptSection(chunks)
	want := directSection + "\n" + prompt

	if got != want {
		t.Fatalf("registry-migrated injection drifted from direct call:\ngot:\n%q\n\nwant:\n%q", got, want)
	}
}

// TestInjectKnowledgeSources_RegistryMultiProviderOrdering asserts that
// when multiple agentplugin.ContextProvider implementations are
// registered (the Phase 4 case), they all contribute sections in
// registration order — knowledge-sources first because gosources
// registers itself in init().
func TestInjectKnowledgeSources_RegistryMultiProviderOrdering(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()
	gosources.SetProviderForTest(&stubSourcesProvider{
		chunks: []gosources.Chunk{
			{SourceID: "s1", SourceName: "doc.pdf", Text: "knowledge body", Score: 0.9},
		},
	})

	// Local extra provider — simulates a future column-hints / area-priority
	// plugin attaching via the same hook.
	extraName := "test-extra-provider"
	defer unregisterContextProviderForTest(extraName)
	agentplugin.RegisterContextProvider(agentplugin.ContextProviderFunc{
		ProviderName: extraName,
		Fn: func(_ context.Context, _, _ string, _ agentplugin.ContextProviderOpts) (string, error) {
			return "## Extra Context\nbody-from-extra-provider", nil
		},
	})

	o := &Orchestrator{projectID: "proj_abc"}
	got := o.injectKnowledgeSources(context.Background(), "PROMPT", "q", 5)

	if !strings.Contains(got, "## Project Knowledge") {
		t.Fatalf("expected knowledge-sources section in injection; got:\n%s", got)
	}
	if !strings.Contains(got, "body-from-extra-provider") {
		t.Fatalf("expected extra provider's body in injection; got:\n%s", got)
	}
	idxKnow := strings.Index(got, "## Project Knowledge")
	idxExtra := strings.Index(got, "## Extra Context")
	idxPrompt := strings.Index(got, "PROMPT")
	if idxKnow < 0 || idxExtra < 0 || idxPrompt < 0 || idxKnow >= idxExtra || idxExtra >= idxPrompt {
		t.Fatalf("expected order knowledge -> extra -> prompt; got:\n%s", got)
	}
}

// TestInjectKnowledgeSources_RegistryProviderErrorIsContained covers the
// error-isolation guarantee: if one provider fails, others still render
// and the prompt is not aborted.
func TestInjectKnowledgeSources_RegistryProviderErrorIsContained(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()
	gosources.SetProviderForTest(&stubSourcesProvider{
		chunks: []gosources.Chunk{
			{SourceID: "s1", SourceName: "doc.pdf", Text: "ok body", Score: 0.9},
		},
	})

	failName := "test-failing-provider"
	defer unregisterContextProviderForTest(failName)
	agentplugin.RegisterContextProvider(agentplugin.ContextProviderFunc{
		ProviderName: failName,
		Fn: func(_ context.Context, _, _ string, _ agentplugin.ContextProviderOpts) (string, error) {
			return "", errBoom
		},
	})

	o := &Orchestrator{projectID: "proj_abc"}
	got := o.injectKnowledgeSources(context.Background(), "PROMPT", "q", 5)

	if !strings.Contains(got, "## Project Knowledge") || !strings.Contains(got, "ok body") {
		t.Fatalf("knowledge section must still render when a sibling provider fails; got:\n%s", got)
	}
	if !strings.Contains(got, "PROMPT") {
		t.Fatalf("original prompt body lost; got:\n%s", got)
	}
}
