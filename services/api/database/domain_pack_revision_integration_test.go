//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// The revision log's hard parts are all Mongo's: a pin that has to match a
// document with no `revision` field at all, a $push that trims in the same
// write, and operators merged into a caller's own update. A mock proves none
// of that.

func userRev(summary string) *models.PackRevision {
	return &models.PackRevision{Origin: models.PackRevisionOriginUser, Summary: summary}
}

// Every pack that exists today has no revision field. Its first edit must
// become revision 1 rather than reporting a conflict that is not one.
func TestInteg_PackRevision_LegacyPackFirstEdit(t *testing.T) {
	ctx := context.Background()
	repo := NewDomainPackRepository(testDB)
	const slug = "rev-legacy"

	// Insert the way a pre-revision build did: straight document, no field.
	legacy := testPack(slug)
	legacy.Revision = 0
	if _, err := testDB.Collection("domain_packs").InsertOne(ctx, legacy); err != nil {
		t.Fatalf("seed legacy pack: %v", err)
	}
	var raw bson.M
	if err := testDB.Collection("domain_packs").FindOne(ctx, bson.M{"slug": slug}).Decode(&raw); err != nil {
		t.Fatalf("read legacy pack: %v", err)
	}
	if _, present := raw["revision"]; present {
		t.Fatalf("fixture is wrong: the legacy pack carries a revision field")
	}

	edited := testPack(slug)
	edited.Name = "Edited"
	if err := repo.Update(ctx, slug, edited, 0, userRev("renamed it")); err != nil {
		t.Fatalf("first edit of a legacy pack: %v", err)
	}

	stored, err := repo.GetBySlug(ctx, slug)
	if err != nil {
		t.Fatalf("GetBySlug: %v", err)
	}
	if stored.Revision != 1 {
		t.Errorf("Revision = %d, want 1", stored.Revision)
	}
	if len(stored.Revisions) != 1 || stored.Revisions[0].Revision != 1 {
		t.Fatalf("Revisions = %+v, want one entry numbered 1", stored.Revisions)
	}
	if stored.Revisions[0].CreatedAt.IsZero() {
		t.Error("the entry has no timestamp — AddPackRevision should stamp it")
	}
}

