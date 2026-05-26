package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/packgen"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// newTestHandler constructs a PackGenerateHandler with throwaway
// background context and returns the WaitGroup the handler will track
// pack-gen goroutines on. Tests that exercise the async path call
// awaitGoroutine on the returned wg.
func newTestHandler(repo database.ProjectRepo) (*PackGenerateHandler, *sync.WaitGroup) {
	wg := &sync.WaitGroup{}
	return NewPackGenerateHandler(repo, context.Background(), wg), wg
}

// stubPackgenProvider lets tests script Generate / RegenerateSection
// outcomes without spinning up the enterprise plugin. Generate runs in
// a goroutine after the new async handler returns, so the stub exposes
// a `done` channel tests can wait on.
type stubPackgenProvider struct {
	mu sync.Mutex

	genResult *packgen.GenerateResult
	genErr    error

	regResult *packgen.RegenerateSectionResult
	regErr    error

	lastGen packgen.GenerateRequest
	lastReg packgen.RegenerateSectionRequest

	// done fires once after Generate returns. Tests that want to assert
	// goroutine completion read this channel with a generous timeout.
	done chan struct{}

	// block, when non-nil, halts Generate until the channel is closed —
	// used to assert the handler responds 202 BEFORE the orchestrator
	// finishes (the actual async-handoff property).
	block chan struct{}

	// panic, when non-empty, makes Generate panic with this string so
	// the panic-recovery test exercises the handler's defer.
	panicMsg string

	// captureCtx, when true, copies the inbound ctx into lastCtx so
	// tests can assert the goroutine got serverBackgroundCtx (not
	// r.Context()).
	captureCtx bool
	lastCtx    context.Context
}

func (s *stubPackgenProvider) Generate(ctx context.Context, req packgen.GenerateRequest) (*packgen.GenerateResult, error) {
	s.mu.Lock()
	s.lastGen = req
	if s.captureCtx {
		s.lastCtx = ctx
	}
	block := s.block
	done := s.done
	panicMsg := s.panicMsg
	s.mu.Unlock()

	defer func() {
		if done != nil {
			close(done)
		}
	}()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if panicMsg != "" {
		panic(panicMsg)
	}
	return s.genResult, s.genErr
}

func (s *stubPackgenProvider) RegenerateSection(_ context.Context, req packgen.RegenerateSectionRequest) (*packgen.RegenerateSectionResult, error) {
	s.mu.Lock()
	s.lastReg = req
	s.mu.Unlock()
	return s.regResult, s.regErr
}

// awaitDone waits up to the given duration for stub.done to close. Use
// when the test cares about the moment the stub's Generate returns
// (e.g., asserting the handler responded BEFORE the stub finished) —
// NOT to be confused with awaitGoroutine, which waits for the handler's
// outer defer to also run.
func awaitDone(t *testing.T, stub *stubPackgenProvider, d time.Duration) {
	t.Helper()
	select {
	case <-stub.done:
	case <-time.After(d):
		t.Fatalf("stub provider Generate did not return within %s", d)
	}
}

// awaitGoroutine waits up to the given duration for the handler's
// pack-gen goroutine to finish AND for its outer defer (panic recovery
// + FinalizePackGenIfStuck safety net) to run. Use after issuing a 202.
func awaitGoroutine(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("pack-gen goroutine did not complete within %s", d)
	}
}

// installStubProvider swaps in a stub Provider for the test and restores
// the registry on cleanup. Tests that want the no-op (community) build
// can simply skip calling this helper.
func installStubProvider(t *testing.T, p packgen.Provider) {
	t.Helper()
	packgen.ResetForTest()
	packgen.SetProviderForTest(p)
	t.Cleanup(packgen.ResetForTest)
}

// resetPackgenForTest ensures the registry is in the no-op state.
func resetPackgenForTest(t *testing.T) {
	t.Helper()
	packgen.ResetForTest()
	t.Cleanup(packgen.ResetForTest)
}

// --- Generate ---

