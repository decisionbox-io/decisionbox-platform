package discovery

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/vectorstore"
	"github.com/decisionbox-io/decisionbox/libs/go-common/policy"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// --- fakes ---

type fakeLedgerRepo struct {
	ledger  *commonmodels.DiscoveryLedger
	saved   *commonmodels.DiscoveryLedger
	getErr  error
	saveErr error
	// The reflection-in-flight marker, mirrored so a test can check the
	// phase brackets itself and leaves nothing set behind.
	reflectingRunID string
	beganReflection int
	endedReflection int
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
func (f *fakeLedgerRepo) BeginReflection(_ context.Context, _, runID string) error {
	f.reflectingRunID = runID
	f.beganReflection++
	return nil
}
func (f *fakeLedgerRepo) EndReflection(_ context.Context, _, runID string) error {
	if f.reflectingRunID == runID {
		f.reflectingRunID = ""
	}
	f.endedReflection++
	return nil
}

type fakeFindingRepo struct {
	findings  []commonmodels.LedgerFinding
	upserted  []commonmodels.LedgerFinding
	answered  []answeredCall
	// merged holds each finding as it stood after a Merge, which is what the
	// repository writes for a finding this run saw again.
	merged []commonmodels.LedgerFinding
	// statuses holds each finding after the model's verdict was applied.
	statuses []commonmodels.LedgerFinding
	pruneMax  int
	upsertErr error
	listErr   error
}

// List is the ACTIVE set, as the real repository's is — archived findings are
// filtered out here too, so a test that archives one exercises the same
// exclusion the prompt paths rely on.
func (f *fakeFindingRepo) List(_ context.Context, _ string) ([]commonmodels.LedgerFinding, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	active := make([]commonmodels.LedgerFinding, 0, len(f.findings))
	for i := range f.findings {
		if !f.findings[i].Archived {
			active = append(active, f.findings[i])
		}
	}
	return active, nil
}
func (f *fakeFindingRepo) ListMatching(_ context.Context, _ string, keys, ids []string) ([]commonmodels.LedgerFinding, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return matchingFindings(f.findings, keys, ids), nil
}

// matchingFindings mirrors the repository: every row matching one of the keys
// or ids, archived or not. The caller reconciles against what it already read.
func matchingFindings(all []commonmodels.LedgerFinding, keys, ids []string) []commonmodels.LedgerFinding {
	want := map[string]bool{}
	for _, k := range keys {
		want["k:"+k] = true
	}
	for _, id := range ids {
		want["i:"+id] = true
	}
	out := []commonmodels.LedgerFinding{}
	for i := range all {
		if want["k:"+all[i].NormalizedKey] || want["i:"+all[i].ID] {
			out = append(out, all[i])
		}
	}
	return out
}

func (f *fakeFindingRepo) Upsert(_ context.Context, fd *commonmodels.LedgerFinding) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upserted = append(f.upserted, *fd)
	return nil
}
func (f *fakeFindingRepo) Merge(_ context.Context, _, id string, m database.FindingMerge) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	// Applied the way the repository applies it — fields set, trails added,
	// counter incremented — so a test reads back what Mongo would hold.
	for i := range f.findings {
		if f.findings[i].ID != id {
			continue
		}
		fd := &f.findings[i]
		fd.Description, fd.KeyMetric, fd.Evidence = m.Description, m.KeyMetric, m.Evidence
		fd.Severity, fd.AffectedCount = m.Severity, m.AffectedCount
		if len(m.StatusChanges) > 0 {
			fd.Status = m.StatusChanges[len(m.StatusChanges)-1].Status
		}
		fd.LastSeen, fd.SourceDiscoveryID = m.LastSeen, m.SourceDiscoveryID
		fd.Liked = fd.Liked || m.Liked
		fd.SeenCount++
		// A merge is an observation: it always brings the finding back.
		fd.Archived = false
		fd.ArchivedAt = time.Time{}
		fd.SourceInsightIDs = appendUnique(fd.SourceInsightIDs, m.InsightIDs)
		if m.Sighting != nil {
			fd.Sightings = commonmodels.TrimFindingTrail(append(fd.Sightings, *m.Sighting))
		}
		fd.StatusHistory = commonmodels.TrimFindingTrail(append(fd.StatusHistory, m.StatusChanges...))
		f.merged = append(f.merged, *fd)
		return nil
	}
	return nil
}
func (f *fakeFindingRepo) ApplyStatus(_ context.Context, _, id, status string, c commonmodels.FindingStatusChange) error {
	for i := range f.findings {
		if f.findings[i].ID != id {
			continue
		}
		f.findings[i].Status = status
		f.findings[i].StatusHistory = commonmodels.TrimFindingTrail(append(f.findings[i].StatusHistory, c))
		f.statuses = append(f.statuses, f.findings[i])
	}
	return nil
}
func (f *fakeFindingRepo) MarkAnsweredTask(_ context.Context, _ string, findingIDs []string, taskID string) error {
	// Mirrors the repository's $addToSet onto the fake's own documents, so a
	// test reads the edge back where it would really be.
	for _, id := range findingIDs {
		for i := range f.findings {
			if f.findings[i].ID != id {
				continue
			}
			f.findings[i].AnsweredTaskIDs = appendUnique(f.findings[i].AnsweredTaskIDs, []string{taskID})
		}
	}
	f.answered = append(f.answered, answeredCall{taskID: taskID, findingIDs: findingIDs})
	return nil
}
func (f *fakeFindingRepo) Prune(_ context.Context, _ string, max int) error {
	f.pruneMax = max
	return nil
}

// answeredCall records one MarkAnsweredTask call.
type answeredCall struct {
	taskID     string
	findingIDs []string
}

type fakeTaskRepo struct {
	existing []commonmodels.LedgerTask
	inserted []commonmodels.LedgerTask
	updates  []taskUpdate
	// pursued records each MarkPursued call: the run id and the task ids it
	// was given.
	pursued []pursuedCall
	// lost makes every Close report that another reflection closed the thread
	// first, which is what the real repository does when its claim misses.
	lost bool
}

type taskUpdate struct {
	id, status string
	close      database.TaskClose
}

type pursuedCall struct {
	runID string
	ids   []string
}

func (f *fakeTaskRepo) List(_ context.Context, _ string, _ ...string) ([]commonmodels.LedgerTask, error) {
	return f.existing, nil
}
func (f *fakeTaskRepo) Insert(_ context.Context, ts []commonmodels.LedgerTask) error {
	f.inserted = append(f.inserted, ts...)
	return nil
}
func (f *fakeTaskRepo) Close(_ context.Context, _, id string, u database.TaskClose) (bool, error) {
	f.updates = append(f.updates, taskUpdate{id: id, status: u.Status, close: u})
	// The real repository reports false when another reflection got there
	// first; `lost` lets a test stand in that caller's shoes.
	return !f.lost, nil
}
func (f *fakeTaskRepo) MarkPursued(_ context.Context, _ string, ids []string, runID string) error {
	f.pursued = append(f.pursued, pursuedCall{runID: runID, ids: ids})
	return nil
}

type fakeLearningRepo struct {
	upserted []commonmodels.LedgerLearning
	err      error
}

