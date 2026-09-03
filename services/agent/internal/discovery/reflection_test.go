package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- fakes ---

type fakeLedgerRepo struct {
	ledger  *commonmodels.DiscoveryLedger
	saved   *commonmodels.DiscoveryLedger
	getErr  error
	saveErr error
}

func (f *fakeLedgerRepo) Get(_ context.Context, projectID string) (*commonmodels.DiscoveryLedger, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.ledger == nil {
		return &commonmodels.DiscoveryLedger{ProjectID: projectID}, nil
	}
	return f.ledger, nil
}
func (f *fakeLedgerRepo) Save(_ context.Context, l *commonmodels.DiscoveryLedger) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = l
	return nil
}

type fakeFindingRepo struct {
	findings  []commonmodels.LedgerFinding
	upserted  []commonmodels.LedgerFinding
	pruneMax  int
	upsertErr error
	listErr   error
}

func (f *fakeFindingRepo) List(_ context.Context, _ string) ([]commonmodels.LedgerFinding, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.findings, nil
}
func (f *fakeFindingRepo) Upsert(_ context.Context, fd *commonmodels.LedgerFinding) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserted = append(f.upserted, *fd)
	return nil
}
func (f *fakeFindingRepo) Prune(_ context.Context, _ string, max int) error {
	f.pruneMax = max
	return nil
}

type fakeTaskRepo struct {
	existing []commonmodels.LedgerTask
	inserted []commonmodels.LedgerTask
}

func (f *fakeTaskRepo) List(_ context.Context, _ string, _ ...string) ([]commonmodels.LedgerTask, error) {
	return f.existing, nil
}
func (f *fakeTaskRepo) Insert(_ context.Context, ts []commonmodels.LedgerTask) error {
	f.inserted = append(f.inserted, ts...)
	return nil
}

type fakeProposalRepo struct {
	open     []commonmodels.PackProposal
	inserted []commonmodels.PackProposal
}

func (f *fakeProposalRepo) Insert(_ context.Context, ps []commonmodels.PackProposal) error {
	f.inserted = append(f.inserted, ps...)
	return nil
}
func (f *fakeProposalRepo) ListForProject(_ context.Context, _ string, _ ...string) ([]commonmodels.PackProposal, error) {
	return f.open, nil
}

// --- pure helpers ---

func TestMaskSQLLiterals(t *testing.T) {
	got := maskSQLLiterals("SELECT * FROM users WHERE email = 'alice@example.com' AND id = 123456")
	if strings.Contains(got, "alice@example.com") {
		t.Errorf("string literal not masked: %s", got)
	}
	if strings.Contains(got, "123456") {
		t.Errorf("long digit run not masked: %s", got)
	}
	if !strings.Contains(got, "FROM users WHERE email") {
		t.Errorf("structure should survive masking: %s", got)
	}
}

func TestBuildFindingCandidates_SubstanceAndMasking(t *testing.T) {
	result := &models.DiscoveryResult{
		ProjectID: "proj-1",
		Insights: []models.Insight{
			{
				AnalysisArea:  "churn",
				Name:          "High EU churn",
				Description:   "Churn is elevated",
				Severity:      "high",
				AffectedCount: 100,
				SQLMetadata:   &models.SQLMetadata{Query: "SELECT churn FROM t WHERE country = 'DE'"},
				Indicators:    []string{"signal-a", "signal-b"},
			},
			{Name: ""}, // dropped (no name)
		},
	}
	got := buildFindingCandidates(result)
	if len(got) != 1 {
		t.Fatalf("want 1 candidate (empty name dropped), got %d", len(got))
	}
	f := got[0]
	if f.SQL == "" || strings.Contains(f.SQL, "'DE'") {
		t.Errorf("SQL should be carried but masked: %q", f.SQL)
	}
	if f.NormalizedKey == "" || f.Evidence == "" || f.KeyMetric == "" {
		t.Errorf("substance not populated: %+v", f)
	}
}

func TestFindingMagnitudeChanged(t *testing.T) {
	prior := &commonmodels.LedgerFinding{Severity: "high", AffectedCount: 100}
	same := &commonmodels.LedgerFinding{Severity: "high", AffectedCount: 105}
	if findingMagnitudeChanged(prior, same, 0.2) {
		t.Error("5% delta should not be a trend at 0.2 threshold")
	}
	bigger := &commonmodels.LedgerFinding{Severity: "high", AffectedCount: 200}
	if !findingMagnitudeChanged(prior, bigger, 0.2) {
		t.Error("100% delta should be a trend")
	}
	sevChange := &commonmodels.LedgerFinding{Severity: "critical", AffectedCount: 100}
	if !findingMagnitudeChanged(prior, sevChange, 0.2) {
		t.Error("severity change should be a trend")
	}
}

