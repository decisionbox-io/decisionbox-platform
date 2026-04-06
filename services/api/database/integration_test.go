//go:build integration

package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
)

var testDB *DB

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := tcmongo.Run(ctx, "mongo:7.0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB start failed: %v\n", err)
		os.Exit(1)
	}
	defer container.Terminate(ctx)

	uri, _ := container.ConnectionString(ctx)
	cfg := gomongo.DefaultConfig()
	cfg.URI = uri
	cfg.Database = "db_repo_integration_test"

	client, err := gomongo.NewClient(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "MongoDB connect failed: %v\n", err)
		os.Exit(1)
	}
	defer client.Disconnect(ctx)

	testDB = New(client)
	if err := InitDatabase(ctx, testDB); err != nil {
		fmt.Fprintf(os.Stderr, "InitDatabase failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// --- InsightRepository ---

func TestInteg_InsightRepo_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	repo := NewInsightRepository(testDB)

	insight := &commonmodels.StandaloneInsight{
		ID:           "ins-integ-1",
		ProjectID:    "proj-integ-1",
		DiscoveryID:  "disc-integ-1",
		Domain:       "gaming",
		Category:     "match3",
		AnalysisArea: "churn",
		Name:         "High churn at Level 45",
		Description:  "Players leaving after tutorial",
		Severity:     "high",
		Confidence:   0.85,
		CreatedAt:    time.Now(),
	}

	if err := repo.Create(ctx, insight); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, "ins-integ-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "High churn at Level 45" {
		t.Errorf("Name = %q, want %q", got.Name, "High churn at Level 45")
	}
	if got.ProjectID != "proj-integ-1" {
		t.Errorf("ProjectID = %q", got.ProjectID)
	}
}

func TestInteg_InsightRepo_ListByProject(t *testing.T) {
	ctx := context.Background()
	repo := NewInsightRepository(testDB)

	// Create multiple insights for different projects
	for i := 0; i < 3; i++ {
		repo.Create(ctx, &commonmodels.StandaloneInsight{
			ID:          fmt.Sprintf("ins-list-%d", i),
			ProjectID:   "proj-list-1",
			DiscoveryID: "disc-list-1",
			Name:        fmt.Sprintf("Insight %d", i),
			Severity:    "medium",
			CreatedAt:   time.Now().Add(time.Duration(i) * time.Minute),
		})
	}
	repo.Create(ctx, &commonmodels.StandaloneInsight{
		ID:          "ins-list-other",
		ProjectID:   "proj-list-2",
		DiscoveryID: "disc-list-2",
		Name:        "Other project insight",
		CreatedAt:   time.Now(),
	})

	// List by project
	results, err := repo.ListByProject(ctx, "proj-list-1", 50, 0)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 insights, got %d", len(results))
	}

	// Verify ordering (newest first)
	if len(results) >= 2 && results[0].CreatedAt.Before(results[1].CreatedAt) {
		t.Error("results should be ordered newest first")
	}

	// List with limit
	results, err = repo.ListByProject(ctx, "proj-list-1", 2, 0)
	if err != nil {
		t.Fatalf("ListByProject with limit: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 with limit, got %d", len(results))
	}

	// List with offset
	results, err = repo.ListByProject(ctx, "proj-list-1", 50, 2)
	if err != nil {
		t.Fatalf("ListByProject with offset: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 with offset=2, got %d", len(results))
	}
}

func TestInteg_InsightRepo_CountAndEmbedding(t *testing.T) {
	ctx := context.Background()
	repo := NewInsightRepository(testDB)

	count, err := repo.CountByProject(ctx, "proj-list-1")
	if err != nil {
		t.Fatalf("CountByProject: %v", err)
	}
	if count < 3 {
		t.Errorf("count = %d, want >= 3", count)
	}

	// Update embedding
	err = repo.UpdateEmbedding(ctx, "ins-integ-1", "embedded text here", "text-embedding-3-small")
	if err != nil {
		t.Fatalf("UpdateEmbedding: %v", err)
	}

	got, _ := repo.GetByID(ctx, "ins-integ-1")
	if got.EmbeddingText != "embedded text here" {
		t.Errorf("EmbeddingText = %q", got.EmbeddingText)
	}
	if got.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("EmbeddingModel = %q", got.EmbeddingModel)
	}

	// GetLatestEmbeddingModel
	model, err := repo.GetLatestEmbeddingModel(ctx, "proj-integ-1")
	if err != nil {
		t.Fatalf("GetLatestEmbeddingModel: %v", err)
	}
	if model != "text-embedding-3-small" {
		t.Errorf("latest model = %q", model)
	}
}