func (f *fakeLearningRepo) Upsert(_ context.Context, l *commonmodels.LedgerLearning) error {
	if f.err != nil {
		return f.err
	}
	f.upserted = append(f.upserted, *l)
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

func TestBuildFindingCandidates_Substance(t *testing.T) {
	result := &models.DiscoveryResult{
		ProjectID: "proj-1",
		Insights: []models.Insight{
			{
				AnalysisArea:  "churn",
				Name:          "High EU churn",
				Description:   "Churn is elevated",
				Severity:      "high",
				AffectedCount: 100,
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
	if f.NormalizedKey == "" || f.Evidence == "" || f.KeyMetric == "" {
		t.Errorf("substance not populated: %+v", f)
	}
}

// TestApplyTaskStatusUpdates verifies grounded closure: only done/dropped
// against a known open task id is applied, unknown ids and invalid statuses are
// ignored, and the closed tasks' normalized keys are returned for dedup.
func TestApplyTaskStatusUpdates(t *testing.T) {
	t1 := commonmodels.LedgerTask{ID: "t1", Status: "open", NormalizedKey: "k1"}
	t2 := commonmodels.LedgerTask{ID: "t2", Status: "open", NormalizedKey: "k2"}
	tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{t1, t2}}
	o := &Orchestrator{projectID: "p1", taskRepo: tr}

	ref := &parsedReflection{}
	ref.TaskStatusUpdates = []struct {
		TaskID               string   `json:"task_id"`
		Status               string   `json:"status"`
		Reason               string   `json:"reason"`
		ResolvedByFindingIDs []string `json:"resolved_by_finding_ids"`
	}{
		{TaskID: "t1", Status: "done"},
		{TaskID: "t2", Status: "dropped"},
		{TaskID: "ghost", Status: "done"},  // unknown id → skip
		{TaskID: "t1", Status: "reopened"}, // invalid status → skip
	}
	closed := o.applyTaskStatusUpdates(context.Background(), &models.DiscoveryResult{ID: "d1"}, ref, consolidationResult{})

	if len(tr.updates) != 2 {
		t.Fatalf("want 2 status writes (t1 done, t2 dropped), got %d: %+v", len(tr.updates), tr.updates)
	}
	if !closed["k1"] || !closed["k2"] || len(closed) != 2 {
		t.Errorf("closed keys should be {k1,k2}, got %+v", closed)
	}
}

// TestApplyNextTasks_DedupAndSupersedes verifies a follow-up that matches a task
// just closed this run is NOT recreated (no close→recreate ping-pong), and that
// supersedes is carried onto the new task.
func TestApplyNextTasks_DedupAndSupersedes(t *testing.T) {
	tr := &fakeTaskRepo{} // no open tasks
	o := &Orchestrator{projectID: "p1", runID: "r1", taskRepo: tr}

	repeatKey := commonmodels.NormalizedQuestionKey("re-check the churn spike")
	ref := &parsedReflection{}
	ref.NextTasks = []struct {
		Title            string   `json:"title"`
		Text             string   `json:"text"`
		Kind             string   `json:"kind"`
		TargetType       string   `json:"target_type"`
		TargetID         string   `json:"target_id"`
		Supersedes       string   `json:"supersedes"`
		OriginFindingIDs []string `json:"origin_finding_ids"`
	}{
		{Title: "Recheck churn", Text: "re-check the churn spike"},                          // matches a closed task → dropped
		{Title: "Profile power sellers", Text: "profile the top sellers", Supersedes: "t9"}, // fresh → kept, keeps link
	}
	o.applyNextTasks(context.Background(), ref, map[string]bool{repeatKey: true}, consolidationResult{})

	if len(tr.inserted) != 1 {
		t.Fatalf("want 1 inserted (the closed repeat is skipped), got %d: %+v", len(tr.inserted), tr.inserted)
	}
	if tr.inserted[0].Supersedes != "t9" {
		t.Errorf("supersedes should be carried, got %q", tr.inserted[0].Supersedes)
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
	cons, err := o.consolidateFindings(context.Background(), result)
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	newCount, total, reSeen := cons.NewCount, cons.TotalCount, cons.ReSeen
	if newCount != 1 {
		t.Errorf("want 1 new finding, got %d", newCount)
	}
	if total != 2 {
		t.Errorf("want total 2, got %d", total)
	}
	// The carried finding was surfaced again — that, and only that, is what
	// licenses asking the model to re-judge it.
	if len(reSeen) != 1 {
		t.Errorf("want 1 carried finding re-seen, got %d", len(reSeen))
	}
	// The merged finding must be marked changed with a bumped seen count.
	var merged *commonmodels.LedgerFinding
	for i := range fr.merged {
		if fr.merged[i].ID == "f1" {
			merged = &fr.merged[i]
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

// TestRunPhaseReflection_EnvUnset_DefaultsOn pins Layer A's default. An unset
// DISCOVERY_REFLECTION_ENABLED must behave as enabled: the phase is already
// protected by the enterprise-only ledger/finding repos and the sources
// entitlement, so defaulting Layer A off only produced silently-empty ledgers
// on deployments that never set it.
func TestRunPhaseReflection_EnvUnset_DefaultsOn(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "") // empty => default branch

	client, _ := stubClient(t, `{"coverage_summary":"x"}`, nil)
	fr := &fakeFindingRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: &fakeLedgerRepo{}, findingRepo: fr,
	}
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1",
		Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}})

	if len(fr.upserted) == 0 {
		t.Fatal("unset deployment flag must default ON and consolidate findings, upserted=0")
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

// TestRunPhaseReflection_BracketsItselfWithTheInFlightMarker: the one-shot
// backfill cannot tell from run status that the ledger is still being written
// — a run is `completed` before this phase starts — so the phase says so
// itself, and must always take the marker back down. Every exit below the
// gates is a best-effort bail-out, and a marker left set would block the
// backfill on a project nothing is writing.
func TestRunPhaseReflection_BracketsItselfWithTheInFlightMarker(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")

	client, _ := stubClient(t, `{"coverage_summary":"x"}`, nil)
	lr := &fakeLedgerRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: lr, findingRepo: &fakeFindingRepo{},
	}
	o.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1",
		Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}})

	if lr.beganReflection != 1 || lr.endedReflection != 1 {
		t.Errorf("the phase must bracket itself exactly once: began=%d ended=%d",
			lr.beganReflection, lr.endedReflection)
	}
	if lr.reflectingRunID != "" {
		t.Errorf("the marker must not be left set, got %q", lr.reflectingRunID)
	}

	// A phase that bails out early still clears it. An upsert failure is one
	// of the best-effort paths that returns without finishing.
	lr2 := &fakeLedgerRepo{}
	o2 := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-2", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: lr2, findingRepo: &fakeFindingRepo{listErr: errors.New("mongo is down")},
	}
	o2.RunPhaseReflection(context.Background(), &models.DiscoveryResult{ID: "d1",
		Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}})
	if lr2.endedReflection != 1 || lr2.reflectingRunID != "" {
		t.Errorf("a bail-out must still clear the marker: ended=%d set=%q",
			lr2.endedReflection, lr2.reflectingRunID)
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

// Regression: two insights with the same area+name in ONE run must merge into a
// single ledger finding, not create two docs and double-count newCount.
func TestConsolidateFindings_SameRunDuplicateMerges(t *testing.T) {
	fr := &fakeFindingRepo{}
	o := &Orchestrator{projectID: "p1", findingRepo: fr}
	result := &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{
			{AnalysisArea: "churn", Name: "High churn", Severity: "high", AffectedCount: 100},
			{AnalysisArea: "churn", Name: "High churn", Severity: "high", AffectedCount: 100}, // dup
		},
	}
	cons, err := o.consolidateFindings(context.Background(), result)
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	newCount, total, reSeen := cons.NewCount, cons.TotalCount, cons.ReSeen
	if newCount != 1 {
		t.Errorf("same-run duplicate should count as 1 new finding, got %d", newCount)
	}
	if total != 1 {
		t.Errorf("total should be 1 (one deduped finding), got %d", total)
	}
	// The duplicate merged into a finding this run created moments earlier.
	// That is not evidence about the project's history, and counting it as
	// such would order a first run to re-judge its own work.
	if len(reSeen) != 0 {
		t.Errorf("a same-run duplicate merge is not a re-seen prior finding, got %d", len(reSeen))
	}
}

// --- Investigation-graph provenance (enterprise#389) ---

// TestBuildFindingCandidates_CarriesInsightID is the cheapest and most
// load-bearing edge in the whole investigation graph: the insight id keys the
// insight in the discovery document, the standalone collection and the Qdrant
// point, so carrying it onto the finding is what makes every downstream link
// (narrative, SQL, quality caveats, validation verdict) resolvable.
func TestBuildFindingCandidates_CarriesInsightID(t *testing.T) {
	result := &models.DiscoveryResult{
		ProjectID: "p1",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high"},
			{ID: "  ", AnalysisArea: "fraud", Name: "Card testing", Severity: "critical"}, // blank id
		},
	}
	got := buildFindingCandidates(result)
	if len(got) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(got))
	}
	if len(got[0].SourceInsightIDs) != 1 || got[0].SourceInsightIDs[0] != "ins-1" {
		t.Errorf("first candidate should carry its insight id, got %v", got[0].SourceInsightIDs)
	}
	// An empty string is not a provenance edge — storing one would produce a
	// finding that claims to know where it came from and cannot say.
	if got[1].SourceInsightIDs != nil {
		t.Errorf("a blank insight id must not be stored, got %v", got[1].SourceInsightIDs)
	}
}

