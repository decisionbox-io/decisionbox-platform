package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	goauth "github.com/decisionbox-io/decisionbox/libs/go-common/auth"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// putPack sends a PUT of body as sub and returns the recorder.
func putPack(t *testing.T, h *DomainPacksHandler, slug, sub string, body any) *httptest.ResponseRecorder {
	t.Helper()
	bs, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest("PUT", "/api/v1/domain-packs/"+slug, strings.NewReader(string(bs)))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("slug", slug)
	req = asCaller(req, sub)
	w := httptest.NewRecorder()
	h.Update(w, req)
	return w
}

func storedPack(t *testing.T, repo *mockDomainPackRepo, slug string) *models.DomainPack {
	t.Helper()
	p, err := repo.GetBySlug(context.Background(), slug)
	if err != nil || p == nil {
		t.Fatalf("read stored pack %s: %v", slug, err)
	}
	return p
}

// The Playbook editor round-trips a pack through a form and sends it back. A
// body that omits the revision fields — which is every body a client that
// predates them sends — must not wipe the history.
func TestDomainPacksHandler_Update_PreservesHistoryWhenBodyOmitsIt(t *testing.T) {
	repo := newMockDomainPackRepo()
	existing := testDomainPack("gaming", "match3")
	existing.Revision = 4
	existing.Revisions = []models.PackRevision{
		{Revision: 4, Origin: models.PackRevisionOriginAskSession, SessionID: "s1", ProposalID: "p1", Summary: "added analysis area \"churn\""},
	}
	repo.add(existing)
	h := NewDomainPacksHandler(repo)

	// A body with no revision fields at all, exactly as an older client sends.
	body := map[string]any{}
	bs, _ := json.Marshal(testDomainPack("gaming", "match3"))
	_ = json.Unmarshal(bs, &body)
	delete(body, "revision")
	delete(body, "revisions")
	body["name"] = "Updated Gaming"

	w := putPack(t, h, "gaming", "alice", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	stored := storedPack(t, repo, "gaming")
	if stored.Revision != 5 {
		t.Errorf("Revision = %d, want 5 (4 + this edit)", stored.Revision)
	}
	if len(stored.Revisions) != 2 {
		t.Fatalf("Revisions = %d entries, want 2 (the old one kept, this one appended)", len(stored.Revisions))
	}
	if stored.Revisions[0].ProposalID != "p1" {
		t.Errorf("the earlier revision was lost: %+v", stored.Revisions[0])
	}
	got := stored.Revisions[1]
	if got.Origin != models.PackRevisionOriginUser {
		t.Errorf("Origin = %q, want %q", got.Origin, models.PackRevisionOriginUser)
	}
	if got.ActorSub != "alice" {
		t.Errorf("ActorSub = %q, want alice", got.ActorSub)
	}
	if got.Revision != 5 {
		t.Errorf("entry Revision = %d, want 5", got.Revision)
	}
	if got.CreatedAt.IsZero() {
		t.Error("entry has no timestamp")
	}
}

// A body that carries a forged history cannot install it either: the fields
// are server-owned, exactly like _id.
func TestDomainPacksHandler_Update_IgnoresSubmittedHistory(t *testing.T) {
	repo := newMockDomainPackRepo()
	existing := testDomainPack("gaming", "match3")
	existing.Revision = 2
	existing.Revisions = []models.PackRevision{{Revision: 2, Origin: models.PackRevisionOriginUser, ActorSub: "real"}}
	repo.add(existing)
	h := NewDomainPacksHandler(repo)

	forged := testDomainPack("gaming", "match3")
	forged.Name = "Updated"
	forged.Revision = 99
	forged.Revisions = []models.PackRevision{{Revision: 99, Origin: models.PackRevisionOriginUser, ActorSub: "someone-else", Summary: "not what happened"}}

	if w := putPack(t, h, "gaming", "alice", forged); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	stored := storedPack(t, repo, "gaming")
	if stored.Revision != 3 {
		t.Errorf("Revision = %d, want 3 — the body's 99 must not be honoured", stored.Revision)
	}
	if len(stored.Revisions) != 2 {
		t.Fatalf("Revisions = %d, want 2: %+v", len(stored.Revisions), stored.Revisions)
	}
	for _, r := range stored.Revisions {
		if r.ActorSub == "someone-else" {
			t.Error("a submitted revision entry was stored")
		}
	}
}

// Saving a form nobody edited is not a change to the Playbook. Recording one
// would fill the history with entries that say nothing.
func TestDomainPacksHandler_Update_NoContentChange_RecordsNothing(t *testing.T) {
	repo := newMockDomainPackRepo()
	existing := testDomainPack("gaming", "match3")
	existing.Revision = 2
	repo.add(existing)
	h := NewDomainPacksHandler(repo)

	if w := putPack(t, h, "gaming", "alice", testDomainPack("gaming", "match3")); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	stored := storedPack(t, repo, "gaming")
	if stored.Revision != 2 {
		t.Errorf("Revision = %d, want 2 — an identical save must not bump", stored.Revision)
	}
	if len(stored.Revisions) != 0 {
		t.Errorf("Revisions = %+v, want none", stored.Revisions)
	}
}

// The edit names the areas it touched, because "someone edited the pack" is
// what the timestamp already says.
func TestDomainPacksHandler_Update_SummaryNamesChangedAreas(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	edited := testDomainPack("gaming", "match3")
	edited.AnalysisAreas.Base[0].Prompt = "Analyze {{DATASET}} differently: {{QUERY_RESULTS}}"

	if w := putPack(t, h, "gaming", "alice", edited); w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	stored := storedPack(t, repo, "gaming")
	if len(stored.Revisions) != 1 {
		t.Fatalf("Revisions = %+v, want one", stored.Revisions)
	}
	rev := stored.Revisions[0]
	if len(rev.AreaIDs) != 1 || rev.AreaIDs[0] != "test_area" {
		t.Errorf("AreaIDs = %v, want [test_area]", rev.AreaIDs)
	}
	if !strings.Contains(rev.Summary, "test_area") {
		t.Errorf("Summary = %q, should name the area", rev.Summary)
	}
	if len(rev.Before) != 1 || !strings.Contains(rev.Before[0].Prompt, "{{TOTAL_QUERIES}}") {
		t.Errorf("Before = %+v, should hold the area as it stood", rev.Before)
	}
}

// Two people editing the same pack: the second write is refused rather than
// silently overwriting the first, and the message says so.
func TestDomainPacksHandler_Update_ConcurrentEdit_409(t *testing.T) {
	repo := newMockDomainPackRepo()
	existing := testDomainPack("gaming", "match3")
	existing.Revision = 1
	repo.add(existing)
	// shiftingPackRepo moves the pack on between the handler's read and its
	// write, which is exactly what a concurrent editor does.
	h := NewDomainPacksHandler(&shiftingPackRepo{mockDomainPackRepo: repo})

	edited := testDomainPack("gaming", "match3")
	edited.Name = "Bob's edit"
	w := putPack(t, h, "gaming", "bob", edited)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "changed by someone else") {
		t.Errorf("body = %s, should explain the conflict", w.Body.String())
	}
	if got := storedPack(t, repo, "gaming").Name; got == "Bob's edit" {
		t.Error("the refused write landed anyway")
	}
}

