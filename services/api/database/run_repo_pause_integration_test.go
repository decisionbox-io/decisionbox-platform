//go:build integration

package database

// Operator-pause repository tests (issue #508), against a real MongoDB.
//
// The filters are where this feature lives, and a filter that matches nothing
// fails silently — so these assert against written documents rather than
// through a fake that re-implements the same predicates.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// seedPausableRun inserts a live run with the given attempt and phase.
func seedPausableRun(t *testing.T, ctx context.Context, status string, attempt int) string {
	t.Helper()
	doc := bson.M{
		"project_id": "proj-pause",
		"status":     status,
		"phase":      "exploration",
		"started_at": time.Now(),
		"updated_at": time.Now(),
	}
	if attempt > 0 {
		doc["attempt"] = attempt
	}
	res, err := testDB.Collection("discovery_runs").InsertOne(ctx, doc)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return res.InsertedID.(primitive.ObjectID).Hex()
}

func readRun(t *testing.T, ctx context.Context, repo *RunRepository, id string) *models.DiscoveryRun {
	t.Helper()
	run, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if run == nil {
		t.Fatal("run vanished")
	}
	return run
}

// TestRequestPause_StampsTheRequestWithoutChangingTheStatus is the central
// invariant: the run stays active while it drains.
//
// An agent is alive and querying the warehouse in that window, so every
// predicate meaning "this run is active" has to keep saying yes. Writing a
// status here instead would have been the easy version and a concurrency bug.
func TestRequestPause_StampsTheRequestWithoutChangingTheStatus(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedPausableRun(t, ctx, "running", 1)
	deadline := time.Now().Add(30 * time.Minute)

	got, err := repo.RequestPause(ctx, id, 1, deadline)
	if err != nil {
		t.Fatalf("RequestPause: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status = %q, want it still running while the run drains", got.Status)
	}
	if got.PauseRequestedAt == nil {
		t.Fatal("pause_requested_at was not stamped; the agent polls for exactly this")
	}
	if got.PauseRequestedAttempt != 1 {
		t.Errorf("pause_requested_attempt = %d, want 1", got.PauseRequestedAttempt)
	}
	if got.PauseDeadlineAt == nil {
		t.Fatal("pause_deadline_at was not stamped")
	}

	// And it is durable, not just in the returned copy.
	stored := readRun(t, ctx, repo, id)
	if stored.PauseRequestedAt == nil || stored.PauseDeadlineAt == nil {
		t.Error("the request did not survive the round trip")
	}
}

func TestRequestPause_Refusals(t *testing.T) {
	ctx := context.Background()
	repo := NewRunRepository(testDB)
	deadline := time.Now().Add(30 * time.Minute)

	t.Run("a terminal run has nothing to ask", func(t *testing.T) {
		for _, status := range []string{"completed", "failed", "cancelled", models.RunStatusPaused} {
			dropRuns(t, ctx)
			id := seedPausableRun(t, ctx, status, 1)
			if _, err := repo.RequestPause(ctx, id, 1, deadline); !errors.Is(err, ErrNoPausableRun) {
				t.Errorf("status %s: err = %v, want ErrNoPausableRun", status, err)
			}
		}
	})

	t.Run("a discarded run", func(t *testing.T) {
		dropRuns(t, ctx)
		id := seedPausableRun(t, ctx, "running", 1)
		now := time.Now()
		mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{"discarded_at": now}})
		if _, err := repo.RequestPause(ctx, id, 1, deadline); !errors.Is(err, ErrNoPausableRun) {
			t.Errorf("err = %v, want ErrNoPausableRun", err)
		}
	})

	t.Run("an attempt the caller did not read", func(t *testing.T) {
		// A resume between the handler's read and this write must make the
		// request match nothing rather than pause an attempt nobody
		// evaluated. Same fence Discard and BeginResume apply.
		dropRuns(t, ctx)
		id := seedPausableRun(t, ctx, "running", 3)
		if _, err := repo.RequestPause(ctx, id, 2, deadline); !errors.Is(err, ErrNoPausableRun) {
			t.Errorf("err = %v, want ErrNoPausableRun", err)
		}
		if _, err := repo.RequestPause(ctx, id, 3, deadline); err != nil {
			t.Errorf("the matching attempt was refused: %v", err)
		}
	})

	t.Run("a request already pending", func(t *testing.T) {
		dropRuns(t, ctx)
		id := seedPausableRun(t, ctx, "running", 1)
		if _, err := repo.RequestPause(ctx, id, 1, deadline); err != nil {
			t.Fatalf("first request: %v", err)
		}
		if _, err := repo.RequestPause(ctx, id, 1, deadline); !errors.Is(err, ErrNoPausableRun) {
			t.Errorf("err = %v, want ErrNoPausableRun — a second click must not extend the drain window", err)
		}
	})
}