// A write pinned on a revision that has moved lands nothing and says why.
func TestInteg_PackRevision_StaleWriteRefused(t *testing.T) {
	ctx := context.Background()
	repo := NewDomainPackRepository(testDB)
	const slug = "rev-stale"

	pack := testPack(slug)
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if pack.Revision != 1 {
		t.Fatalf("a created pack is revision %d, want 1", pack.Revision)
	}

	first := testPack(slug)
	first.Name = "First writer"
	if err := repo.Update(ctx, slug, first, 1, userRev("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// A second writer that read the pack at revision 1 is now stale.
	stale := testPack(slug)
	stale.Name = "Stale writer"
	err := repo.Update(ctx, slug, stale, 1, userRev("stale"))
	if !errors.Is(err, ErrPackRevisionConflict) {
		t.Fatalf("stale write error = %v, want ErrPackRevisionConflict", err)
	}

	stored, _ := repo.GetBySlug(ctx, slug)
	if stored.Name != "First writer" {
		t.Errorf("Name = %q — the stale write landed", stored.Name)
	}
	if stored.Revision != 2 {
		t.Errorf("Revision = %d, want 2 — a refused write must not bump", stored.Revision)
	}
	if len(stored.Revisions) != 1 {
		t.Errorf("Revisions = %d entries, want 1 — a refused write must not log", len(stored.Revisions))
	}

	// Re-reading and retrying succeeds, which is what makes the refusal safe.
	retry := testPack(slug)
	retry.Name = "Retried"
	if err := repo.Update(ctx, slug, retry, stored.Revision, userRev("retried")); err != nil {
		t.Fatalf("retry at the current revision: %v", err)
	}
	stored, _ = repo.GetBySlug(ctx, slug)
	if stored.Revision != 3 || len(stored.Revisions) != 2 {
		t.Errorf("after retry: revision %d with %d entries, want 3 and 2", stored.Revision, len(stored.Revisions))
	}
}

// rev == nil is the "nothing changed" save: no bump, no entry, and the pin is
// not applied, so it cannot fail on a revision it never read.
func TestInteg_PackRevision_NilRevisionNeitherBumpsNorLogs(t *testing.T) {
	ctx := context.Background()
	repo := NewDomainPackRepository(testDB)
	const slug = "rev-nil"

	pack := testPack(slug)
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Update(ctx, slug, testPack(slug), 1, userRev("one real edit")); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// A stale atRevision is irrelevant when nothing is being recorded.
	if err := repo.Update(ctx, slug, testPack(slug), 999, nil); err != nil {
		t.Fatalf("no-change Update: %v", err)
	}
	stored, _ := repo.GetBySlug(ctx, slug)
	if stored.Revision != 2 {
		t.Errorf("Revision = %d, want 2", stored.Revision)
	}
	if len(stored.Revisions) != 1 {
		t.Errorf("Revisions = %d entries, want 1", len(stored.Revisions))
	}
}

// The log is bounded in the same write that appends to it, so a pack cannot
// grow without limit as it is edited.
func TestInteg_PackRevision_HistoryTrimsOldestFirst(t *testing.T) {
	ctx := context.Background()
	repo := NewDomainPackRepository(testDB)
	const slug = "rev-trim"
	t.Setenv("PLAYBOOK_REVISION_HISTORY_MAX", "3")

	pack := testPack(slug)
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("Create: %v", err)
	}

	at := pack.Revision
	for i := 1; i <= 5; i++ {
		edited := testPack(slug)
		edited.Name = fmt.Sprintf("edit %d", i)
		if err := repo.Update(ctx, slug, edited, at, userRev(fmt.Sprintf("edit %d", i))); err != nil {
			t.Fatalf("edit %d: %v", i, err)
		}
		at++
	}

	stored, _ := repo.GetBySlug(ctx, slug)
	if stored.Revision != 6 {
		t.Errorf("Revision = %d, want 6 (1 + five edits)", stored.Revision)
	}
	if len(stored.Revisions) != 3 {
		t.Fatalf("Revisions = %d entries, want 3 (the cap)", len(stored.Revisions))
	}
	// Newest last, oldest dropped.
	for i, want := range []string{"edit 3", "edit 4", "edit 5"} {
		if stored.Revisions[i].Summary != want {
			t.Errorf("Revisions[%d].Summary = %q, want %q", i, stored.Revisions[i].Summary, want)
		}
	}
	if stored.Revisions[2].Revision != 6 {
		t.Errorf("newest entry Revision = %d, want 6", stored.Revisions[2].Revision)
	}
}

// AddPackRevision is used by a second writer that brings its own $set, $push
// and $inc — the enterprise area applier. Merging must keep both sides.
func TestInteg_PackRevision_MergesIntoCallerOperators(t *testing.T) {
	ctx := context.Background()
	col := testDB.Collection("domain_packs")
	repo := NewDomainPackRepository(testDB)
	const slug = "rev-merge"

	pack := testPack(slug)
	if err := repo.Create(ctx, pack); err != nil {
		t.Fatalf("Create: %v", err)
	}

	update := bson.M{
		"$set":  bson.M{"description": "area-scoped write"},
		"$push": bson.M{"analysis_areas.base": models.PackAnalysisArea{ID: "added-area", Name: "Added", Priority: 2, Prompt: "p"}},
		"$inc":  bson.M{"input_tokens": 100},
	}
	AddPackRevision(update, pack.Revision, models.PackRevision{
		Origin:     models.PackRevisionOriginAskSession,
		ActorSub:   "alice",
		SessionID:  "sess-1",
		ProposalID: "prop-1",
		Summary:    `added analysis area "added-area"`,
		AreaIDs:    []string{"added-area"},
	}, PackRevisionHistoryMax())

	res, err := col.UpdateOne(ctx, bson.M{"slug": slug, "revision": PackRevisionPin(pack.Revision)}, update)
	if err != nil {
		t.Fatalf("merged update: %v", err)
	}
	if res.MatchedCount != 1 {
		t.Fatalf("MatchedCount = %d, want 1", res.MatchedCount)
	}

	stored, _ := repo.GetBySlug(ctx, slug)
	if stored.Description != "area-scoped write" {
		t.Errorf("the caller's $set was lost: %q", stored.Description)
	}
	if stored.InputTokens != 100 {
		t.Errorf("the caller's $inc was lost: %d", stored.InputTokens)
	}
	var found bool
	for _, a := range stored.AnalysisAreas.Base {
		if a.ID == "added-area" {
			found = true
		}
	}
	if !found {
		t.Error("the caller's $push was lost")
	}
	if stored.Revision != pack.Revision+1 {
		t.Errorf("Revision = %d, want %d", stored.Revision, pack.Revision+1)
	}
	if len(stored.Revisions) != 1 {
		t.Fatalf("Revisions = %+v, want one entry", stored.Revisions)
	}
	entry := stored.Revisions[0]
	if entry.Origin != models.PackRevisionOriginAskSession || entry.SessionID != "sess-1" || entry.ProposalID != "prop-1" || entry.ActorSub != "alice" {
		t.Errorf("entry = %+v, want the ask_session attribution intact", entry)
	}
}

// A revision has to say where it came from; an unattributed one is refused
// rather than written as an anonymous change.
func TestInteg_PackRevision_OriginRequired(t *testing.T) {
	ctx := context.Background()
	repo := NewDomainPackRepository(testDB)
	const slug = "rev-origin"
	if err := repo.Create(ctx, testPack(slug)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := repo.Update(ctx, slug, testPack(slug), 1, &models.PackRevision{Summary: "no origin"})
	if err == nil {
		t.Fatal("an origin-less revision should be refused")
	}
}
