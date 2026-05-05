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
// Registration is unconditional: when no enterprise plugin is loaded,
// GetProvider() returns the NoOp implementation, RetrieveContext returns
// zero chunks, and Section returns "" — the agent's renderer drops empty
// sections, so behavior is identical to the pre-registry call site.
func init() {
	agentplugin.RegisterContextProvider(knowledgeSourcesProvider{})
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