// TestConsolidateFindings_SightingsAndStatusTrail covers the two edges that
// turn a counter into history: one sighting per run (so a magnitude trend is
// drawable), and a status move that arrives with the evidence for it.
func TestConsolidateFindings_SightingsAndStatusTrail(t *testing.T) {
	prior := commonmodels.LedgerFinding{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "high", AffectedCount: 100, Status: commonmodels.LedgerFindingStatusConfirmed,
		SeenCount: 1,
		Sightings: []commonmodels.FindingSighting{{RunID: "r0", DiscoveryID: "d0", AffectedCount: 100}},
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{prior}}
	o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}

	result := &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{
			// Same key, magnitude jumped 100 → 300 → a trend.
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 300},
			// Brand new.
			{ID: "ins-2", AnalysisArea: "fraud", Name: "Card testing spike", Severity: "critical", AffectedCount: 12},
		},
	}
	cons, err := o.consolidateFindings(context.Background(), result)
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	// Every insight resolves to the finding it landed on. Without this map the
	// model cannot point at a finding from this run at all.
	if got := cons.FindingIDByInsightID["ins-1"]; got != "f1" {
		t.Errorf("ins-1 should resolve to the merged finding f1, got %q", got)
	}
	if got := cons.FindingIDByInsightID["ins-2"]; got == "" || got == "f1" {
		t.Errorf("ins-2 should resolve to the new finding, got %q", got)
	}

	// A finding seen again is MERGED (a delta the other writers survive); a
	// new one is written whole. They land in different places for that reason.
	var merged, fresh *commonmodels.LedgerFinding
	for i := range fr.merged {
		if fr.merged[i].ID == "f1" {
			merged = &fr.merged[i]
		}
	}
	for i := range fr.upserted {
		if fr.upserted[i].ID != "f1" {
			fresh = &fr.upserted[i]
		}
	}
	if merged == nil || fresh == nil {
		t.Fatalf("expected a merged and a new finding, got merged=%+v upserted=%+v", fr.merged, fr.upserted)
	}

	// The merge appends this run's sighting and keeps the earlier one, so the
	// trend is 100 → 300 rather than "300, seen twice".
	if len(merged.Sightings) != 2 {
		t.Fatalf("want 2 sightings on the merged finding, got %d: %+v", len(merged.Sightings), merged.Sightings)
	}
	last := merged.Sightings[1]
	if last.RunID != "r1" || last.DiscoveryID != "d1" || last.AffectedCount != 300 || last.InsightID != "ins-1" {
		t.Errorf("this run's sighting is wrong: %+v", last)
	}
	if merged.SeenCount != 2 {
		t.Errorf("SeenCount should still be the honest total, got %d", merged.SeenCount)
	}
	if !slices.Contains(merged.SourceInsightIDs, "ins-1") {
		t.Errorf("the merge should record the insight it absorbed, got %v", merged.SourceInsightIDs)
	}

	// A server-detected trend explains itself; a status with no account of why
	// it moved is the thing the page could not show before.
	if merged.Status != commonmodels.LedgerFindingStatusChanged {
		t.Errorf("merged status = %q, want changed", merged.Status)
	}
	if len(merged.StatusHistory) != 1 {
		t.Fatalf("want 1 status change recorded, got %d: %+v", len(merged.StatusHistory), merged.StatusHistory)
	}
	sc := merged.StatusHistory[0]
	if sc.Source != commonmodels.FindingStatusSourceTrend || sc.RunID != "r1" || sc.DiscoveryID != "d1" {
		t.Errorf("trend status change is mis-attributed: %+v", sc)
	}
	if !strings.Contains(sc.Reason, "100") || !strings.Contains(sc.Reason, "300") {
		t.Errorf("the trend reason must name the values that moved, got %q", sc.Reason)
	}

	// A new finding opens its own trail with its first sighting.
	if len(fresh.Sightings) != 1 || fresh.Sightings[0].InsightID != "ins-2" {
		t.Errorf("new finding sighting wrong: %+v", fresh.Sightings)
	}
	if len(fresh.StatusHistory) != 1 || fresh.StatusHistory[0].Status != commonmodels.LedgerFindingStatusConfirmed {
		t.Errorf("new finding should open a status trail: %+v", fresh.StatusHistory)
	}
}

// TestConsolidateFindings_TrendReasonNamesSeverity covers the other half of the
// trend signal: when severity moved, that is what the reason must say.
func TestConsolidateFindings_TrendReasonNamesSeverity(t *testing.T) {
	prior := commonmodels.LedgerFinding{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "medium", AffectedCount: 100, Status: commonmodels.LedgerFindingStatusConfirmed, SeenCount: 1,
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{prior}}
	o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}
	_, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "critical", AffectedCount: 100},
		},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if len(fr.merged) != 1 || len(fr.merged[0].StatusHistory) != 1 {
		t.Fatalf("expected one status change, got %+v", fr.merged)
	}
	if got := fr.merged[0].StatusHistory[0].Reason; !strings.Contains(got, "medium") || !strings.Contains(got, "critical") {
		t.Errorf("reason should name the severity move, got %q", got)
	}
}

// TestConsolidateFindings_ReSeenArchivedFindingIsRevived is the half of the
// soft archive that is easy to get wrong. The dedup pass must see archived
// findings, or a finding the retention cap archived and this run saw again is
// written a second time under the same normalized key — two documents for one
// finding, and the edges pointing at the first one orphaned.
func TestConsolidateFindings_ReSeenArchivedFindingIsRevived(t *testing.T) {
	archived := commonmodels.LedgerFinding{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "high", AffectedCount: 100, Status: commonmodels.LedgerFindingStatusConfirmed,
		SeenCount: 1, Archived: true, ArchivedAt: time.Now().Add(-time.Hour),
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{archived}}
	o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}

	cons, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
		},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if cons.NewCount != 0 {
		t.Errorf("re-seeing an archived finding is not a new finding, got NewCount=%d", cons.NewCount)
	}
	if len(fr.merged) != 1 || fr.merged[0].ID != "f1" {
		t.Fatalf("expected a merge into f1, got %+v", fr.merged)
	}
	if fr.merged[0].Archived || !fr.merged[0].ArchivedAt.IsZero() {
		t.Errorf("a finding seen again must come back into the active set: %+v", fr.merged[0])
	}
}

// TestConsolidateFindings_ExactKeyMatchSkipsTheVectorSearch: semantic dedup is
// the fallback for a candidate no key matches, so it must only be asked about
// those. On a run where every finding is a re-sighting — which is most runs on
// a mature project — searching anyway is one round trip per finding for an
// answer that is thrown away, inside a phase on a deadline.
func TestConsolidateFindings_ExactKeyMatchSkipsTheVectorSearch(t *testing.T) {
	known := commonmodels.LedgerFinding{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "high", AffectedCount: 100, SeenCount: 1,
		Status: commonmodels.LedgerFindingStatusConfirmed,
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{known}}
	vs := &mockVectorStore{}
	o := &Orchestrator{
		projectID: "p1", runID: "r2", findingRepo: fr,
		embeddingProvider: &mockEmbeddingProvider{dims: 3, model: "m"},
		vectorStore:       vs,
	}

	// One candidate that matches by key, one that does not.
	if _, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d2",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
			{ID: "ins-2", AnalysisArea: "logistics", Name: "Something new", Severity: "low", AffectedCount: 5},
		},
	}); err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if vs.searches != 1 {
		t.Errorf("only the candidate with no key match should be searched for, got %d searches", vs.searches)
	}
}

// TestDedupSearchBudget_LeavesTimeToWrite: the semantic lookups are optional
// — they only run when the exact key already missed — so they must never be
// able to spend the phase's whole deadline and leave the writes with none.
func TestDedupSearchBudget_LeavesTimeToWrite(t *testing.T) {
	// With plenty of time, the ceiling applies.
	plenty, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := dedupSearchBudget(plenty); got != ledgerDedupSearchBudget {
		t.Errorf("want the ceiling (%s), got %s", ledgerDedupSearchBudget, got)
	}

	// With little time left, a share of it — so most of what remains is still
	// there for the writes.
	tight, cancelTight := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelTight()
	got := dedupSearchBudget(tight)
	if got >= 30*time.Second || got <= 0 {
		t.Errorf("want a share of the 30s left, got %s", got)
	}

	// Already past the deadline: no budget at all, rather than a negative one.
	done, cancelDone := context.WithTimeout(context.Background(), 0)
	defer cancelDone()
	if got := dedupSearchBudget(done); got != 0 {
		t.Errorf("an expired phase has no search budget, got %s", got)
	}

	// No deadline at all (tests, single-binary runs) is the ceiling.
	if got := dedupSearchBudget(context.Background()); got != ledgerDedupSearchBudget {
		t.Errorf("want the ceiling with no deadline, got %s", got)
	}
}

