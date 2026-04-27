//go:build integration

package database

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/models"
	"go.mongodb.org/mongo-driver/bson"
)

// --- BookmarkListRepository ---

func TestInteg_BookmarkList_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	repo := NewBookmarkListRepository(testDB)

	list := &models.BookmarkList{
		ProjectID:   "proj-bl-1",
		UserID:      "alice",
		Name:        "Retention",
		Description: "churn ideas",
		Color:       "#2b7",
	}
	if err := repo.Create(ctx, list); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if list.ID == "" {
		t.Fatal("ID not populated after Create")
	}
	if list.CreatedAt.IsZero() || list.UpdatedAt.IsZero() {
		t.Error("timestamps not set")
	}

	got, err := repo.GetByID(ctx, "proj-bl-1", "alice", list.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Retention" {
		t.Errorf("Name = %q", got.Name)
	}
	if got.ItemCount != 0 {
		t.Errorf("ItemCount = %d, want 0", got.ItemCount)
	}
}

func TestInteg_BookmarkList_GetByID_WrongUser(t *testing.T) {
	ctx := context.Background()
	repo := NewBookmarkListRepository(testDB)

	list := &models.BookmarkList{ProjectID: "proj-wu", UserID: "alice", Name: "L"}
	_ = repo.Create(ctx, list)

	_, err := repo.GetByID(ctx, "proj-wu", "bob", list.ID)
	if !errors.Is(err, ErrBookmarkListNotFound) {
		t.Errorf("err = %v, want ErrBookmarkListNotFound (cross-user)", err)
	}
	_, err = repo.GetByID(ctx, "proj-other", "alice", list.ID)
	if !errors.Is(err, ErrBookmarkListNotFound) {
		t.Errorf("err = %v, want ErrBookmarkListNotFound (cross-project)", err)
	}
}

func TestInteg_BookmarkList_ListOrdering(t *testing.T) {
	ctx := context.Background()
	repo := NewBookmarkListRepository(testDB)

	var ids []string
	for i := 0; i < 3; i++ {
		l := &models.BookmarkList{
			ProjectID: "proj-ord",
			UserID:    "u-ord",
			Name:      fmt.Sprintf("L%d", i),
		}
		if err := repo.Create(ctx, l); err != nil {
			t.Fatalf("Create: %v", err)
		}
		ids = append(ids, l.ID)
	}
	// BSON DateTime is millisecond-resolution; without this sleep the
	// three Creates and the Update can all land on the same tick, so
	// updated_at ties and the secondary sort order is undefined.
	time.Sleep(5 * time.Millisecond)
	// Touch the middle one so it becomes most-recent.
	name := "L1-updated"
	_, err := repo.Update(ctx, "proj-ord", "u-ord", ids[1], UpdateFields{Name: &name})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	out, err := repo.List(ctx, "proj-ord", "u-ord")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	if out[0].ID != ids[1] {
		t.Errorf("most-recently-updated should be first, got %q", out[0].Name)
	}
}

func TestInteg_BookmarkList_Update_Partial(t *testing.T) {
	ctx := context.Background()
	repo := NewBookmarkListRepository(testDB)

	list := &models.BookmarkList{ProjectID: "p-upd", UserID: "alice", Name: "old", Description: "keep"}
	_ = repo.Create(ctx, list)
	// Read back so we have DB-precision timestamps to compare against.
	stored, err := repo.GetByID(ctx, "p-upd", "alice", list.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	origCreated := stored.CreatedAt

	name := "new"
	updated, err := repo.Update(ctx, "p-upd", "alice", list.ID, UpdateFields{Name: &name})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "new" {
		t.Errorf("Name = %q", updated.Name)
	}
	if updated.Description != "keep" {
		t.Errorf("description should be preserved, got %q", updated.Description)
	}
	if !updated.CreatedAt.Equal(origCreated) {
		t.Errorf("CreatedAt = %v, want %v (should not change on update)", updated.CreatedAt, origCreated)
	}
	if updated.UpdatedAt.Before(origCreated) {
		t.Error("UpdatedAt should be >= CreatedAt after update")
	}
}

func TestInteg_BookmarkList_Update_WrongUser(t *testing.T) {
	ctx := context.Background()
	repo := NewBookmarkListRepository(testDB)
	list := &models.BookmarkList{ProjectID: "p-uwu", UserID: "alice", Name: "L"}
	_ = repo.Create(ctx, list)

	name := "hijack"
	_, err := repo.Update(ctx, "p-uwu", "bob", list.ID, UpdateFields{Name: &name})
	if !errors.Is(err, ErrBookmarkListNotFound) {
		t.Errorf("err = %v, want ErrBookmarkListNotFound", err)
	}
}

// --- BookmarkRepository ---

func TestInteg_Bookmark_AddIdempotent(t *testing.T) {
	ctx := context.Background()
	listRepo := NewBookmarkListRepository(testDB)
	bmRepo := NewBookmarkRepository(testDB)

	list := &models.BookmarkList{ProjectID: "p-bmi", UserID: "alice", Name: "L"}
	_ = listRepo.Create(ctx, list)

	bm := &models.Bookmark{
		ListID: list.ID, ProjectID: "p-bmi", UserID: "alice",
		TargetType: "insight", TargetID: "i1",
	}
	first, err := bmRepo.Add(ctx, bm)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	dup := &models.Bookmark{
		ListID: list.ID, ProjectID: "p-bmi", UserID: "alice",
		TargetType: "insight", TargetID: "i1",
	}
	second, err := bmRepo.Add(ctx, dup)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("duplicate add should return same ID, got %q vs %q", second.ID, first.ID)
	}
}

