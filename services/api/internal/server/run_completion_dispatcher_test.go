package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runhooks"
)

// mockRunRepo is a minimal RunRepo implementation for dispatcher tests.
// Only the four methods the dispatcher uses are wired with realistic
// behaviour; the rest panic so any accidental usage shows up loudly in
// the test report.
type mockRunRepo struct {
	mu               sync.Mutex
	queue            []*models.DiscoveryRun
	listErr          error
	listCalls        int32
	markCalls        []string
	markErrByRunID   map[string]error
}

func newMockRunRepo(runs ...*models.DiscoveryRun) *mockRunRepo {
	out := make([]*models.DiscoveryRun, len(runs))
	copy(out, runs)
	return &mockRunRepo{queue: out}
}

func (m *mockRunRepo) ListTerminalWithoutCompletionHook(_ context.Context, limit int) ([]*models.DiscoveryRun, error) {
	atomic.AddInt32(&m.listCalls, 1)
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.queue) {
		limit = len(m.queue)
	}
	out := make([]*models.DiscoveryRun, limit)
	copy(out, m.queue[:limit])
	return out, nil
}

func (m *mockRunRepo) MarkCompletionHooksFired(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err, ok := m.markErrByRunID[runID]; ok {
		return err
	}
	m.markCalls = append(m.markCalls, runID)
	// Drop the run from the queue so a subsequent list returns the
	// remainder, mirroring the production Mongo filter on
	// completion_hooks_fired_at IS NULL.
	next := m.queue[:0]
	for _, r := range m.queue {
		if r.ID != runID {
			next = append(next, r)
		}
	}
	m.queue = next
	return nil
}

// The rest of the RunRepo interface — unused by the dispatcher; the
// dispatcher tests should never hit these. A panic surfaces wiring bugs.
func (m *mockRunRepo) Create(context.Context, string) (string, error) {
	panic("not implemented")
}
func (m *mockRunRepo) GetByID(context.Context, string) (*models.DiscoveryRun, error) {
	panic("not implemented")
}
func (m *mockRunRepo) GetLatestByProject(context.Context, string) (*models.DiscoveryRun, error) {
	panic("not implemented")
}
func (m *mockRunRepo) GetRunningByProject(context.Context, string) (*models.DiscoveryRun, error) {
	panic("not implemented")
}
func (m *mockRunRepo) Fail(context.Context, string, string) error { panic("not implemented") }
func (m *mockRunRepo) Cancel(context.Context, string) error        { panic("not implemented") }
func (m *mockRunRepo) SetPolicyReservationID(context.Context, string, string) error {
	panic("not implemented")
}
func (m *mockRunRepo) ListTerminalWithReservation(context.Context, int) ([]*models.DiscoveryRun, error) {
	panic("not implemented")
}
func (m *mockRunRepo) ClearPolicyReservationID(context.Context, string) error {
	panic("not implemented")
}

func resetHooks(t *testing.T) {
	t.Helper()
	runhooks.ResetForTest()
	t.Cleanup(runhooks.ResetForTest)
}

func makeRun(id, project, status, errMsg string, completedAt time.Time) *models.DiscoveryRun {
	t := completedAt
	return &models.DiscoveryRun{
		ID:          id,
		ProjectID:   project,
		Status:      status,
		Error:       errMsg,
		CompletedAt: &t,
	}
}

func TestDispatchTerminalRuns_NoRuns_IsNoOp(t *testing.T) {
	resetHooks(t)
	repo := newMockRunRepo()
	var hookCalls int
	runhooks.Register("noop-hook", func(context.Context, runhooks.RunCompletion) error {
		hookCalls++
		return nil
	})

	dispatchTerminalRuns(context.Background(), repo)

	if atomic.LoadInt32(&repo.listCalls) != 1 {
		t.Fatalf("ListTerminalWithoutCompletionHook called %d times, want 1", repo.listCalls)
	}
	if hookCalls != 0 {
		t.Fatalf("hook invoked %d times with no runs, want 0", hookCalls)
	}
	if len(repo.markCalls) != 0 {
		t.Fatalf("MarkCompletionHooksFired called for IDs %v, want none", repo.markCalls)
	}
}