// TestConsolidateFindings_SemanticRevivalCountsTowardTheTotal: a reworded
// insight that semantically matches an archived finding has put that finding
// back into the active set. It is pulled in one at a time rather than through
// the bulk read, so the active count has to see it — leaving it out
// understates the convergence denominator and overstates the marginal-new
// ratio the next run steers by.
func TestConsolidateFindings_SemanticRevivalCountsTowardTheTotal(t *testing.T) {
	archived := commonmodels.LedgerFinding{
		ID: "f-old", ProjectID: "p1", Area: "churn", Name: "Churn is up in the EU",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "Churn is up in the EU"),
		Severity:      "high", AffectedCount: 100, SeenCount: 1,
		Status: commonmodels.LedgerFindingStatusConfirmed, Archived: true,
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{archived}}
	// A vector store that names the archived finding — the rewording case the
	// exact key cannot catch.
	vs := &namingVectorStore{id: "f-old"}
	o := &Orchestrator{
		projectID: "p1", runID: "r2", findingRepo: fr,
		embeddingProvider: &mockEmbeddingProvider{dims: 3, model: "m"},
		vectorStore:       vs,
	}

	cons, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d2",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
		},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if cons.NewCount != 0 {
		t.Errorf("a revival is not a new finding: %d", cons.NewCount)
	}
	if cons.TotalCount != 1 {
		t.Errorf("the revived finding is active again and must be counted: %d", cons.TotalCount)
	}
	if len(fr.merged) != 1 || fr.merged[0].ID != "f-old" {
		t.Errorf("want a merge into the archived finding, got %+v", fr.merged)
	}
}

// namingVectorStore answers every search with one id.
type namingVectorStore struct {
	mockVectorStore
	id string
}

func (n *namingVectorStore) Search(_ context.Context, _ []float64, _ vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	return []vectorstore.SearchResult{{ID: n.id, Score: 1}}, nil
}

// TestConsolidateFindings_WritesExactMatchesBeforeAnySearch is the ordering
// guarantee. Recording the run's findings is work the ledger owes; the
// semantic lookup is an optional nicety for candidates no key matched. Doing
// every lookup first made the optional step able to destroy the mandatory one
// — a stalled vector store burned the phase's deadline and NOTHING was
// written, including findings that matched exactly and never needed a lookup.
func TestConsolidateFindings_WritesExactMatchesBeforeAnySearch(t *testing.T) {
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "high", AffectedCount: 100, SeenCount: 1,
		Status: commonmodels.LedgerFindingStatusConfirmed,
	}}}
	// A vector store that hangs until the phase's deadline, as a stalled
	// Qdrant does.
	vs := &hangingVectorStore{}
	o := &Orchestrator{
		projectID: "p1", runID: "r2", findingRepo: fr,
		embeddingProvider: &mockEmbeddingProvider{dims: 3, model: "m"},
		vectorStore:       vs,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, err := o.consolidateFindings(ctx, &models.DiscoveryResult{
		ProjectID: "p1", ID: "d2",
		Insights: []models.Insight{
			// Matches by key: must be stored whatever the vector store does.
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
			// Matches nothing: this is the one that needs the lookup.
			{ID: "ins-2", AnalysisArea: "logistics", Name: "Something new", Severity: "low", AffectedCount: 5},
		},
	}); err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	if len(fr.merged) != 1 || fr.merged[0].ID != "f1" {
		t.Errorf("the exact match must be stored despite the stalled lookup: %+v", fr.merged)
	}
	if vs.searches == 0 {
		t.Error("the fixture must actually reach the lookup, or it proves nothing")
	}
}

// hangingVectorStore stands in for a vector store that never answers.
type hangingVectorStore struct {
	mockVectorStore
	searches int
}

func (h *hangingVectorStore) Search(ctx context.Context, _ []float64, _ vectorstore.SearchOpts) ([]vectorstore.SearchResult, error) {
	h.searches++
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestConsolidateFindings_ReportsWhatItCouldNotRecord: consolidation is
// best-effort and must never fail a run that has already succeeded — but the
// convergence point it feeds says "this run found N", so a pass that wrote
// nothing would report N=0. Not silence: a false account of a run that found
// things. What it could not record is counted and carried out.
func TestConsolidateFindings_ReportsWhatItCouldNotRecord(t *testing.T) {
	fr := &fakeFindingRepo{upsertErr: errors.New("mongo said no")}
	o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}

	cons, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "A", Severity: "high", AffectedCount: 1},
			{ID: "ins-2", AnalysisArea: "churn", Name: "B", Severity: "high", AffectedCount: 2},
		},
	})
	if err != nil {
		t.Fatalf("consolidate must not fail the run: %v", err)
	}
	if cons.Unrecorded != 2 {
		t.Errorf("both findings were lost and both must be counted, got %d", cons.Unrecorded)
	}
	if cons.NewCount != 0 {
		t.Errorf("nothing landed, so nothing is new: %d", cons.NewCount)
	}

	// And a read it cannot do means none of them landed either.
	blind := &fakeFindingRepo{listErr: errors.New("mongo is down")}
	o2 := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: blind}
	cons2, err2 := o2.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{{ID: "ins-1", AnalysisArea: "churn", Name: "A", Severity: "high", AffectedCount: 1}},
	})
	if err2 == nil {
		t.Error("a failed read is an error, not a quiet no-op")
	}
	if cons2.Unrecorded != 1 {
		t.Errorf("a pass that never started recorded nothing, got %d", cons2.Unrecorded)
	}
}

// TestConsolidateFindings_MatchesARowUnarchivedUnderIt: the dedup pass reads
// the active set, then the rows matching this run's keys. If the second read
// were filtered to archived rows, a finding that was archived when the first
// read ran and un-archived by another reflection before the second would fall
// through both — and a second row would be written under the same key,
// splitting the finding's provenance in two.
func TestConsolidateFindings_MatchesARowUnarchivedUnderIt(t *testing.T) {
	// Active to the second read, archived to the first: the fake's List
	// filters archived rows out, so this stands in for exactly that window.
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "high", AffectedCount: 100, SeenCount: 1,
		Status: commonmodels.LedgerFindingStatusConfirmed,
	}}}
	o := &Orchestrator{projectID: "p1", runID: "r2", findingRepo: fr}

	cons, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d2",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
		},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if cons.NewCount != 0 {
		t.Errorf("the existing row must be matched, not counted new: %d", cons.NewCount)
	}
	if len(fr.upserted) != 0 {
		t.Errorf("no second row under the same key: %+v", fr.upserted)
	}
	if len(fr.merged) != 1 || fr.merged[0].ID != "f1" {
		t.Errorf("want a merge into f1, got %+v", fr.merged)
	}
}

// TestConsolidateFindings_AFailedWriteLeavesTheSnapshotAlone: two of a run's
// insights can merge into the same finding. If the first write fails, the
// second has to be measured against what is STORED — not against the
// observation that never persisted, or a low-to-high move is measured from
// the high it never recorded and the trend goes unwritten.
func TestConsolidateFindings_AFailedWriteLeavesTheSnapshotAlone(t *testing.T) {
	stored := commonmodels.LedgerFinding{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "low", AffectedCount: 10, SeenCount: 1,
		Status: commonmodels.LedgerFindingStatusConfirmed,
	}
	fr := &fakeFindingRepo{
		findings:  []commonmodels.LedgerFinding{stored},
		upsertErr: errors.New("mongo said no"),
	}
	o := &Orchestrator{projectID: "p1", runID: "r2", findingRepo: fr}

	if _, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d2",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 900},
		},
	}); err != nil {
		t.Fatalf("consolidate: %v", err)
	}

	// Nothing landed, and the in-memory copy still describes what is stored.
	if fr.findings[0].Severity != "low" || fr.findings[0].AffectedCount != 10 {
		t.Errorf("a failed write must leave the snapshot as it was: %+v", fr.findings[0])
	}
	if len(fr.findings[0].StatusHistory) != 0 {
		t.Errorf("a failed write records no history: %+v", fr.findings[0].StatusHistory)
	}
}