func TestInteg_Bookmark_ConcurrentDuplicateAdd(t *testing.T) {
	ctx := context.Background()
	listRepo := NewBookmarkListRepository(testDB)
	bmRepo := NewBookmarkRepository(testDB)
	col := testDB.Collection("bookmarks")

	list := &models.BookmarkList{ProjectID: "p-race", UserID: "alice", Name: "L"}
	_ = listRepo.Create(ctx, list)

	const workers = 10
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			bm := &models.Bookmark{
				ListID: list.ID, ProjectID: "p-race", UserID: "alice",
				TargetType: "insight", TargetID: "i-race",
			}
			if _, err := bmRepo.Add(ctx, bm); err != nil {
				t.Errorf("concurrent Add: %v", err)
			}
		}()
	}
	wg.Wait()

	count, err := col.CountDocuments(ctx, bson.M{
		"list_id":     list.ID,
		"target_type": "insight",
		"target_id":   "i-race",
	})
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if count != 1 {
		t.Errorf("concurrent dup adds produced %d documents, want 1 (unique index)", count)
	}
}

func TestInteg_Bookmark_ListsContaining(t *testing.T) {
	ctx := context.Background()
	listRepo := NewBookmarkListRepository(testDB)
	bmRepo := NewBookmarkRepository(testDB)

	la := &models.BookmarkList{ProjectID: "p-lc", UserID: "alice", Name: "A"}
	lb := &models.BookmarkList{ProjectID: "p-lc", UserID: "alice", Name: "B"}
	_ = listRepo.Create(ctx, la)
	_ = listRepo.Create(ctx, lb)

	add := func(listID, tt, tid, uid string) {
		_, _ = bmRepo.Add(ctx, &models.Bookmark{
			ListID: listID, ProjectID: "p-lc", UserID: uid,
			TargetType: tt, TargetID: tid,
		})
	}
	add(la.ID, "insight", "i1", "alice")
	add(lb.ID, "insight", "i1", "alice")
	add(la.ID, "insight", "i2", "alice") // different target
	add(la.ID, "insight", "i1", "bob")   // different user

	ids, err := bmRepo.ListsContaining(ctx, "p-lc", "alice", "insight", "i1")
	if err != nil {
		t.Fatalf("ListsContaining: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("got %d lists, want 2", len(ids))
	}
}

func TestInteg_Bookmark_CascadeOnListDelete(t *testing.T) {
	ctx := context.Background()
	listRepo := NewBookmarkListRepository(testDB)
	bmRepo := NewBookmarkRepository(testDB)
	col := testDB.Collection("bookmarks")

	list := &models.BookmarkList{ProjectID: "p-casc", UserID: "alice", Name: "L"}
	_ = listRepo.Create(ctx, list)
	for i := 0; i < 3; i++ {
		_, _ = bmRepo.Add(ctx, &models.Bookmark{
			ListID: list.ID, ProjectID: "p-casc", UserID: "alice",
			TargetType: "insight", TargetID: fmt.Sprintf("i%d", i),
		})
	}

	if err := listRepo.Delete(ctx, "p-casc", "alice", list.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	count, err := col.CountDocuments(ctx, bson.M{"list_id": list.ID})
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if count != 0 {
		t.Errorf("bookmarks remaining after cascade = %d, want 0", count)
	}
}

func TestInteg_Bookmark_DeleteWrongUser(t *testing.T) {
	ctx := context.Background()
	listRepo := NewBookmarkListRepository(testDB)
	bmRepo := NewBookmarkRepository(testDB)

	list := &models.BookmarkList{ProjectID: "p-dwu", UserID: "alice", Name: "L"}
	_ = listRepo.Create(ctx, list)
	bm, _ := bmRepo.Add(ctx, &models.Bookmark{
		ListID: list.ID, ProjectID: "p-dwu", UserID: "alice",
		TargetType: "insight", TargetID: "i1",
	})

	err := bmRepo.Delete(ctx, "p-dwu", "bob", list.ID, bm.ID)
	if !errors.Is(err, ErrBookmarkNotFound) {
		t.Errorf("err = %v, want ErrBookmarkNotFound", err)
	}
}

// --- ReadMarkRepository ---

func TestInteg_ReadMark_UpsertIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := NewReadMarkRepository(testDB)
	col := testDB.Collection("read_marks")

	mark := &models.ReadMark{
		ProjectID: "p-rm", UserID: "alice",
		TargetType: "insight", TargetID: "i1",
	}
	for i := 0; i < 5; i++ {
		if err := repo.Upsert(ctx, mark); err != nil {
			t.Fatalf("Upsert iter %d: %v", i, err)
		}
	}
	count, err := col.CountDocuments(ctx, bson.M{
		"project_id":  "p-rm",
		"user_id":     "alice",
		"target_type": "insight",
		"target_id":   "i1",
	})
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (upsert should not duplicate)", count)
	}
}