// TestRequestPause_ALapsedRequestCanBeMadeAgain is the other half of the
// duplicate rule. One unlucky request — an agent wedged inside a long
// warehouse query — must not take the button away for the rest of the run.
func TestRequestPause_ALapsedRequestCanBeMadeAgain(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedPausableRun(t, ctx, "running", 1)

	past := time.Now().Add(-time.Hour)
	mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{
		"pause_requested_at": past,
		"pause_deadline_at":  past,
	}})

	got, err := repo.RequestPause(ctx, id, 1, time.Now().Add(30*time.Minute))
	if err != nil {
		t.Fatalf("RequestPause over a lapsed one: %v", err)
	}
	if got.PauseDeadlineAt == nil || !got.PauseDeadlineAt.After(time.Now()) {
		t.Errorf("the new request did not get a live deadline: %v", got.PauseDeadlineAt)
	}
}

// TestRequestPause_AZeroDeadlineRemovesAStaleOne. Without this, a deployment
// that switches the drain timeout off would have its brand-new request read as
// already lapsed, because the expired instant from an earlier request would
// still be on the document.
func TestRequestPause_AZeroDeadlineRemovesAStaleOne(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedPausableRun(t, ctx, "running", 1)

	past := time.Now().Add(-time.Hour)
	mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{"pause_deadline_at": past}})

	got, err := repo.RequestPause(ctx, id, 1, time.Time{})
	if err != nil {
		t.Fatalf("RequestPause: %v", err)
	}
	if got.PauseDeadlineAt != nil {
		t.Errorf("pause_deadline_at = %v, want it removed so the request does not read as lapsed", got.PauseDeadlineAt)
	}
}

// TestBeginResume_AcceptsAPausedRunAndClearsTheRequest. A paused run re-enters
// by exactly the path crash recovery uses — and any request stamped against
// the previous attempt has to go, or the attempt this flip starts would stop
// at its first seam.
func TestBeginResume_AcceptsAPausedRunAndClearsTheRequest(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedPausableRun(t, ctx, models.RunStatusPaused, 1)

	now := time.Now()
	mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{
		"pause_requested_at":      now,
		"pause_requested_attempt": 1,
		"pause_deadline_at":       now.Add(time.Hour),
		"last_paused_at":          now,
	}})

	run, err := repo.BeginResume(ctx, id)
	if err != nil {
		t.Fatalf("BeginResume on a paused run: %v", err)
	}
	if run.Status != "running" {
		t.Errorf("status = %q, want running", run.Status)
	}
	if run.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", run.Attempt)
	}
	if run.PauseRequestedAt != nil || run.PauseDeadlineAt != nil || run.PauseRequestedAttempt != 0 {
		t.Errorf("the resume left a pause request behind (%v, %v, %d)",
			run.PauseRequestedAt, run.PauseDeadlineAt, run.PauseRequestedAttempt)
	}
	// Kept: it is history, and it is what explains a gap in the middle of a
	// run's timeline.
	if run.LastPausedAt == nil {
		t.Error("last_paused_at was cleared; a resumed run loses the record of having been paused")
	}
}

// TestTerminalWritesClearAPendingPauseRequest. A request cannot outlive the run
// it was made against: left behind it describes a stop that can never happen,
// and the dashboard reads its presence as "pausing".
func TestTerminalWritesClearAPendingPauseRequest(t *testing.T) {
	ctx := context.Background()
	repo := NewRunRepository(testDB)

	for _, tc := range []struct {
		name  string
		apply func(id string) error
	}{
		{"Fail", func(id string) error { return repo.Fail(ctx, id, "boom") }},
		{"FailAttempt", func(id string) error {
			_, err := repo.FailAttempt(ctx, id, 1, "boom")
			return err
		}},
		{"Cancel", func(id string) error { return repo.Cancel(ctx, id) }},
		{"CleanupStaleRuns", func(string) error {
			_, err := repo.CleanupStaleRuns(ctx)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dropRuns(t, ctx)
			id := seedPausableRun(t, ctx, "running", 1)
			now := time.Now()
			mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{
				"pause_requested_at":      now,
				"pause_requested_attempt": 1,
				"pause_deadline_at":       now.Add(time.Hour),
			}})

			if err := tc.apply(id); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			stored := readRun(t, ctx, repo, id)
			if stored.PauseRequestedAt != nil || stored.PauseDeadlineAt != nil || stored.PauseRequestedAttempt != 0 {
				t.Errorf("%s left a pause request on a terminal run (%v, %v, %d)",
					tc.name, stored.PauseRequestedAt, stored.PauseDeadlineAt, stored.PauseRequestedAttempt)
			}
		})
	}
}