func TestInteg_InsightRepo_UpdateDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := NewInsightRepository(testDB)

	err := repo.UpdateDuplicate(ctx, "ins-integ-1", "ins-original-1", 0.97)
	if err != nil {
		t.Fatalf("UpdateDuplicate: %v", err)
	}

	got, _ := repo.GetByID(ctx, "ins-integ-1")
	if got.DuplicateOf != "ins-original-1" {
		t.Errorf("DuplicateOf = %q", got.DuplicateOf)
	}
	if got.SimilarityScore != 0.97 {
		t.Errorf("SimilarityScore = %f", got.SimilarityScore)
	}
}

func TestInteg_InsightRepo_ListByDiscovery(t *testing.T) {
	ctx := context.Background()
	repo := NewInsightRepository(testDB)

	results, err := repo.ListByDiscovery(ctx, "disc-integ-1")
	if err != nil {
		t.Fatalf("ListByDiscovery: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 insight for disc-integ-1, got %d", len(results))
	}
}

// --- RecommendationRepository ---

func TestInteg_RecRepo_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	repo := NewRecommendationRepository(testDB)

	rec := &commonmodels.StandaloneRecommendation{
		ID:          "rec-integ-1",
		ProjectID:   "proj-integ-1",
		DiscoveryID: "disc-integ-1",
		Title:       "Add retry mechanics",
		Description: "Implement retries at Level 45",
		Priority:    1,
		Confidence:  0.78,
		ExpectedImpact: commonmodels.ExpectedImpact{
			Metric:               "D7 retention",
			EstimatedImprovement: "15-20%",
		},
		CreatedAt: time.Now(),
	}

	if err := repo.Create(ctx, rec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, "rec-integ-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Title != "Add retry mechanics" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.ExpectedImpact.Metric != "D7 retention" {
		t.Errorf("Impact.Metric = %q", got.ExpectedImpact.Metric)
	}
}

func TestInteg_RecRepo_ListAndCount(t *testing.T) {
	ctx := context.Background()
	repo := NewRecommendationRepository(testDB)

	results, err := repo.ListByProject(ctx, "proj-integ-1", 50, 0)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(results) < 1 {
		t.Errorf("expected >= 1, got %d", len(results))
	}

	count, err := repo.CountByProject(ctx, "proj-integ-1")
	if err != nil {
		t.Fatalf("CountByProject: %v", err)
	}
	if count < 1 {
		t.Errorf("count = %d", count)
	}
}

func TestInteg_RecRepo_EmbeddingAndDuplicate(t *testing.T) {
	ctx := context.Background()
	repo := NewRecommendationRepository(testDB)

	err := repo.UpdateEmbedding(ctx, "rec-integ-1", "rec embed text", "test-model")
	if err != nil {
		t.Fatalf("UpdateEmbedding: %v", err)
	}

	err = repo.UpdateDuplicate(ctx, "rec-integ-1", "rec-original-1", 0.96)
	if err != nil {
		t.Fatalf("UpdateDuplicate: %v", err)
	}

	got, _ := repo.GetByID(ctx, "rec-integ-1")
	if got.EmbeddingText != "rec embed text" {
		t.Errorf("EmbeddingText = %q", got.EmbeddingText)
	}
	if got.DuplicateOf != "rec-original-1" {
		t.Errorf("DuplicateOf = %q", got.DuplicateOf)
	}
}

// --- SearchHistoryRepository ---