func TestInteg_ReadMark_ConcurrentUpsert(t *testing.T) {
	ctx := context.Background()
	repo := NewReadMarkRepository(testDB)
	col := testDB.Collection("read_marks")

	const workers = 10
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			if err := repo.Upsert(ctx, &models.ReadMark{
				ProjectID: "p-rmc", UserID: "alice",
				TargetType: "insight", TargetID: "i1",
			}); err != nil {
				t.Errorf("Upsert: %v", err)
			}
		}()
	}
	wg.Wait()

	count, err := col.CountDocuments(ctx, bson.M{
		"project_id": "p-rmc", "user_id": "alice",
		"target_type": "insight", "target_id": "i1",
	})
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if count != 1 {
		t.Errorf("concurrent upserts produced %d docs, want 1", count)
	}
}

func TestInteg_ReadMark_DeleteIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := NewReadMarkRepository(testDB)

	// Delete when nothing exists — should not error.
	if err := repo.Delete(ctx, "p-dne", "alice", "insight", "ghost"); err != nil {
		t.Errorf("Delete of missing mark should be idempotent: %v", err)
	}
}

func TestInteg_ReadMark_ListReadIDs_ScopingMatrix(t *testing.T) {
	ctx := context.Background()
	repo := NewReadMarkRepository(testDB)

	seed := []models.ReadMark{
		{ProjectID: "p-sm", UserID: "alice", TargetType: "insight", TargetID: "i1"},
		{ProjectID: "p-sm", UserID: "alice", TargetType: "insight", TargetID: "i2"},
		{ProjectID: "p-sm", UserID: "alice", TargetType: "recommendation", TargetID: "r1"},
		{ProjectID: "p-sm", UserID: "bob", TargetType: "insight", TargetID: "i1"},
		{ProjectID: "p-other", UserID: "alice", TargetType: "insight", TargetID: "i1"},
	}
	for _, m := range seed {
		m := m
		if err := repo.Upsert(ctx, &m); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	ids, err := repo.ListReadIDs(ctx, "p-sm", "alice", "insight")
	if err != nil {
		t.Fatalf("ListReadIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("len = %d, want 2 (alice's insights in p-sm)", len(ids))
	}
}

func TestInteg_ReadMark_LargeSet(t *testing.T) {
	ctx := context.Background()
	repo := NewReadMarkRepository(testDB)

	const n = 1000
	for i := 0; i < n; i++ {
		if err := repo.Upsert(ctx, &models.ReadMark{
			ProjectID: "p-large", UserID: "alice",
			TargetType: "insight", TargetID: fmt.Sprintf("i-%d", i),
		}); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}
	ids, err := repo.ListReadIDs(ctx, "p-large", "alice", "insight")
	if err != nil {
		t.Fatalf("ListReadIDs: %v", err)
	}
	if len(ids) != n {
		t.Errorf("len = %d, want %d", len(ids), n)
	}
}

// --- Schema / indexes ---

func TestInteg_BookmarkSchema_UniqueIndexesExist(t *testing.T) {
	ctx := context.Background()
	bmCol := testDB.Collection("bookmarks")
	rmCol := testDB.Collection("read_marks")

	cursor, err := bmCol.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("bookmarks indexes: %v", err)
	}
	var bmIdx []bson.M
	_ = cursor.All(ctx, &bmIdx)
	if !hasUniqueIndex(bmIdx, []string{"list_id", "target_type", "target_id"}) {
		t.Errorf("bookmarks missing unique index on (list_id, target_type, target_id): %v", bmIdx)
	}

	cursor, err = rmCol.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("read_marks indexes: %v", err)
	}
	var rmIdx []bson.M
	_ = cursor.All(ctx, &rmIdx)
	if !hasUniqueIndex(rmIdx, []string{"project_id", "user_id", "target_type", "target_id"}) {
		t.Errorf("read_marks missing unique index on (project_id, user_id, target_type, target_id): %v", rmIdx)
	}
}

func hasUniqueIndex(indexes []bson.M, keys []string) bool {
	for _, idx := range indexes {
		unique, _ := idx["unique"].(bool)
		if !unique {
			continue
		}
		key, ok := idx["key"].(bson.M)
		if !ok {
			continue
		}
		if len(key) != len(keys) {
			continue
		}
		match := true
		for _, k := range keys {
			if _, ok := key[k]; !ok {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
