//go:build integration

package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	goauth "github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// UpdateTitle's correctness is almost entirely about the lookup key, and the
// key is a Mongo filter — a mock that reimplements it proves only that the
// reimplementation agrees with itself. These run against real Mongo.

// seedTitleSessions writes the fixture set once per test, under a project id
// unique to the caller so tests sharing the package's container cannot
// interfere with each other.
func seedTitleSessions(t *testing.T, projectID string) *AskSessionRepository {
	t.Helper()
	ctx := context.Background()
	repo := NewAskSessionRepository(testDB)

	for _, s := range []*commonmodels.AskSession{
		{ID: projectID + "-alice", ProjectID: projectID, UserID: "alice", Title: "alice original", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: projectID + "-bob", ProjectID: projectID, UserID: "bob", Title: "bob original", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: projectID + "-legacy", ProjectID: projectID, UserID: goauth.AnonymousSubject, Title: "shared original", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: projectID + "-ownerless", ProjectID: projectID, UserID: "", Title: "unexplained original", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		{ID: projectID + "-elsewhere", ProjectID: projectID + "-other", UserID: "alice", Title: "other project", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	} {
		if err := repo.Create(ctx, s); err != nil {
			t.Fatalf("seed %s: %v", s.ID, err)
		}
	}
	return repo
}

func titleOf(t *testing.T, repo *AskSessionRepository, projectID, sessionID string) string {
	t.Helper()
	// userID "" skips the owner filter — this is a test reading the stored
	// document, not a request path.
	got, err := repo.GetByID(context.Background(), projectID, "", sessionID)
	if err != nil {
		t.Fatalf("read back %s: %v", sessionID, err)
	}
	return got.Title
}

func TestInteg_AskSessionRepo_UpdateTitle_OwnerRenamesAndBumpsUpdatedAt(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-owner"
	repo := seedTitleSessions(t, pid)

	before, err := repo.GetByID(ctx, pid, "alice", pid+"-alice")
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	// Mongo stores millisecond precision, so a same-instant write could read
	// back as equal. Wait past that rather than asserting on a tie.
	time.Sleep(5 * time.Millisecond)

	if err := repo.UpdateTitle(ctx, pid, "alice", pid+"-alice", "Q3 churn"); err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}

	after, err := repo.GetByID(ctx, pid, "alice", pid+"-alice")
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if after.Title != "Q3 churn" {
		t.Errorf("title = %q, want %q", after.Title, "Q3 churn")
	}
	// A renamed conversation has been touched, so it must sort as recent in
	// the list the client shows.
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at did not advance: before %s, after %s", before.UpdatedAt, after.UpdatedAt)
	}
	// Renaming must not disturb anything else on the document.
	if after.MessageCount != before.MessageCount || len(after.Messages) != len(before.Messages) {
		t.Errorf("rename changed the message set: %d/%d messages, %d/%d count",
			len(after.Messages), len(before.Messages), after.MessageCount, before.MessageCount)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("rename moved created_at: %s → %s", before.CreatedAt, after.CreatedAt)
	}
}

func TestInteg_AskSessionRepo_UpdateTitle_WrongOwnerDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-wrong-owner"
	repo := seedTitleSessions(t, pid)

	err := repo.UpdateTitle(ctx, pid, "bob", pid+"-alice", "bob was here")

	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("err = %v, want ErrAskSessionNotFound", err)
	}
	// The important half: the refusal is a no-op, not just a non-200.
	if got := titleOf(t, repo, pid, pid+"-alice"); got != "alice original" {
		t.Errorf("alice's title = %q, want it untouched by bob", got)
	}
}

func TestInteg_AskSessionRepo_UpdateTitle_WrongProjectDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-wrong-project"
	repo := seedTitleSessions(t, pid)

	// The session is alice's, but it lives in a different project.
	err := repo.UpdateTitle(ctx, pid, "alice", pid+"-elsewhere", "cross-project")

	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("err = %v, want ErrAskSessionNotFound", err)
	}
	if got := titleOf(t, repo, pid+"-other", pid+"-elsewhere"); got != "other project" {
		t.Errorf("title = %q, want it untouched across projects", got)
	}
}

func TestInteg_AskSessionRepo_UpdateTitle_UnknownIDIsNotFound(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-unknown"
	seedTitleSessions(t, pid)
	repo := NewAskSessionRepository(testDB)

	err := repo.UpdateTitle(ctx, pid, "alice", pid+"-does-not-exist", "ghost")

	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("err = %v, want ErrAskSessionNotFound", err)
	}
}