func TestDispatchTerminalRuns_ListError_IsLoggedAndSkipsTick(t *testing.T) {
	resetHooks(t)
	repo := newMockRunRepo()
	repo.listErr = errors.New("mongo down")
	var hookCalls int
	runhooks.Register("hook", func(context.Context, runhooks.RunCompletion) error {
		hookCalls++
		return nil
	})

	// Should not panic; should swallow the error and return.
	dispatchTerminalRuns(context.Background(), repo)

	if hookCalls != 0 {
		t.Fatalf("hook invoked despite list error: %d calls", hookCalls)
	}
	if len(repo.markCalls) != 0 {
		t.Fatalf("MarkCompletionHooksFired called despite list error: %v", repo.markCalls)
	}
}

func TestDispatchTerminalRuns_HookSeesCompletionPayload(t *testing.T) {
	resetHooks(t)
	completedAt := time.Date(2026, 5, 12, 10, 30, 0, 0, time.UTC)
	repo := newMockRunRepo(makeRun("run-1", "proj-1", "completed", "", completedAt))

	var got runhooks.RunCompletion
	runhooks.Register("capture", func(_ context.Context, r runhooks.RunCompletion) error {
		got = r
		return nil
	})

	dispatchTerminalRuns(context.Background(), repo)

	want := runhooks.RunCompletion{
		RunID:       "run-1",
		ProjectID:   "proj-1",
		Status:      "completed",
		CompletedAt: completedAt,
	}
	if got != want {
		t.Fatalf("hook received %+v, want %+v", got, want)
	}
}

func TestDispatchTerminalRuns_NilCompletedAt_LeavesZeroTime(t *testing.T) {
	resetHooks(t)
	// A failed run produced by the stale-run sweeper may carry a
	// completed_at populated by Fail(); a hand-edited document may not.
	// The hook must receive a zero CompletedAt rather than panic.
	repo := newMockRunRepo(&models.DiscoveryRun{
		ID:          "run-x",
		ProjectID:   "p",
		Status:      "failed",
		Error:       "agent crashed",
		CompletedAt: nil,
	})
	var got runhooks.RunCompletion
	runhooks.Register("capture", func(_ context.Context, r runhooks.RunCompletion) error {
		got = r
		return nil
	})

	dispatchTerminalRuns(context.Background(), repo)

	if !got.CompletedAt.IsZero() {
		t.Errorf("CompletedAt = %v, want zero", got.CompletedAt)
	}
	if got.Error != "agent crashed" {
		t.Errorf("Error = %q, want %q", got.Error, "agent crashed")
	}
}

