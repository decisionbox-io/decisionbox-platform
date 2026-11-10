package handler

// Operator-pause handler tests (issue #508).
//
// What this endpoint does is stamp a request and return 202 — it signals no
// agent, calls no runner and writes no status. So most of what is worth
// testing is the refusals, because each of them is the difference between
// telling an operator something useful and leaving them watching a
// "Pausing…" that will never resolve.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// newPauseFixture reuses the resume fixture's wiring — same project, same
// repos — and puts its run into the state a pause actually applies to: live,
// mid-exploration.
func newPauseFixture(t *testing.T) *resumeFixture {
	t.Helper()
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = "running"
	f.runs.runs["run-1"].Phase = "exploration"
	return f
}

func pauseReq(runID string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/runs/"+runID+"/pause", nil)
	req.SetPathValue("runId", runID)
	return req.WithContext(auth.WithUser(req.Context(), &auth.UserPrincipal{
		Sub: "tester", OrgID: "default", Roles: []string{"admin"},
	}))
}

func postPause(f *resumeFixture, runID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.h.PauseRun(w, pauseReq(runID))
	return w
}

// TestPauseRun_StampsTheRequestAndNothingElse is the happy path, and the
// "nothing else" half is the design: the cooperative stop travels through the
// run document because the agent already reads it at every phase boundary, so
// this handler must not reach for the runner, a signal, or a status write.
func TestPauseRun_StampsTheRequestAndNothingElse(t *testing.T) {
	f := newPauseFixture(t)

	w := postPause(f, "run-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "pause_requested") {
		t.Errorf("body does not report the request: %s", w.Body.String())
	}
	if n := len(f.requestPauseCallsSafe()); n != 1 {
		t.Fatalf("RequestPause calls = %d, want 1", n)
	}
	// The run stays `running`, deliberately: an agent is alive and querying
	// the warehouse while it drains, so everything that asks "is this run
	// active" has to keep saying yes.
	if got := f.runs.runs["run-1"].Status; got != "running" {
		t.Errorf("status = %q, want it still running while the run drains", got)
	}
	// No status write and no hard kill. The runner's Cancel is what makes
	// cancel terminal; a pause that touched it would be a cancel.
	if n := len(f.runner.calls()); n != 0 {
		t.Errorf("pause spawned %d agents", n)
	}
}

