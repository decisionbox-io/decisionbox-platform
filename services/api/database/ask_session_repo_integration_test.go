//go:build integration

package database

import (
	"context"
	"errors"
	"testing"
	"time"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// seedSession inserts a session and nudges updated_at so ordering is
// deterministic (Create stamps time.Now(); a tiny sleep between calls
// keeps the sequence distinct).
func seedSession(t *testing.T, repo *AskSessionRepository, id, projectID, userID, title string) {
	t.Helper()
	if err := repo.Create(context.Background(), &commonmodels.AskSession{
		ID:        id,
		ProjectID: projectID,
		UserID:    userID,
		Title:     title,
	}); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
	time.Sleep(5 * time.Millisecond)
}

func TestInteg_AskSessionRepo_ListByProjectAndUser(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)

	const proj = "asp-proj-1"
	// u1: two sessions in proj (s2 newer than s1); u2: one in proj;
	// u1 also has one in a different project (must not leak in).
	seedSession(t, repo, "asp-s1", proj, "u1", "first")
	seedSession(t, repo, "asp-s2", proj, "u1", "second")
	seedSession(t, repo, "asp-s3", proj, "u2", "other user")
	seedSession(t, repo, "asp-s4", "asp-proj-2", "u1", "other project")
	seedSession(t, repo, "asp-anon", proj, "anonymous", "legacy anon")

	// u1 sees only their two sessions in this project, newest first.
	got, err := repo.ListByProjectAndUser(ctx, proj, "u1", 20)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("u1 list len = %d, want 2 (got %v)", len(got), ids(got))
	}
	if got[0].ID != "asp-s2" || got[1].ID != "asp-s1" {
		t.Errorf("u1 order = %v, want [asp-s2 asp-s1] (updated_at desc)", ids(got))
	}

	// limit is honoured.
	oneOnly, err := repo.ListByProjectAndUser(ctx, proj, "u1", 1)
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	if len(oneOnly) != 1 || oneOnly[0].ID != "asp-s2" {
		t.Errorf("limit=1 = %v, want [asp-s2]", ids(oneOnly))
	}

	// The other user's session is scoped out.
	u2, _ := repo.ListByProjectAndUser(ctx, proj, "u2", 20)
	if len(u2) != 1 || u2[0].ID != "asp-s3" {
		t.Errorf("u2 list = %v, want [asp-s3]", ids(u2))
	}

	// Legacy anonymous sessions remain retrievable for the anonymous caller.
	anon, _ := repo.ListByProjectAndUser(ctx, proj, "anonymous", 20)
	if len(anon) != 1 || anon[0].ID != "asp-anon" {
		t.Errorf("anon list = %v, want [asp-anon]", ids(anon))
	}
}

func TestInteg_AskSessionRepo_UpdateTitle(t *testing.T) {
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)

	seedSession(t, repo, "upd-s1", "upd-proj", "u1", "original")
	before, err := repo.GetByID(ctx, "upd-s1")
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	if err := repo.UpdateTitle(ctx, "upd-s1", "renamed"); err != nil {
		t.Fatalf("update title: %v", err)
	}

	after, err := repo.GetByID(ctx, "upd-s1")
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.Title != "renamed" {
		t.Errorf("title = %q, want renamed", after.Title)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at not bumped: before=%v after=%v", before.UpdatedAt, after.UpdatedAt)
	}

	// Unknown id surfaces ErrAskSessionNotFound so the handler can 404.
	err = repo.UpdateTitle(ctx, "upd-nonexistent", "x")
	if err == nil {
		t.Fatal("update title on unknown id returned nil, want error")
	}
	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("update title unknown id error = %v, want wrapping ErrAskSessionNotFound", err)
	}
}

func ids(sessions []*commonmodels.AskSession) []string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.ID
	}
	return out
}