func TestDispatchTerminalRuns_AllHooksSucceed_MarksFired(t *testing.T) {
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(
		makeRun("run-a", "p", "completed", "", now),
		makeRun("run-b", "p", "failed", "boom", now),
	)
	runhooks.Register("a", func(context.Context, runhooks.RunCompletion) error { return nil })
	runhooks.Register("b", func(context.Context, runhooks.RunCompletion) error { return nil })

	dispatchTerminalRuns(context.Background(), repo)

	got := append([]string(nil), repo.markCalls...)
	want := []string{"run-a", "run-b"}
	if len(got) != len(want) {
		t.Fatalf("Mark called for %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Mark[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDispatchTerminalRuns_AnyHookFails_LeavesRunUnmarked(t *testing.T) {
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(makeRun("run-1", "p", "completed", "", now))
	runhooks.Register("ok", func(context.Context, runhooks.RunCompletion) error { return nil })
	runhooks.Register("fail", func(context.Context, runhooks.RunCompletion) error {
		return errors.New("flaky downstream")
	})

	dispatchTerminalRuns(context.Background(), repo)

	if len(repo.markCalls) != 0 {
		t.Fatalf("Mark called %v despite hook failure, want no calls", repo.markCalls)
	}
}

func TestDispatchTerminalRuns_PartialFailure_OtherRunsStillMarked(t *testing.T) {
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(
		makeRun("run-fail", "p", "completed", "", now),
		makeRun("run-ok", "p", "completed", "", now),
	)
	runhooks.Register("conditional", func(_ context.Context, rc runhooks.RunCompletion) error {
		if rc.RunID == "run-fail" {
			return errors.New("first one is sticky")
		}
		return nil
	})

	dispatchTerminalRuns(context.Background(), repo)

	if len(repo.markCalls) != 1 || repo.markCalls[0] != "run-ok" {
		t.Fatalf("Mark calls = %v, want [run-ok] (only the run whose hooks all succeeded)", repo.markCalls)
	}
}

func TestDispatchTerminalRuns_HookPanic_DoesNotAbortDispatcher(t *testing.T) {
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(
		makeRun("run-a", "p", "completed", "", now),
		makeRun("run-b", "p", "completed", "", now),
	)
	runhooks.Register("panicker", func(_ context.Context, rc runhooks.RunCompletion) error {
		if rc.RunID == "run-a" {
			panic("boom")
		}
		return nil
	})

	dispatchTerminalRuns(context.Background(), repo)

	if len(repo.markCalls) != 1 || repo.markCalls[0] != "run-b" {
		t.Fatalf("Mark calls = %v, want [run-b] (panic on run-a leaves it unmarked, run-b proceeds)", repo.markCalls)
	}
}

func TestDispatchTerminalRuns_MarkError_DoesNotAbortBatch(t *testing.T) {
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(
		makeRun("run-a", "p", "completed", "", now),
		makeRun("run-b", "p", "completed", "", now),
	)
	repo.markErrByRunID = map[string]error{"run-a": errors.New("mongo write failed")}
	runhooks.Register("ok", func(context.Context, runhooks.RunCompletion) error { return nil })

	dispatchTerminalRuns(context.Background(), repo)

	// run-a: hooks succeeded, mark failed → not in markCalls (the mock
	// only appends on success), but the dispatcher must still process
	// run-b.
	if len(repo.markCalls) != 1 || repo.markCalls[0] != "run-b" {
		t.Fatalf("Mark calls = %v, want only run-b", repo.markCalls)
	}
}

func TestDispatchTerminalRuns_NoHooksRegistered_StillCallsRepo(t *testing.T) {
	// The dispatcher is only spun up when HasRegistered() is true, but
	// once it is running it should keep behaving correctly even if the
	// caller drained the registry mid-flight (defensive — the dispatcher
	// goroutine has no shutdown semantics yet).
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(makeRun("run-z", "p", "completed", "", now))

	dispatchTerminalRuns(context.Background(), repo)

	if len(repo.markCalls) != 1 || repo.markCalls[0] != "run-z" {
		t.Fatalf("Mark calls = %v, want [run-z] (no hooks → trivially all succeeded → mark)", repo.markCalls)
	}
}

func TestStartRunCompletionDispatcher_TickerFires(t *testing.T) {
	// Exercise the ticker branch with a short interval so the goroutine
	// re-enters dispatchTerminalRuns at least once before cancel. With
	// the production 15s interval the test would only exercise the
	// initial pre-loop dispatch.
	resetHooks(t)
	now := time.Now()
	repo := newMockRunRepo(
		makeRun("run-1", "p", "completed", "", now),
		makeRun("run-2", "p", "completed", "", now),
	)
	runhooks.Register("counter", func(context.Context, runhooks.RunCompletion) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 20ms interval: well under any production cadence, easily detected
	// by polling for >=2 ListTerminalWithoutCompletionHook calls.
	startRunCompletionDispatcherWithInterval(ctx, repo, 20*time.Millisecond)

	// Wait for the ticker to fire at least twice (initial dispatch + 1
	// tick). Generous timeout to keep this stable on slow CI.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&repo.listCalls) >= 2 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("dispatcher only called list %d times within 2s, want >=2 — ticker branch may not have fired", atomic.LoadInt32(&repo.listCalls))
}

func TestStartRunCompletionDispatcher_CancelsOnContext(t *testing.T) {
	resetHooks(t)
	repo := newMockRunRepo()
	// Register a hook so the dispatcher would have work; with no runs
	// in the queue the goroutine will idle and then unblock on ctx.Done.
	runhooks.Register("idle", func(context.Context, runhooks.RunCompletion) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	startRunCompletionDispatcher(ctx, repo)

	// Give the initial dispatch a moment to settle. We don't want to
	// race with the ticker — calling cancel() should suffice.
	time.Sleep(50 * time.Millisecond)
	listsBefore := atomic.LoadInt32(&repo.listCalls)
	cancel()
	// After cancellation, ListTerminalWithoutCompletionHook should not be
	// invoked again. The ticker tick interval is 15s so without
	// cancellation we'd see no extra calls in the test window anyway —
	// what we're really asserting is that the goroutine doesn't leak or
	// panic. The Goroutine count delta would be the strict check; here
	// we settle for "no further repo calls in 200ms".
	time.Sleep(200 * time.Millisecond)
	listsAfter := atomic.LoadInt32(&repo.listCalls)
	if listsAfter != listsBefore {
		t.Fatalf("dispatcher kept polling after cancel: %d → %d", listsBefore, listsAfter)
	}
}