// TestConsolidateFindings_RevivedTerminalFindingBecomesActive is the other
// half of the soft archive. Un-archiving a resolved or refuted finding is not
// enough on its own: both statuses are terminal, so the read context still
// leaves it out of the next prompt and the next prune archives it again — it
// would be invisible however often it came back. While pruning deleted, the
// run that saw it again simply recreated it as confirmed.
func TestConsolidateFindings_RevivedTerminalFindingBecomesActive(t *testing.T) {
	for _, status := range []string{
		commonmodels.LedgerFindingStatusResolved,
		commonmodels.LedgerFindingStatusRefuted,
	} {
		t.Run(status, func(t *testing.T) {
			archived := commonmodels.LedgerFinding{
				ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
				NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
				Severity:      "high", AffectedCount: 100, Status: status,
				SeenCount: 1, Archived: true, ArchivedAt: time.Now().Add(-time.Hour),
			}
			fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{archived}}
			o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}

			if _, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
				ProjectID: "p1", ID: "d1",
				Insights: []models.Insight{
					{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
				},
			}); err != nil {
				t.Fatalf("consolidate: %v", err)
			}
			if len(fr.merged) != 1 {
				t.Fatalf("expected one merge, got %+v", fr.merged)
			}
			got := fr.merged[0]
			if got.Status != commonmodels.LedgerFindingStatusConfirmed {
				t.Errorf("a revived %s finding must come back active, got %q", status, got.Status)
			}
			// And the move is recorded, so the page can say why it changed.
			if len(got.StatusHistory) == 0 {
				t.Fatal("the revival must be recorded in the status history")
			}
			last := got.StatusHistory[len(got.StatusHistory)-1]
			if last.Status != commonmodels.LedgerFindingStatusConfirmed || last.Reason == "" || last.RunID != "r1" {
				t.Errorf("the revival entry must name its run and its reason: %+v", last)
			}
		})
	}

	// An active status is left exactly as it was.
	monitoring := commonmodels.LedgerFinding{
		ID: "f1", ProjectID: "p1", Area: "churn", Name: "High EU churn",
		NormalizedKey: commonmodels.NormalizedFindingKey("churn", "High EU churn"),
		Severity:      "high", AffectedCount: 100,
		Status:    commonmodels.LedgerFindingStatusMonitoring,
		SeenCount: 1, Archived: true, ArchivedAt: time.Now().Add(-time.Hour),
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{monitoring}}
	o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}
	if _, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{
			{ID: "ins-1", AnalysisArea: "churn", Name: "High EU churn", Severity: "high", AffectedCount: 100},
		},
	}); err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	if fr.merged[0].Status != commonmodels.LedgerFindingStatusMonitoring {
		t.Errorf("an active status is not a revival and must not move: %q", fr.merged[0].Status)
	}
	if len(fr.merged[0].StatusHistory) != 0 {
		t.Errorf("nothing moved, so nothing is recorded: %+v", fr.merged[0].StatusHistory)
	}
}

// TestConsolidateFindings_TotalExcludesArchived pins the convergence
// denominator. The dedup list deliberately includes archived rows; counting
// them as "total findings" would quietly deflate the marginal-new ratio the
// evolution policy steers on.
func TestConsolidateFindings_TotalExcludesArchived(t *testing.T) {
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "f1", NormalizedKey: "k1", Archived: true},
		{ID: "f2", NormalizedKey: "k2"},
	}}
	o := &Orchestrator{projectID: "p1", runID: "r1", findingRepo: fr}
	cons, err := o.consolidateFindings(context.Background(), &models.DiscoveryResult{
		ProjectID: "p1", ID: "d1",
		Insights: []models.Insight{{ID: "ins-1", AnalysisArea: "new", Name: "Fresh thing", Severity: "low"}},
	})
	if err != nil {
		t.Fatalf("consolidate: %v", err)
	}
	// One active prior + one new = 2. The archived one is history, not stock.
	if cons.TotalCount != 2 {
		t.Errorf("total should count the active set only, got %d", cons.TotalCount)
	}
}

// TestResolveFindingIDs covers the id-acceptance rule in one place: a prior
// ledger id, one of this run's insight ids, an invented id, and duplicates.
func TestResolveFindingIDs(t *testing.T) {
	cons := consolidationResult{FindingIDByInsightID: map[string]string{"ins-1": "f9"}}
	known := map[string]struct{}{"f1": {}}

	got := cons.resolveFindingIDs([]string{"f1", "ins-1", "nope", " ", "f1"}, known)
	if len(got) != 2 || got[0] != "f1" || got[1] != "f9" {
		t.Errorf("want [f1 f9] (invented and duplicate dropped), got %v", got)
	}
	if out := cons.resolveFindingIDs(nil, known); out != nil {
		t.Errorf("no ids in → no ids out, got %v", out)
	}
	if out := cons.resolveFindingIDs([]string{"ghost"}, known); out != nil {
		t.Errorf("an invented id alone must resolve to nothing, got %v", out)
	}
}

// TestApplyTaskStatusUpdates_CloseMetadataAndMirroredEdge is the answer to "we
// followed this up — what did we find?". Closing used to write a status and a
// timestamp; it now records which run closed the thread, why, and the findings
// that answered it, mirrored onto those findings so the tree is walkable from
// either end.
func TestApplyTaskStatusUpdates_CloseMetadataAndMirroredEdge(t *testing.T) {
	tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{
		{ID: "t1", NormalizedKey: "k1", Status: commonmodels.LedgerTaskStatusOpen},
		{ID: "t2", NormalizedKey: "k2", Status: commonmodels.LedgerTaskStatusOpen},
	}}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "f-prior", ProjectID: "p1"},
		{ID: "f-new", ProjectID: "p1"},
	}}
	// The findings the prompt showed this run — what an id the model returns
	// is validated against.
	o := &Orchestrator{
		projectID: "p1", runID: "r1", taskRepo: tr, findingRepo: fr,
		promptFindingIDs: idsOf(fr.findings),
	}

	ref := &parsedReflection{}
	ref.TaskStatusUpdates = []struct {
		TaskID               string   `json:"task_id"`
		Status               string   `json:"status"`
		Reason               string   `json:"reason"`
		ResolvedByFindingIDs []string `json:"resolved_by_finding_ids"`
	}{
		// A prior ledger id and one of this run's insight ids, plus an invented one.
		{TaskID: "t1", Status: "done", Reason: "9 of 12 share the same centre",
			ResolvedByFindingIDs: []string{"f-prior", "ins-new", "ghost"}},
		// A dead end: nothing to point at, and nothing must be stored.
		{TaskID: "t2", Status: "dropped", Reason: "the table was dropped upstream",
			ResolvedByFindingIDs: []string{"f-prior"}},
	}
	cons := consolidationResult{FindingIDByInsightID: map[string]string{"ins-new": "f-new"}}

	closed := o.applyTaskStatusUpdates(context.Background(),
		&models.DiscoveryResult{ID: "d1"}, ref, cons)

	if len(closed) != 2 {
		t.Fatalf("both tasks should be closed, got %+v", closed)
	}
	if len(tr.updates) != 2 {
		t.Fatalf("want 2 closes, got %d", len(tr.updates))
	}

	var done, dropped *database.TaskClose
	for i := range tr.updates {
		switch tr.updates[i].id {
		case "t1":
			done = &tr.updates[i].close
		case "t2":
			dropped = &tr.updates[i].close
		}
	}
	if done == nil || dropped == nil {
		t.Fatalf("both closes should be recorded: %+v", tr.updates)
	}
	if done.RunID != "r1" || done.DiscoveryID != "d1" || done.At.IsZero() {
		t.Errorf("the closing run must be stamped: %+v", done)
	}
	if done.Reason != "9 of 12 share the same centre" {
		t.Errorf("close reason = %q", done.Reason)
	}
	if !slices.Equal(done.ResolvedByFindingIDs, []string{"f-prior", "f-new"}) {
		t.Errorf("resolved-by should resolve both id spaces and drop the invented id, got %v", done.ResolvedByFindingIDs)
	}
	// A `dropped` is a proven dead end. Recording findings that "answered" it
	// would be recording an answer to a question that has none.
	if len(dropped.ResolvedByFindingIDs) != 0 {
		t.Errorf("a dropped task must carry no resolved-by ids, got %v", dropped.ResolvedByFindingIDs)
	}

	// The reverse edge lands on each answering finding, exactly once — and as
	// an atomic append, not by replacing the finding document.
	mirrored := map[string][]string{}
	for i := range fr.findings {
		mirrored[fr.findings[i].ID] = fr.findings[i].AnsweredTaskIDs
	}
	for _, id := range []string{"f-prior", "f-new"} {
		if !slices.Equal(mirrored[id], []string{"t1"}) {
			t.Errorf("finding %s should record answering t1, got %v", id, mirrored[id])
		}
	}
	if len(fr.upserted) != 0 {
		t.Errorf("the mirrored edge must not replace a finding document: %+v", fr.upserted)
	}
	// One call for the one closed thread; the dropped one answers nothing.
	if len(fr.answered) != 1 || fr.answered[0].taskID != "t1" {
		t.Errorf("want one answered-task write for t1, got %+v", fr.answered)
	}
}

