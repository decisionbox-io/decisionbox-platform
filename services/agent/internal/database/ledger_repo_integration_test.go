//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"go.mongodb.org/mongo-driver/bson"
)

// Discovery Ledger repositories against a real MongoDB (enterprise#389).
//
// These cover the behaviour the investigation graph depends on and that a fake
// cannot prove: the retention cap now ARCHIVES rather than deletes, the active
// set excludes archived rows while the dedup read includes them, a task close
// stamps its metadata in one write, and a learning is deduped on its key.

func seedFinding(t *testing.T, repo *LedgerFindingRepository, id, projectID, status string, lastSeen time.Time) {
	t.Helper()
	f := &commonmodels.LedgerFinding{
		ID: id, ProjectID: projectID, Name: id, Status: status,
		NormalizedKey: id, LastSeen: lastSeen, FirstSeen: lastSeen, SeenCount: 1,
	}
	if err := repo.Upsert(context.Background(), f); err != nil {
		t.Fatalf("seed finding %s: %v", id, err)
	}
}

func TestLedgerFindingRepository_PruneArchivesInsteadOfDeleting(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewLedgerFindingRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	base := time.Now().Add(-100 * time.Hour)
	// Least valuable first: refuted, then resolved, then active by age.
	seedFinding(t, repo, "refuted-old", "p1", commonmodels.LedgerFindingStatusRefuted, base)
	seedFinding(t, repo, "resolved-old", "p1", commonmodels.LedgerFindingStatusResolved, base.Add(time.Hour))
	seedFinding(t, repo, "active-old", "p1", commonmodels.LedgerFindingStatusConfirmed, base.Add(2*time.Hour))
	seedFinding(t, repo, "active-new", "p1", commonmodels.LedgerFindingStatusConfirmed, time.Now())
	// Another project must be untouched by this project's cap.
	seedFinding(t, repo, "other-project", "p2", commonmodels.LedgerFindingStatusConfirmed, base)

	if err := repo.Prune(ctx, "p1", 2); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Nothing is deleted — that is the whole point. Edges from tasks, runs and
	// insights point at these ids.
	total, err := db.Collection(CollectionDiscoveryLedgerFindings).CountDocuments(ctx, bson.M{"project_id": "p1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 4 {
		t.Errorf("prune must not delete: %d documents remain, want 4", total)
	}

	active, err := repo.List(ctx, "p1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("active set should be capped at 2, got %d: %+v", len(active), active)
	}
	for _, f := range active {
		if f.ID == "refuted-old" || f.ID == "resolved-old" {
			t.Errorf("%s should have been archived first (least valuable)", f.ID)
		}
	}

	all, err := repo.ListIncludingArchived(ctx, "p1")
	if err != nil {
		t.Fatalf("list including archived: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("the dedup read must still see every finding, got %d", len(all))
	}
	archived := 0
	for _, f := range all {
		if f.Archived {
			archived++
			if f.ArchivedAt.IsZero() {
				t.Errorf("%s archived with no timestamp", f.ID)
			}
		}
	}
	if archived != 2 {
		t.Errorf("want 2 archived, got %d", archived)
	}

	// Another project's findings are not collateral.
	otherActive, err := repo.List(ctx, "p2")
	if err != nil {
		t.Fatalf("list p2: %v", err)
	}
	if len(otherActive) != 1 {
		t.Errorf("p2 should be untouched, got %d active", len(otherActive))
	}
}

// TestLedgerFindingRepository_PruneIsIdempotent is the trap the active-set
// filter exists for: if Prune counted archived rows against the cap, the
// collection would stay permanently over max and every later run would archive
// another slice of live findings.
func TestLedgerFindingRepository_PruneIsIdempotent(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewLedgerFindingRepository(db)

	base := time.Now().Add(-10 * time.Hour)
	for _, id := range []string{"f1", "f2", "f3", "f4", "f5"} {
		seedFinding(t, repo, id, "p1", commonmodels.LedgerFindingStatusConfirmed, base)
		base = base.Add(time.Hour)
	}
	for round := 0; round < 3; round++ {
		if err := repo.Prune(ctx, "p1", 3); err != nil {
			t.Fatalf("prune round %d: %v", round, err)
		}
		active, err := repo.List(ctx, "p1")
		if err != nil {
			t.Fatalf("list round %d: %v", round, err)
		}
		if len(active) != 3 {
			t.Fatalf("round %d: active set = %d, want a stable 3", round, len(active))
		}
	}
}

func TestLedgerFindingRepository_PruneNoOps(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewLedgerFindingRepository(db)
	seedFinding(t, repo, "f1", "p1", commonmodels.LedgerFindingStatusConfirmed, time.Now())

	// A non-positive cap disables pruning entirely.
	if err := repo.Prune(ctx, "p1", 0); err != nil {
		t.Fatalf("prune(0): %v", err)
	}
	// Under the cap, nothing is archived.
	if err := repo.Prune(ctx, "p1", 10); err != nil {
		t.Fatalf("prune(10): %v", err)
	}
	active, err := repo.List(ctx, "p1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(active) != 1 || active[0].Archived {
		t.Errorf("nothing should have been archived: %+v", active)
	}
}

// TestLedgerFindingRepository_ArchivedIndexExists — the active-set filter is on
// every prompt read path, so it needs an index to go with it.
func TestLedgerFindingRepository_ArchivedIndexExists(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := NewLedgerFindingRepository(db).EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	cur, err := db.Collection(CollectionDiscoveryLedgerFindings).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var specs []bson.M
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatalf("decode indexes: %v", err)
	}
	for _, spec := range specs {
		if key, ok := spec["key"].(bson.M); ok {
			if _, hasProject := key["project_id"]; hasProject {
				if _, hasArchived := key["archived"]; hasArchived {
					return
				}
			}
		}
	}
	t.Errorf("no (project_id, archived) index found: %+v", specs)
}

// TestLedgerTaskRepository_CloseStampsMetadata is "we followed this up — what
// did we find?" at the storage layer.
func TestLedgerTaskRepository_CloseStampsMetadata(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewLedgerTaskRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}
	if err := repo.Insert(ctx, []commonmodels.LedgerTask{
		{ID: "t1", ProjectID: "p1", Text: "check the centres", Kind: commonmodels.LedgerTaskKindHypothesis,
			Status: commonmodels.LedgerTaskStatusOpen, NormalizedKey: "k1"},
		{ID: "t2", ProjectID: "p1", Text: "a dead end", Kind: commonmodels.LedgerTaskKindNextTask,
			Status: commonmodels.LedgerTaskStatusOpen, NormalizedKey: "k2"},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	at := time.Now().Truncate(time.Millisecond)
	won, err := repo.Close(ctx, "p1", "t1", TaskClose{
		Status: commonmodels.LedgerTaskStatusDone, RunID: "r2", DiscoveryID: "d2", At: at,
		Reason: "9 of 12 share the same centre", ResolvedByFindingIDs: []string{"f1", "f2"},
	})
	if err != nil {
		t.Fatalf("close t1: %v", err)
	}
	if !won {
		t.Error("closing an open task is a win")
	}
	// A dead end: no findings answered it, so no key must be stored.
	if _, err := repo.Close(ctx, "p1", "t2", TaskClose{
		Status: commonmodels.LedgerTaskStatusDropped, RunID: "r2", DiscoveryID: "d2", At: at,
		Reason: "the table was dropped upstream",
	}); err != nil {
		t.Fatalf("close t2: %v", err)
	}

	tasks, err := repo.List(ctx, "p1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]commonmodels.LedgerTask{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	done := byID["t1"]
	if done.Status != commonmodels.LedgerTaskStatusDone || done.ClosedRunID != "r2" ||
		done.ClosedDiscoveryID != "d2" || done.CloseReason != "9 of 12 share the same centre" {
		t.Errorf("close metadata not stamped: %+v", done)
	}
	if done.ClosedAt.IsZero() || !done.ClosedAt.Equal(at.UTC()) {
		t.Errorf("closed_at = %v, want %v", done.ClosedAt, at.UTC())
	}
	if len(done.ResolvedByFindingIDs) != 2 {
		t.Errorf("resolved-by not stored: %+v", done.ResolvedByFindingIDs)
	}

	var raw bson.M
	if err := db.Collection(CollectionDiscoveryLedgerTasks).FindOne(ctx, bson.M{"_id": "t2"}).Decode(&raw); err != nil {
		t.Fatalf("read t2: %v", err)
	}
	if _, present := raw["resolved_by_finding_ids"]; present {
		t.Errorf("a dropped task must store no resolved-by key, got %v", raw["resolved_by_finding_ids"])
	}

	// A second close of a task that is already closed must not land: two
	// reflections can read the same open thread, and the second overwriting
	// the first would leave a thread recorded as dropped while still naming
	// the findings that answered it.
	second, err := repo.Close(ctx, "p1", "t1", TaskClose{
		Status: commonmodels.LedgerTaskStatusDropped, RunID: "r3", DiscoveryID: "d3", At: at,
		Reason: "on reflection, a dead end",
	})
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
	if second {
		t.Error("closing an already-closed task must report that it did not win")
	}
	reread, err := repo.List(ctx, "p1", commonmodels.LedgerTaskStatusDone)
	if err != nil || len(reread) != 1 {
		t.Fatalf("t1 must still be done: n=%d err=%v", len(reread), err)
	}
	if reread[0].ClosedRunID != "r2" || reread[0].CloseReason != "9 of 12 share the same centre" {
		t.Errorf("the first close must stand whole: %+v", reread[0])
	}

	// Closing a task in another project must not match.
	if _, err := repo.Close(ctx, "other", "t1", TaskClose{Status: commonmodels.LedgerTaskStatusDropped}); err != nil {
		t.Fatalf("cross-project close should be a silent no-match, not an error: %v", err)
	}
	after, err := repo.List(ctx, "p1", commonmodels.LedgerTaskStatusDone)
	if err != nil {
		t.Fatalf("list done: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("t1 must still be done, got %+v", after)
	}
}

// TestLedgerTaskRepository_MarkPursuedIsIdempotent — the pursued record is
// written every run, so a repeat must not grow the array.
func TestLedgerTaskRepository_MarkPursuedIsIdempotent(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewLedgerTaskRepository(db)
	if err := repo.Insert(ctx, []commonmodels.LedgerTask{
		{ID: "t1", ProjectID: "p1", Text: "a", Status: commonmodels.LedgerTaskStatusOpen, NormalizedKey: "k1"},
		{ID: "t2", ProjectID: "p1", Text: "b", Status: commonmodels.LedgerTaskStatusOpen, NormalizedKey: "k2"},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := repo.MarkPursued(ctx, "p1", []string{"t1", "t2"}, "r1"); err != nil {
			t.Fatalf("mark pursued: %v", err)
		}
	}
	if err := repo.MarkPursued(ctx, "p1", []string{"t1"}, "r2"); err != nil {
		t.Fatalf("mark pursued r2: %v", err)
	}
	// Empty inputs are a no-op, not an error.
	if err := repo.MarkPursued(ctx, "p1", nil, "r3"); err != nil {
		t.Fatalf("empty ids: %v", err)
	}
	if err := repo.MarkPursued(ctx, "p1", []string{"t1"}, ""); err != nil {
		t.Fatalf("empty run: %v", err)
	}

	tasks, err := repo.List(ctx, "p1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, task := range tasks {
		want := []string{"r1"}
		if task.ID == "t1" {
			want = []string{"r1", "r2"}
		}
		if len(task.PursuedInRunIDs) != len(want) {
			t.Errorf("%s pursued = %v, want %v", task.ID, task.PursuedInRunIDs, want)
		}
	}
}

// TestLedgerLearningRepository_UpsertDedupes — a learning repeated across runs
// bumps the row and re-attributes it, rather than filling the collection with
// copies of the same sentence.
func TestLedgerLearningRepository_UpsertDedupes(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewLedgerLearningRepository(db)
	if err := repo.EnsureIndexes(ctx); err != nil {
		t.Fatalf("ensure indexes: %v", err)
	}

	key := commonmodels.NormalizedLearningKey("schema", "order_status 'C' means cancelled")
	first := &commonmodels.LedgerLearning{
		ID: "l1", ProjectID: "p1", Category: "schema", Note: "order_status 'C' means cancelled",
		Relevance: 0.9, RunID: "r1", DiscoveryID: "d1", NormalizedKey: key,
	}
	if err := repo.Upsert(ctx, first); err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	// A later run says the same thing, with a trivially different wording that
	// normalizes to the same key.
	again := &commonmodels.LedgerLearning{
		ID: "l2", ProjectID: "p1", Category: "Schema", Note: "order_status  'C' means cancelled!",
		Relevance: 0.8, RunID: "r2", DiscoveryID: "d2",
		NormalizedKey: commonmodels.NormalizedLearningKey("Schema", "order_status  'C' means cancelled!"),
	}
	if err := repo.Upsert(ctx, again); err != nil {
		t.Fatalf("upsert again: %v", err)
	}

	got, err := repo.List(ctx, "p1", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 deduped learning, got %d: %+v", len(got), got)
	}
	if got[0].ID != "l1" {
		t.Errorf("the original row must be kept, got %q", got[0].ID)
	}
	if got[0].SeenCount != 2 {
		t.Errorf("seen_count = %d, want 2", got[0].SeenCount)
	}
	if got[0].RunID != "r2" || got[0].DiscoveryID != "d2" {
		t.Errorf("attribution should follow the latest run, got %s/%s", got[0].RunID, got[0].DiscoveryID)
	}
	if got[0].CreatedAt.IsZero() || got[0].UpdatedAt.IsZero() {
		t.Errorf("timestamps not stamped: %+v", got[0])
	}

	// A learning with no key cannot be deduped, so it is refused rather than
	// written as an unaddressable row.
	if err := repo.Upsert(ctx, &commonmodels.LedgerLearning{ID: "l3", ProjectID: "p1", Note: "x"}); err == nil {
		t.Error("a learning with no normalized key must be refused")
	}

	// Another project's identical learning is its own row.
	if err := repo.Upsert(ctx, &commonmodels.LedgerLearning{
		ID: "l4", ProjectID: "p2", Category: "schema", Note: "order_status 'C' means cancelled",
		RunID: "r1", NormalizedKey: key,
	}); err != nil {
		t.Fatalf("upsert other project: %v", err)
	}
	other, err := repo.List(ctx, "p2", 0)
	if err != nil {
		t.Fatalf("list p2: %v", err)
	}
	if len(other) != 1 || other[0].ID != "l4" {
		t.Errorf("p2 should have its own row, got %+v", other)
	}

	// The limit is honoured.
	limited, err := repo.List(ctx, "p1", 1)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limit not honoured, got %d", len(limited))
	}
}