// TestPausedRunIsNeitherActiveNorTerminal asserts the §3.1 predicate table
// against real documents, because every row of it is a behaviour somebody
// could break by adding `paused` to the wrong status list.
func TestPausedRunIsNeitherActiveNorTerminal(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedPausableRun(t, ctx, models.RunStatusPaused, 1)
	mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{"policy_reservation_id": "res-1"}})

	// Not active: a paused run holds no agent, so it must not block the
	// project's next run.
	if run, err := repo.GetRunningByProject(ctx, "proj-pause"); err != nil {
		t.Fatalf("GetRunningByProject: %v", err)
	} else if run != nil {
		t.Errorf("a paused run counted as active (%s); it would block every new run for the project", run.ID)
	}
	// A real ObjectID for the exclusion, since the repo parses it.
	if run, err := repo.GetOtherRunningByProject(ctx, "proj-pause", primitive.NewObjectID().Hex()); err != nil {
		t.Fatalf("GetOtherRunningByProject: %v", err)
	} else if run != nil {
		t.Errorf("a paused run counted as a competing active run (%s)", run.ID)
	}

	// Not terminal: its outcome is still to come, so completion hooks must
	// not fire and its reservation must not be closed.
	hooks, err := repo.ListTerminalWithoutCompletionHook(ctx, 50)
	if err != nil {
		t.Fatalf("ListTerminalWithoutCompletionHook: %v", err)
	}
	for _, r := range hooks {
		if r.ID == id {
			t.Error("a paused run was queued for completion hooks; plugins would see an outcome that has not happened")
		}
	}
	reserved, err := repo.ListTerminalWithReservation(ctx, 50)
	if err != nil {
		t.Fatalf("ListTerminalWithReservation: %v", err)
	}
	for _, r := range reserved {
		if r.ID == id {
			t.Error("a paused run's reservation was queued for closing; the resume opens none of its own, so there would be nothing to reopen")
		}
	}

	// Not swept: the startup sweep marks in-flight runs failed, and a paused
	// run is not in flight.
	if _, err := repo.CleanupStaleRuns(ctx); err != nil {
		t.Fatalf("CleanupStaleRuns: %v", err)
	}
	if got := readRun(t, ctx, repo, id).Status; got != models.RunStatusPaused {
		t.Errorf("the startup sweep changed a paused run to %q", got)
	}

	// And it still represents the project, which is where Resume lives.
	latest, err := repo.GetLatestByProject(ctx, "proj-pause")
	if err != nil {
		t.Fatalf("GetLatestByProject: %v", err)
	}
	if latest == nil || latest.ID != id {
		t.Errorf("the paused run is not the project's current run; the dashboard would not show it, so it could not be resumed")
	}
}

// TestClearExplorationCheckpointMarker_WorksOnAPausedRun. The marker is what
// makes Resume appear, and a paused run's checkpoints can still expire
// underneath it — leaving it behind would keep offering a Resume that can only
// answer 409.
func TestClearExplorationCheckpointMarker_WorksOnAPausedRun(t *testing.T) {
	ctx := context.Background()
	dropRuns(t, ctx)
	repo := NewRunRepository(testDB)
	id := seedPausableRun(t, ctx, models.RunStatusPaused, 1)
	mustUpdateRun(t, ctx, id, bson.M{"$set": bson.M{"last_checkpoint_step": 42}})

	if err := repo.ClearExplorationCheckpointMarker(ctx, id); err != nil {
		t.Fatalf("ClearExplorationCheckpointMarker: %v", err)
	}
	if got := readRun(t, ctx, repo, id).LastCheckpointStep; got != 0 {
		t.Errorf("last_checkpoint_step = %d, want 0", got)
	}
}

// mustUpdateRun applies a raw update to a seeded run.
func mustUpdateRun(t *testing.T, ctx context.Context, id string, update bson.M) {
	t.Helper()
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		t.Fatalf("bad id %q: %v", id, err)
	}
	if _, err := testDB.Collection("discovery_runs").UpdateByID(ctx, oid, update); err != nil {
		t.Fatalf("update run: %v", err)
	}
}
