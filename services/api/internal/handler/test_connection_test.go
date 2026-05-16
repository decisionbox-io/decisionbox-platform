package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/embedding"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// runnerStub captures RunSync args so tests can assert the agent was
// invoked with the expected --test-connection target.
type runnerStub struct {
	mu      sync.Mutex
	calls   []runner.RunSyncOptions
	result  *runner.RunSyncResult
	syncErr error
}

func (s *runnerStub) Run(_ context.Context, _ runner.RunOptions) error                    { return nil }
func (s *runnerStub) Cancel(_ context.Context, _ string) error                            { return nil }
func (s *runnerStub) RunIndexSchema(_ context.Context, _ runner.IndexSchemaOptions) error { return nil }
func (s *runnerStub) RunSync(_ context.Context, opts runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, opts)
	if s.syncErr != nil {
		return s.result, s.syncErr
	}
	if s.result == nil {
		return &runner.RunSyncResult{Output: []byte(`{"success":true}`)}, nil
	}
	return s.result, nil
}

var _ runner.Runner = (*runnerStub)(nil)

func testProjectForConnTest() *models.Project {
	return &models.Project{ID: "proj-1", Name: "test", LLM: models.LLMConfig{Provider: "claude", Model: "claude-sonnet-4-6"}, Embedding: embedding.ProjectConfig{Provider: "openai", Model: "text-embedding-3-small"}}
}

func TestTestEmbedding_RoutesToAgent(t *testing.T) {
	repo := &stubProjectRepo{project: testProjectForConnTest()}
	r := &runnerStub{}
	h := NewTestConnectionHandler(repo, r)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/embedding", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.TestEmbedding(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(r.calls) != 1 {
		t.Fatalf("RunSync calls = %d, want 1", len(r.calls))
	}
	args := strings.Join(r.calls[0].Args, " ")
	if !strings.Contains(args, "--test-connection embedding") {
		t.Errorf("args = %q, want --test-connection embedding", args)
	}
	if r.calls[0].ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q, want proj-1", r.calls[0].ProjectID)
	}
}

func TestTestBlurbLLM_RoutesToAgent(t *testing.T) {
	repo := &stubProjectRepo{project: testProjectForConnTest()}
	r := &runnerStub{}
	h := NewTestConnectionHandler(repo, r)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/blurb-llm", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.TestBlurbLLM(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(r.calls) != 1 {
		t.Fatalf("RunSync calls = %d, want 1", len(r.calls))
	}
	args := strings.Join(r.calls[0].Args, " ")
	if !strings.Contains(args, "--test-connection blurb-llm") {
		t.Errorf("args = %q, want --test-connection blurb-llm", args)
	}
}

func TestTestEmbedding_ProjectNotFound(t *testing.T) {
	repo := &stubProjectRepo{project: nil}
	r := &runnerStub{}
	h := NewTestConnectionHandler(repo, r)

	req := httptest.NewRequest("POST", "/api/v1/projects/missing/test/embedding", nil)
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.TestEmbedding(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestTestBlurbLLM_ProjectNotFound(t *testing.T) {
	repo := &stubProjectRepo{project: nil}
	r := &runnerStub{}
	h := NewTestConnectionHandler(repo, r)

	req := httptest.NewRequest("POST", "/api/v1/projects/missing/test/blurb-llm", nil)
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.TestBlurbLLM(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestTestEmbedding_AgentReportsFailure(t *testing.T) {
	// Agent failure path: runner returns (result-with-stdout, error). The
	// handler parses the embedded JSON failure object out of stdout
	// and surfaces it to the client as 200 + {success:false,error:...}
	// — matches the agent-error path that TestLLM already exercises in
	// production today.
	repo := &stubProjectRepo{project: testProjectForConnTest()}
	r := &runnerStub{
		result:  &runner.RunSyncResult{Output: []byte(`{"success":false,"error":"embedding test failed: bad key"}`)},
		syncErr: assertableError("agent exited 1"),
	}
	h := NewTestConnectionHandler(repo, r)

	req := httptest.NewRequest("POST", "/api/v1/projects/proj-1/test/embedding", nil)
	req.SetPathValue("id", "proj-1")
	w := httptest.NewRecorder()
	h.TestEmbedding(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (agent failures surface in JSON body)", w.Code)
	}
	var wrapped struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapped); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if wrapped.Data["success"] != false {
		t.Errorf("success = %v, want false", wrapped.Data["success"])
	}
}

type assertableError string

func (e assertableError) Error() string { return string(e) }