func TestParseReflection(t *testing.T) {
	_, err := parseReflection("not json")
	if err == nil {
		t.Error("non-JSON should error")
	}
	ref, err := parseReflection("```json\n{\"coverage_summary\":\"ok\",\"next_tasks\":[{\"text\":\"look at orders\"}]}\n```")
	if err != nil {
		t.Fatalf("fenced JSON should parse: %v", err)
	}
	if ref.CoverageSummary != "ok" || len(ref.NextTasks) != 1 {
		t.Errorf("bad parse: %+v", ref)
	}
}

// --- consolidateFindings (dedup + trend, no embedder → exact-key path) ---

func TestConsolidateFindings_NewAndTrend(t *testing.T) {
	fr := &fakeFindingRepo{
		findings: []commonmodels.LedgerFinding{
			{ID: "f1", Area: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100,
				NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"), Status: "confirmed"},
		},
	}
	o := &Orchestrator{projectID: "proj-1", findingRepo: fr}

	result := &models.DiscoveryResult{
		ProjectID: "proj-1",
		ID:        "disc-2",
		Insights: []models.Insight{
			// Reworded/same key with a big magnitude jump → trend "changed".
			{AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 300},
			// A brand new finding.
			{AnalysisArea: "fraud", Name: "Card testing spike", Severity: "critical", AffectedCount: 12},
		},
	}
	newCount, total, err := o.consolidateFindings(context.Background(), result)
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if newCount != 1 {
		t.Errorf("want 1 new finding, got %d", newCount)
	}
	if total != 2 {
		t.Errorf("want total 2, got %d", total)
	}
	// The merged finding must be marked changed with a bumped seen count.
	var merged *commonmodels.LedgerFinding
	for i := range fr.upserted {
		if fr.upserted[i].ID == "f1" {
			merged = &fr.upserted[i]
		}
	}
	if merged == nil {
		t.Fatal("existing finding f1 was not upserted")
	}
	if merged.Status != commonmodels.LedgerFindingStatusChanged {
		t.Errorf("magnitude jump should mark 'changed', got %q", merged.Status)
	}
	if merged.SeenCount != 1 { // prior had 0 seen count in the fixture; +1
		t.Errorf("seen count should bump, got %d", merged.SeenCount)
	}
}

// --- gating ---

func TestRunPhaseReflection_ToggleOff_NoWork(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	fr := &fakeFindingRepo{}
	o := &Orchestrator{
		reflectionEnabled: false, projectID: "proj-1", runID: "run-1", // Layer B off
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr,
	}
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1",
		Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}})

	if len(fr.upserted) != 0 {
		t.Fatalf("toggle off must do no work, upserted=%d", len(fr.upserted))
	}
}

func TestRunPhaseReflection_EnvOff_NoWork(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "false")
	fr := &fakeFindingRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1",
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr,
	}
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1",
		Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}})

	if len(fr.upserted) != 0 {
		t.Fatalf("deployment flag off must do no work, upserted=%d", len(fr.upserted))
	}
}

func TestRunPhaseReflection_LicenseGate_NoWork(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	policy.RegisterChecker(denyChecker{policy.NewNoopChecker()})
	t.Cleanup(func() { policy.RegisterChecker(policy.NewNoopChecker()) })

	client, prov := stubClient(t, `{"coverage_summary":"x"}`, nil)
	fr := &fakeFindingRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr,
	}
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1",
		Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}})

	if prov.calls != 0 || len(fr.upserted) != 0 {
		t.Fatalf("no sources entitlement must do no work: calls=%d upserted=%d", prov.calls, len(fr.upserted))
	}
}

// --- happy path (env + entitlement on) with a stub LLM ---