func TestInteg_SearchHistoryRepo_SaveAndList(t *testing.T) {
	ctx := context.Background()
	repo := NewSearchHistoryRepository(testDB)

	entry := &commonmodels.SearchHistory{
		ID:             "sh-integ-1",
		UserID:         "user-1",
		ProjectID:      "proj-integ-1",
		Query:          "why is churn high?",
		Type:           "search",
		ResultsCount:   5,
		TopResultScore: 0.89,
		CreatedAt:      time.Now(),
	}

	if err := repo.Save(ctx, entry); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Save another
	repo.Save(ctx, &commonmodels.SearchHistory{
		ID:        "sh-integ-2",
		UserID:    "user-1",
		ProjectID: "proj-integ-1",
		Query:     "retention patterns",
		Type:      "search",
		CreatedAt: time.Now(),
	})

	// List by project
	results, err := repo.ListByProject(ctx, "proj-integ-1", 10)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected >= 2 history entries, got %d", len(results))
	}

	// List by user
	results, err = repo.ListByUser(ctx, "user-1", 10)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected >= 2 user entries, got %d", len(results))
	}
}

// --- AskSessionRepository ---

func TestInteg_AskSessionRepo_CRUD(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)

	session := &commonmodels.AskSession{
		ID:        "session-integ-1",
		ProjectID: "proj-integ-1",
		UserID:    "user-1",
		Title:     "What causes churn?",
		Messages: []commonmodels.AskSessionMessage{
			{
				Question:   "What causes churn?",
				Answer:     "Based on insights [1]...",
				Model:      "claude-sonnet",
				TokensUsed: 150,
				CreatedAt:  time.Now(),
			},
		},
	}

	// Create
	if err := repo.Create(ctx, session); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get
	got, err := repo.GetByID(ctx, "session-integ-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Title != "What causes churn?" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.MessageCount != 1 {
		t.Errorf("MessageCount = %d, want 1", got.MessageCount)
	}

	// Append message
	err = repo.AppendMessage(ctx, "session-integ-1", commonmodels.AskSessionMessage{
		Question:   "Tell me more about Level 45",
		Answer:     "Level 45 shows...",
		Model:      "claude-sonnet",
		TokensUsed: 200,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	got, _ = repo.GetByID(ctx, "session-integ-1")
	if got.MessageCount != 2 {
		t.Errorf("MessageCount after append = %d, want 2", got.MessageCount)
	}
	if len(got.Messages) != 2 {
		t.Errorf("Messages len = %d, want 2", len(got.Messages))
	}

	// List by project
	sessions, err := repo.ListByProject(ctx, "proj-integ-1", 10)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(sessions) < 1 {
		t.Errorf("expected >= 1 session, got %d", len(sessions))
	}
	// List should exclude messages (projection)
	if len(sessions[0].Messages) > 0 {
		t.Error("ListByProject should exclude messages (projection)")
	}

	// Delete
	err = repo.Delete(ctx, "session-integ-1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = repo.GetByID(ctx, "session-integ-1")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestInteg_InsightRepo_CreateMany(t *testing.T) {
	ctx := context.Background()
	repo := NewInsightRepository(testDB)

	insights := []*commonmodels.StandaloneInsight{
		{ID: "ins-many-1", ProjectID: "proj-many", Name: "Insight A", CreatedAt: time.Now()},
		{ID: "ins-many-2", ProjectID: "proj-many", Name: "Insight B", CreatedAt: time.Now()},
	}

	if err := repo.CreateMany(ctx, insights); err != nil {
		t.Fatalf("CreateMany: %v", err)
	}

	results, _ := repo.ListByProject(ctx, "proj-many", 50, 0)
	if len(results) != 2 {
		t.Errorf("expected 2, got %d", len(results))
	}
}

func TestInteg_RecRepo_CreateMany(t *testing.T) {
	ctx := context.Background()
	repo := NewRecommendationRepository(testDB)

	recs := []*commonmodels.StandaloneRecommendation{
		{ID: "rec-many-1", ProjectID: "proj-many", Title: "Rec A", CreatedAt: time.Now()},
		{ID: "rec-many-2", ProjectID: "proj-many", Title: "Rec B", CreatedAt: time.Now()},
	}

	if err := repo.CreateMany(ctx, recs); err != nil {
		t.Fatalf("CreateMany: %v", err)
	}

	results, _ := repo.ListByProject(ctx, "proj-many", 50, 0)
	if len(results) != 2 {
		t.Errorf("expected 2, got %d", len(results))
	}
}
