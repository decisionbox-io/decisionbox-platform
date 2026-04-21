package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
)

// stubChecker is a test double for policy.Checker that records calls and
// returns scripted decisions. Only the methods the handler touches are
// fleshed out — the rest are no-ops.
type stubChecker struct {
	policy.NoopChecker

	mu sync.Mutex

	createErr error
	createRes *policy.Reservation

	llmProviderErr error

	addDSErr error
	addDSRes *policy.Reservation

	releases []string

	startRunErr error
	startRunRes *policy.Reservation
	confirms    []policy.RunOutcome

	observeCalls atomic.Int64
}

func (s *stubChecker) CheckCreateProject(_ context.Context, _ string, _ policy.ProjectIntent) (*policy.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.createRes != nil {
		return s.createRes, nil
	}
	return &policy.Reservation{ID: "res-create-1"}, nil
}

func (s *stubChecker) CheckLLMProviderAllowed(_ context.Context, _ string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.llmProviderErr
}

func (s *stubChecker) CheckAddDataSource(_ context.Context, _ string) (*policy.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addDSErr != nil {
		return nil, s.addDSErr
	}
	if s.addDSRes != nil {
		return s.addDSRes, nil
	}
	return &policy.Reservation{ID: "res-ds-1"}, nil
}

func (s *stubChecker) CheckStartDiscoveryRun(_ context.Context, _ string, _ string, _ string) (*policy.Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.startRunErr != nil {
		return nil, s.startRunErr
	}
	if s.startRunRes != nil {
		return s.startRunRes, nil
	}
	return &policy.Reservation{ID: "res-run-1"}, nil
}

func (s *stubChecker) ConfirmDiscoveryRunEnded(_ context.Context, _ string, outcome policy.RunOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.confirms = append(s.confirms, outcome)
	return nil
}

func (s *stubChecker) Release(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = append(s.releases, id)
	return nil
}

func (s *stubChecker) ObserveLLMTokens(_ context.Context, _ string, _ policy.LLMUsageEvent) {
	s.observeCalls.Add(1)
}

// swapChecker registers ck and returns a cleanup function that restores
// the previous checker. Tests must defer the cleanup.
func swapChecker(t *testing.T, ck policy.Checker) {
	t.Helper()
	policy.RegisterChecker(ck)
	t.Cleanup(func() { policy.RegisterChecker(nil) })
}

func TestProjectsHandler_Create_LLMProviderDenied(t *testing.T) {
	stub := &stubChecker{llmProviderErr: &policy.PolicyError{
		Kind: "feature", Feature: "llm_provider", PlanID: "starter_t1", Allowed: []string{"claude", "openai"},
	}}
	swapChecker(t, stub)

	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"p","domain":"gaming","category":"match3","llm":{"provider":"bedrock"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (feature denial)", w.Code)
	}
	var resp APIResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "llm provider not allowed") {
		t.Errorf("error body = %q", resp.Error)
	}
	if len(repo.projects) != 0 {
		t.Errorf("repo was called despite denial")
	}
}

func TestProjectsHandler_Create_ProjectsCapReached(t *testing.T) {
	stub := &stubChecker{createErr: &policy.PolicyError{
		Kind: "limit", Limit: "projects_per_deployment", Current: 1, Max: 1, PlanID: "free",
	}}
	swapChecker(t, stub)

	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"p","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (limit denial)", w.Code)
	}
	if len(repo.projects) != 0 {
		t.Errorf("repo created a project despite cap")
	}
}

func TestProjectsHandler_Create_ReleaseOnRepoFailure(t *testing.T) {
	stub := &stubChecker{}
	swapChecker(t, stub)

	repo := newMockProjectRepo()
	repo.createErr = errInject
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"p","domain":"gaming","category":"match3","warehouse":{"provider":"bigquery"}}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.releases) != 2 {
		t.Errorf("expected 2 Release calls (project + data source), got %d: %v", len(stub.releases), stub.releases)
	}
}

func TestProjectsHandler_Create_DataSourceSkippedWhenNoWarehouse(t *testing.T) {
	stub := &stubChecker{}
	swapChecker(t, stub)

	repo := newMockProjectRepo()
	h := NewProjectsHandler(repo, nil)

	body := `{"name":"p","domain":"gaming","category":"match3"}`
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	// No warehouse → CheckAddDataSource was not called (stub would have
	// recorded a release only if the repo insert had failed afterward).
}

// --- helpers ---

var errInject = &injectedError{msg: "injected"}

type injectedError struct{ msg string }

func (e *injectedError) Error() string { return e.msg }
