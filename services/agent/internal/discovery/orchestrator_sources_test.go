package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	gosources "github.com/decisionbox-io/decisionbox/libs/go-common/sources"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

type stubSourcesProvider struct {
	chunks  []gosources.Chunk
	err     error
	gotProj string
	gotQ    string
	gotOpts gosources.RetrieveOpts
}

func (s *stubSourcesProvider) RetrieveContext(_ context.Context, projectID, query string, opts gosources.RetrieveOpts) ([]gosources.Chunk, error) {
	s.gotProj = projectID
	s.gotQ = query
	s.gotOpts = opts
	return s.chunks, s.err
}

func TestInjectKnowledgeSources_NoOpProviderReturnsPromptUnchanged(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	o := &Orchestrator{projectID: "proj_abc"}
	want := "original prompt body"

	got := o.injectKnowledgeSources(context.Background(), want, "some query", knowledgeTopKAnalysis)

	if got != want {
		t.Errorf("expected prompt to be unchanged when no provider is registered\nwant: %q\ngot:  %q", want, got)
	}
}

func TestInjectKnowledgeSources_StubProviderInjectsSection(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &stubSourcesProvider{
		chunks: []gosources.Chunk{
			{SourceName: "handbook.pdf", Text: "Retention is measured weekly.", Score: 0.9, Metadata: map[string]string{"page": "12"}},
		},
	}
	gosources.SetProviderForTest(stub)

	o := &Orchestrator{projectID: "proj_abc"}
	original := "## Original Prompt\nDo the analysis."

	got := o.injectKnowledgeSources(context.Background(), original, "some query", 5)

	if !strings.Contains(got, "## Project Knowledge") {
		t.Errorf("expected '## Project Knowledge' section in injected prompt, got:\n%s", got)
	}
	if !strings.Contains(got, "Retention is measured weekly.") {
		t.Errorf("expected chunk text in injected prompt, got:\n%s", got)
	}
	if !strings.Contains(got, original) {
		t.Errorf("expected original prompt body to be preserved, got:\n%s", got)
	}
	// Knowledge section must precede the original prompt body so the LLM reads
	// context before the task description.
	idxKB := strings.Index(got, "## Project Knowledge")
	idxOriginal := strings.Index(got, "## Original Prompt")
	if idxKB < 0 || idxOriginal < 0 || idxKB >= idxOriginal {
		t.Error("expected knowledge section to precede the original prompt body")
	}
}

func TestInjectKnowledgeSources_PassesProjectIDAndOpts(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &stubSourcesProvider{}
	gosources.SetProviderForTest(stub)

	o := &Orchestrator{projectID: "proj_xyz"}
	o.injectKnowledgeSources(context.Background(), "p", "the query text", 7)

	if stub.gotProj != "proj_xyz" {
		t.Errorf("projectID = %q, want %q", stub.gotProj, "proj_xyz")
	}
	if stub.gotQ != "the query text" {
		t.Errorf("query = %q, want %q", stub.gotQ, "the query text")
	}
	if stub.gotOpts.Limit != 7 {
		t.Errorf("Limit = %d, want 7", stub.gotOpts.Limit)
	}
	if stub.gotOpts.MinScore != knowledgeMinScore {
		t.Errorf("MinScore = %v, want %v", stub.gotOpts.MinScore, knowledgeMinScore)
	}
}

func TestInjectKnowledgeSources_ProviderErrorReturnsPromptUnchanged(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &stubSourcesProvider{err: errors.New("retrieval failed")}
	gosources.SetProviderForTest(stub)

	o := &Orchestrator{projectID: "proj_abc"}
	want := "original prompt body"
	got := o.injectKnowledgeSources(context.Background(), want, "q", 5)

	if got != want {
		t.Errorf("on provider error, prompt must be returned unchanged\nwant: %q\ngot:  %q", want, got)
	}
}

func TestInjectKnowledgeSources_EmptyChunksReturnsPromptUnchanged(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &stubSourcesProvider{chunks: []gosources.Chunk{}}
	gosources.SetProviderForTest(stub)

	o := &Orchestrator{projectID: "proj_abc"}
	want := "original prompt"
	got := o.injectKnowledgeSources(context.Background(), want, "q", 5)

	if got != want {
		t.Errorf("with empty chunks, prompt must be unchanged\nwant: %q\ngot:  %q", want, got)
	}
}

func TestInjectKnowledgeSources_EmptyQueryShortCircuits(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &stubSourcesProvider{chunks: []gosources.Chunk{{Text: "x"}}}
	gosources.SetProviderForTest(stub)

	o := &Orchestrator{projectID: "proj_abc"}
	want := "original prompt"
	got := o.injectKnowledgeSources(context.Background(), want, "", 5)

	if got != want {
		t.Errorf("empty query should skip retrieval and return prompt unchanged")
	}
	if stub.gotQ != "" {
		t.Error("provider should not have been called when query is empty")
	}
}

func TestInjectKnowledgeSources_ZeroTopKShortCircuits(t *testing.T) {
	gosources.ResetForTest()
	defer gosources.ResetForTest()

	stub := &stubSourcesProvider{chunks: []gosources.Chunk{{Text: "x"}}}
	gosources.SetProviderForTest(stub)

	o := &Orchestrator{projectID: "proj_abc"}
	want := "original prompt"
	got := o.injectKnowledgeSources(context.Background(), want, "q", 0)

	if got != want {
		t.Errorf("zero topK should skip retrieval and return prompt unchanged")
	}
	if stub.gotQ != "" {
		t.Error("provider should not have been called when topK is zero")
	}
}

func TestAreaNamesCSV(t *testing.T) {
	o := &Orchestrator{}
	got := o.areaNamesCSV([]AnalysisArea{
		{Name: "Churn"},
		{Name: "Retention"},
		{Name: "Monetization"},
	})
	want := "Churn, Retention, Monetization"
	if got != want {
		t.Errorf("areaNamesCSV = %q, want %q", got, want)
	}
}

func TestAreaNamesCSV_Empty(t *testing.T) {
	o := &Orchestrator{}
	if got := o.areaNamesCSV(nil); got != "" {
		t.Errorf("areaNamesCSV(nil) = %q, want empty", got)
	}
}

func TestRecommendationsKnowledgeQuery(t *testing.T) {
	o := &Orchestrator{}
	got := o.recommendationsKnowledgeQuery([]models.Insight{
		{Name: "High churn at level 45"},
		{Name: "Drop in D7 retention"},
	})
	want := "recommendations for: High churn at level 45, Drop in D7 retention"
	if got != want {
		t.Errorf("recommendationsKnowledgeQuery = %q, want %q", got, want)
	}
}

func TestRecommendationsKnowledgeQuery_Empty(t *testing.T) {
	o := &Orchestrator{}
	if got := o.recommendationsKnowledgeQuery(nil); got != "" {
		t.Errorf("recommendationsKnowledgeQuery(nil) = %q, want empty", got)
	}
}

func TestRecommendationsKnowledgeQuery_Truncates(t *testing.T) {
	o := &Orchestrator{}
	insights := make([]models.Insight, 0, 50)
	for i := 0; i < 50; i++ {
		insights = append(insights, models.Insight{Name: "Insight name that is moderately long"})
	}
	got := o.recommendationsKnowledgeQuery(insights)
	if len(got) > 200 {
		t.Errorf("recommendationsKnowledgeQuery should truncate to <=200 chars, got %d", len(got))
	}
}
