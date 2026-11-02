//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func setupMongoDB(t *testing.T) (*DB, func()) {
	t.Helper()
	ctx := context.Background()

	container, err := mongodb.Run(ctx, "mongo:7.0")
	if err != nil {
		t.Fatalf("failed to start MongoDB container: %v", err)
	}

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	mongoCfg := gomongo.DefaultConfig()
	mongoCfg.URI = connStr
	mongoCfg.Database = "test_decisionbox"

	client, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		t.Fatalf("failed to connect to MongoDB: %v", err)
	}

	cleanup := func() {
		client.Disconnect(ctx)
		container.Terminate(ctx)
	}

	return New(client), cleanup
}

func TestProjectRepository_CRUD(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewProjectRepository(db)

	// Insert a project the production way: `_id` is a Mongo-generated
	// ObjectId. The agent's GetByID accepts only the hex form of that
	// ObjectId — same shape the API writes when handling POST /projects.
	oid := primitive.NewObjectID()
	doc := bson.M{
		"_id":      oid,
		"name":     "Test Game",
		"domain":   "gaming",
		"category": "match3",
		"warehouse": bson.M{
			"provider": "bigquery",
			"datasets": []string{"test_dataset"},
		},
		"llm": bson.M{
			"provider": "claude",
			"model":    "claude-sonnet-4-20250514",
		},
		"status":     "active",
		"created_at": time.Now(),
		"updated_at": time.Now(),
	}

	col := db.Collection(CollectionProjects)
	if _, err := col.InsertOne(ctx, doc); err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}

	// Get by ID
	got, err := repo.GetByID(ctx, oid.Hex())
	if err != nil {
		t.Fatalf("GetByID error: %v", err)
	}
	if got.Name != "Test Game" {
		t.Errorf("Name = %q, want %q", got.Name, "Test Game")
	}
	if got.Domain != "gaming" {
		t.Errorf("Domain = %q, want %q", got.Domain, "gaming")
	}
	if got.Category != "match3" {
		t.Errorf("Category = %q, want %q", got.Category, "match3")
	}
}

func TestProjectRepository_NotFound(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewProjectRepository(db)

	// Well-formed hex but no matching document.
	_, err := repo.GetByID(ctx, primitive.NewObjectID().Hex())
	if err == nil {
		t.Error("should error for nonexistent project")
	}
}

// Non-hex IDs are rejected at the boundary. Production data always uses
// ObjectId — any string that can't be parsed as 24-char hex is a typo,
// stale URL, or misrouted request, and we want it to fail fast.
func TestProjectRepository_RejectsNonHexID(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()

	repo := NewProjectRepository(db)
	if _, err := repo.GetByID(context.Background(), "test-project-1"); err == nil {
		t.Error("should error for non-hex project id")
	}
}

func TestContextRepository_SaveAndGet(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewContextRepository(db)
	_ = repo.EnsureIndexes(ctx)

	// Get nonexistent — should return new context
	pctx, err := repo.GetByProjectID(ctx, "proj-new")
	if err != nil {
		t.Fatalf("GetByProjectID error: %v", err)
	}
	if pctx.ProjectID != "proj-new" {
		t.Error("should create new context for unknown project")
	}

	// Save context
	pctx.AddNote("test", "learned something", 0.9)
	pctx.RecordDiscovery(true)

	err = repo.Save(ctx, pctx)
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Retrieve and verify
	got, err := repo.GetByProjectID(ctx, "proj-new")
	if err != nil {
		t.Fatalf("GetByProjectID after save error: %v", err)
	}
	if got.TotalDiscoveries != 1 {
		t.Errorf("TotalDiscoveries = %d, want 1", got.TotalDiscoveries)
	}
	if len(got.Notes) != 1 {
		t.Errorf("Notes = %d, want 1", len(got.Notes))
	}
}

