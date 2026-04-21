package handler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/decisionbox-io/decisionbox/services/api/database"
	"github.com/decisionbox-io/decisionbox/services/api/models"
	"github.com/decisionbox-io/decisionbox/services/api/internal/runner"
)

// Compile-time checks: mocks satisfy interfaces.
var (
	_ database.ProjectRepo    = (*mockProjectRepo)(nil)
	_ database.DiscoveryRepo  = (*mockDiscoveryRepo)(nil)
	_ database.RunRepo        = (*mockRunRepo)(nil)
	_ database.FeedbackRepo   = (*mockFeedbackRepo)(nil)
	_ database.PricingRepo    = (*mockPricingRepo)(nil)
	_ database.DomainPackRepo = (*mockDomainPackRepo)(nil)
	_ runner.Runner           = (*mockRunner)(nil)
)

// mockProjectRepo implements database.ProjectRepo using an in-memory map.
type mockProjectRepo struct {
	mu       sync.Mutex
	projects map[string]*models.Project
	nextID   int

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error
}

func newMockProjectRepo() *mockProjectRepo {
	return &mockProjectRepo{
		projects: make(map[string]*models.Project),
	}
}

func (m *mockProjectRepo) Create(_ context.Context, p *models.Project) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	p.ID = fmt.Sprintf("proj-%d", m.nextID)
	p.CreatedAt = time.Now()
	p.UpdatedAt = time.Now()
	stored := *p
	m.projects[p.ID] = &stored
	return nil
}

func (m *mockProjectRepo) GetByID(_ context.Context, id string) (*models.Project, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (m *mockProjectRepo) List(_ context.Context, limit, offset int) ([]*models.Project, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*models.Project
	for _, p := range m.projects {
		cp := *p
		result = append(result, &cp)
	}
	// Apply offset
	if offset > 0 && offset < len(result) {
		result = result[offset:]
	} else if offset >= len(result) {
		return []*models.Project{}, nil
	}
	// Apply limit
	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockProjectRepo) Update(_ context.Context, id string, p *models.Project) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[id]; !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	p.UpdatedAt = time.Now()
	stored := *p
	m.projects[id] = &stored
	return nil
}

func (m *mockProjectRepo) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[id]; !ok {
		return fmt.Errorf("project not found: %s", id)
	}
	delete(m.projects, id)
	return nil
}

func (m *mockProjectRepo) Count(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.projects), nil
}

func (m *mockProjectRepo) CountWithWarehouse(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, p := range m.projects {
		if p.Warehouse.Provider != "" {
			n++
		}
	}
	return n, nil
}

// mockDiscoveryRepo implements database.DiscoveryRepo using an in-memory slice.
type mockDiscoveryRepo struct {
	mu          sync.Mutex
	discoveries []*models.DiscoveryResult

	getErr     error
	getLatErr  error
	getDateErr error
	listErr    error
}

func newMockDiscoveryRepo() *mockDiscoveryRepo {
	return &mockDiscoveryRepo{}
}