// TestApplyTaskStatusUpdates_SecondUpdateForOneThreadIsIgnored: a reflection
// that names the same thread twice must not apply both. A `done` followed by a
// `dropped` would leave the thread dropped while still naming the findings
// that answered it, and leave those findings mirroring a thread that, by its
// own record, was never answered.
func TestApplyTaskStatusUpdates_SecondUpdateForOneThreadIsIgnored(t *testing.T) {
	tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{
		{ID: "t1", NormalizedKey: "k1", Status: commonmodels.LedgerTaskStatusOpen},
	}}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{{ID: "f1", ProjectID: "p1"}}}
	// The findings the prompt showed this run — what an id the model returns
	// is validated against.
	o := &Orchestrator{
		projectID: "p1", runID: "r1", taskRepo: tr, findingRepo: fr,
		promptFindingIDs: idsOf(fr.findings),
	}

	ref := &parsedReflection{}
	ref.TaskStatusUpdates = []struct {
		TaskID               string   `json:"task_id"`
		Status               string   `json:"status"`
		Reason               string   `json:"reason"`
		ResolvedByFindingIDs []string `json:"resolved_by_finding_ids"`
	}{
		{TaskID: "t1", Status: "done", Reason: "answered", ResolvedByFindingIDs: []string{"f1"}},
		{TaskID: "t1", Status: "dropped", Reason: "on reflection, a dead end"},
	}

	o.applyTaskStatusUpdates(context.Background(), &models.DiscoveryResult{ID: "d1"},
		ref, consolidationResult{FindingIDByInsightID: map[string]string{}})

	if len(tr.updates) != 1 {
		t.Fatalf("only the first close may be applied, got %+v", tr.updates)
	}
	if tr.updates[0].status != commonmodels.LedgerTaskStatusDone {
		t.Errorf("the first close stands, got %q", tr.updates[0].status)
	}
}

// TestApplyTaskStatusUpdates_LosingTheRaceMirrorsNothing: when another
// reflection closed the thread first, the repository reports that this call
// did not win. Mirroring the edge anyway would point a finding at a close it
// had no part in.
func TestApplyTaskStatusUpdates_LosingTheRaceMirrorsNothing(t *testing.T) {
	tr := &fakeTaskRepo{
		existing: []commonmodels.LedgerTask{
			{ID: "t1", NormalizedKey: "k1", Status: commonmodels.LedgerTaskStatusOpen},
		},
		lost: true,
	}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{{ID: "f1", ProjectID: "p1"}}}
	// The findings the prompt showed this run — what an id the model returns
	// is validated against.
	o := &Orchestrator{
		projectID: "p1", runID: "r1", taskRepo: tr, findingRepo: fr,
		promptFindingIDs: idsOf(fr.findings),
	}

	ref := &parsedReflection{}
	ref.TaskStatusUpdates = []struct {
		TaskID               string   `json:"task_id"`
		Status               string   `json:"status"`
		Reason               string   `json:"reason"`
		ResolvedByFindingIDs []string `json:"resolved_by_finding_ids"`
	}{{TaskID: "t1", Status: "done", Reason: "answered", ResolvedByFindingIDs: []string{"f1"}}}

	closed := o.applyTaskStatusUpdates(context.Background(), &models.DiscoveryResult{ID: "d1"},
		ref, consolidationResult{FindingIDByInsightID: map[string]string{}})

	// The key still comes back, because what it stops is the follow-up pass
	// recreating a thread that is now closed — just as true when the close
	// was someone else's.
	if len(closed) != 1 {
		t.Errorf("a thread closed by someone else is still closed: %+v", closed)
	}
	for i := range fr.upserted {
		if len(fr.upserted[i].AnsweredTaskIDs) != 0 {
			t.Errorf("no edge may be mirrored after losing the claim: %+v", fr.upserted[i])
		}
	}
}

// TestKnownFindingIDs_IsWhatThePromptShowed: the active set moves during the
// LLM call — another reflection can archive a finding while the model is
// thinking — so validating against it afterwards would throw out an id this
// run genuinely put on the page, leaving a thread with no parent or a close
// with no answering finding.
func TestKnownFindingIDs_IsWhatThePromptShowed(t *testing.T) {
	// The repository no longer holds the finding: it was archived mid-run.
	fr := &fakeFindingRepo{findings: nil}
	o := &Orchestrator{
		projectID: "p1", runID: "r1", findingRepo: fr,
		promptFindingIDs: []string{"f-on-the-page"},
	}
	known := o.knownFindingIDs(context.Background())
	if _, ok := known["f-on-the-page"]; !ok {
		t.Error("an id the prompt showed stays valid however the active set moved")
	}
	if len(known) != 1 {
		t.Errorf("only what was shown: %v", known)
	}

	// A run whose prompt carried no findings validates nothing.
	empty := (&Orchestrator{projectID: "p1"}).knownFindingIDs(context.Background())
	if len(empty) != 0 {
		t.Errorf("a prompt with no findings admits no ids: %v", empty)
	}
}

// TestMarkThreadsPursued holds the honest-record rule. A run that had a thread
// in front of it pursued it whether or not it closed it — and in `off` mode the
// queue is never rendered into the exploration prompt, so the run genuinely did
// not see those threads and claiming otherwise would be a fabricated edge.
func TestMarkThreadsPursued(t *testing.T) {
	open := []commonmodels.LedgerTask{{ID: "t1"}, {ID: "t2"}}

	t.Run("stamps the threads the run was shown", func(t *testing.T) {
		tr := &fakeTaskRepo{existing: open}
		o := &Orchestrator{
			projectID: "p1", runID: "r1", taskRepo: tr,
			shownTaskIDs: []string{"t1", "t2"},
		}
		o.markThreadsPursued(context.Background())
		if len(tr.pursued) != 1 {
			t.Fatalf("want 1 MarkPursued call, got %d", len(tr.pursued))
		}
		if tr.pursued[0].runID != "r1" || !slices.Equal(tr.pursued[0].ids, []string{"t1", "t2"}) {
			t.Errorf("every thread the run was shown should be stamped, got %+v", tr.pursued[0])
		}
	})

	t.Run("records what the run saw, not what the queue says now", func(t *testing.T) {
		// The previous run's reflection can close one of these and open
		// another while this run is exploring. Reading the queue here would
		// record a thread this run was never told about and miss the one it
		// was.
		tr := &fakeTaskRepo{existing: []commonmodels.LedgerTask{{ID: "opened-later"}}}
		o := &Orchestrator{
			projectID: "p1", runID: "r1", taskRepo: tr,
			shownTaskIDs: []string{"shown-at-the-start"},
		}
		o.markThreadsPursued(context.Background())
		if len(tr.pursued) != 1 || !slices.Equal(tr.pursued[0].ids, []string{"shown-at-the-start"}) {
			t.Errorf("the run pursued what it was shown: %+v", tr.pursued)
		}
	})

	t.Run("records nothing in off mode", func(t *testing.T) {
		// Off mode never puts the queue in front of the run, so the read
		// context captures nothing and there is nothing to record. The gate
		// is at the capture, not here.
		tr := &fakeTaskRepo{existing: open}
		o := &Orchestrator{projectID: "p1", runID: "r1", taskRepo: tr}
		o.markThreadsPursued(context.Background())
		if len(tr.pursued) != 0 {
			t.Errorf("off mode never showed the run these threads: %+v", tr.pursued)
		}
	})

	t.Run("keeps what a run was shown when the setting changes under it", func(t *testing.T) {
		// Evolution can be switched off — or fail to resolve and default to
		// off — while a run is in flight. The run was still shown these
		// threads, and discarding a true record because a setting moved
		// afterwards would be rewriting history to match the present.
		tr := &fakeTaskRepo{existing: nil}
		o := &Orchestrator{
			projectID: "p1", runID: "r1", taskRepo: tr,
			shownTaskIDs: []string{"t1", "t2"},
		}
		o.markThreadsPursued(context.Background())
		if len(tr.pursued) != 1 || !slices.Equal(tr.pursued[0].ids, []string{"t1", "t2"}) {
			t.Errorf("what the run was shown stands: %+v", tr.pursued)
		}
	})

	t.Run("a run shown no threads is a no-op", func(t *testing.T) {
		tr := &fakeTaskRepo{}
		o := &Orchestrator{projectID: "p1", runID: "r1", taskRepo: tr}
		o.markThreadsPursued(context.Background())
		if len(tr.pursued) != 0 {
			t.Errorf("nothing to stamp should write nothing, got %+v", tr.pursued)
		}
	})
}