type shiftingPackRepo struct {
	*mockDomainPackRepo
}

func (s *shiftingPackRepo) Update(ctx context.Context, slug string, pack *models.DomainPack, atRevision int, rev *models.PackRevision) error {
	s.mu.Lock()
	s.packs[slug].Revision++
	s.mu.Unlock()
	return s.mockDomainPackRepo.Update(ctx, slug, pack, atRevision, rev)
}

// Export's portable format carries no history: it names actor subjects and
// conversations that mean nothing in another deployment.
func TestDomainPacksHandler_Export_StripsHistory(t *testing.T) {
	repo := newMockDomainPackRepo()
	p := testDomainPack("gaming", "match3")
	p.Revision = 9
	p.Revisions = []models.PackRevision{{Revision: 9, Origin: models.PackRevisionOriginAskSession, ActorSub: "alice", SessionID: "sess-1", ProposalID: "prop-1"}}
	repo.add(p)
	h := NewDomainPacksHandler(repo)

	req := httptest.NewRequest("GET", "/api/v1/domain-packs/gaming/export", nil)
	req.SetPathValue("slug", "gaming")
	w := httptest.NewRecorder()
	h.Export(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{"sess-1", "prop-1", "revisions", "\"revision\""} {
		if strings.Contains(body, leak) {
			t.Errorf("export leaked %q: %s", leak, body)
		}
	}
}

// And an import that carries one does not install it.
func TestDomainPacksHandler_Import_DropsHistory(t *testing.T) {
	repo := newMockDomainPackRepo()
	h := NewDomainPacksHandler(repo)

	p := testDomainPack("imported", "general")
	p.Revision = 42
	p.Revisions = []models.PackRevision{{Revision: 42, Origin: models.PackRevisionOriginUser, ActorSub: "elsewhere"}}
	body, _ := json.Marshal(map[string]any{
		"format":         "decisionbox-domain-pack",
		"format_version": 1,
		"pack":           p,
	})

	req := httptest.NewRequest("POST", "/api/v1/domain-packs/import", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Import(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	stored := storedPack(t, repo, "imported")
	if stored.Revision != 1 {
		t.Errorf("Revision = %d, want 1 — an imported pack starts its own history", stored.Revision)
	}
	if len(stored.Revisions) != 0 {
		t.Errorf("Revisions = %+v, want none", stored.Revisions)
	}
}

// Under NoAuth there is nobody to name, and the edit is still recorded.
func TestDomainPacksHandler_Update_NoAuthActor(t *testing.T) {
	repo := newMockDomainPackRepo()
	repo.add(testDomainPack("gaming", "match3"))
	h := NewDomainPacksHandler(repo)

	edited := testDomainPack("gaming", "match3")
	edited.Name = "Renamed"
	if w := putPack(t, h, "gaming", goauth.AnonymousSubject, edited); w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	stored := storedPack(t, repo, "gaming")
	if len(stored.Revisions) != 1 || stored.Revisions[0].Origin != models.PackRevisionOriginUser {
		t.Fatalf("Revisions = %+v, want one user entry", stored.Revisions)
	}
}