// TestPauseRun_FencesOnTheAttemptItRead pins the fence the repository applies,
// because the handler is what supplies the attempt.
//
// A resume between the read and the write would otherwise pause an attempt
// nobody evaluated — the same reason Discard and BeginResume fence the same
// way.
func TestPauseRun_FencesOnTheAttemptItRead(t *testing.T) {
	f := newPauseFixture(t)
	f.runs.runs["run-1"].Attempt = 3

	if w := postPause(f, "run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	calls := f.requestPauseCallsSafe()
	if len(calls) != 1 {
		t.Fatalf("RequestPause calls = %d, want 1", len(calls))
	}
	if calls[0].Attempt != 3 {
		t.Errorf("fenced on attempt %d, want 3 — the attempt the handler actually read", calls[0].Attempt)
	}
}

// TestPauseRun_StampsADeadlineAndReportsIt. The deadline is the whole reason a
// request expires: without one, a request the agent can never honour would let
// a run pause hours later, unattended. It is reported back so the dashboard
// can say when it will give up rather than hard-coding a duration the
// deployment may have changed.
func TestPauseRun_StampsADeadlineAndReportsIt(t *testing.T) {
	f := newPauseFixture(t)

	before := time.Now()
	w := postPause(f, "run-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	calls := f.requestPauseCallsSafe()
	if len(calls) != 1 || calls[0].Deadline.IsZero() {
		t.Fatalf("no deadline stamped: %+v", calls)
	}
	if !calls[0].Deadline.After(before) {
		t.Errorf("deadline %v is not in the future", calls[0].Deadline)
	}
	if !strings.Contains(w.Body.String(), "pause_deadline_at") {
		t.Errorf("the deadline is not reported to the caller: %s", w.Body.String())
	}
}

// TestPauseRun_DrainTimeoutOfZeroDisablesTheDeadline covers the deployment
// that would rather wait out a multi-hour warehouse scan than have its
// request lapse.
func TestPauseRun_DrainTimeoutOfZeroDisablesTheDeadline(t *testing.T) {
	t.Setenv("DISCOVERY_PAUSE_DRAIN_TIMEOUT", "0")
	f := newPauseFixture(t)

	if w := postPause(f, "run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	calls := f.requestPauseCallsSafe()
	if len(calls) != 1 {
		t.Fatalf("RequestPause calls = %d, want 1", len(calls))
	}
	if !calls[0].Deadline.IsZero() {
		t.Errorf("deadline = %v, want the zero time so the request does not lapse", calls[0].Deadline)
	}
}

// TestPauseDrainTimeout_FallsBackSafely. A typo must not silently switch the
// deadline off — the fail-safe direction here is the documented default, not
// "no deadline", because a request that never lapses is the landmine the
// deadline exists to defuse.
func TestPauseDrainTimeout_FallsBackSafely(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"unset", ""},
		{"not a duration", "15min"},
		{"negative", "-5m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value == "" {
				t.Setenv("DISCOVERY_PAUSE_DRAIN_TIMEOUT", "")
			} else {
				t.Setenv("DISCOVERY_PAUSE_DRAIN_TIMEOUT", tc.value)
			}
			if got := pauseDrainTimeout(); got != defaultPauseDrainTimeout {
				t.Errorf("pauseDrainTimeout() = %v, want the %v default", got, defaultPauseDrainTimeout)
			}
		})
	}

	t.Run("a valid duration is honoured", func(t *testing.T) {
		t.Setenv("DISCOVERY_PAUSE_DRAIN_TIMEOUT", "90s")
		if got := pauseDrainTimeout(); got != 90*time.Second {
			t.Errorf("pauseDrainTimeout() = %v, want 90s", got)
		}
	})
}

// TestPauseRun_OnlyActiveRunsCanBePaused. There is nothing to ask a finished
// run to do, and accepting the request would leave a marker nothing will ever
// read — which the dashboard renders as "Pausing…" for ever.
func TestPauseRun_OnlyActiveRunsCanBePaused(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled", models.RunStatusPaused} {
		t.Run(status, func(t *testing.T) {
			f := newPauseFixture(t)
			f.runs.runs["run-1"].Status = status

			w := postPause(f, "run-1")

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), status) {
				t.Errorf("the refusal must name the run's actual status; body = %s", w.Body.String())
			}
		})
	}
}

// TestPauseRun_RefusesPastThePausableWindow is the refusal that exists to
// avoid a silence.
//
// A pause only costs nothing where a checkpoint has just landed. The
// persistence tail checkpoints nothing and owes durability for work already
// done, so pausing there would discard it for a run seconds from finishing.
// The agent would simply not honour the request — correct, and invisible. So
// the operator is told now, synchronously, with the alternative named.
//
// `recommendations` is deliberately NOT in this list any more: per-recommendation
// resume gave that phase durable seams, so a pause there is free and refusing
// it made those seams unreachable.
func TestPauseRun_RefusesPastThePausableWindow(t *testing.T) {
	for _, phase := range []string{"saving", "embed_index", "complete"} {
		t.Run(phase, func(t *testing.T) {
			f := newPauseFixture(t)
			f.runs.runs["run-1"].Phase = phase

			w := postPause(f, "run-1")

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, phase) {
				t.Errorf("the refusal must name the phase so the operator knows why; body = %s", body)
			}
			if !strings.Contains(strings.ToLower(body), "cancel") {
				t.Errorf("the refusal should point at the alternative; body = %s", body)
			}
			if n := len(f.requestPauseCallsSafe()); n != 0 {
				t.Errorf("stamped %d requests for a run past the window", n)
			}
		})
	}

	for _, phase := range []string{"", "init", "schema_discovery", "exploration", "analysis", "recommendations"} {
		t.Run("accepted in "+phase, func(t *testing.T) {
			f := newPauseFixture(t)
			f.runs.runs["run-1"].Phase = phase

			if w := postPause(f, "run-1"); w.Code != http.StatusAccepted {
				t.Fatalf("status = %d in phase %q, want 202; body = %s", w.Code, phase, w.Body.String())
			}
		})
	}
}

