//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
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

	// Insert a project document directly
	project := models.Project{
		ID:       "test-project-1",
		Name:     "Test Game",
		Domain:   "gaming",
		Category: "match3",
		Warehouse: models.WarehouseConfig{
			Provider: "bigquery",
			Dataset:  "test_dataset",
		},
		LLM: models.LLMConfig{
			Provider: "claude",
			Model:    "claude-sonnet-4-20250514",
		},
		Status:    "active",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Insert directly
	col := db.Collection(CollectionProjects)
	_, err := col.InsertOne(ctx, project)
	if err != nil {
		t.Fatalf("failed to insert project: %v", err)
	}

	// Get by ID
	got, err := repo.GetByID(ctx, "test-project-1")
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

	_, err := repo.GetByID(ctx, "nonexistent")
	if err == nil {
		t.Error("should error for nonexistent project")
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
		AnalysisLog: []models.AnalysisStep{
			{AreaID: "churn", Prompt: "analyze...", Response: "{}", TokensIn: 500, TokensOut: 200},
		},
	}

	err := repo.Save(ctx, result)
	if err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Get latest
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
	if len(got.AnalysisLog) != 1 {
		t.Errorf("AnalysisLog = %d, want 1", len(got.AnalysisLog))
	}
	if got.AnalysisLog[0].TokensIn != 500 {
		t.Errorf("TokensIn = %d, want 500", got.AnalysisLog[0].TokensIn)
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
