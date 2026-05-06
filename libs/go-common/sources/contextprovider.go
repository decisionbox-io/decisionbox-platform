package sources

import (
	"context"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// ContextProviderName is the canonical agentplugin.ContextProvider name for
// the project knowledge-sources retriever. Tests reference it; production
// callers don't.
const ContextProviderName = "knowledge-sources"

// init registers the knowledge-sources provider with agentplugin so the
// agent's prompt-assembly layer doesn't have to hardcode this package.
//
// Registration uses RegisterDefaultContextProvider so an overriding
// plugin (e.g. an enterprise notes-aware retriever) that calls
// ReplaceContextProvider("knowledge-sources") wins regardless of Go's
// init order — if the plugin runs first it appends, and our register
// here becomes a no-op; if we run first the plugin's Replace swaps us
// out. With no plugin loaded, GetProvider() returns the NoOp impl
// and Section returns "" — the renderer drops empty sections, so
// behavior matches the pre-registry call site.
func init() {
	agentplugin.RegisterDefaultContextProvider(knowledgeSourcesProvider{})
}

// knowledgeSourcesProvider adapts the in-package Provider to the
// agentplugin.ContextProvider contract. It always defers to GetProvider()
// at call time, so swapping providers via Configure / SetProviderForTest
// takes effect immediately without re-registering.
type knowledgeSourcesProvider struct{}

func (knowledgeSourcesProvider) Name() string { return ContextProviderName }

func (knowledgeSourcesProvider) Section(ctx context.Context, projectID, query string, opts agentplugin.ContextProviderOpts) (string, error) {
	if query == "" || opts.Limit <= 0 {
		return "", nil
	}
	chunks, err := GetProvider().RetrieveContext(ctx, projectID, query, RetrieveOpts{
		Limit:    opts.Limit,
		MinScore: opts.MinScore,
	})
	if err != nil {
		return "", err
	}
	return FormatPromptSection(chunks), nil
}