// TestPauseRun_DuplicateRequestIs409 — a second click must not quietly extend
// the drain window the first one opened.
func TestPauseRun_DuplicateRequestIs409(t *testing.T) {
	f := newPauseFixture(t)

	if w := postPause(f, "run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("first pause: status = %d, want 202", w.Code)
	}
	w := postPause(f, "run-1")
	if w.Code != http.StatusConflict {
		t.Fatalf("second pause: status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	if n := len(f.requestPauseCallsSafe()); n != 1 {
		t.Errorf("RequestPause applied %d times, want 1", n)
	}
}

// TestPauseRun_ARequestThatLapsedCanBeMadeAgain is the other half of the
// duplicate rule. A lapsed request must not block a fresh attempt at pausing
// for ever — otherwise one unlucky request (agent wedged in a long query)
// would take the button away permanently.
func TestPauseRun_ARequestThatLapsedCanBeMadeAgain(t *testing.T) {
	f := newPauseFixture(t)
	past := time.Now().Add(-time.Hour)
	f.runs.runs["run-1"].PauseRequestedAt = &past
	f.runs.runs["run-1"].PauseDeadlineAt = &past

	if w := postPause(f, "run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 — a lapsed request must not block a new one; body = %s", w.Code, w.Body.String())
	}
}

// TestPauseRun_UnknownRunIs404 / project gone / no access. The route is keyed
// by run id, so it sits outside ProjectACLMiddleware and has to authorise
// against the run's project itself — the same gap ResumeRun and DiscardRun
// close at their own entry.
func TestPauseRun_UnknownRunIs404(t *testing.T) {
	f := newPauseFixture(t)
	if w := postPause(f, "nope"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPauseRun_MissingProjectIs404(t *testing.T) {
	f := newPauseFixture(t)
	delete(f.projs.projects, "p1")
	if w := postPause(f, "run-1"); w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
}

func TestPauseRun_WithoutProjectAccessIs403(t *testing.T) {
	f := newPauseFixture(t)
	f.projs.projects["p1"].AllowedRoles = []string{"analyst"}

	req := httptest.NewRequest("POST", "/api/v1/runs/run-1/pause", nil)
	req.SetPathValue("runId", "run-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.UserPrincipal{
		Sub: "tester", OrgID: "default", Roles: []string{"member"},
	}))
	w := httptest.NewRecorder()
	f.h.PauseRun(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", w.Code, w.Body.String())
	}
	if n := len(f.requestPauseCallsSafe()); n != 0 {
		t.Errorf("stamped %d requests for a caller without access", n)
	}
}

// TestPauseRun_RepositoryFailureIs500 keeps the ErrNoPausableRun 409 and a
// genuine infrastructure failure distinguishable: the first is an answer, the
// second is worth retrying.
func TestPauseRun_RepositoryFailureIs500(t *testing.T) {
	f := newPauseFixture(t)
	f.runs.requestPauseErr = errors.New("mongo is down")

	w := postPause(f, "run-1")

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
}

func TestPauseRun_NoPausableRunIs409(t *testing.T) {
	f := newPauseFixture(t)
	f.runs.requestPauseErr = database.ErrNoPausableRun

	w := postPause(f, "run-1")

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
}

// --- the three verbs a paused run meets ------------------------------------

// TestResumeRun_AcceptsAPausedRun is the economy of the whole feature: a
// cooperatively stopped run is a crashed run that stopped tidily, so it
// re-enters by exactly the path crash recovery already uses. No second entry
// point, no second flip, no second set of fences.
func TestResumeRun_AcceptsAPausedRun(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = models.RunStatusPaused

	w := f.post("run-1")

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	if n := len(f.runner.calls()); n != 1 {
		t.Fatalf("spawned %d agents, want 1", n)
	}
	if !f.runner.calls()[0].Resume {
		t.Error("the agent was not spawned with --resume")
	}
	if got := f.runs.runs["run-1"].Attempt; got != 2 {
		t.Errorf("attempt = %d, want 2 — the same runID on its next attempt", got)
	}
}