// TestInteg_AskSessionRepo_UpdateTitle_SharedAndOwnerlessRows pins the two
// halves of the legacy rule the read path already follows: a session written
// before per-user scoping (owner == the NoAuth subject) stays reachable by
// everyone, while one with an unexplained empty owner is reachable by nobody.
func TestInteg_AskSessionRepo_UpdateTitle_SharedAndOwnerlessRows(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-legacy"
	repo := seedTitleSessions(t, pid)

	if err := repo.UpdateTitle(ctx, pid, "alice", pid+"-legacy", "renamed shared"); err != nil {
		t.Errorf("renaming a shared legacy session: err = %v, want nil", err)
	}
	if got := titleOf(t, repo, pid, pid+"-legacy"); got != "renamed shared" {
		t.Errorf("shared session title = %q, want it renamed", got)
	}

	err := repo.UpdateTitle(ctx, pid, "alice", pid+"-ownerless", "claimed")
	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("ownerless session: err = %v, want ErrAskSessionNotFound", err)
	}
	if got := titleOf(t, repo, pid, pid+"-ownerless"); got != "unexplained original" {
		t.Errorf("ownerless session title = %q, want it untouched", got)
	}
}

// TestInteg_AskSessionRepo_UpdateTitle_AnonymousCallerMatchesOnlySharedRows
// covers the other side of the NoAuth rule: with authentication off the caller
// IS the shared subject, so it reaches the shared rows and not a real user's.
func TestInteg_AskSessionRepo_UpdateTitle_AnonymousCallerMatchesOnlySharedRows(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-anon-caller"
	repo := seedTitleSessions(t, pid)

	if err := repo.UpdateTitle(ctx, pid, goauth.AnonymousSubject, pid+"-legacy", "noauth rename"); err != nil {
		t.Errorf("shared row: err = %v, want nil", err)
	}

	err := repo.UpdateTitle(ctx, pid, goauth.AnonymousSubject, pid+"-alice", "noauth takeover")
	if !errors.Is(err, ErrAskSessionNotFound) {
		t.Errorf("real user's row: err = %v, want ErrAskSessionNotFound", err)
	}
	if got := titleOf(t, repo, pid, pid+"-alice"); got != "alice original" {
		t.Errorf("alice's title = %q, want it untouched by the anonymous caller", got)
	}
}

// TestInteg_AskSessionRepo_UpdateTitle_StoresMultiByteTitleIntact guards the
// round trip for a non-Latin title: the handler truncates by rune, and the
// document must come back byte-identical.
func TestInteg_AskSessionRepo_UpdateTitle_StoresMultiByteTitleIntact(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-utf8"
	repo := seedTitleSessions(t, pid)

	title := "Çeyrek müşteri kaybı — 3. çeyrek · 営業成績"
	if err := repo.UpdateTitle(ctx, pid, "alice", pid+"-alice", title); err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}

	if got := titleOf(t, repo, pid, pid+"-alice"); got != title {
		t.Errorf("title = %q, want %q", got, title)
	}
}

// TestInteg_AskSessionRepo_UpdateTitle_IsIdempotent — renaming to the same
// value is a match, not a miss. UpdateOne reports MatchedCount, not
// ModifiedCount, precisely so an unchanged write is not reported as 404.
func TestInteg_AskSessionRepo_UpdateTitle_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-idempotent"
	repo := seedTitleSessions(t, pid)

	if err := repo.UpdateTitle(ctx, pid, "alice", pid+"-alice", "same"); err != nil {
		t.Fatalf("first rename: %v", err)
	}
	if err := repo.UpdateTitle(ctx, pid, "alice", pid+"-alice", "same"); err != nil {
		t.Errorf("renaming to the same title: err = %v, want nil", err)
	}
	if got := titleOf(t, repo, pid, pid+"-alice"); got != "same" {
		t.Errorf("title = %q, want %q", got, "same")
	}
}

// TestInteg_AskSessionRepo_UpdateTitle_WritesWhatItIsGiven documents the
// division of labour: trimming and bounding belong to the handler, and the
// repository stores the value verbatim. If that ever changes, the handler's
// echo of the stored title would start lying.
func TestInteg_AskSessionRepo_UpdateTitle_WritesWhatItIsGiven(t *testing.T) {
	ctx := context.Background()
	const pid = "proj-title-verbatim"
	repo := seedTitleSessions(t, pid)

	long := strings.Repeat("x", 500)
	if err := repo.UpdateTitle(ctx, pid, "alice", pid+"-alice", long); err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}

	if got := titleOf(t, repo, pid, pid+"-alice"); got != long {
		t.Errorf("stored title length = %d, want %d — the repository must not bound the value", len(got), len(long))
	}
}
