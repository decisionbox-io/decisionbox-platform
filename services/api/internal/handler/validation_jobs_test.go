package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
)

// --- mock ValidationJobRepo ----------------------------------------

type mockJobRepo struct {
	mu             sync.Mutex
	jobs           map[string]*models.ValidationJob
	enqueueErr     error
	countActive    int64
	countActiveErr error
	cancelErr      error
}

func newMockJobRepo() *mockJobRepo {
	return &mockJobRepo{jobs: make(map[string]*models.ValidationJob)}
}

func (m *mockJobRepo) Enqueue(_ context.Context, job *models.ValidationJob) error {
	if m.enqueueErr != nil {
		return m.enqueueErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job.Status = models.ValidationJobStatusPending
	stored := *job
	m.jobs[job.ID] = &stored
	return nil
}

func (m *mockJobRepo) GetByID(_ context.Context, id string) (*models.ValidationJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, ok := m.jobs[id]; ok {
		copy := *j
		return &copy, nil
	}
	return nil, nil
}

func (m *mockJobRepo) ListByDoc(_ context.Context, discoveryID, docKind, docID string, _ int) ([]models.ValidationJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.ValidationJob, 0)
	for _, j := range m.jobs {
		if j.DiscoveryID == discoveryID && j.DocKind == docKind && j.DocID == docID {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (m *mockJobRepo) Cancel(_ context.Context, id string) error {
	if m.cancelErr != nil {
		return m.cancelErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return database.ErrNotFound
	}
	if j.IsTerminal() {
		return database.ErrAlreadyTerminal
	}
	j.Status = models.ValidationJobStatusCancelled
	return nil
}

func (m *mockJobRepo) CountActiveByDoc(_ context.Context, _, _, _ string) (int64, error) {
	if m.countActiveErr != nil {
		return 0, m.countActiveErr
	}
	return m.countActive, nil
}

// Compile-time check.
var _ database.ValidationJobRepo = (*mockJobRepo)(nil)

// --- test setup helpers --------------------------------------------

// setupHandler wires the three mocks the validation-jobs handler
// needs. The discovery's project_id and embedded insight/rec arrays
// are pre-populated so the happy path can find the doc.
func setupHandler(t *testing.T, validationEnabled bool) (*ValidationJobsHandler, *mockJobRepo, *mockProjectRepo, *mockDiscoveryRepo, string, string, string, string) {
	t.Helper()
	jobs := newMockJobRepo()
	projects := newMockProjectRepo()
	discoveries := newMockDiscoveryRepo()

	// Seed a project with the requested validation toggle.
	proj := &models.Project{Name: "p", Domain: "gaming", Category: "match3"}
	flag := validationEnabled
	proj.ValidationEnabled = &flag
	if err := projects.Create(context.Background(), proj); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	projectID := proj.ID

	// Seed a discovery referencing one insight + one recommendation.
	discoveryID := "disc-" + projectID
	insightID := "ins-aaa"
	recID := "rec-bbb"
	discoveries.add(&models.DiscoveryResult{
		ID:        discoveryID,
		ProjectID: projectID,
		Insights: []models.Insight{
			{ID: insightID, Name: "i", Severity: "high"},
		},
		Recommendations: []models.Recommendation{
			{ID: recID, Title: "r", Priority: 1},
		},
	})

	h := NewValidationJobsHandler(jobs, discoveries, projects)
	return h, jobs, projects, discoveries, projectID, discoveryID, insightID, recID
}

// decodeJob unwraps the {data: ...} envelope writeJSON applies and
// returns the inner ValidationJob.
func decodeJob(t *testing.T, body string) models.ValidationJob {
	t.Helper()
	var env struct {
		Data models.ValidationJob `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&env); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, body)
	}
	return env.Data
}

// decodeJobs unwraps {data: [...]} for ListByDoc.
func decodeJobs(t *testing.T, body string) []models.ValidationJob {
	t.Helper()
	var env struct {
		Data []models.ValidationJob `json:"data"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&env); err != nil {
		t.Fatalf("decode body: %v (body=%q)", err, body)
	}
	return env.Data
}

// --- ValidateInsight -----------------------------------------------

func TestValidationJobsHandler_ValidateInsight_HappyPath(t *testing.T) {
	h, jobs, _, _, _, did, iid, _ := setupHandler(t, true)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/discoveries/"+did+"/insights/"+iid+"/validate", nil)
	req.SetPathValue("did", did)
	req.SetPathValue("iid", iid)
	rec := httptest.NewRecorder()
	h.ValidateInsight(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	got := decodeJob(t, rec.Body.String())
	if got.ID == "" {
		t.Errorf("response should carry a job id")
	}
	if got.DocKind != models.ValidationJobDocKindInsight {
		t.Errorf("doc_kind = %q", got.DocKind)
	}
	if got.DocID != iid {
		t.Errorf("doc_id = %q, want %q", got.DocID, iid)
	}
	if got.Status != models.ValidationJobStatusPending {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if len(jobs.jobs) != 1 {
		t.Errorf("expected one job in the repo, got %d", len(jobs.jobs))
	}
}

func TestValidationJobsHandler_ValidateInsight_404OnMissingDiscovery(t *testing.T) {
	h, _, _, _, _, _, iid, _ := setupHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("did", "nope-not-a-real-id")
	req.SetPathValue("iid", iid)
	rec := httptest.NewRecorder()
	h.ValidateInsight(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestValidationJobsHandler_ValidateInsight_404OnUnknownInsight(t *testing.T) {
	h, _, _, _, _, did, _, _ := setupHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("did", did)
	req.SetPathValue("iid", "ins-does-not-exist")
	rec := httptest.NewRecorder()
	h.ValidateInsight(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestValidationJobsHandler_ValidateInsight_403WhenValidationDisabled(t *testing.T) {
	h, _, _, _, _, did, iid, _ := setupHandler(t, false)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("did", did)
	req.SetPathValue("iid", iid)
	rec := httptest.NewRecorder()
	h.ValidateInsight(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestValidationJobsHandler_ValidateInsight_409WhenActiveJobExists(t *testing.T) {
	h, jobs, _, _, _, did, iid, _ := setupHandler(t, true)
	jobs.countActive = 1 // simulate a pending job is already there

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("did", did)
	req.SetPathValue("iid", iid)
	rec := httptest.NewRecorder()
	h.ValidateInsight(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestValidationJobsHandler_ValidateInsight_409WhenRepoRacesToDuplicate(t *testing.T) {
	h, jobs, _, _, _, did, iid, _ := setupHandler(t, true)
	jobs.enqueueErr = database.ErrDuplicateActiveJob // race past the precheck

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("did", did)
	req.SetPathValue("iid", iid)
	rec := httptest.NewRecorder()
	h.ValidateInsight(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

// --- ValidateRecommendation ----------------------------------------

func TestValidationJobsHandler_ValidateRecommendation_HappyPath(t *testing.T) {
	h, _, _, _, _, did, _, rid := setupHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("did", did)
	req.SetPathValue("rid", rid)
	rec := httptest.NewRecorder()
	h.ValidateRecommendation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	got := decodeJob(t, rec.Body.String())
	if got.DocKind != models.ValidationJobDocKindRecommendation {
		t.Errorf("doc_kind = %q", got.DocKind)
	}
}

// --- Cancel --------------------------------------------------------

func TestValidationJobsHandler_Cancel_HappyPath(t *testing.T) {
	h, jobs, _, _, _, _, _, _ := setupHandler(t, true)
	jobs.jobs["job-1"] = &models.ValidationJob{
		ID:     "job-1",
		Status: models.ValidationJobStatusRunning,
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("jid", "job-1")
	rec := httptest.NewRecorder()
	h.Cancel(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if jobs.jobs["job-1"].Status != models.ValidationJobStatusCancelled {
		t.Errorf("status not flipped to cancelled")
	}
	// Verify the body is a parseable JSON envelope so the dashboard's
	// request() helper doesn't reject the success as a non-JSON
	// misconfigured-proxy error.
	if !strings.Contains(rec.Body.String(), "cancelled") {
		t.Errorf("response body should carry the cancelled status, got %q", rec.Body.String())
	}
}

// The in-flight agent process must receive the cancel signal, not
// just the Mongo row. WithCanceller injects a worker; Cancel must
// call its Cancel(jobID) method.
type spyCanceller struct{ called []string }

func (s *spyCanceller) Cancel(jobID string) bool {
	s.called = append(s.called, jobID)
	return true
}

func TestValidationJobsHandler_Cancel_SignalsTheWorker(t *testing.T) {
	h, jobs, _, _, _, _, _, _ := setupHandler(t, true)
	spy := &spyCanceller{}
	h = h.WithCanceller(spy)

	jobs.jobs["job-running"] = &models.ValidationJob{
		ID:     "job-running",
		Status: models.ValidationJobStatusRunning,
	}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("jid", "job-running")
	rec := httptest.NewRecorder()
	h.Cancel(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(spy.called) != 1 || spy.called[0] != "job-running" {
		t.Errorf("worker.Cancel was not called for the job, got %+v", spy.called)
	}
}

func TestValidationJobsHandler_Cancel_404OnMissing(t *testing.T) {
	h, _, _, _, _, _, _, _ := setupHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("jid", "no-such-job")
	rec := httptest.NewRecorder()
	h.Cancel(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestValidationJobsHandler_Cancel_409OnAlreadyTerminal(t *testing.T) {
	h, jobs, _, _, _, _, _, _ := setupHandler(t, true)
	jobs.jobs["done"] = &models.ValidationJob{
		ID:     "done",
		Status: models.ValidationJobStatusCompleted,
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("jid", "done")
	rec := httptest.NewRecorder()
	h.Cancel(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestValidationJobsHandler_Cancel_500OnRepoError(t *testing.T) {
	h, jobs, _, _, _, _, _, _ := setupHandler(t, true)
	jobs.cancelErr = errors.New("mongo down")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("jid", "job-x")
	rec := httptest.NewRecorder()
	h.Cancel(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// --- ListByDoc -----------------------------------------------------

func TestValidationJobsHandler_ListByDoc_HappyPath(t *testing.T) {
	h, jobs, _, _, _, did, iid, _ := setupHandler(t, true)
	jobs.jobs["j1"] = &models.ValidationJob{
		ID: "j1", DiscoveryID: did, DocKind: models.ValidationJobDocKindInsight, DocID: iid,
	}
	jobs.jobs["j2"] = &models.ValidationJob{
		ID: "j2", DiscoveryID: did, DocKind: models.ValidationJobDocKindRecommendation, DocID: iid,
	}

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/discoveries/"+did+"/validation-jobs?doc_kind=insight&doc_id="+iid, nil)
	req.SetPathValue("did", did)
	rec := httptest.NewRecorder()
	h.ListByDoc(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%q", rec.Code, rec.Body.String())
	}
	got := decodeJobs(t, rec.Body.String())
	if len(got) != 1 {
		t.Errorf("only the insight job should match the filter, got %d", len(got))
	}
}

func TestValidationJobsHandler_ListByDoc_400OnBadDocKind(t *testing.T) {
	h, _, _, _, _, did, iid, _ := setupHandler(t, true)
	req := httptest.NewRequest(http.MethodGet, "/?doc_kind=xyz&doc_id="+iid, nil)
	req.SetPathValue("did", did)
	rec := httptest.NewRecorder()
	h.ListByDoc(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