func TestPackGenerate_Generate_NoProviderConfigured_404(t *testing.T) {
	resetPackgenForTest(t)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-1", State: models.ProjectStatePackGenerationPending}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_MissingProjectID_400(t *testing.T) {
	resetPackgenForTest(t)
	installStubProvider(t, &stubPackgenProvider{})

	h, _ := newTestHandler(newMockProjectRepo())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects//pack-generate", nil)
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_ProjectNotFound_404(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	h, _ := newTestHandler(newMockProjectRepo())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/missing/pack-generate", nil)
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_WrongState_409(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-2", State: models.ProjectStateReady}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Generate_NoGeneratePackPayload_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-3", State: models.ProjectStatePackGenerationPending}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// seedPendingPackGen creates a project ready for an async pack-gen
// enqueue: state=pack_generation_pending, generate_pack.enabled=true,
// no existing marker.
func seedPendingPackGen(t *testing.T, repo *mockProjectRepo, id, slug, desc string) *models.Project {
	t.Helper()
	p := &models.Project{
		ID:    id,
		State: models.ProjectStatePackGenerationPending,
		GeneratePack: &models.GeneratePackConfig{
			Enabled:     true,
			PackName:    slug + "-name",
			PackSlug:    slug,
			Description: desc,
		},
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Create resets ID to "proj-N"; use the returned ID for the caller.
	return p
}

// doGenerate runs the async Generate handler and waits for the
// background goroutine's full lifecycle (provider call + handler's
// safety-net defer) to complete via the handler's WaitGroup. Returns
// the response code + body for the test to assert on.
func doGenerate(t *testing.T, h *PackGenerateHandler, wg *sync.WaitGroup, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+projectID+"/pack-generate", nil)
	req.SetPathValue("id", projectID)
	w := httptest.NewRecorder()
	h.Generate(w, req)
	if wg != nil {
		awaitGoroutine(t, wg, 2*time.Second)
	}
	return w
}

// TestPackGenerate_Generate_AsyncEnqueue covers the happy path: handler
// returns 202 with a server-generated run_id, marker is written before
// the response, and the goroutine receives the project's generate-pack
// payload verbatim.
func TestPackGenerate_Generate_AsyncEnqueue_202(t *testing.T) {
	stub := &stubPackgenProvider{
		genResult: &packgen.GenerateResult{Async: true},
		done:      make(chan struct{}),
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-async", "acme", "match-3 puzzle")

	h, hwg := newTestHandler(repo)
	w := doGenerate(t, h, hwg, p.ID)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	var wrapper struct {
		Data generateResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp := wrapper.Data
	if resp.RunID == "" {
		t.Error("response must carry a non-empty run_id")
	}
	if !resp.Async {
		t.Error("response.async must be true")
	}
	if stub.lastGen.PackSlug != "acme" {
		t.Errorf("provider received PackSlug=%q, want acme", stub.lastGen.PackSlug)
	}
	if stub.lastGen.Description != "match-3 puzzle" {
		t.Errorf("provider received Description=%q", stub.lastGen.Description)
	}
	if stub.lastGen.RunID != resp.RunID {
		t.Errorf("provider received RunID=%q, handler returned %q (must match)", stub.lastGen.RunID, resp.RunID)
	}

	// Stub returned cleanly (Async:true, nil err) — the handler's
	// safety-net defer trusts the provider's contract and does NOT
	// fire. The marker stays set; a real provider would have written
	// its own terminal state (done + $unset marker) before returning.
	got, _ := repo.GetByID(context.Background(), p.ID)
	if got.PackGenRequest == nil {
		t.Error("marker should remain set after a clean provider return — the provider owns its own terminal-state cleanup; the safety-net defer must NOT clear the marker on success")
	}
	if got.PackGenLastError != "" {
		t.Errorf("pack_gen_last_error = %q, want empty after a clean provider return (defer must not have run)", got.PackGenLastError)
	}
}

// TestPackGenerate_Generate_AsyncEnqueue_AtomicMarkerCleared covers the
// retry-after-failure flow: when the project already has a
// pack_gen_last_error from a prior run, enqueue must clear it in the
// same atomic update that writes the new marker.
func TestPackGenerate_Generate_AsyncEnqueue_ClearsPriorError(t *testing.T) {
	stub := &stubPackgenProvider{
		genResult: &packgen.GenerateResult{Async: true},
		done:      make(chan struct{}),
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-retry", "acme", "")
	// Set a prior failure error.
	stored := repo.projects[p.ID]
	stored.PackGenLastError = "synthesise pack: llm timeout"

	h, hwg := newTestHandler(repo)
	w := doGenerate(t, h, hwg, p.ID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	// The mock's EnqueuePackGen clears PackGenLastError on the same
	// in-memory write that sets the marker. Re-read and confirm.
	// (The safety-net defer runs AFTER and resets state back to
	// pending — but it writes "generation interrupted; please retry"
	// as the new error, not the prior one.)
	got, _ := repo.GetByID(context.Background(), p.ID)
	if got.PackGenLastError == "synthesise pack: llm timeout" {
		t.Errorf("prior error %q should have been cleared by atomic enqueue", got.PackGenLastError)
	}
}

// TestPackGenerate_Generate_IdempotentMarkerSet_Pending covers the
// duplicate-POST case while a previous run is still in-flight (state
// has not yet flipped to pack_generation).
func TestPackGenerate_Generate_IdempotentMarkerSet_Pending(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-idem-pending", "acme", "")
	// Manually set the marker (simulating a previous in-flight run).
	stored := repo.projects[p.ID]
	stored.PackGenRequest = &models.PackGenRequest{
		RunID:       "20260526T120000.000Z",
		RequestedAt: time.Now(),
	}

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	var wrapper struct {
		Data generateResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp := wrapper.Data
	if resp.RunID != "20260526T120000.000Z" {
		t.Errorf("idempotent response should return existing run_id; got %q", resp.RunID)
	}
}

// TestPackGenerate_Generate_IdempotentMarkerSet_InGeneration covers the
// duplicate-POST case after the orchestrator has flipped state to
// pack_generation but before the terminal write. Per Codex review: this
// MUST return 202, not 409.
func TestPackGenerate_Generate_IdempotentMarkerSet_InGeneration(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-idem-running",
		State: models.ProjectStatePackGeneration,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
		PackGenRequest: &models.PackGenRequest{
			RunID:       "20260526T120000.000Z",
			RequestedAt: time.Now(),
		},
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (marker present, state=pack_generation must return idempotent 202 NOT 409); body = %s", w.Code, w.Body.String())
	}
	var wrapper struct {
		Data generateResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &wrapper); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp := wrapper.Data
	if resp.RunID != "20260526T120000.000Z" {
		t.Errorf("response run_id = %q, want existing marker run_id", resp.RunID)
	}
}

// TestPackGenerate_Generate_IdempotentMarkerSet_Done covers the
// stale-marker edge case: orchestrator already wrote terminal state but
// somehow didn't unset the marker. Per Codex round 2: this must return
// 409 ("already complete"), not 202.
func TestPackGenerate_Generate_IdempotentMarkerSet_Done_409(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-stale-done",
		State: models.ProjectStatePackGenerationDone,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
		PackGenRequest: &models.PackGenRequest{
			RunID:       "20260526T120000.000Z",
			RequestedAt: time.Now(),
		},
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (marker present + state=done = stale-marker case); body = %s", w.Code, w.Body.String())
	}
}

// TestPackGenerate_Generate_NoMarker_StateInGeneration_500 covers the
// inconsistent-state case the runbook documents: a previous run died
// without writing a terminal state AND without leaving the marker. The
// handler refuses to enqueue (it would shadow whatever happened) and
// surfaces 500 so the operator notices.
func TestPackGenerate_Generate_NoMarker_StateInGeneration_500(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-inconsistent",
		State: models.ProjectStatePackGeneration,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
		// No marker — inconsistent.
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (inconsistent state); body = %s", w.Code, w.Body.String())
	}
}

// TestPackGenerate_Generate_NoMarker_StateDone_409 covers the normal
// "you already finished this; nothing to generate" case.
func TestPackGenerate_Generate_NoMarker_StateDone_409(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{
		ID:    "p-done",
		State: models.ProjectStatePackGenerationDone,
		GeneratePack: &models.GeneratePackConfig{Enabled: true, PackName: "Acme", PackSlug: "acme"},
	}
	if err := repo.Create(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

// TestPackGenerate_Generate_HandlerReturnsBeforeProviderUnblocks asserts
// the async-handoff property: the handler MUST respond 202 while the
// stubbed provider is still blocked. If this test ever sees the
// provider's "done" signal before the handler's response, the handler
// has regressed to synchronous behavior.
func TestPackGenerate_Generate_HandlerReturnsBeforeProviderUnblocks(t *testing.T) {
	stub := &stubPackgenProvider{
		genResult: &packgen.GenerateResult{Async: true},
		done:      make(chan struct{}),
		block:     make(chan struct{}),
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-blocking", "acme", "")

	h, _ := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()

	// Channel the handler's return.
	handlerReturned := make(chan struct{})
	go func() {
		h.Generate(w, req)
		close(handlerReturned)
	}()

	select {
	case <-handlerReturned:
		// Good — handler returned even though stub is still blocked.
	case <-stub.done:
		t.Fatal("stub provider finished before handler returned — handler is synchronous")
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return within 2 s (likely blocked on the provider)")
	}

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}

	// Release the stub so the goroutine completes before the test exits.
	close(stub.block)
	awaitDone(t, stub, 2*time.Second)
}

// TestPackGenerate_Generate_GoroutineSurvivesRequestCtxCancel asserts
// that canceling the inbound request's context does NOT abort the
// spawned goroutine. The goroutine must run on serverBackgroundCtx,
// not r.Context().
func TestPackGenerate_Generate_GoroutineSurvivesRequestCtxCancel(t *testing.T) {
	stub := &stubPackgenProvider{
		genResult: &packgen.GenerateResult{Async: true},
		done:      make(chan struct{}),
		block:     make(chan struct{}),
		captureCtx: true,
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-survival", "acme", "")

	serverCtx, serverCancel := context.WithCancel(context.Background())
	defer serverCancel()
	h := NewPackGenerateHandler(repo, serverCtx, &sync.WaitGroup{})

	reqCtx, reqCancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil).WithContext(reqCtx)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	// Cancel the inbound request. The goroutine must keep running.
	reqCancel()
	// Give the cancel time to propagate (no-op for the goroutine).
	time.Sleep(50 * time.Millisecond)

	// Release the stub. Now the goroutine should complete.
	close(stub.block)
	awaitDone(t, stub, 2*time.Second)

	// The stub captured the ctx it received. That ctx MUST not be done
	// (we only canceled reqCtx, not serverCtx).
	if stub.lastCtx == nil {
		t.Fatal("stub did not capture ctx")
	}
	if err := stub.lastCtx.Err(); err != nil {
		t.Errorf("provider ctx was canceled (%v) when only the request ctx should have been — handler is using r.Context() instead of serverBackgroundCtx", err)
	}
}

// TestPackGenerate_Generate_PanicRecovery asserts that a panicking
// provider is recovered, logged, and the safety-net defer cleans up the
// marker so the wizard can retry.
func TestPackGenerate_Generate_PanicRecovery(t *testing.T) {
	stub := &stubPackgenProvider{
		done:     make(chan struct{}),
		panicMsg: "deliberate test panic",
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-panic", "acme", "")

	h, hwg := newTestHandler(repo)
	w := doGenerate(t, h, hwg, p.ID)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	// Safety-net defer ran — marker is cleared, state is back to pending,
	// error message is the canned "generation interrupted; please retry".
	got, _ := repo.GetByID(context.Background(), p.ID)
	if got.PackGenRequest != nil {
		t.Error("marker should be cleared after panic")
	}
	if got.EffectiveState() != models.ProjectStatePackGenerationPending {
		t.Errorf("state = %q, want pack_generation_pending", got.State)
	}
	if !strings.Contains(got.PackGenLastError, "interrupted") {
		t.Errorf("pack_gen_last_error = %q, expected to contain 'interrupted'", got.PackGenLastError)
	}
}

// TestPackGenerate_Generate_SafetyNetNoOpWhenOrchestratorWroteDone
// asserts the defer's conditional UpdateOne is a no-op when the
// orchestrator already wrote state=pack_generation_done. The defer must
// not overwrite a clean terminal state.
func TestPackGenerate_Generate_SafetyNetNoOpWhenOrchestratorWroteDone(t *testing.T) {
	// Stub's Generate writes the terminal state into Mongo (simulating
	// the orchestrator) before returning. The handler's defer should
	// then see state=done + no marker and no-op.
	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-done-clean", "acme", "")

	stub := &stubPackgenProvider{done: make(chan struct{})}
	stub.genResult = &packgen.GenerateResult{Async: true}
	// We can't easily make the stub mutate Mongo from inside Generate
	// because the stub is invoked from the goroutine; instead we use
	// the mock repo's seam: after enqueue but BEFORE the goroutine
	// runs, set the project state to done + unset marker, simulating
	// the orchestrator's success terminal write completing while the
	// goroutine returns nil.
	stub.block = make(chan struct{})
	installStubProvider(t, stub)

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	// Simulate the orchestrator's success write landing while the
	// goroutine is blocked.
	repo.mu.Lock()
	stored := repo.projects[p.ID]
	stored.State = models.ProjectStatePackGenerationDone
	stored.PackGenRequest = nil
	repo.mu.Unlock()

	// Release stub so the goroutine completes and the defer fires.
	close(stub.block)
	awaitDone(t, stub, 2*time.Second)

	// Defer's FinalizePackGenIfStuck predicate misses (state=done,
	// marker absent). State stays done.
	got, _ := repo.GetByID(context.Background(), p.ID)
	if got.EffectiveState() != models.ProjectStatePackGenerationDone {
		t.Errorf("state = %q after defer ran, want pack_generation_done (defer must not overwrite a clean done)", got.State)
	}
}

// TestPackGenerate_Generate_EnqueueRepoError_500 covers the
// transient-state-churn path — EnqueuePackGen returns a non-typed error
// (Mongo write failure).
func TestPackGenerate_Generate_EnqueueRepoError_500(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-enq-err", "acme", "")
	repo.enqueuePackGenErr = errors.New("mongo: write conflict")

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

// TestPackGenerate_Generate_EnqueueSurvivesRequestCtxCancel asserts the
// enqueue write runs on a detached context — a client disconnect
// during the Mongo round-trip must not leave a marker landed without
// the goroutine spawning.
func TestPackGenerate_Generate_EnqueueSurvivesRequestCtxCancel(t *testing.T) {
	stub := &stubPackgenProvider{
		genResult: &packgen.GenerateResult{Async: true},
		done:      make(chan struct{}),
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-enq-ctx", "acme", "")

	h, hwg := newTestHandler(repo)

	// Build a request whose context is ALREADY canceled — simulates a
	// client that disconnected before the handler ran the enqueue.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	reqCancel() // cancel immediately

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate", nil).WithContext(reqCtx)
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.Generate(w, req)

	// The handler hit the project-load with r.Context(), which is canceled.
	// The mock returns nil for canceled-ctx (it doesn't honor ctx), so
	// the test exercises the post-validation enqueue path. Real Mongo
	// would respect the canceled ctx and return context.Canceled —
	// the handler now uses a detached enqueue ctx, so the enqueue
	// must still succeed and the goroutine must spawn.
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (enqueue must survive request-ctx cancel); body = %s", w.Code, w.Body.String())
	}
	awaitGoroutine(t, hwg, 2*time.Second)
}

// TestPackGenerate_Generate_SafetyNetFiresOnProviderError asserts the
// safety-net defer DOES fire when the provider returns an error. This
// covers the case where the orchestrator's own revert-on-error closure
// failed (e.g. its detached-ctx Mongo write timed out) — the marker
// would otherwise leak. The orchestrator-revert-succeeded case is
// covered by SafetyNetNoOpWhenOrchestratorWroteDone above.
func TestPackGenerate_Generate_SafetyNetFiresOnProviderError(t *testing.T) {
	stub := &stubPackgenProvider{
		genErr: errors.New("simulated LLM failure"),
		done:   make(chan struct{}),
	}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := seedPendingPackGen(t, repo, "p-prov-err", "acme", "")

	h, hwg := newTestHandler(repo)
	w := doGenerate(t, h, hwg, p.ID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	got, _ := repo.GetByID(context.Background(), p.ID)
	if got.PackGenRequest != nil {
		t.Error("marker should be cleared by safety-net defer after provider error")
	}
	if got.EffectiveState() != models.ProjectStatePackGenerationPending {
		t.Errorf("state = %q, want pack_generation_pending", got.State)
	}
	if !strings.Contains(got.PackGenLastError, "interrupted") {
		t.Errorf("pack_gen_last_error = %q, want canned 'generation interrupted; please retry'", got.PackGenLastError)
	}
}

func TestPackGenerate_Generate_RepoError_500(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	repo.getErr = errors.New("mongo: connection refused")

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p-x/pack-generate", nil)
	req.SetPathValue("id", "p-x")
	w := httptest.NewRecorder()
	h.Generate(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

// --- RegenerateSection ---

func TestPackGenerate_Regenerate_NoProviderConfigured_404(t *testing.T) {
	resetPackgenForTest(t)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r1", State: models.ProjectStatePackGenerationDone, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	body := `{"section":"categories","feedback":"more retention"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_BadJSON_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	h, _ := newTestHandler(newMockProjectRepo())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/pack-generate/regenerate", strings.NewReader("not json"))
	req.SetPathValue("id", "p")
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_MissingFields_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	h, _ := newTestHandler(newMockProjectRepo())

	cases := []string{
		`{"section":"","feedback":"x"}`,
		`{"section":"x","feedback":""}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p/pack-generate/regenerate", strings.NewReader(body))
		req.SetPathValue("id", "p")
		w := httptest.NewRecorder()
		h.RegenerateSection(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%s: status = %d, want 400", body, w.Code)
		}
	}
}

func TestPackGenerate_Regenerate_WrongState_409(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r2", State: models.ProjectStateReady, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	body := `{"section":"categories","feedback":"more retention"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_Success(t *testing.T) {
	stub := &stubPackgenProvider{regResult: &packgen.RegenerateSectionResult{PackSlug: "acme", Section: "categories", Attempts: 1}}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r3", State: models.ProjectStatePackGenerationDone, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	body := `{"section":"categories","feedback":"more retention focus"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if stub.lastReg.Feedback != "more retention focus" {
		t.Errorf("provider received Feedback=%q", stub.lastReg.Feedback)
	}
	if stub.lastReg.PackSlug != "acme" {
		t.Errorf("provider received PackSlug=%q (should be project.Domain)", stub.lastReg.PackSlug)
	}
}

func TestPackGenerate_Regenerate_MissingProjectID_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	h, _ := newTestHandler(newMockProjectRepo())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects//pack-generate/regenerate", strings.NewReader(`{"section":"x","feedback":"y"}`))
	req.SetPathValue("id", "")
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_RepoError_500(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	repo := newMockProjectRepo()
	repo.getErr = errors.New("mongo: connection refused")
	h, _ := newTestHandler(repo)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/p-x/pack-generate/regenerate", strings.NewReader(`{"section":"categories","feedback":"y"}`))
	req.SetPathValue("id", "p-x")
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_ProjectNotFound_404(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})
	h, _ := newTestHandler(newMockProjectRepo())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/missing/pack-generate/regenerate", strings.NewReader(`{"section":"categories","feedback":"y"}`))
	req.SetPathValue("id", "missing")
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_ProviderErrNotConfigured_404(t *testing.T) {
	stub := &stubPackgenProvider{regErr: packgen.ErrNotConfigured}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r5", State: models.ProjectStatePackGenerationDone, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(`{"section":"categories","feedback":"y"}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_ProviderError_500(t *testing.T) {
	stub := &stubPackgenProvider{regErr: errors.New("LLM unavailable")}
	installStubProvider(t, stub)

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r6", State: models.ProjectStatePackGenerationDone, Domain: "acme"}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(`{"section":"categories","feedback":"y"}`))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

func TestPackGenerate_Regenerate_NoPackOnProject_400(t *testing.T) {
	installStubProvider(t, &stubPackgenProvider{})

	repo := newMockProjectRepo()
	p := &models.Project{ID: "p-r4", State: models.ProjectStatePackGenerationDone, Domain: ""}
	_ = repo.Create(context.Background(), p)

	h, _ := newTestHandler(repo)
	body := `{"section":"categories","feedback":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+p.ID+"/pack-generate/regenerate", strings.NewReader(body))
	req.SetPathValue("id", p.ID)
	w := httptest.NewRecorder()
	h.RegenerateSection(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

// --- Project create — wizard mode ---

func TestProjectsCreate_WizardMode_AcceptsEmptyDomain(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name": "Acme Project",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "acme-gaming",
		},
		"llm": map[string]string{"provider": "claude", "model": "claude-sonnet"},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data models.Project `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	created := resp.Data
	if created.State != models.ProjectStatePackGenerationPending {
		t.Errorf("State = %q, want %q", created.State, models.ProjectStatePackGenerationPending)
	}
	if created.Domain != "" {
		t.Errorf("wizard projects must have empty Domain on create, got %q", created.Domain)
	}
	// Wizard projects must not be enqueued for schema indexing: the
	// handler skips that path because the wizard hasn't supplied a
	// warehouse yet. (The mock repo defaults SchemaIndexStatus to
	// "ready" on insert; the handler-driven status would be
	// "pending_indexing" — assert the handler did not set that.)
	if created.SchemaIndexStatus == models.SchemaIndexStatusPendingIndexing {
		t.Errorf("wizard projects should NOT be enqueued for indexing; got %q", created.SchemaIndexStatus)
	}
	if created.GeneratePack == nil || !created.GeneratePack.Enabled {
		t.Error("GeneratePack should be carried through to the persisted document")
	}
}

func TestProjectsCreate_WizardMode_RejectsInvalidSlug(t *testing.T) {
	swapChecker(t, &stubChecker{})
	h := NewProjectsHandler(newMockProjectRepo(), newMockDomainPackRepo())

	body := map[string]interface{}{
		"name": "Acme",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "Bad Slug!", // not slug-shaped
		},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
}

func TestProjectsCreate_WizardMode_SlugCollisionConflict(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	// Pre-seed a pack with the slug the wizard wants.
	_ = domainPacks.Create(context.Background(), &models.DomainPack{Slug: "acme-gaming", Name: "Existing"})
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name": "Acme",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "acme-gaming",
		},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

func TestProjectsCreate_WizardMode_RequiresNameAndSlug(t *testing.T) {
	swapChecker(t, &stubChecker{})
	h := NewProjectsHandler(newMockProjectRepo(), newMockDomainPackRepo())

	cases := []map[string]interface{}{
		{"enabled": true, "pack_slug": "x"},                    // missing pack_name
		{"enabled": true, "pack_name": "X"},                    // missing pack_slug
		{"enabled": true, "pack_name": "X", "pack_slug": "a"},  // slug too short
	}
	for i, gp := range cases {
		body := map[string]interface{}{"name": "P", "generate_pack": gp}
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
		w := httptest.NewRecorder()
		h.Create(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400; body = %s", i, w.Code, w.Body.String())
		}
	}
}

func TestProjectsCreate_WizardMode_GetBySlugError_500(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	domainPacks.getErr = errors.New("mongo: connection refused")
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name": "Acme",
		"generate_pack": map[string]interface{}{
			"enabled":   true,
			"pack_name": "Acme Gaming",
			"pack_slug": "acme-gaming",
		},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

func TestProjectsCreate_NonWizard_DefaultsStateToReady(t *testing.T) {
	swapChecker(t, &stubChecker{})
	repo := newMockProjectRepo()
	domainPacks := newMockDomainPackRepo()
	_ = domainPacks.Create(context.Background(), &models.DomainPack{
		Slug: "gaming",
		Name: "Gaming",
		Categories: []models.PackCategory{{ID: "match-3", Name: "Match-3"}},
		Prompts: models.PackPrompts{Base: models.BasePrompts{
			Exploration:     "explore {{PROFILE}}",
			Recommendations: "recommend",
			BaseContext:     "{{PROFILE}} {{PREVIOUS_CONTEXT}}",
		}},
	})
	h := NewProjectsHandler(repo, domainPacks)

	body := map[string]interface{}{
		"name":     "Regular Project",
		"domain":   "gaming",
		"category": "match-3",
		"llm":      map[string]string{"provider": "claude", "model": "claude-sonnet"},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	h.Create(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data models.Project `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.State != models.ProjectStateReady {
		t.Errorf("State = %q, want %q", resp.Data.State, models.ProjectStateReady)
	}
}
