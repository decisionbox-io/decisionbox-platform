//go:build integration

package database

import (
	"context"
	"testing"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
)

func setupTestMongoDB(t *testing.T) (*DB, func()) {
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
	mongoCfg.Database = "test_domain_packs"

	client, err := gomongo.NewClient(ctx, mongoCfg)
	if err != nil {
		t.Fatalf("failed to connect to MongoDB: %v", err)
	}

	db := New(client)

	// Initialize indexes (same as production)
	if err := InitDatabase(ctx, db); err != nil {
		t.Fatalf("InitDatabase failed: %v", err)
	}

	cleanup := func() {
		client.Disconnect(ctx)
		container.Terminate(ctx)
	}

	return db, cleanup
}

func testPack(slug string) *models.DomainPack {
	return &models.DomainPack{
		Slug:        slug,
		Name:        slug + " Pack",
		Description: "Test pack for " + slug,
		Version:     "1.0.0",
		IsPublished: true,
		Categories: []models.PackCategory{
			{ID: "default", Name: "Default", Description: "Default category"},
		},
		Prompts: models.PackPrompts{
			Base: models.BasePrompts{
				BaseContext:     "{{PROFILE}}",
				Exploration:     "explore {{DATASET}} {{SCHEMA_INFO}} {{ANALYSIS_AREAS}}",
				Recommendations: "recommend {{INSIGHTS_DATA}}",
			},
			Categories: map[string]models.CategoryPrompts{},
		},
		AnalysisAreas: models.PackAnalysisAreas{
			Base: []models.PackAnalysisArea{
				{ID: "area1", Name: "Area 1", Description: "Test", Keywords: []string{"test"}, Priority: 1, Prompt: "analyze"},
			},
			Categories: map[string][]models.PackAnalysisArea{},
		},
		ProfileSchema: models.PackProfileSchema{
			Base:       map[string]interface{}{"type": "object"},
			Categories: map[string]map[string]interface{}{},
		},
	}
}

func TestDomainPackRepo_Create(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	pack := testPack("gaming")
	err := repo.Create(ctx, pack)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if pack.ID == "" {
		t.Error("ID should be set after create")
	}
	if pack.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if pack.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestDomainPackRepo_Create_DuplicateSlug(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	pack1 := testPack("gaming")
	if err := repo.Create(ctx, pack1); err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	pack2 := testPack("gaming")
	err := repo.Create(ctx, pack2)
	if err == nil {
		t.Fatal("second Create should fail with duplicate slug")
	}
	if !contains(err.Error(), "already exists") {
		t.Errorf("error = %q, should mention 'already exists'", err.Error())
	}
}

func TestDomainPackRepo_GetBySlug(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	repo.Create(ctx, testPack("gaming"))

	// Found
	pack, err := repo.GetBySlug(ctx, "gaming")
	if err != nil {
		t.Fatalf("GetBySlug error: %v", err)
	}
	if pack == nil {
		t.Fatal("pack should not be nil")
	}
	if pack.Slug != "gaming" {
		t.Errorf("slug = %q", pack.Slug)
	}
	if pack.Name != "gaming Pack" {
		t.Errorf("name = %q", pack.Name)
	}

	// Not found
	pack, err = repo.GetBySlug(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetBySlug nonexistent error: %v", err)
	}
	if pack != nil {
		t.Error("should return nil for nonexistent slug")
	}
}

func TestDomainPackRepo_GetByID(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	created := testPack("gaming")
	repo.Create(ctx, created)

	// Found
	pack, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID error: %v", err)
	}
	if pack == nil {
		t.Fatal("pack should not be nil")
	}
	if pack.Slug != "gaming" {
		t.Errorf("slug = %q", pack.Slug)
	}

	// Not found
	pack, err = repo.GetByID(ctx, "000000000000000000000000")
	if err != nil {
		t.Fatalf("GetByID not found error: %v", err)
	}
	if pack != nil {
		t.Error("should return nil for nonexistent ID")
	}

	// Invalid ID format
	pack, err = repo.GetByID(ctx, "not-an-oid")
	if err != nil {
		t.Fatalf("GetByID invalid error: %v", err)
	}
	if pack != nil {
		t.Error("should return nil for invalid ObjectID")
	}
}

func TestDomainPackRepo_List(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	// Empty
	packs, err := repo.List(ctx, false)
	if err != nil {
		t.Fatalf("List empty error: %v", err)
	}
	if len(packs) != 0 {
		t.Errorf("expected 0, got %d", len(packs))
	}

	// Create 3 packs (2 published, 1 unpublished)
	gaming := testPack("gaming")
	gaming.IsPublished = true
	repo.Create(ctx, gaming)

	social := testPack("social")
	social.IsPublished = true
	repo.Create(ctx, social)

	draft := testPack("draft")
	draft.IsPublished = false
	repo.Create(ctx, draft)

	// List all
	packs, err = repo.List(ctx, false)
	if err != nil {
		t.Fatalf("List all error: %v", err)
	}
	if len(packs) != 3 {
		t.Errorf("List all = %d, want 3", len(packs))
	}

	// List published only
	packs, err = repo.List(ctx, true)
	if err != nil {
		t.Fatalf("List published error: %v", err)
	}
	if len(packs) != 2 {
		t.Errorf("List published = %d, want 2", len(packs))
	}
	for _, p := range packs {
		if !p.IsPublished {
			t.Errorf("unpublished pack %q in published-only list", p.Slug)
		}
	}

	// Verify sorted by name
	if packs[0].Name > packs[1].Name {
		t.Errorf("not sorted by name: %q > %q", packs[0].Name, packs[1].Name)
	}
}

