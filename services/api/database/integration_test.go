//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	goauth "github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	gomongo "github.com/decisionbox-io/decisionbox/libs/go-common/mongodb"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/bson"
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
				Question:     "What causes churn?",
				Answer:       "Based on insights [1]...",
				Model:        "claude-sonnet",
				InputTokens:  100,
				OutputTokens: 50,
				CreatedAt:    time.Now(),
			},
		},
	}

	// Create
	if err := repo.Create(ctx, session); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get
	got, err := repo.GetByID(ctx, "proj-integ-1", "user-1", "session-integ-1")
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
	err = repo.AppendMessage(ctx, "proj-integ-1", "user-1", "session-integ-1", commonmodels.AskSessionMessage{
		Question:     "Tell me more about Level 45",
		Answer:       "Level 45 shows...",
		Model:        "claude-sonnet",
		InputTokens:  140,
		OutputTokens: 60,
		CreatedAt:    time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	got, _ = repo.GetByID(ctx, "proj-integ-1", "user-1", "session-integ-1")
	if got.MessageCount != 2 {
		t.Errorf("MessageCount after append = %d, want 2", got.MessageCount)
	}
	if len(got.Messages) != 2 {
		t.Errorf("Messages len = %d, want 2", len(got.Messages))
	}

	// List by project
	sessions, err := repo.ListByProject(ctx, "proj-integ-1", "user-1", 10, "", "")
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

	// Seed-scoped listing: create two seeded sessions for the same project but
	// different insights, then confirm the seed filter returns only the matching
	// one and projects the seed ref (not the bulky text).
	seededA := &commonmodels.AskSession{
		ID: "session-seed-a", ProjectID: "proj-integ-1", UserID: "user-1", Title: "about insight A",
		SeedContext: &commonmodels.AskSessionSeed{Type: "insight", ID: "ins-A", Label: "Insight A", Text: "long text A"},
	}
	seededB := &commonmodels.AskSession{
		ID: "session-seed-b", ProjectID: "proj-integ-1", UserID: "user-1", Title: "about insight B",
		SeedContext: &commonmodels.AskSessionSeed{Type: "insight", ID: "ins-B", Label: "Insight B", Text: "long text B"},
	}
	if err := repo.Create(ctx, seededA); err != nil {
		t.Fatalf("create seededA: %v", err)
	}
	if err := repo.Create(ctx, seededB); err != nil {
		t.Fatalf("create seededB: %v", err)
	}
	filtered, err := repo.ListByProject(ctx, "proj-integ-1", "user-1", 10, "insight", "ins-A")
	if err != nil {
		t.Fatalf("ListByProject seed filter: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != "session-seed-a" {
		t.Fatalf("seed filter should return only session-seed-a, got %d: %+v", len(filtered), filtered)
	}
	if filtered[0].SeedContext == nil || filtered[0].SeedContext.ID != "ins-A" || filtered[0].SeedContext.Label != "Insight A" {
		t.Fatalf("seed ref not projected: %+v", filtered[0].SeedContext)
	}
	if filtered[0].SeedContext.Text != "" {
		t.Errorf("bulky seed text should be excluded from the list projection, got %q", filtered[0].SeedContext.Text)
	}
	// A non-matching seed id returns nothing.
	if none, _ := repo.ListByProject(ctx, "proj-integ-1", "user-1", 10, "insight", "ins-ZZZ"); len(none) != 0 {
		t.Errorf("seed filter for unknown id should be empty, got %d", len(none))
	}

	// Delete
	err = repo.Delete(ctx, "proj-integ-1", "user-1", "session-integ-1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = repo.GetByID(ctx, "proj-integ-1", "user-1", "session-integ-1")
	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("after delete: err = %v, want ErrAskSessionNotFound", err)
	}
	// Deleting it again matched nothing and must say so rather than report success.
	if err := repo.Delete(ctx, "proj-integ-1", "user-1", "session-integ-1"); !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("second delete: err = %v, want ErrAskSessionNotFound", err)
	}
}

// TestInteg_AskSessionRepo_CreateWithNilMessages_ThenAppend pins the
// contract that an agentic flow can create a session before knowing
// the first message and persist via AppendMessage afterwards. Before
// the fix, Create({Messages: nil}) wrote `messages: null` to Mongo
// and the subsequent AppendMessage's `$push` failed with "the field
// 'messages' must be an array but is of type null".
func TestInteg_AskSessionRepo_CreateWithNilMessages_ThenAppend(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)

	session := &commonmodels.AskSession{
		ID:        "session-nil-msgs",
		ProjectID: "proj-nil-msgs",
		UserID:    "user-1",
		Title:     "Empty on create",
		Messages:  nil, // intentional — agentic handler creates session first, persists later
	}
	if err := repo.Create(ctx, session); err != nil {
		t.Fatalf("Create with nil Messages: %v", err)
	}

	// AppendMessage must succeed even though the session was created
	// with no messages — the repository normalises nil → [] on insert.
	err := repo.AppendMessage(ctx, "proj-nil-msgs", "user-1", "session-nil-msgs", commonmodels.AskSessionMessage{
		Question:  "first turn",
		Answer:    "ok",
		Model:     "claude",
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("AppendMessage on nil-Messages session: %v", err)
	}

	got, err := repo.GetByID(ctx, "proj-nil-msgs", "user-1", "session-nil-msgs")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.MessageCount != 1 || len(got.Messages) != 1 {
		t.Fatalf("expected 1 message after append; got count=%d len=%d", got.MessageCount, len(got.Messages))
	}
	if got.Messages[0].Question != "first turn" {
		t.Fatalf("Messages[0].Question = %q", got.Messages[0].Question)
	}

	_ = repo.Delete(ctx, "proj-nil-msgs", "user-1", "session-nil-msgs")
}

// TestInteg_AskSessionRepo_AppendToLegacyNullSession is the
// backward-compat half: a row written by an older build that left
// `messages: null` must still be appendable via the new aggregation-
// pipeline AppendMessage. We can't get the BSON encoder to produce
// `null` through the Go API any more (Create now normalises), so the
// test seeds the document directly.
func TestInteg_AskSessionRepo_AppendToLegacyNullSession(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)

	if _, err := testDB.Collection("ask_sessions").InsertOne(ctx, bson.M{
		"_id":           "session-legacy-null",
		"project_id":    "proj-legacy",
		"user_id":       "user-1",
		"title":         "legacy",
		"messages":      nil,
		"message_count": nil,
		"created_at":    time.Now(),
		"updated_at":    time.Now(),
	}); err != nil {
		t.Fatalf("seed legacy doc: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, "proj-legacy", "user-1", "session-legacy-null") })

	if err := repo.AppendMessage(ctx, "proj-legacy", "user-1", "session-legacy-null", commonmodels.AskSessionMessage{
		Question:  "first turn after legacy null",
		Answer:    "ok",
		Model:     "claude",
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("AppendMessage on legacy null session: %v", err)
	}
	got, err := repo.GetByID(ctx, "proj-legacy", "user-1", "session-legacy-null")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.MessageCount != 1 || len(got.Messages) != 1 {
		t.Fatalf("legacy null append: count=%d len=%d, want 1/1", got.MessageCount, len(got.Messages))
	}

	// The repair branch runs the same owner-scoped filter as the fast path, so it
	// is not a way around the key.
	if err := repo.AppendMessage(ctx, "proj-legacy", "someone-else", "session-legacy-null", commonmodels.AskSessionMessage{
		Question: "not mine", Answer: "no", CreatedAt: time.Now(),
	}); !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("append by another user: err = %v, want ErrAskSessionNotFound", err)
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

// TestInteg_AskSessionRepo_OwnerScoping is the isolation contract: a
// conversation is reachable only through a key that carries its owner. It
// mirrors the bookmark-list cases (TestInteg_BookmarkList_GetByID_WrongUser),
// because a conversation is the same kind of per-user data.
func TestInteg_AskSessionRepo_OwnerScoping(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)
	const project = "proj-owner"

	seed := []*commonmodels.AskSession{
		{ID: "own-a1", ProjectID: project, UserID: "alice", Title: "alice one"},
		{ID: "own-a2", ProjectID: project, UserID: "alice", Title: "alice two"},
		{ID: "own-b1", ProjectID: project, UserID: "bob", Title: "bob one"},
		{ID: "own-a3", ProjectID: "proj-owner-other", UserID: "alice", Title: "alice elsewhere"},
	}
	for _, s := range seed {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
		id := s.ID
		pid := s.ProjectID
		t.Cleanup(func() { _ = repo.Delete(ctx, pid, "", id) })
	}

	// Read
	if _, err := repo.GetByID(ctx, project, "alice", "own-a1"); err != nil {
		t.Errorf("alice reading her own session: %v", err)
	}
	if _, err := repo.GetByID(ctx, project, "bob", "own-a1"); !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("bob reading alice's session: err = %v, want ErrAskSessionNotFound", err)
	}
	if _, err := repo.GetByID(ctx, "proj-owner-other", "alice", "own-a1"); !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("alice reading her session through the wrong project: err = %v, want ErrAskSessionNotFound", err)
	}

	// List
	got, err := repo.ListByProject(ctx, project, "alice", 10, "", "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("alice's list = %d sessions, want 2 (not bob's, not her other project): %+v", len(got), got)
	}
	// The internal-caller contract: an empty owner sees the whole project.
	if all, _ := repo.ListByProject(ctx, project, "", 10, "", ""); len(all) != 3 {
		t.Errorf("unscoped list = %d, want 3", len(all))
	}

	// Write
	msg := commonmodels.AskSessionMessage{Question: "q", Answer: "a", CreatedAt: time.Now()}
	if err := repo.AppendMessage(ctx, project, "bob", "own-a1", msg); !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("bob appending to alice's session: err = %v, want ErrAskSessionNotFound", err)
	}
	after, _ := repo.GetByID(ctx, project, "alice", "own-a1")
	if after.MessageCount != 0 || len(after.Messages) != 0 {
		t.Errorf("a refused append still wrote: count=%d len=%d", after.MessageCount, len(after.Messages))
	}

	// Delete
	if err := repo.Delete(ctx, project, "bob", "own-a1"); !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("bob deleting alice's session: err = %v, want ErrAskSessionNotFound", err)
	}
	if _, err := repo.GetByID(ctx, project, "alice", "own-a1"); err != nil {
		t.Errorf("a refused delete removed the document anyway: %v", err)
	}
	if err := repo.Delete(ctx, project, "alice", "own-a1"); err != nil {
		t.Errorf("alice deleting her own session: %v", err)
	}
}

