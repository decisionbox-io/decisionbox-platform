//go:build integration

package backfill

import (
	"context"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/bson"
)

// The investigation-graph backfill, against a real MongoDB.
//
// The behaviour worth testing here is almost entirely about restraint: what it
// refuses to derive, and whether it says so. A backfill that silently produced
// a plausible edge for everything would pass a happy-path test and be worse
// than useless on a real project.

func backfillMongo(t *testing.T) (*gomongo.Client, func()) {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "backfill_ledger_graph_test"
	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	return client, func() {
		_ = client.Disconnect(ctx)
		_ = container.Terminate(ctx)
	}
}

// seedLegacyLedger writes the shape a project that has been running since
// before any of this existed actually has: findings with a normalized key, a
// source discovery and a status, and no provenance whatsoever.
func seedLegacyLedger(t *testing.T, client *gomongo.Client, projectID string) {
	t.Helper()
	ctx := context.Background()
	firstSeen := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)

	insights := []any{
		// Exactly matches a finding's area + name.
		bson.M{"_id": "ins-1", "project_id": projectID, "discovery_id": "disc-1",
			"analysis_area": "churn", "name": "High EU churn"},
		// The same finding surfaced again by a later run: both are real sources.
		bson.M{"_id": "ins-1b", "project_id": projectID, "discovery_id": "disc-2",
			"analysis_area": "churn", "name": "High EU churn"},
		// A REWORDING of the dead-stock finding. The semantic-dedup path would
		// have merged this at write time; an exact key match cannot, and must
		// not pretend otherwise.
		bson.M{"_id": "ins-2", "project_id": projectID, "discovery_id": "disc-1",
			"analysis_area": "inventory", "name": "Unsold stock sits with a handful of sellers"},
		// Another project's insight with the same name — must never be linked.
		bson.M{"_id": "ins-other", "project_id": "other-project", "discovery_id": "disc-x",
			"analysis_area": "churn", "name": "High EU churn"},
	}
	if _, err := client.Collection("insights").InsertMany(ctx, insights); err != nil {
		t.Fatalf("seed insights: %v", err)
	}

	findings := []any{
		bson.M{
			"_id": "f-churn", "project_id": projectID, "area": "churn", "name": "High EU churn",
			"normalized_key": commonmodels.NormalizedFindingKey("churn", "High EU churn"),
			"severity":       "high", "status": commonmodels.LedgerFindingStatusConfirmed,
			"seen_count": 3, "affected_count": 240, "key_metric": "affected=240",
			"first_seen": firstSeen, "last_seen": firstSeen.Add(72 * time.Hour),
			"source_discovery_id": "disc-2", "updated_at": firstSeen.Add(72 * time.Hour),
		},
		bson.M{
			"_id": "f-stock", "project_id": projectID, "area": "inventory",
			"name":           "Dead stock concentrated in a few sellers",
			"normalized_key": commonmodels.NormalizedFindingKey("inventory", "Dead stock concentrated in a few sellers"),
			"severity":       "medium", "status": commonmodels.LedgerFindingStatusMonitoring,
			"seen_count": 1, "first_seen": firstSeen, "last_seen": firstSeen,
			"source_discovery_id": "disc-1", "updated_at": firstSeen,
		},
		// Already linked by a post-#389 run: must be left exactly as it is.
		bson.M{
			"_id": "f-fresh", "project_id": projectID, "area": "fraud", "name": "Card testing",
			"normalized_key": commonmodels.NormalizedFindingKey("fraud", "Card testing"),
			"severity":       "critical", "status": commonmodels.LedgerFindingStatusConfirmed,
			"seen_count": 1, "first_seen": firstSeen, "last_seen": firstSeen,
			"source_discovery_id": "disc-2", "updated_at": firstSeen,
			"source_insight_ids": []string{"ins-kept"},
			"sightings":          []bson.M{{"run_id": "run-kept", "discovery_id": "disc-2"}},
			"status_history":     []bson.M{{"status": "confirmed", "source": "reflection"}},
		},
	}
	if _, err := client.Collection(gomongo.CollectionDiscoveryLedgerFindings).InsertMany(ctx, findings); err != nil {
		t.Fatalf("seed findings: %v", err)
	}
}

