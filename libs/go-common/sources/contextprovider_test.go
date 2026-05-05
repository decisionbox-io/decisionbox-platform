package sources

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// stubKnowledgeProvider lets tests inject controlled retrieval behavior.
type stubKnowledgeProvider struct {
	chunks    []Chunk
	err       error
	gotOpts   RetrieveOpts
	gotProj   string
	gotQuery  string
}

func (s *stubKnowledgeProvider) RetrieveContext(_ context.Context, projectID string, query string, opts RetrieveOpts) ([]Chunk, error) {
	s.gotProj = projectID
	s.gotQuery = query
	s.gotOpts = opts
	return s.chunks, s.err
}

func TestKnowledgeSourcesProvider_RegisteredByInit(t *testing.T) {
	// init() should have registered the canonical provider before any test runs.
	provs := agentplugin.GetAllContextProviders()
	for _, p := range provs {
		if p.Name() == ContextProviderName {
			return
		}
	}
	t.Fatalf("expected agentplugin to have %q registered after sources package init; got %v", ContextProviderName, providerNames(provs))
}

func providerNames(provs []agentplugin.ContextProvider) []string {
	out := make([]string, len(provs))
	for i, p := range provs {
		out[i] = p.Name()
	}
	return out
}

func TestKnowledgeSourcesProvider_EmptyQueryReturnsEmpty(t *testing.T) {
	resetForTest()
	defer resetForTest()

	stub := &stubKnowledgeProvider{chunks: []Chunk{{SourceID: "s1", SourceName: "doc.pdf", Text: "hello", Score: 0.9}}}
	SetProviderForTest(stub)

	got, err := knowledgeSourcesProvider{}.Section(context.Background(), "proj", "", agentplugin.ContextProviderOpts{Limit: 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("empty query must return empty section; got %q", got)
	}
	if stub.gotQuery != "" || stub.gotProj != "" {
		t.Fatalf("provider should not have been invoked; got proj=%q query=%q", stub.gotProj, stub.gotQuery)
	}
}

func TestKnowledgeSourcesProvider_ZeroLimitReturnsEmpty(t *testing.T) {
	resetForTest()
	defer resetForTest()

	stub := &stubKnowledgeProvider{chunks: []Chunk{{SourceID: "s1", Text: "hello"}}}
	SetProviderForTest(stub)

	got, err := knowledgeSourcesProvider{}.Section(context.Background(), "proj", "the question", agentplugin.ContextProviderOpts{Limit: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("zero limit must return empty section; got %q", got)
	}
}

func TestKnowledgeSourcesProvider_NoChunksReturnsEmpty(t *testing.T) {
	resetForTest()
	defer resetForTest()

	stub := &stubKnowledgeProvider{chunks: nil}
	SetProviderForTest(stub)

	got, err := knowledgeSourcesProvider{}.Section(context.Background(), "proj", "q", agentplugin.ContextProviderOpts{Limit: 5, MinScore: 0.4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("no chunks must yield empty section; got %q", got)
	}
	// Provider was still consulted with the right opts.
	if stub.gotProj != "proj" || stub.gotQuery != "q" {
		t.Fatalf("provider got proj=%q query=%q, want proj=q", stub.gotProj, stub.gotQuery)
	}
	if stub.gotOpts.Limit != 5 || stub.gotOpts.MinScore != 0.4 {
		t.Fatalf("provider got opts %+v, want Limit=5 MinScore=0.4", stub.gotOpts)
	}
}

func TestKnowledgeSourcesProvider_ChunksFormatted(t *testing.T) {
	resetForTest()
	defer resetForTest()

	stub := &stubKnowledgeProvider{
		chunks: []Chunk{
			{SourceID: "s1", SourceName: "handbook.pdf", SourceType: "pdf", Text: "first chunk", Score: 0.87, Metadata: map[string]string{"page": "12"}},
			{SourceID: "s2", SourceName: "glossary.md", SourceType: "md", Text: "second chunk", Score: 0.74},
		},
	}
	SetProviderForTest(stub)

	got, err := knowledgeSourcesProvider{}.Section(context.Background(), "proj", "what is X", agentplugin.ContextProviderOpts{Limit: 8, MinScore: 0.4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Match the exact rendering produced by FormatPromptSection so we
	// can be confident the migrated path produces byte-for-byte identical
	// output to the previous direct call.
	want := FormatPromptSection(stub.chunks)
	if got != want {
		t.Fatalf("Section output differs from FormatPromptSection:\n got=%q\nwant=%q", got, want)
	}
	if !strings.Contains(got, "## Project Knowledge") {
		t.Fatalf("expected project knowledge heading; got %q", got)
	}
}

func TestKnowledgeSourcesProvider_PropagatesError(t *testing.T) {
	resetForTest()
	defer resetForTest()

	wantErr := errors.New("boom")
	SetProviderForTest(&stubKnowledgeProvider{err: wantErr})

	got, err := knowledgeSourcesProvider{}.Section(context.Background(), "proj", "q", agentplugin.ContextProviderOpts{Limit: 5})
	if got != "" {
		t.Fatalf("on error, section should be empty; got %q", got)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error not propagated; got %v", err)
	}
}

func TestKnowledgeSourcesProvider_DefaultIsNoopAndEmpty(t *testing.T) {
	resetForTest()
	defer resetForTest()

	got, err := knowledgeSourcesProvider{}.Section(context.Background(), "proj", "q", agentplugin.ContextProviderOpts{Limit: 5, MinScore: 0.4})
	if err != nil {
		t.Fatalf("default NoOp provider must not return error; got %v", err)
	}
	if got != "" {
		t.Fatalf("default NoOp provider must yield empty section; got %q", got)
	}
}

// TestKnowledgeSourcesProvider_AgentPluginRendersIdenticallyToDirectCall is
// the regression guard called out in the plan: the agent's pre-registry
// prompt assembly produced "<section>\n<prompt>"; the new path goes through
// agentplugin.RenderSections. This test asserts that for a representative
// chunk set, the bytes the agent prepends to its prompt are unchanged.
func TestKnowledgeSourcesProvider_AgentPluginRendersIdenticallyToDirectCall(t *testing.T) {
	resetForTest()
	defer resetForTest()

	chunks := []Chunk{
		{SourceID: "s1", SourceName: "handbook.pdf", SourceType: "pdf", Text: "first chunk", Score: 0.87, Metadata: map[string]string{"page": "12"}},
		{SourceID: "s2", SourceName: "glossary.md", SourceType: "md", Text: "second chunk", Score: 0.74},
	}
	SetProviderForTest(&stubKnowledgeProvider{chunks: chunks})

	// Pre-registry: orchestrator did `section + "\n" + prompt`, where
	// section came straight from FormatPromptSection.
	directSection := FormatPromptSection(chunks)

	// New path: agentplugin renders sections from registered providers.
	rendered := agentplugin.RenderSections(context.Background(), "proj", "q", agentplugin.ContextProviderOpts{Limit: 8, MinScore: 0.4}, nil)

	// Both should agree byte-for-byte after stripping a trailing newline,
	// since the orchestrator concatenates "<section>\n<prompt>" either way.
	if strings.TrimRight(rendered, "\n") != strings.TrimRight(directSection, "\n") {
		t.Fatalf("rendered output drifted from FormatPromptSection:\n rendered=%q\n direct=%q", rendered, directSection)
	}
}