// TestInteg_AskSessionRepo_LegacyAnonymousIsShared covers the compatibility
// rule: a session written before per-user scoping carries the NoAuth subject
// and no real owner, so it stays readable by everyone rather than becoming
// invisible to the people already reading it. A session with no owner at all is
// a row we cannot explain, and is shared with nobody.
func TestInteg_AskSessionRepo_LegacyAnonymousIsShared(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)
	const project = "proj-legacy-shared"

	for _, s := range []*commonmodels.AskSession{
		{ID: "leg-shared", ProjectID: project, UserID: goauth.AnonymousSubject, Title: "written with auth off"},
		{ID: "leg-alice", ProjectID: project, UserID: "alice", Title: "alice's own"},
	} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
		id := s.ID
		t.Cleanup(func() { _ = repo.Delete(ctx, project, "", id) })
	}
	// A document with no user_id field at all, as only a direct write can make.
	if _, err := testDB.Collection("ask_sessions").InsertOne(ctx, bson.M{
		"_id": "leg-ownerless", "project_id": project, "title": "no owner",
		"messages": []interface{}{}, "message_count": 0,
		"created_at": time.Now(), "updated_at": time.Now(),
	}); err != nil {
		t.Fatalf("seed ownerless: %v", err)
	}
	t.Cleanup(func() { _ = repo.Delete(ctx, project, "", "leg-ownerless") })

	for _, caller := range []string{"alice", "bob"} {
		if _, err := repo.GetByID(ctx, project, caller, "leg-shared"); err != nil {
			t.Errorf("%s reading the legacy shared session: %v", caller, err)
		}
		if _, err := repo.GetByID(ctx, project, caller, "leg-ownerless"); !errors.Is(err, ErrAskSessionNotFound) {
			t.Errorf("%s reading the ownerless session: err = %v, want ErrAskSessionNotFound", caller, err)
		}
	}

	// bob sees the shared one and nothing of alice's.
	got, err := repo.ListByProject(ctx, project, "bob", 10, "", "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(got) != 1 || got[0].ID != "leg-shared" {
		t.Fatalf("bob's list = %+v, want only leg-shared", got)
	}

	// With authentication off the caller IS the shared subject, so the list is
	// the whole project's anonymous history exactly as it was — and still not
	// the row we cannot explain.
	got, _ = repo.ListByProject(ctx, project, goauth.AnonymousSubject, 10, "", "")
	if len(got) != 1 || got[0].ID != "leg-shared" {
		t.Fatalf("anonymous caller's list = %+v, want only leg-shared", got)
	}
}
