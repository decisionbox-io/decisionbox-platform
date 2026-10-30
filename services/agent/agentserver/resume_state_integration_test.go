//go:build integration

// Integration test for loadResumeState, the first thing `agent --resume` does.
//
// It is here rather than in a unit test because the thing worth pinning is the
// interaction with real Mongo: which document the attempt number comes from,
// and what happens when that document cannot be read. Both are decided by
// driver behaviour (a missing document is not an error; an unreachable server
// is) that a fake would simply assert into existence.
package agentserver

import (
	"context"
	"strings"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func resumeTestDB(t *testing.T) (*database.DB, string) {
	t.Helper()
	ctx := context.Background()

	c, err := mongodb.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	uri, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("mongo conn string: %v", err)
	}
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "agentserver_resume_state_test"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("mongo client: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(ctx) })

	return database.New(client), uri
}

// disconnectedDB returns a DB whose client is already closed, so every
// operation through it fails. Used to make ONE repository's reads fail while
// the others keep working.
func disconnectedDB(t *testing.T, uri string) *database.DB {
	t.Helper()
	// Built against the live container so the connect succeeds — gomongo
	// pings on NewClient, so an unreachable URI would fail here instead and
	// leave nothing to test with. Disconnecting afterwards is what makes
	// every subsequent operation fail, deterministically and at once.
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "agentserver_resume_state_test"
	client, err := gomongo.NewClient(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second mongo client: %v", err)
	}
	if err := client.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	return database.New(client)
}

// seedRun writes a run document directly. attempt < 0 writes no attempt field,
// which is how a run created before the counter existed reads back.
func seedRun(t *testing.T, db *database.DB, attempt int, activeMs int64) string {
	t.Helper()
	oid := primitive.NewObjectID()
	doc := bson.M{
		"_id":        oid,
		"project_id": "proj-1",
		"status":     "running",
		"started_at": time.Now().UTC(),
		"active_ms":  activeMs,
	}
	if attempt >= 0 {
		doc["attempt"] = attempt
	}
	if _, err := db.Collection("discovery_runs").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return oid.Hex()
}

// seedCheckpoint writes one replayable step stamped with the given attempt.
func seedCheckpoint(t *testing.T, cp *database.DiscoveryCheckpointRepository, runID string, attempt, step int) {
	t.Helper()
	err := cp.SaveStep(context.Background(), database.CheckpointStepInput{
		ProjectID: "proj-1",
		RunID:     runID,
		Attempt:   attempt,
		Step: models.ExplorationStep{
			Step:      step,
			Action:    "query_data",
			Query:     "SELECT 1",
			Timestamp: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatalf("seed checkpoint step %d: %v", step, err)
	}
}

// TestLoadResumeState_TakesTheAttemptFromTheRunDocument is the case the
// derivation got wrong.
//
// Checkpoints are only restamped as the replay re-persists them, so an attempt
// that died before its first checkpoint write leaves them on the attempt
// before it. Here the run is on attempt 3 while its checkpoints still say 1 —
// exactly what a resume whose middle attempt died early produces. Deriving
// `set.Attempt + 1` would answer 2, and every attempt-fenced write this
// process then made would match nothing.
func TestLoadResumeState_TakesTheAttemptFromTheRunDocument(t *testing.T) {
	ctx := context.Background()
	db, _ := resumeTestDB(t)
	runRepo := database.NewRunRepository(db)
	cpRepo := database.NewDiscoveryCheckpointRepository(db)

	runID := seedRun(t, db, 3, 120_000)
	seedCheckpoint(t, cpRepo, runID, 1, 1)
	seedCheckpoint(t, cpRepo, runID, 1, 2)

	st, err := loadResumeState(ctx, runID, 0, runRepo, cpRepo)
	if err != nil {
		t.Fatalf("loadResumeState: %v", err)
	}
	if st.Attempt != 3 {
		t.Errorf("Attempt = %d, want 3 — the run document is authoritative; a derived 2 would fence every write against a number nothing matches", st.Attempt)
	}
	if st.PriorActiveMs != 120_000 {
		t.Errorf("PriorActiveMs = %d, want 120000", st.PriorActiveMs)
	}
	if st.Checkpoints.Len() != 2 {
		t.Errorf("replayable steps = %d, want 2", st.Checkpoints.Len())
	}
}

// TestLoadResumeState_RefusesWithoutAnAuthoritativeAttempt covers the three
// ways the attempt can be unknown.
//
// All three used to proceed on a derived number. Getting the fence wrong does
// not degrade a run — every write matches nothing, the agent reads that as
// having been superseded and exits deliberately WITHOUT reporting a failure,
// and the run is stranded `running` with no agent behind it. A refused resume
// is a recoverable error the operator retries; a stranded run is not.
func TestLoadResumeState_RefusesWithoutAnAuthoritativeAttempt(t *testing.T) {
	ctx := context.Background()
	db, uri := resumeTestDB(t)
	runRepo := database.NewRunRepository(db)
	cpRepo := database.NewDiscoveryCheckpointRepository(db)

	t.Run("the run document is gone", func(t *testing.T) {
		// Checkpoints that clearly support a replay, but no run to resume as.
		runID := primitive.NewObjectID().Hex()
		seedCheckpoint(t, cpRepo, runID, 1, 1)

		_, err := loadResumeState(ctx, runID, 0, runRepo, cpRepo)
		if err == nil {
			t.Fatal("expected a refusal: there is no run document to take an attempt from")
		}
		if !strings.Contains(err.Error(), "no longer exists") {
			t.Errorf("error = %v, want it to name the missing run", err)
		}
	})

	t.Run("the run document carries no attempt", func(t *testing.T) {
		runID := seedRun(t, db, -1, 0)
		seedCheckpoint(t, cpRepo, runID, 1, 1)

		_, err := loadResumeState(ctx, runID, 0, runRepo, cpRepo)
		if err == nil {
			t.Fatal("expected a refusal rather than a guessed attempt")
		}
		if !strings.Contains(err.Error(), "no attempt number") {
			t.Errorf("error = %v, want it to say the attempt is missing", err)
		}
	})

	t.Run("the read itself fails", func(t *testing.T) {
		// This is the branch the old code fell through on, and it has to be
		// isolated: a dead CONTEXT would fail LoadPrefix first and never
		// reach the run read, which makes the assertion pass for the wrong
		// reason. So the checkpoint repo stays on the live client and only
		// the run repo is given a disconnected one — checkpoints load, the
		// run document does not.
		runID := seedRun(t, db, 4, 0)
		seedCheckpoint(t, cpRepo, runID, 1, 1)

		deadRunRepo := database.NewRunRepository(disconnectedDB(t, uri))

		_, err := loadResumeState(ctx, runID, 0, deadRunRepo, cpRepo)
		if err == nil {
			t.Fatal("expected a refusal when the run document cannot be read; proceeding on a derived attempt is what strands the run")
		}
		if !strings.Contains(err.Error(), "before resuming it") {
			t.Errorf("error = %v, want the run-read failure, not some earlier step", err)
		}
	})
}

// TestLoadResumeState_RefusesWhenTheRunHasMovedOn is the fence's last hole.
//
// Every fence in the system compares against the run's current attempt. A
// workload that starts slowly can come up after the API restarted — its
// startup sweep marks in-flight runs `failed` without reaping them — and
// after an operator resumed again. Reading the attempt from the document
// then makes this process adopt the LIVE attempt's number, which does not
// turn it away: it makes it indistinguishable from the attempt that now owns
// the run, free to overwrite that attempt's checkpoints and its result.
//
// So the spawner states which attempt it launched, and a mismatch stops the
// process before it spends anything.
func TestLoadResumeState_RefusesWhenTheRunHasMovedOn(t *testing.T) {
	ctx := context.Background()
	db, _ := resumeTestDB(t)
	runRepo := database.NewRunRepository(db)
	cpRepo := database.NewDiscoveryCheckpointRepository(db)

	// The run is on attempt 3; this process was spawned as attempt 2.
	runID := seedRun(t, db, 3, 0)
	seedCheckpoint(t, cpRepo, runID, 1, 1)

	_, err := loadResumeState(ctx, runID, 2, runRepo, cpRepo)
	if err == nil {
		t.Fatal("expected a refusal: adopting attempt 3 would make this process indistinguishable from the attempt that owns the run")
	}
	if !strings.Contains(err.Error(), "has moved on to attempt 3") {
		t.Errorf("error = %v, want it to name both attempts", err)
	}

	// The matching case proceeds, so the pin cannot be silently fatal.
	st, err := loadResumeState(ctx, runID, 3, runRepo, cpRepo)
	if err != nil {
		t.Fatalf("the spawning attempt must be allowed to run: %v", err)
	}
	if st.Attempt != 3 {
		t.Errorf("Attempt = %d, want 3", st.Attempt)
	}
}