func TestRunPhaseReflection_HappyPath_AutoMode(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	// auto evolution mode so next_tasks + pack deltas are applied.
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeAuto})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	resp := `{"coverage_summary":"orders covered; events untouched","covered_tables":["ds.orders"],` +
		`"covered_areas":["churn"],"learnings":[{"category":"schema","note":"status 4 means closed","relevance":0.8}],` +
		`"next_tasks":[{"text":"explore the events tables","kind":"next_task"}],` +
		`"domain_pack_deltas":[{"action":"add_area","area_id":"fraud","area_name":"Fraud","rationale":"fraud signals recur"}]}`
	client, prov := stubClient(t, resp, nil)

	ledger := &fakeLedgerRepo{}
	fr := &fakeFindingRepo{}
	tr := &fakeTaskRepo{}
	pr := &fakeProposalRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: ledger, findingRepo: fr, taskRepo: tr, proposalRepo: pr,
	}

	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{
		ID:        "disc-1",
		ProjectID: "proj-1",
		Schemas:   map[string]models.TableSchema{"ds.orders": {}, "ds.events": {}},
		Insights: []models.Insight{
			{AnalysisArea: "churn", Name: "High churn", Severity: "high", AffectedCount: 40},
		},
	})

	if prov.calls == 0 {
		t.Fatal("expected the reflection LLM to be called")
	}
	if len(fr.upserted) != 1 {
		t.Fatalf("want 1 finding captured, got %d", len(fr.upserted))
	}
	if len(tr.inserted) != 1 {
		t.Fatalf("want 1 next-task inserted, got %d", len(tr.inserted))
	}
	if len(pr.inserted) != 1 {
		t.Fatalf("want 1 pack proposal inserted, got %d", len(pr.inserted))
	}
	if pr.inserted[0].Status != commonmodels.PackProposalStatusProposed {
		t.Errorf("proposal must be 'proposed', got %q", pr.inserted[0].Status)
	}
	if ledger.saved == nil || len(ledger.saved.Convergence) != 1 {
		t.Fatalf("ledger convergence not recorded: %+v", ledger.saved)
	}
	if fr.pruneMax == 0 {
		t.Error("prune should have been called with the max cap")
	}
}

func TestRunPhaseReflection_OffMode_NoTasksOrProposals(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff})
	t.Cleanup(func() { agentplugin.RegisterDiscoveryPolicyProvider(stubPolicy{mode: agentplugin.EvolutionModeOff}) })

	resp := `{"coverage_summary":"x","next_tasks":[{"text":"t"}],"domain_pack_deltas":[{"action":"add_area","area_id":"a","rationale":"r"}]}`
	client, _ := stubClient(t, resp, nil)
	fr := &fakeFindingRepo{}
	tr := &fakeTaskRepo{}
	pr := &fakeProposalRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr, taskRepo: tr, proposalRepo: pr,
	}

	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1", ProjectID: "proj-1",
		Insights: []models.Insight{{AnalysisArea: "a", Name: "n", Severity: "low", AffectedCount: 1}}})

	// Findings are still captured (low-risk), but no tasks/proposals under off.
	if len(fr.upserted) != 1 {
		t.Fatalf("findings should still be captured under off, got %d", len(fr.upserted))
	}
	if len(tr.inserted) != 0 || len(pr.inserted) != 0 {
		t.Fatalf("off mode must not surface tasks/proposals: tasks=%d proposals=%d", len(tr.inserted), len(pr.inserted))
	}
}

// --- best-effort isolation ---

func TestRunPhaseReflection_LLMError_Swallowed(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	client, _ := stubClient(t, "", errors.New("boom"))
	fr := &fakeFindingRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr,
	}
	// Must not panic / propagate. Findings still captured deterministically.
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1", ProjectID: "proj-1",
		Insights: []models.Insight{{AnalysisArea: "a", Name: "n", Severity: "low", AffectedCount: 1}}})
	if len(fr.upserted) != 1 {
		t.Fatalf("finding capture should survive an LLM failure, got %d", len(fr.upserted))
	}
}

func TestRunPhaseReflection_StoreError_Swallowed(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	client, _ := stubClient(t, `{"coverage_summary":"x"}`, nil)
	fr := &fakeFindingRepo{upsertErr: context.DeadlineExceeded, listErr: nil}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr,
	}
	// A failing finding store must be swallowed (best-effort).
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1", ProjectID: "proj-1",
		Insights: []models.Insight{{AnalysisArea: "a", Name: "n", Severity: "low", AffectedCount: 1}}})
}

// stubPolicy is a DiscoveryPolicyProvider returning a fixed mode.
type stubPolicy struct {
	mode agentplugin.EvolutionMode
}

func (s stubPolicy) Policy(context.Context, string) (agentplugin.DiscoveryPolicy, error) {
	return agentplugin.DiscoveryPolicy{EvolutionMode: s.mode, FrontierPolicy: agentplugin.FrontierBalanced}, nil
}
func (stubPolicy) Name() string { return "stub" }