func (m *mockDiscoveryRepo) GetByID(_ context.Context, id string) (*models.DiscoveryResult, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.discoveries {
		if d.ID == id {
			cp := *d
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockDiscoveryRepo) GetLatest(_ context.Context, projectID string) (*models.DiscoveryResult, error) {
	if m.getLatErr != nil {
		return nil, m.getLatErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *models.DiscoveryResult
	for _, d := range m.discoveries {
		if d.ProjectID == projectID {
			if latest == nil || d.DiscoveryDate.After(latest.DiscoveryDate) {
				latest = d
			}
		}
	}
	if latest == nil {
		return nil, nil
	}
	cp := *latest
	return &cp, nil
}

func (m *mockDiscoveryRepo) GetByDate(_ context.Context, projectID string, date time.Time) (*models.DiscoveryResult, error) {
	if m.getDateErr != nil {
		return nil, m.getDateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dateStr := date.Format("2006-01-02")
	for _, d := range m.discoveries {
		if d.ProjectID == projectID && d.DiscoveryDate.Format("2006-01-02") == dateStr {
			cp := *d
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockDiscoveryRepo) List(_ context.Context, projectID string, limit int) ([]*models.DiscoveryResult, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*models.DiscoveryResult
	for _, d := range m.discoveries {
		if d.ProjectID == projectID {
			cp := *d
			result = append(result, &cp)
		}
	}
	if limit > 0 && limit < len(result) {
		result = result[:limit]
	}
	return result, nil
}

func (m *mockDiscoveryRepo) add(d *models.DiscoveryResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.discoveries = append(m.discoveries, d)
}

// mockRunRepo implements database.RunRepo using an in-memory map.
type mockRunRepo struct {
	mu     sync.Mutex
	runs   map[string]*models.DiscoveryRun
	nextID int

	createErr     error
	getErr        error
	getLatestErr  error
	getRunningErr error
	failErr       error
	cancelErr     error
}

func newMockRunRepo() *mockRunRepo {
	return &mockRunRepo{
		runs: make(map[string]*models.DiscoveryRun),
	}
}

func (m *mockRunRepo) Create(_ context.Context, projectID string) (string, error) {
	if m.createErr != nil {
		return "", m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("run-%d", m.nextID)
	m.runs[id] = &models.DiscoveryRun{
		ID:        id,
		ProjectID: projectID,
		Status:    "running",
		Phase:     "starting",
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	return id, nil
}

func (m *mockRunRepo) GetByID(_ context.Context, runID string) (*models.DiscoveryRun, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (m *mockRunRepo) GetLatestByProject(_ context.Context, projectID string) (*models.DiscoveryRun, error) {
	if m.getLatestErr != nil {
		return nil, m.getLatestErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *models.DiscoveryRun
	for _, r := range m.runs {
		if r.ProjectID == projectID {
			if latest == nil || r.StartedAt.After(latest.StartedAt) {
				latest = r
			}
		}
	}
	if latest == nil {
		return nil, nil
	}
	cp := *latest
	return &cp, nil
}

func (m *mockRunRepo) GetRunningByProject(_ context.Context, projectID string) (*models.DiscoveryRun, error) {
	if m.getRunningErr != nil {
		return nil, m.getRunningErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.ProjectID == projectID && (r.Status == "running" || r.Status == "pending") {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockRunRepo) Fail(_ context.Context, runID string, errMsg string) error {
	if m.failErr != nil {
		return m.failErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return fmt.Errorf("run not found: %s", runID)
	}
	r.Status = "failed"
	r.Error = errMsg
	now := time.Now()
	r.CompletedAt = &now
	return nil
}

func (m *mockRunRepo) Cancel(_ context.Context, runID string) error {
	if m.cancelErr != nil {
		return m.cancelErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return fmt.Errorf("run not found: %s", runID)
	}
	r.Status = "cancelled"
	now := time.Now()
	r.CompletedAt = &now
	return nil
}

func (m *mockRunRepo) SetPolicyReservationID(_ context.Context, runID, reservationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return fmt.Errorf("run not found: %s", runID)
	}
	r.PolicyReservationID = reservationID
	return nil
}

func (m *mockRunRepo) ListTerminalWithReservation(_ context.Context, limit int) ([]*models.DiscoveryRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*models.DiscoveryRun, 0, len(m.runs))
	for _, r := range m.runs {
		terminal := r.Status == "completed" || r.Status == "failed" || r.Status == "cancelled"
		if terminal && r.PolicyReservationID != "" {
			cp := *r
			out = append(out, &cp)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *mockRunRepo) ClearPolicyReservationID(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return fmt.Errorf("run not found: %s", runID)
	}
	r.PolicyReservationID = ""
	return nil
}

// addRun inserts a run directly for testing.
func (m *mockRunRepo) addRun(run *models.DiscoveryRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[run.ID] = run
}

// mockFeedbackRepo implements database.FeedbackRepo using an in-memory slice.
type mockFeedbackRepo struct {
	mu       sync.Mutex
	items    []*models.Feedback
	nextID   int

	upsertErr error
	listErr   error
	deleteErr error
}

func newMockFeedbackRepo() *mockFeedbackRepo {
	return &mockFeedbackRepo{}
}

func (m *mockFeedbackRepo) Upsert(_ context.Context, fb *models.Feedback) (*models.Feedback, error) {
	if m.upsertErr != nil {
		return nil, m.upsertErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for existing feedback on same target (upsert behavior)
	for i, existing := range m.items {
		if existing.DiscoveryID == fb.DiscoveryID &&
			existing.TargetType == fb.TargetType &&
			existing.TargetID == fb.TargetID {
			fb.ID = existing.ID
			stored := *fb
			m.items[i] = &stored
			return &stored, nil
		}
	}

	m.nextID++
	fb.ID = fmt.Sprintf("fb-%d", m.nextID)
	stored := *fb
	m.items = append(m.items, &stored)
	return &stored, nil
}

func (m *mockFeedbackRepo) ListByDiscovery(_ context.Context, discoveryID string) ([]*models.Feedback, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*models.Feedback
	for _, fb := range m.items {
		if fb.DiscoveryID == discoveryID {
			cp := *fb
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (m *mockFeedbackRepo) Delete(_ context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, fb := range m.items {
		if fb.ID == id {
			m.items = append(m.items[:i], m.items[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("feedback not found: %s", id)
}

// mockDomainPackRepo implements database.DomainPackRepo using an in-memory map.
type mockDomainPackRepo struct {
	mu     sync.Mutex
	packs  map[string]*models.DomainPack
	nextID int

	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error
}

func newMockDomainPackRepo() *mockDomainPackRepo {
	return &mockDomainPackRepo{packs: make(map[string]*models.DomainPack)}
}

func (m *mockDomainPackRepo) Create(_ context.Context, pack *models.DomainPack) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.packs[pack.Slug]; exists {
		return fmt.Errorf("domain pack with slug %q already exists", pack.Slug)
	}
	m.nextID++
	pack.ID = fmt.Sprintf("dp-%d", m.nextID)
	pack.CreatedAt = time.Now()
	pack.UpdatedAt = time.Now()
	stored := *pack
	m.packs[pack.Slug] = &stored
	return nil
}

func (m *mockDomainPackRepo) GetBySlug(_ context.Context, slug string) (*models.DomainPack, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.packs[slug]
	if !ok {
		return nil, nil
	}
	cp := *p
	return &cp, nil
}

func (m *mockDomainPackRepo) GetByID(_ context.Context, id string) (*models.DomainPack, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.packs {
		if p.ID == id {
			cp := *p
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockDomainPackRepo) List(_ context.Context, publishedOnly bool) ([]*models.DomainPack, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*models.DomainPack, 0)
	for _, p := range m.packs {
		if publishedOnly && !p.IsPublished {
			continue
		}
		cp := *p
		result = append(result, &cp)
	}
	return result, nil
}

func (m *mockDomainPackRepo) Update(_ context.Context, slug string, pack *models.DomainPack) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.packs[slug]; !ok {
		return fmt.Errorf("domain pack not found: %s", slug)
	}
	pack.UpdatedAt = time.Now()
	stored := *pack
	m.packs[slug] = &stored
	return nil
}

func (m *mockDomainPackRepo) Delete(_ context.Context, slug string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.packs[slug]; !ok {
		return fmt.Errorf("domain pack not found: %s", slug)
	}
	delete(m.packs, slug)
	return nil
}

func (m *mockDomainPackRepo) add(pack *models.DomainPack) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if pack.ID == "" {
		m.nextID++
		pack.ID = fmt.Sprintf("dp-%d", m.nextID)
	}
	m.packs[pack.Slug] = pack
}

// testDomainPack returns a minimal domain pack for testing.
func testDomainPack(slug, category string) *models.DomainPack {
	return &models.DomainPack{
		Slug:        slug,
		Name:        slug,
		IsPublished: true,
		Categories: []models.PackCategory{
			{ID: category, Name: category, Description: "test category"},
		},
		Prompts: models.PackPrompts{
			Base: models.BasePrompts{
				BaseContext:     "Context: {{PROFILE}}\n{{PREVIOUS_CONTEXT}}",
				Exploration:     "Explore {{DATASET}} using {{SCHEMA_INFO}} with {{FILTER}} {{FILTER_CONTEXT}} {{FILTER_RULE}} areas: {{ANALYSIS_AREAS}}",
				Recommendations: "Recommend based on {{INSIGHTS_DATA}} summary: {{INSIGHTS_SUMMARY}} date: {{DISCOVERY_DATE}}",
			},
			Categories: map[string]models.CategoryPrompts{
				category: {ExplorationContext: "Category-specific context for " + category},
			},
		},
		AnalysisAreas: models.PackAnalysisAreas{
			Base: []models.PackAnalysisArea{
				{
					ID: "test_area", Name: "Test Area", Description: "Test analysis area",
					Keywords: []string{"test"}, Priority: 1,
					Prompt: "Analyze {{DATASET}} with {{TOTAL_QUERIES}} queries: {{QUERY_RESULTS}}",
				},
			},
			Categories: map[string][]models.PackAnalysisArea{},
		},
		ProfileSchema: models.PackProfileSchema{
			Base:       map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			Categories: map[string]map[string]interface{}{},
		},
	}
}

// mockPricingRepo implements database.PricingRepo using a single in-memory value.
type mockPricingRepo struct {
	mu      sync.Mutex
	pricing *models.Pricing

	getErr  error
	saveErr error
}

func newMockPricingRepo() *mockPricingRepo {
	return &mockPricingRepo{}
}

func (m *mockPricingRepo) Get(_ context.Context) (*models.Pricing, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pricing == nil {
		return nil, nil
	}
	cp := *m.pricing
	return &cp, nil
}

func (m *mockPricingRepo) Save(_ context.Context, pricing *models.Pricing) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pricing.UpdatedAt = time.Now()
	stored := *pricing
	m.pricing = &stored
	return nil
}

// mockRunner implements runner.Runner for testing discovery trigger/cancel.
type mockRunner struct {
	mu       sync.Mutex
	runCalls []runner.RunOptions
	canceled []string

	runErr    error
	cancelErr error
}

func newMockRunner() *mockRunner {
	return &mockRunner{}
}

func (m *mockRunner) Run(_ context.Context, opts runner.RunOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runCalls = append(m.runCalls, opts)
	if m.runErr != nil {
		return m.runErr
	}
	return nil
}

func (m *mockRunner) RunSync(_ context.Context, _ runner.RunSyncOptions) (*runner.RunSyncResult, error) {
	return &runner.RunSyncResult{Output: []byte("{}")}, nil
}

func (m *mockRunner) Cancel(_ context.Context, runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canceled = append(m.canceled, runID)
	if m.cancelErr != nil {
		return m.cancelErr
	}
	return nil
}