// TestApplyNextTasks_OriginFindingIDs covers the edge that gives the ROOT of a
// follow-up chain a parent. `supersedes` only ever links a task to an earlier
// task, so without this the first thread in every chain is unattributed.
func TestApplyNextTasks_OriginFindingIDs(t *testing.T) {
	tr := &fakeTaskRepo{}
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{{ID: "f-prior"}}}
	// The findings the prompt showed this run — what an id the model returns
	// is validated against.
	o := &Orchestrator{
		projectID: "p1", runID: "r1", taskRepo: tr, findingRepo: fr,
		promptFindingIDs: idsOf(fr.findings),
	}

	ref := &parsedReflection{}
	ref.NextTasks = []struct {
		Title            string   `json:"title"`
		Text             string   `json:"text"`
		Kind             string   `json:"kind"`
		TargetType       string   `json:"target_type"`
		TargetID         string   `json:"target_id"`
		Supersedes       string   `json:"supersedes"`
		OriginFindingIDs []string `json:"origin_finding_ids"`
	}{
		{Title: "Chase the centre", Text: "check the fulfilment centres",
			OriginFindingIDs: []string{"f-prior", "ins-new", "invented"}},
		{Title: "No attribution", Text: "a thread with no origin"},
	}
	o.applyNextTasks(context.Background(), ref, nil,
		consolidationResult{FindingIDByInsightID: map[string]string{"ins-new": "f-new"}})

	if len(tr.inserted) != 2 {
		t.Fatalf("want 2 tasks inserted, got %d", len(tr.inserted))
	}
	if !slices.Equal(tr.inserted[0].OriginFindingIDs, []string{"f-prior", "f-new"}) {
		t.Errorf("origins should resolve both id spaces and drop the invented id, got %v", tr.inserted[0].OriginFindingIDs)
	}
	if tr.inserted[1].OriginFindingIDs != nil {
		t.Errorf("a thread the model did not attribute must store nothing, got %v", tr.inserted[1].OriginFindingIDs)
	}
}

// TestApplyLearnings covers the channel that existed and was invisible: the
// reflection phase already produced durable learnings and wrote them only into
// the project-context notes, unattributed, in a collection the ledger never
// reads.
func TestApplyLearnings(t *testing.T) {
	lr := &fakeLearningRepo{}
	o := &Orchestrator{projectID: "p1", runID: "r1", learningRepo: lr}
	ref := &parsedReflection{}
	ref.Learnings = []struct {
		Category  string  `json:"category"`
		Note      string  `json:"note"`
		Relevance float64 `json:"relevance"`
	}{
		{Category: "schema", Note: "order_status 'C' means cancelled", Relevance: 0.9},
		{Category: "schema", Note: "   ", Relevance: 0.5},                         // empty → skipped
		{Category: "domain", Note: "grain is one row per shipment", Relevance: 7}, // out of range → default
	}
	o.applyLearnings(context.Background(), &models.DiscoveryResult{ID: "d1"}, ref)

	if len(lr.upserted) != 2 {
		t.Fatalf("want 2 learnings stored (the blank one skipped), got %d", len(lr.upserted))
	}
	first := lr.upserted[0]
	if first.RunID != "r1" || first.DiscoveryID != "d1" || first.ProjectID != "p1" {
		t.Errorf("a learning must be attributed to the run that produced it: %+v", first)
	}
	if first.NormalizedKey != commonmodels.NormalizedLearningKey("schema", "order_status 'C' means cancelled") {
		t.Errorf("dedup key not set as expected: %q", first.NormalizedKey)
	}
	if lr.upserted[1].Relevance != defaultLearningRelevance {
		t.Errorf("an out-of-range relevance should fall back to the default, got %v", lr.upserted[1].Relevance)
	}
}

// TestApplyLearnings_NoStoreIsANoOp — the ledger learning collection is only
// wired by the agent server, so a unit/single-binary build has none. It must
// cost nothing rather than panic in a best-effort phase.
func TestApplyLearnings_NoStoreIsANoOp(t *testing.T) {
	o := &Orchestrator{projectID: "p1", runID: "r1"}
	ref := &parsedReflection{}
	ref.Learnings = []struct {
		Category  string  `json:"category"`
		Note      string  `json:"note"`
		Relevance float64 `json:"relevance"`
	}{{Category: "schema", Note: "something durable"}}
	o.applyLearnings(context.Background(), &models.DiscoveryResult{ID: "d1"}, ref)
}

// TestAppendFindingTrails covers the two append helpers' dedup rules directly:
// re-running the phase must not double-count a sighting, and a re-judgement
// that changes nothing must not grow the document.
func TestAppendFindingTrails(t *testing.T) {
	t.Run("a sighting is deduped on run and insight", func(t *testing.T) {
		f := &commonmodels.LedgerFinding{}
		s := commonmodels.FindingSighting{RunID: "r1", InsightID: "ins-1"}
		appendFindingSighting(f, s)
		appendFindingSighting(f, s)
		if len(f.Sightings) != 1 {
			t.Errorf("want 1 sighting, got %d", len(f.Sightings))
		}
		// Two insights from the same run that land on the same finding are two
		// real observations.
		appendFindingSighting(f, commonmodels.FindingSighting{RunID: "r1", InsightID: "ins-2"})
		if len(f.Sightings) != 2 {
			t.Errorf("want 2 sightings, got %d", len(f.Sightings))
		}
	})

	t.Run("an unchanged verdict from the same run is not re-recorded", func(t *testing.T) {
		f := &commonmodels.LedgerFinding{}
		c := commonmodels.FindingStatusChange{
			Status: commonmodels.LedgerFindingStatusConfirmed, RunID: "r1",
			Source: commonmodels.FindingStatusSourceReflection,
		}
		appendFindingStatusChange(f, c)
		appendFindingStatusChange(f, c)
		if len(f.StatusHistory) != 1 {
			t.Errorf("want 1 status entry, got %d", len(f.StatusHistory))
		}
		// A trend detected by the server and a verdict from the model are two
		// different statements about the same run.
		trend := c
		trend.Source = commonmodels.FindingStatusSourceTrend
		appendFindingStatusChange(f, trend)
		if len(f.StatusHistory) != 2 {
			t.Errorf("want 2 status entries, got %d", len(f.StatusHistory))
		}
	})
}

// TestApplyReflection_RecordsStatusReason is the #3 edge through applyReflection
// itself: the model already emits a reason per status change and it was being
// dropped on the floor.
func TestApplyReflection_RecordsStatusReason(t *testing.T) {
	fr := &fakeFindingRepo{findings: []commonmodels.LedgerFinding{
		{ID: "f1", ProjectID: "p1", Status: commonmodels.LedgerFindingStatusConfirmed},
	}}
	// Verdicts are applied to the findings the prompt showed.
	o := &Orchestrator{
		projectID: "p1", runID: "r1", findingRepo: fr,
		promptFindingIDs: idsOf(fr.findings),
	}
	ref := &parsedReflection{}
	ref.StatusUpdates = []struct {
		FindingID string `json:"finding_id"`
		Status    string `json:"status"`
		Reason    string `json:"reason"`
	}{
		{FindingID: "f1", Status: commonmodels.LedgerFindingStatusRefuted, Reason: "the join was wrong"},
		{FindingID: "ghost", Status: commonmodels.LedgerFindingStatusResolved, Reason: "invented"},
		{FindingID: "f1", Status: "nonsense", Reason: "invalid status"},
	}
	o.applyReflection(context.Background(), &models.DiscoveryResult{ID: "d1"},
		agentplugin.DiscoveryPolicy{EvolutionMode: agentplugin.EvolutionModeOff}, ref, consolidationResult{})

	if len(fr.statuses) != 1 {
		t.Fatalf("only the one valid update should be written, got %d", len(fr.statuses))
	}
	f := fr.statuses[0]
	if f.Status != commonmodels.LedgerFindingStatusRefuted {
		t.Errorf("status = %q, want refuted", f.Status)
	}
	if len(f.StatusHistory) != 1 {
		t.Fatalf("want 1 status change, got %+v", f.StatusHistory)
	}
	sc := f.StatusHistory[0]
	if sc.Reason != "the join was wrong" || sc.RunID != "r1" || sc.DiscoveryID != "d1" ||
		sc.Source != commonmodels.FindingStatusSourceReflection {
		t.Errorf("status change not recorded as emitted: %+v", sc)
	}
}