func TestDomainPackRepo_Update(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	original := testPack("gaming")
	repo.Create(ctx, original)

	// Update
	updated := testPack("gaming")
	updated.Name = "Updated Gaming"
	updated.Description = "New description"
	err := repo.Update(ctx, "gaming", updated)
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}

	// Verify
	pack, _ := repo.GetBySlug(ctx, "gaming")
	if pack.Name != "Updated Gaming" {
		t.Errorf("name = %q, want 'Updated Gaming'", pack.Name)
	}
	if pack.Description != "New description" {
		t.Errorf("description = %q", pack.Description)
	}

	// Update nonexistent
	err = repo.Update(ctx, "nonexistent", testPack("nonexistent"))
	if err == nil {
		t.Error("should fail for nonexistent slug")
	}
}

func TestDomainPackRepo_Delete(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	repo.Create(ctx, testPack("gaming"))

	// Delete
	err := repo.Delete(ctx, "gaming")
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	// Verify gone
	pack, _ := repo.GetBySlug(ctx, "gaming")
	if pack != nil {
		t.Error("pack should be deleted")
	}

	// Delete nonexistent
	err = repo.Delete(ctx, "nonexistent")
	if err == nil {
		t.Error("should fail for nonexistent slug")
	}
}

func TestDomainPackRepo_FullDocumentRoundTrip(t *testing.T) {
	db, cleanup := setupTestMongoDB(t)
	defer cleanup()
	ctx := context.Background()
	repo := NewDomainPackRepository(db)

	// Create a pack with all fields populated
	pack := &models.DomainPack{
		Slug:        "full-test",
		Name:        "Full Test",
		Description: "Tests all fields round-trip through MongoDB",
		Version:     "2.1.0",
		IsPublished: true,
		Categories: []models.PackCategory{
			{ID: "cat1", Name: "Category 1", Description: "First"},
			{ID: "cat2", Name: "Category 2", Description: "Second"},
		},
		Prompts: models.PackPrompts{
			Base: models.BasePrompts{
				BaseContext:     "base context with {{PROFILE}}",
				Exploration:     "explore {{DATASET}} {{SCHEMA_INFO}} {{ANALYSIS_AREAS}}",
				Recommendations: "recommend {{INSIGHTS_DATA}}",
			},
			Categories: map[string]models.CategoryPrompts{
				"cat1": {ExplorationContext: "cat1 extra context"},
			},
		},
		AnalysisAreas: models.PackAnalysisAreas{
			Base: []models.PackAnalysisArea{
				{ID: "churn", Name: "Churn", Description: "Churn analysis",
					Keywords: []string{"churn", "retention"}, Priority: 1, Prompt: "analyze churn"},
				{ID: "engagement", Name: "Engagement", Description: "Engagement analysis",
					Keywords: []string{"session", "dau"}, Priority: 2, Prompt: "analyze engagement"},
			},
			Categories: map[string][]models.PackAnalysisArea{
				"cat1": {
					{ID: "cat1-specific", Name: "Cat1 Area", Description: "Category-specific",
						Keywords: []string{"cat1"}, Priority: 3, Prompt: "analyze cat1"},
				},
			},
		},
		ProfileSchema: models.PackProfileSchema{
			Base: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"info": map[string]interface{}{"type": "object", "properties": map[string]interface{}{
						"name": map[string]interface{}{"type": "string"},
					}},
				},
			},
			Categories: map[string]map[string]interface{}{
				"cat1": {"properties": map[string]interface{}{
					"extra": map[string]interface{}{"type": "string"},
				}},
			},
		},
	}

	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	// Read back
	got, err := repo.GetBySlug(ctx, "full-test")
	if err != nil {
		t.Fatalf("GetBySlug error: %v", err)
	}

	// Verify all fields survived the round-trip
	if got.Version != "2.1.0" {
		t.Errorf("version = %q", got.Version)
	}
	if len(got.Categories) != 2 {
		t.Errorf("categories = %d", len(got.Categories))
	}
	if got.Prompts.Base.BaseContext != "base context with {{PROFILE}}" {
		t.Errorf("base_context = %q", got.Prompts.Base.BaseContext)
	}
	if got.Prompts.Categories["cat1"].ExplorationContext != "cat1 extra context" {
		t.Errorf("cat1 exploration_context = %q", got.Prompts.Categories["cat1"].ExplorationContext)
	}
	if len(got.AnalysisAreas.Base) != 2 {
		t.Errorf("base areas = %d", len(got.AnalysisAreas.Base))
	}
	if got.AnalysisAreas.Base[0].Keywords[0] != "churn" {
		t.Errorf("area keywords = %v", got.AnalysisAreas.Base[0].Keywords)
	}
	catAreas := got.AnalysisAreas.Categories["cat1"]
	if len(catAreas) != 1 || catAreas[0].ID != "cat1-specific" {
		t.Errorf("cat1 areas = %v", catAreas)
	}
	if got.ProfileSchema.Base["type"] != "object" {
		t.Errorf("base schema type = %v", got.ProfileSchema.Base["type"])
	}
	if got.ProfileSchema.Categories["cat1"] == nil {
		t.Error("cat1 profile schema should exist")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
