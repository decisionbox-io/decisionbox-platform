package handler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// Compile-time checks.
var (
	_ database.BookmarkListRepo = (*mockBookmarkListRepo)(nil)
	_ database.BookmarkRepo     = (*mockBookmarkRepo)(nil)
	_ database.ReadMarkRepo     = (*mockReadMarkRepo)(nil)
)

// --- mockBookmarkListRepo ---

type mockBookmarkListRepo struct {
	mu     sync.Mutex
	lists  map[string]*models.BookmarkList
	nextID int

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error

	// Optional cascade hook invoked inside Delete after removing the list.
	onDelete func(listID string)
}

func newMockBookmarkListRepo() *mockBookmarkListRepo {
	return &mockBookmarkListRepo{lists: make(map[string]*models.BookmarkList)}
}

func (m *mockBookmarkListRepo) Create(_ context.Context, list *models.BookmarkList) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	list.ID = fmt.Sprintf("list-%d", m.nextID)
	now := time.Now().UTC()
	list.CreatedAt = now
	list.UpdatedAt = now
	cp := *list
	m.lists[list.ID] = &cp
	return nil
}

func (m *mockBookmarkListRepo) GetByID(_ context.Context, projectID, userID, listID string) (*models.BookmarkList, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.lists[listID]
	if !ok || l.ProjectID != projectID || l.UserID != userID {
		return nil, database.ErrBookmarkListNotFound
	}
	cp := *l
	return &cp, nil
}

func (m *mockBookmarkListRepo) List(_ context.Context, projectID, userID string) ([]*models.BookmarkList, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*models.BookmarkList
	for _, l := range m.lists {
		if l.ProjectID == projectID && l.UserID == userID {
			cp := *l
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (m *mockBookmarkListRepo) Update(_ context.Context, projectID, userID, listID string, patch database.UpdateFields) (*models.BookmarkList, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.lists[listID]
	if !ok || l.ProjectID != projectID || l.UserID != userID {
		return nil, database.ErrBookmarkListNotFound
	}
	if patch.Name != nil {
		l.Name = *patch.Name
	}
	if patch.Description != nil {
		l.Description = *patch.Description
	}
	if patch.Color != nil {
		l.Color = *patch.Color
	}
	l.UpdatedAt = time.Now().UTC()
	cp := *l
	return &cp, nil
}

func (m *mockBookmarkListRepo) Delete(_ context.Context, projectID, userID, listID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	l, ok := m.lists[listID]
	if !ok || l.ProjectID != projectID || l.UserID != userID {
		m.mu.Unlock()
		return database.ErrBookmarkListNotFound
	}
	delete(m.lists, listID)
	m.mu.Unlock()
	if m.onDelete != nil {
		m.onDelete(listID)
	}
	return nil
}

// --- mockBookmarkRepo ---

type mockBookmarkRepo struct {
	mu     sync.Mutex
	items  map[string]*models.Bookmark
	nextID int

	addErr    error
	listErr   error
	deleteErr error

	addCount int
}

func newMockBookmarkRepo() *mockBookmarkRepo {
	return &mockBookmarkRepo{items: make(map[string]*models.Bookmark)}
}

func (m *mockBookmarkRepo) Add(_ context.Context, bm *models.Bookmark) (*models.Bookmark, error) {
	if m.addErr != nil {
		return nil, m.addErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.items {
		if existing.ListID == bm.ListID &&
			existing.TargetType == bm.TargetType &&
			existing.TargetID == bm.TargetID {
			cp := *existing
			return &cp, nil
		}
	}
	m.nextID++
	m.addCount++
	bm.ID = fmt.Sprintf("bm-%d", m.nextID)
	bm.CreatedAt = time.Now().UTC()
	cp := *bm
	m.items[bm.ID] = &cp
	return &cp, nil
}

func (m *mockBookmarkRepo) ListByList(_ context.Context, listID string) ([]*models.Bookmark, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*models.Bookmark
	for _, bm := range m.items {
		if bm.ListID == listID {
			cp := *bm
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *mockBookmarkRepo) Delete(_ context.Context, projectID, userID, listID, bookmarkID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	bm, ok := m.items[bookmarkID]
	if !ok || bm.ProjectID != projectID || bm.UserID != userID || bm.ListID != listID {
		return database.ErrBookmarkNotFound
	}
	delete(m.items, bookmarkID)
	return nil
}

func (m *mockBookmarkRepo) ListsContaining(_ context.Context, projectID, userID, targetType, targetID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, bm := range m.items {
		if bm.ProjectID == projectID && bm.UserID == userID &&
			bm.TargetType == targetType && bm.TargetID == targetID {
			out = append(out, bm.ListID)
		}
	}
	return out, nil
}

// cascadeTo wires the list repo's onDelete hook so that deleting a list in the
// mock list repo also cleans up its bookmarks here — mirroring real cascade.
func (m *mockBookmarkRepo) cascadeTo(list *mockBookmarkListRepo) {
	list.onDelete = func(listID string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for id, bm := range m.items {
			if bm.ListID == listID {
				delete(m.items, id)
			}
		}
	}
}

// --- mockReadMarkRepo ---

type mockReadMarkRepo struct {
	mu    sync.Mutex
	items map[string]*models.ReadMark

	upsertErr error
	deleteErr error
	listErr   error
}

func newMockReadMarkRepo() *mockReadMarkRepo {
	return &mockReadMarkRepo{items: make(map[string]*models.ReadMark)}
}

func markKey(projectID, userID, targetType, targetID string) string {
	return projectID + "|" + userID + "|" + targetType + "|" + targetID
}

func (m *mockReadMarkRepo) Upsert(_ context.Context, mark *models.ReadMark) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mark.ReadAt = time.Now().UTC()
	key := markKey(mark.ProjectID, mark.UserID, mark.TargetType, mark.TargetID)
	cp := *mark
	m.items[key] = &cp
	return nil
}

func (m *mockReadMarkRepo) Delete(_ context.Context, projectID, userID, targetType, targetID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, markKey(projectID, userID, targetType, targetID))
	return nil
}

func (m *mockReadMarkRepo) ListReadIDs(_ context.Context, projectID, userID, targetType string) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0)
	for _, mark := range m.items {
		if mark.ProjectID == projectID && mark.UserID == userID && mark.TargetType == targetType {
			ids = append(ids, mark.TargetID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// errSentinel is a small helper for returning custom errors in tests.
func errSentinel(msg string) error { return errors.New(msg) }