// TestResumeRun_ClearsAStalePauseRequest. A request stamped moments before the
// resume must not stop the attempt the resume starts: the operator asked the
// OLD attempt to pause, and it either did or died trying.
func TestResumeRun_ClearsAStalePauseRequest(t *testing.T) {
	f := newResumeFixture(t)
	now := time.Now()
	deadline := now.Add(time.Hour)
	f.runs.runs["run-1"].Status = models.RunStatusPaused
	f.runs.runs["run-1"].PauseRequestedAt = &now
	f.runs.runs["run-1"].PauseDeadlineAt = &deadline
	f.runs.runs["run-1"].PauseRequestedAttempt = 1

	if w := f.post("run-1"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", w.Code, w.Body.String())
	}
	run := f.runs.runs["run-1"]
	if run.PauseRequestedAt != nil || run.PauseDeadlineAt != nil || run.PauseRequestedAttempt != 0 {
		t.Errorf("the resume left a pause request behind (%v, %v, %d); the new attempt would stop at its first seam",
			run.PauseRequestedAt, run.PauseDeadlineAt, run.PauseRequestedAttempt)
	}
}

// TestResumeRun_RefusalNamesBothResumableStatuses — the message is what a
// stale dashboard's user reads, so it has to say what is actually possible.
func TestResumeRun_RefusalNamesBothResumableStatuses(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = "cancelled"

	w := f.post("run-1")

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "failed") || !strings.Contains(body, "paused") {
		t.Errorf("the refusal should name both resumable statuses; body = %s", body)
	}
}

// TestCancelRun_AcceptsAPausedRunAndMakesItTerminal. Cancel is the one verb
// that ends a paused run, and that is why DiscardRun refuses one: a paused run
// is not terminal, so nothing else would ever close its plan reservation or
// purge its checkpoints.
func TestCancelRun_AcceptsAPausedRunAndMakesItTerminal(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = models.RunStatusPaused

	req := httptest.NewRequest("DELETE", "/api/v1/runs/run-1", nil)
	req.SetPathValue("runId", "run-1")
	w := httptest.NewRecorder()
	f.h.CancelRun(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if got := f.runs.runs["run-1"].Status; got != "cancelled" {
		t.Errorf("status = %q, want cancelled", got)
	}
	if len(f.cps.deleted) != 1 || f.cps.deleted[0] != "run-1" {
		t.Errorf("checkpoints deleted = %v, want [run-1]: a cancelled run is not resumable", f.cps.deleted)
	}
}

// TestDiscardRun_RefusesAPausedRun. Not tidiness: Discard leaves the STATUS
// alone by design, and `paused` is not terminal — so a discarded-while-paused
// run would never appear in ListTerminalWithReservation and would hold its
// concurrent-run reservation open for ever, with nothing left to reconcile
// from.
func TestDiscardRun_RefusesAPausedRun(t *testing.T) {
	f := newResumeFixture(t)
	f.runs.runs["run-1"].Status = models.RunStatusPaused

	req := httptest.NewRequest("POST", "/api/v1/runs/run-1/discard", nil)
	req.SetPathValue("runId", "run-1")
	req = req.WithContext(auth.WithUser(req.Context(), &auth.UserPrincipal{
		Sub: "tester", OrgID: "default", Roles: []string{"admin"},
	}))
	w := httptest.NewRecorder()
	f.h.DiscardRun(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", w.Code, w.Body.String())
	}
	body := strings.ToLower(w.Body.String())
	if !strings.Contains(body, "resume") || !strings.Contains(body, "cancel") {
		t.Errorf("the refusal should name both ways out of a paused run; body = %s", w.Body.String())
	}
	if f.runs.runs["run-1"].DiscardedAt != nil {
		t.Error("the run was discarded anyway; its reservation would leak")
	}
}