func TestBackfillLedgerGraph_LinksWhatItCanAndReportsWhatItCannot(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	ctx := context.Background()
	const projectID = "p-legacy"
	seedLegacyLedger(t, client, projectID)

	// A single run, well away from any task timestamp.
	completed := time.Date(2026, 1, 13, 9, 5, 0, 0, time.UTC)
	if _, err := client.Collection(gomongo.CollectionDiscoveryRuns).InsertMany(ctx, []any{
		bson.M{"_id": "run-1", "project_id": projectID, "discovery_id": "disc-1",
			"started_at": completed.Add(-72 * time.Hour), "completed_at": completed.Add(-71 * time.Hour)},
		bson.M{"_id": "run-2", "project_id": projectID, "discovery_id": "disc-2",
			"started_at": completed.Add(-time.Hour), "completed_at": completed},
	}); err != nil {
		t.Fatalf("seed runs: %v", err)
	}

	counts, err := backfillLedgerGraphProject(ctx, client, projectID, 10, defaultCloseWindow, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	if counts.FindingsScanned != 3 {
		t.Errorf("scanned = %d, want 3", counts.FindingsScanned)
	}
	if counts.FindingsLinked != 1 {
		t.Errorf("linked = %d, want 1 (only the exact key match)", counts.FindingsLinked)
	}
	// The reworded one is the honest gap, and the count is how an operator
	// learns it exists.
	if counts.FindingsUnlinkable != 1 {
		t.Errorf("unlinkable = %d, want 1 (the reworded insight)", counts.FindingsUnlinkable)
	}
	if counts.FindingsAlreadyLinked != 1 {
		t.Errorf("already linked = %d, want 1", counts.FindingsAlreadyLinked)
	}

	stored := readFindings(t, client, projectID)

	churn := stored["f-churn"]
	// Both insights behind the key are sources — each really did produce it.
	if len(churn.SourceInsightIDs) != 2 {
		t.Errorf("f-churn should link both insights behind its key, got %v", churn.SourceInsightIDs)
	}
	for _, id := range churn.SourceInsightIDs {
		if id == "ins-other" {
			t.Error("another project's insight must never be linked")
		}
	}
	// One synthetic sighting from first_seen + source_discovery_id, resolved to
	// the run that produced that discovery. The other two sightings behind
	// seen_count=3 are unrecoverable and are not invented.
	if len(churn.Sightings) != 1 {
		t.Fatalf("want exactly 1 synthetic sighting, got %d: %+v", len(churn.Sightings), churn.Sightings)
	}
	if churn.Sightings[0].DiscoveryID != "disc-2" || churn.Sightings[0].RunID != "run-2" {
		t.Errorf("sighting should resolve its run through discovery_runs: %+v", churn.Sightings[0])
	}
	if churn.SeenCount != 3 {
		t.Errorf("seen_count must stay the honest total, got %d", churn.SeenCount)
	}
	if len(churn.StatusHistory) != 1 ||
		churn.StatusHistory[0].Source != commonmodels.FindingStatusSourceBackfill ||
		churn.StatusHistory[0].Status != commonmodels.LedgerFindingStatusConfirmed {
		t.Errorf("status trail should hold one derived entry: %+v", churn.StatusHistory)
	}
	// A derived entry carries no reason. Nothing stored says why the status
	// moved, and a made-up reason would read exactly like a real one.
	if churn.StatusHistory[0].Reason != "" {
		t.Errorf("a derived status entry must invent no reason, got %q", churn.StatusHistory[0].Reason)
	}

	stock := stored["f-stock"]
	if len(stock.SourceInsightIDs) != 0 {
		t.Errorf("a reworded insight must be left unlinked, not guessed: %v", stock.SourceInsightIDs)
	}
	// It still gets its sighting and status trail — those are derivable even
	// when the provenance is not.
	if len(stock.Sightings) != 1 || len(stock.StatusHistory) != 1 {
		t.Errorf("an unlinkable finding still gets its derivable trails: %+v", stock)
	}

	fresh := stored["f-fresh"]
	if len(fresh.SourceInsightIDs) != 1 || fresh.SourceInsightIDs[0] != "ins-kept" {
		t.Errorf("an already-linked finding must not be touched: %v", fresh.SourceInsightIDs)
	}
	if len(fresh.Sightings) != 1 || fresh.Sightings[0].RunID != "run-kept" {
		t.Errorf("an existing sighting trail must not be overwritten: %+v", fresh.Sightings)
	}
}

func TestBackfillLedgerGraph_DryRunWritesNothing(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	ctx := context.Background()
	const projectID = "p-dry"
	seedLegacyLedger(t, client, projectID)

	counts, err := backfillLedgerGraphProject(ctx, client, projectID, 10, defaultCloseWindow, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	// It still reports exactly what it would do — that is the point of the mode.
	if counts.FindingsLinked != 1 || counts.FindingsUnlinkable != 1 {
		t.Errorf("a dry run must still report its plan: %+v", counts)
	}
	stored := readFindings(t, client, projectID)
	if len(stored["f-churn"].SourceInsightIDs) != 0 || len(stored["f-churn"].Sightings) != 0 {
		t.Errorf("dry run wrote to the database: %+v", stored["f-churn"])
	}
}

func TestBackfillLedgerGraph_IsIdempotent(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	ctx := context.Background()
	const projectID = "p-twice"
	seedLegacyLedger(t, client, projectID)

	first, err := backfillLedgerGraphProject(ctx, client, projectID, 10, defaultCloseWindow, false)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	afterFirst := readFindings(t, client, projectID)

	second, err := backfillLedgerGraphProject(ctx, client, projectID, 10, defaultCloseWindow, false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	// Nothing left to do: every field the second run would write is already
	// there, so an interrupted run is simply repeated.
	if second.FindingsLinked != 0 || second.SightingsWritten != 0 || second.StatusTrailsWritten != 0 {
		t.Errorf("a second run should be a no-op, got %+v", second)
	}
	if second.FindingsAlreadyLinked != first.FindingsAlreadyLinked+first.FindingsLinked {
		t.Errorf("the second run should see the first run's links as already linked: %+v", second)
	}
	afterSecond := readFindings(t, client, projectID)
	for id, f := range afterSecond {
		if len(f.Sightings) != len(afterFirst[id].Sightings) {
			t.Errorf("%s sightings grew on a re-run: %d → %d", id, len(afterFirst[id].Sightings), len(f.Sightings))
		}
	}
}

// TestBackfillLedgerGraph_ClosingRunOnlyWhenUnambiguous is the inference this
// backfill is most likely to get wrong, so it is the one that refuses hardest:
// closed_at is simply the task's last write, but the RUN that closed it is
// stamped only when exactly one run is near that time.
func TestBackfillLedgerGraph_ClosingRunOnlyWhenUnambiguous(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	ctx := context.Background()
	const projectID = "p-tasks"

	closedAt := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	if _, err := client.Collection(gomongo.CollectionDiscoveryLedgerTasks).InsertMany(ctx, []any{
		// One run nearby → resolvable.
		bson.M{"_id": "t-clear", "project_id": projectID, "text": "a", "kind": "next_task",
			"status": commonmodels.LedgerTaskStatusDone, "normalized_key": "k1",
			"created_at": closedAt.Add(-48 * time.Hour), "updated_at": closedAt},
		// Two runs nearby → must stay unset.
		bson.M{"_id": "t-ambiguous", "project_id": projectID, "text": "b", "kind": "next_task",
			"status": commonmodels.LedgerTaskStatusDropped, "normalized_key": "k2",
			"created_at": closedAt.Add(-48 * time.Hour), "updated_at": closedAt.Add(240 * time.Hour)},
		// No run anywhere near → closed_at only.
		bson.M{"_id": "t-orphan", "project_id": projectID, "text": "c", "kind": "next_task",
			"status": commonmodels.LedgerTaskStatusDone, "normalized_key": "k3",
			"created_at": closedAt.Add(-48 * time.Hour), "updated_at": closedAt.Add(1000 * time.Hour)},
		// Still open: a close stamp would be a lie.
		bson.M{"_id": "t-open", "project_id": projectID, "text": "d", "kind": "next_task",
			"status": commonmodels.LedgerTaskStatusOpen, "normalized_key": "k4",
			"created_at": closedAt.Add(-48 * time.Hour), "updated_at": closedAt},
	}); err != nil {
		t.Fatalf("seed tasks: %v", err)
	}
	if _, err := client.Collection(gomongo.CollectionDiscoveryRuns).InsertMany(ctx, []any{
		bson.M{"_id": "run-clear", "project_id": projectID, "discovery_id": "disc-clear",
			"started_at": closedAt.Add(-2 * time.Hour), "completed_at": closedAt.Add(-time.Hour)},
		// Two runs within the window of t-ambiguous's updated_at.
		bson.M{"_id": "run-a", "project_id": projectID, "discovery_id": "disc-a",
			"started_at": closedAt.Add(238 * time.Hour), "completed_at": closedAt.Add(239 * time.Hour)},
		bson.M{"_id": "run-b", "project_id": projectID, "discovery_id": "disc-b",
			"started_at": closedAt.Add(240 * time.Hour), "completed_at": closedAt.Add(241 * time.Hour)},
	}); err != nil {
		t.Fatalf("seed runs: %v", err)
	}

	counts, err := backfillLedgerGraphProject(ctx, client, projectID, 10, defaultCloseWindow, false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if counts.TasksScanned != 3 {
		t.Errorf("only closed tasks are scanned, got %d", counts.TasksScanned)
	}
	if counts.TasksClosedStamped != 3 {
		t.Errorf("every closed task gets its closed_at, got %d", counts.TasksClosedStamped)
	}
	if counts.TasksCloseRunResolved != 1 {
		t.Errorf("only the unambiguous task gets a closing run, got %d", counts.TasksCloseRunResolved)
	}
	// The two reasons a closing run is unset are reported apart, because they
	// call for different responses: a tighter window, or nothing at all.
	if counts.TasksCloseRunAmbiguous != 1 {
		t.Errorf("one task had several candidate runs, got %d", counts.TasksCloseRunAmbiguous)
	}
	if counts.TasksCloseRunNoRun != 1 {
		t.Errorf("one task had no candidate run at all, got %d", counts.TasksCloseRunNoRun)
	}

	tasks := readTasks(t, client, projectID)
	if tasks["t-clear"].ClosedRunID != "run-clear" || tasks["t-clear"].ClosedDiscoveryID != "disc-clear" {
		t.Errorf("t-clear: %+v", tasks["t-clear"])
	}
	if tasks["t-ambiguous"].ClosedRunID != "" {
		t.Errorf("two candidate runs means the answer is unknown, got %q", tasks["t-ambiguous"].ClosedRunID)
	}
	if tasks["t-ambiguous"].ClosedAt.IsZero() {
		t.Error("closed_at is still derivable even when the run is not")
	}
	if tasks["t-orphan"].ClosedRunID != "" || tasks["t-orphan"].ClosedAt.IsZero() {
		t.Errorf("t-orphan: %+v", tasks["t-orphan"])
	}
	if !tasks["t-open"].ClosedAt.IsZero() {
		t.Errorf("an open task must never be stamped closed: %+v", tasks["t-open"])
	}
	// resolved_by_finding_ids is never derived, for any of them.
	for id, task := range tasks {
		if len(task.ResolvedByFindingIDs) != 0 {
			t.Errorf("%s: the findings that answered a task are not derivable and must not be guessed: %v",
				id, task.ResolvedByFindingIDs)
		}
	}
}

// TestBackfillLedgerGraph_EmptyProject — a project with no ledger at all must
// be a clean no-op rather than an error, since the all-projects mode walks
// whatever is there.
func TestBackfillLedgerGraph_EmptyProject(t *testing.T) {
	client, cleanup := backfillMongo(t)
	defer cleanup()
	counts, err := backfillLedgerGraphProject(context.Background(), client, "p-nothing", 10, defaultCloseWindow, false)
	if err != nil {
		t.Fatalf("empty project: %v", err)
	}
	if counts.FindingsScanned != 0 || counts.TasksScanned != 0 {
		t.Errorf("nothing to scan: %+v", counts)
	}
}

// TestSoleRunNear covers the window rule directly, including the boundary.
func TestSoleRunNear(t *testing.T) {
	at := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	runs := []runRef{
		{RunID: "r1", DiscoveryID: "d1", At: at.Add(-time.Hour)},
		{RunID: "r2", DiscoveryID: "d2", At: at.Add(48 * time.Hour)},
	}
	if id, disc, n := soleRunNear(runs, at, 6*time.Hour); n != 1 || id != "r1" || disc != "d1" {
		t.Errorf("one run in the window should resolve: %q %q %d", id, disc, n)
	}
	// Two candidates and none are reported apart: the first says to try a
	// tighter window, the second that there is nothing to match against.
	if _, _, n := soleRunNear(runs, at, 72*time.Hour); n != 2 {
		t.Errorf("a window containing both runs is ambiguous, got %d", n)
	}
	if _, _, n := soleRunNear(runs, at.Add(500*time.Hour), 6*time.Hour); n != 0 {
		t.Errorf("no run in the window should report none, got %d", n)
	}
	if _, _, n := soleRunNear(nil, at, 6*time.Hour); n != 0 {
		t.Errorf("no runs at all should report none, got %d", n)
	}
	// Inclusive boundary: a run exactly at the edge counts.
	if _, _, n := soleRunNear(runs[:1], at, time.Hour); n != 1 {
		t.Error("a run exactly at the window edge should count")
	}
}

func readFindings(t *testing.T, client *gomongo.Client, projectID string) map[string]commonmodels.LedgerFinding {
	t.Helper()
	cur, err := client.Collection(gomongo.CollectionDiscoveryLedgerFindings).
		Find(context.Background(), bson.M{"project_id": projectID})
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	var rows []commonmodels.LedgerFinding
	if err := cur.All(context.Background(), &rows); err != nil {
		t.Fatalf("decode findings: %v", err)
	}
	out := make(map[string]commonmodels.LedgerFinding, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

func readTasks(t *testing.T, client *gomongo.Client, projectID string) map[string]commonmodels.LedgerTask {
	t.Helper()
	cur, err := client.Collection(gomongo.CollectionDiscoveryLedgerTasks).
		Find(context.Background(), bson.M{"project_id": projectID})
	if err != nil {
		t.Fatalf("read tasks: %v", err)
	}
	var rows []commonmodels.LedgerTask
	if err := cur.All(context.Background(), &rows); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	out := make(map[string]commonmodels.LedgerTask, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}