// idsOf is the ids of a set of findings, for standing in as "what the
// reflection prompt showed this run".
func idsOf(findings []commonmodels.LedgerFinding) []string {
	out := make([]string, 0, len(findings))
	for i := range findings {
		out = append(out, findings[i].ID)
	}
	return out
}

// TestReflectionContext_DefaultBudget pins the phase's default wall-clock
// budget at three hours.
//
// This is a regression guard with a production failure behind it: at the
// original three minutes, a project carrying 69 ledger findings sent a 32k-token
// reflection prompt, and the call was cancelled mid-flight on two consecutive
// runs. Consolidation (step 1) had already written its findings, so the ledger
// kept growing while coverage, convergence, learnings and next-tasks stopped
// being written — the budget bit hardest on the runs with the most history to
// reason about, which is exactly backwards.
func TestReflectionContext_DefaultBudget(t *testing.T) {
	t.Setenv(discoveryReflectionTimeoutEnv, "")

	ctx, cancel := reflectionContext(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("reflection context carries no deadline")
	}
	if got := time.Until(deadline); got < 3*time.Hour-time.Minute {
		t.Errorf("default reflection budget = %v, want ~3h", got)
	}
}

// TestReflectionContext_EnvOverride covers the knob an operator actually
// reaches for — lowering the budget to cap spend — and the malformed value that
// must fall back rather than produce a zero-length budget that cancels the
// phase before its first call.
func TestReflectionContext_EnvOverride(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		least time.Duration
		most  time.Duration
	}{
		{name: "lowered", value: "10m", least: 9 * time.Minute, most: 10 * time.Minute},
		{name: "raised", value: "6h", least: 5*time.Hour + 59*time.Minute, most: 6 * time.Hour},
		{name: "unparseable falls back", value: "soon", least: 3*time.Hour - time.Minute, most: 3 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(discoveryReflectionTimeoutEnv, tc.value)

			ctx, cancel := reflectionContext(context.Background())
			defer cancel()

			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("reflection context carries no deadline")
			}
			if got := time.Until(deadline); got < tc.least || got > tc.most {
				t.Errorf("budget for %q = %v, want between %v and %v", tc.value, got, tc.least, tc.most)
			}
		})
	}
}

// TestReflectionContext_SurvivesParentCancellation pins the WithoutCancel: the
// phase is invoked after the run-completion event, so the run's own context is
// routinely already cancelled by the time it starts. Without this the whole
// phase would be dead on arrival.
func TestReflectionContext_SurvivesParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	ctx, cancel := reflectionContext(parent)
	defer cancel()

	if err := ctx.Err(); err != nil {
		t.Fatalf("reflection context inherited the parent's cancellation: %v", err)
	}
}

// TestMarkReflectionPending_OnlyWhenThePhaseWillRun: the entry put up before
// the run is stamped completed tells the ledger view to say "updating". Put up
// for a run whose reflection is gated off, nothing would ever take it down, and
// the label would sit there until the entry went stale hours later.
func TestMarkReflectionPending_OnlyWhenThePhaseWillRun(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	client, _ := stubClient(t, `{"coverage_summary":"x"}`, nil)
	orch := func(lr *fakeLedgerRepo, enabled bool) *Orchestrator {
		return &Orchestrator{
			reflectionEnabled: enabled, projectID: "proj-1", runID: "run-1",
			aiClient: client, ledgerRepo: lr, findingRepo: &fakeFindingRepo{},
		}
	}

	lr := &fakeLedgerRepo{}
	o := orch(lr, true)
	o.markReflectionPending(context.Background())
	if lr.reflectingRunID != "run-1" || !o.reflectionPending {
		t.Fatalf("a run that will reflect must be marked pending: set=%q pending=%v", lr.reflectingRunID, o.reflectionPending)
	}

	off := &fakeLedgerRepo{}
	o = orch(off, false) // per-project toggle off
	o.markReflectionPending(context.Background())
	if off.beganReflection != 0 || o.reflectionPending {
		t.Errorf("toggle off must not mark anything pending: began=%d", off.beganReflection)
	}

	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "false")
	envOff := &fakeLedgerRepo{}
	o = orch(envOff, true)
	o.markReflectionPending(context.Background())
	if envOff.beganReflection != 0 {
		t.Errorf("deployment flag off must not mark anything pending: began=%d", envOff.beganReflection)
	}
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")

	policy.RegisterChecker(denyChecker{policy.NewNoopChecker()})
	t.Cleanup(func() { policy.RegisterChecker(policy.NewNoopChecker()) })
	unlicensed := &fakeLedgerRepo{}
	o = orch(unlicensed, true)
	o.markReflectionPending(context.Background())
	if unlicensed.beganReflection != 0 {
		t.Errorf("no sources entitlement must not mark anything pending: began=%d", unlicensed.beganReflection)
	}
}

// TestRunPhaseReflection_TakesThePendingMarkDown: whatever the phase does — run
// to the end, or stop at a gate it passed when the run finished — the entry the
// run put up comes down, so the ledger never claims an update nobody is making.
func TestRunPhaseReflection_TakesThePendingMarkDown(t *testing.T) {
	t.Setenv("DISCOVERY_REFLECTION_ENABLED", "true")
	client, _ := stubClient(t, `{"coverage_summary":"x"}`, nil)
	result := &models.DiscoveryResult{ID: "d1", Insights: []models.Insight{{Name: "x", AnalysisArea: "a"}}}

	// The ordinary case: marked pending, then the phase runs and refreshes the
	// same entry when it actually starts writing.
	lr := &fakeLedgerRepo{}
	o := &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-1", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: lr, findingRepo: &fakeFindingRepo{},
	}
	o.markReflectionPending(context.Background())
	o.RunPhaseReflection(context.Background(), result)
	if lr.beganReflection != 2 {
		t.Errorf("pending mark + the phase's own start = 2 begins, got %d", lr.beganReflection)
	}
	if lr.reflectingRunID != "" {
		t.Errorf("the marker must come down at the end, still set to %q", lr.reflectingRunID)
	}

	// The entitlement went away between the run finishing and the phase
	// starting: the phase stops at its gate, and must still clear the entry.
	gated := &fakeLedgerRepo{}
	o = &Orchestrator{
		reflectionEnabled: true, projectID: "proj-1", runID: "run-2", datasets: []string{"ds"},
		llmInputWindow: 200000, llmOutputCap: 4000, aiClient: client,
		ledgerRepo: gated, findingRepo: &fakeFindingRepo{},
	}
	o.markReflectionPending(context.Background())
	policy.RegisterChecker(denyChecker{policy.NewNoopChecker()})
	t.Cleanup(func() { policy.RegisterChecker(policy.NewNoopChecker()) })
	o.RunPhaseReflection(context.Background(), result)
	if gated.beganReflection != 1 || gated.reflectingRunID != "" {
		t.Errorf("a phase stopped at its gate must clear the pending entry: began=%d set=%q",
			gated.beganReflection, gated.reflectingRunID)
	}

	// And a phase that was never marked pending does not reach for an entry it
	// does not own.
	none := &fakeLedgerRepo{}
	o = &Orchestrator{reflectionEnabled: false, projectID: "proj-1", runID: "run-3", ledgerRepo: none}
	o.RunPhaseReflection(context.Background(), result)
	if none.beganReflection != 0 || none.endedReflection != 0 {
		t.Errorf("a phase gated off from the start touches no marker: began=%d ended=%d",
			none.beganReflection, none.endedReflection)
	}
}