func TestContextRepository_Upsert(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewContextRepository(db)
	_ = repo.EnsureIndexes(ctx)

	// Save twice — should upsert, not duplicate
	pctx := models.NewProjectContext("proj-upsert")
	pctx.RecordDiscovery(true)
	_ = repo.Save(ctx, pctx)

	pctx.RecordDiscovery(true)
	_ = repo.Save(ctx, pctx)

	got, _ := repo.GetByProjectID(ctx, "proj-upsert")
	if got.TotalDiscoveries != 2 {
		t.Errorf("TotalDiscoveries = %d, want 2", got.TotalDiscoveries)
	}
}

func TestDiscoveryRepository_SaveAndGet(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryRepository(db)
	_ = repo.EnsureIndexes(ctx)

	result := &models.DiscoveryResult{
		ProjectID:     "proj-1",
		Domain:        "gaming",
		Category:      "match3",
		DiscoveryDate: time.Now(),
		TotalSteps:    50,
		Insights: []models.Insight{
			{ID: "i1", AnalysisArea: "churn", Name: "Test Churn", AffectedCount: 100},
			{ID: "i2", AnalysisArea: "levels", Name: "Level 42", AffectedCount: 50},
		},
		Recommendations: []models.Recommendation{
			{ID: "r1", Title: "Fix Churn", Priority: 5},
		},
	}

	err := repo.Save(ctx, result)
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Per-area dialog persists into discovery_analysis_steps now (the
	// orchestrator calls SaveAnalysisSteps after the parent doc save).
	logRepo := NewDiscoveryLogRepository(db)
	if err := logRepo.SaveAnalysisSteps(ctx, result.ProjectID, result.ID, "", []models.AnalysisStep{
		{AreaID: "churn", Prompt: "analyze...", Response: "{}", TokensIn: 500, TokensOut: 200},
	}); err != nil {
		t.Fatalf("SaveAnalysisSteps: %v", err)
	}

	got, err := repo.GetLatest(ctx, "proj-1")
	if err != nil {
		t.Fatalf("GetLatest error: %v", err)
	}
	if got == nil {
		t.Fatal("should return saved result")
	}
	if len(got.Insights) != 2 {
		t.Errorf("Insights = %d, want 2", len(got.Insights))
	}
	if len(got.Recommendations) != 1 {
		t.Errorf("Recommendations = %d, want 1", len(got.Recommendations))
	}
	steps, err := logRepo.ListAnalysisStepsByDiscovery(ctx, got.ID)
	if err != nil {
		t.Fatalf("ListAnalysisStepsByDiscovery: %v", err)
	}
	if len(steps) != 1 {
		t.Errorf("AnalysisSteps = %d, want 1", len(steps))
	}
	if steps[0].TokensIn != 500 {
		t.Errorf("TokensIn = %d, want 500", steps[0].TokensIn)
	}
}

func TestDiscoveryRepository_GetLatestOrder(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryRepository(db)
	_ = repo.EnsureIndexes(ctx)

	// Save older discovery
	old := &models.DiscoveryResult{
		ProjectID:     "proj-order",
		DiscoveryDate: time.Now().Add(-24 * time.Hour),
		TotalSteps:    10,
		Insights:      []models.Insight{{ID: "old"}},
	}
	_ = repo.Save(ctx, old)

	// Save newer discovery
	newer := &models.DiscoveryResult{
		ProjectID:     "proj-order",
		DiscoveryDate: time.Now(),
		TotalSteps:    20,
		Insights:      []models.Insight{{ID: "new"}},
	}
	_ = repo.Save(ctx, newer)

	got, _ := repo.GetLatest(ctx, "proj-order")
	if got.TotalSteps != 20 {
		t.Errorf("should return newest, got TotalSteps=%d", got.TotalSteps)
	}
}

func TestDiscoveryRepository_GetLatestNotFound(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryRepository(db)

	got, err := repo.GetLatest(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got != nil {
		t.Error("should return nil for nonexistent project")
	}
}

func TestDebugLogRepository_EnsureIndexes(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDebugLogRepository(db, true)
	err := repo.EnsureIndexes(ctx)
	if err != nil {
		t.Fatalf("EnsureIndexes error: %v", err)
	}
}

// TestDiscoveryRepository_ListByRun_CarriesTheAttempt pins the field that
// bounds retirement, against real Mongo because what can go wrong here is
// storage behaviour a fake cannot reproduce.
//
// The case that matters is the document written BEFORE this field existed: it
// has no `attempt` key at all, decodes to 0, and must read back as 1. Without
// that normalisation every pre-upgrade result looks like attempt 0, which is
// lower than everything, so a resumed run would happily retire results it has
// no claim on — and the strict less-than bound would protect nothing on
// exactly the documents least able to defend themselves.
//
// (`omitempty` does NOT make attempt 1 absent — it omits the zero value, and
// 1 is not zero. A freshly written first attempt stores `attempt: 1`. The
// legacy document below is inserted raw for that reason.)
func TestDiscoveryRepository_ListByRun_CarriesTheAttempt(t *testing.T) {
	db, cleanup := setupMongoDB(t)
	defer cleanup()
	ctx := context.Background()

	repo := NewDiscoveryRepository(db)
	_ = repo.EnsureIndexes(ctx)

	// Attempt 1, written the way the orchestrator writes it: omitempty means
	// no `attempt` key reaches the document.
	first := &models.DiscoveryResult{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 1,
		DiscoveryDate: time.Now().Add(-time.Minute),
	}
	if err := repo.Save(ctx, first); err != nil {
		t.Fatalf("save attempt 1: %v", err)
	}
	// The resumed attempt.
	second := &models.DiscoveryResult{
		ProjectID: "proj-1", RunID: "run-1", Attempt: 2,
		DiscoveryDate: time.Now(),
	}
	if err := repo.Save(ctx, second); err != nil {
		t.Fatalf("save attempt 2: %v", err)
	}
	// A result from another run must not appear at all.
	other := &models.DiscoveryResult{
		ProjectID: "proj-1", RunID: "run-2", Attempt: 1,
		DiscoveryDate: time.Now(),
	}
	if err := repo.Save(ctx, other); err != nil {
		t.Fatalf("save other run: %v", err)
	}

	// A pre-upgrade result: inserted raw, with no `attempt` key at all, which
	// is what every document written before the field existed looks like.
	raw := db.Collection("discoveries")
	legacyID := primitive.NewObjectID()
	if _, err := raw.InsertOne(ctx, bson.M{
		"_id":            legacyID,
		"project_id":     "proj-1",
		"run_id":         "run-1",
		"discovery_date": time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("insert the legacy result: %v", err)
	}
	var stored bson.M
	if err := raw.FindOne(ctx, bson.M{"_id": legacyID}).Decode(&stored); err != nil {
		t.Fatalf("read the legacy result back: %v", err)
	}
	if _, present := stored["attempt"]; present {
		t.Fatalf("the legacy fixture stored an `attempt` field (%v); it is meant to have none", stored["attempt"])
	}

	refs, err := repo.ListByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("ListByRun: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("got %d refs for run-1, want 3: %+v", len(refs), refs)
	}

	// Newest first, as the sort promises.
	got := map[string]int{}
	for _, r := range refs {
		got[r.ID] = r.Attempt
	}
	if got[second.ID] != 2 {
		t.Errorf("attempt 2's result came back as attempt %d", got[second.ID])
	}
	if got[first.ID] != 1 {
		t.Errorf("attempt 1's result came back as attempt %d, want 1", got[first.ID])
	}
	if got[legacyID.Hex()] != 1 {
		t.Errorf("the pre-upgrade result came back as attempt %d, want 1 — an absent attempt must normalise, or a strict less-than bound protects nothing on legacy documents", got[legacyID.Hex()])
	}
	if _, leaked := got[other.ID]; leaked {
		t.Error("a result from another run came back; retirement would delete it")
	}
}
