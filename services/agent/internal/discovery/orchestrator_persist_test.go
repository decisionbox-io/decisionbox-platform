package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// fakeDiscoveryLogPersister captures the arguments each Save* call sees so
// the test can assert that persistSplitLogs forwards every payload to the
// right method with the right project / discovery / run identifiers, and so
// that returning a synthetic error from one call does not stop subsequent
// ones (per the "log and continue" contract).
type fakeDiscoveryLogPersister struct {
	explorationCalls   int
	analysisCalls      int
	validationCalls    int
	recommendationCall int

	gotProjectIDs  []string
	gotDiscoveryID []string
	gotRunIDs      []string

	gotExploration  []models.ExplorationStep
	gotAnalysis     []models.AnalysisStep
	gotValidation   []models.ValidationResult
	gotRecommend    *models.RecommendationStep

	saveExplorationErr   error
	saveAnalysisErr      error
	saveValidationErr    error
	saveRecommendationErr error
}

func (f *fakeDiscoveryLogPersister) recordIDs(projectID, discoveryID, runID string) {
	f.gotProjectIDs = append(f.gotProjectIDs, projectID)
	f.gotDiscoveryID = append(f.gotDiscoveryID, discoveryID)
	f.gotRunIDs = append(f.gotRunIDs, runID)
}

func (f *fakeDiscoveryLogPersister) SaveExplorationSteps(_ context.Context, projectID, discoveryID, runID string, steps []models.ExplorationStep) error {
	f.explorationCalls++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotExploration = steps
	return f.saveExplorationErr
}

func (f *fakeDiscoveryLogPersister) SaveAnalysisSteps(_ context.Context, projectID, discoveryID, runID string, steps []models.AnalysisStep) error {
	f.analysisCalls++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotAnalysis = steps
	return f.saveAnalysisErr
}

func (f *fakeDiscoveryLogPersister) SaveValidationResults(_ context.Context, projectID, discoveryID, runID string, results []models.ValidationResult) error {
	f.validationCalls++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotValidation = results
	return f.saveValidationErr
}

func (f *fakeDiscoveryLogPersister) SaveRecommendationLog(_ context.Context, projectID, discoveryID, runID string, step *models.RecommendationStep) error {
	f.recommendationCall++
	f.recordIDs(projectID, discoveryID, runID)
	f.gotRecommend = step
	return f.saveRecommendationErr
}

func TestPersistSplitLogs_ForwardsAllPayloads(t *testing.T) {
	fake := &fakeDiscoveryLogPersister{}
	o := &Orchestrator{
		projectID:        "proj-1",
		runID:            "run-1",
		discoveryLogRepo: fake,
	}

	exploration := []models.ExplorationStep{{Step: 1}, {Step: 2}}
	analysis := []models.AnalysisStep{{AreaID: "churn"}}
	validations := []models.ValidationResult{{InsightID: "i1"}}
	rec := &models.RecommendationStep{}

	o.persistSplitLogs(context.Background(), "disc-1", exploration, analysis, validations, rec)

	if fake.explorationCalls != 1 || fake.analysisCalls != 1 || fake.validationCalls != 1 || fake.recommendationCall != 1 {
		t.Fatalf("expected each Save* called exactly once; got E=%d A=%d V=%d R=%d",
			fake.explorationCalls, fake.analysisCalls, fake.validationCalls, fake.recommendationCall)
	}

	if len(fake.gotProjectIDs) != 4 {
		t.Fatalf("expected 4 forwarded calls, got %d", len(fake.gotProjectIDs))
	}
	for i, p := range fake.gotProjectIDs {
		if p != "proj-1" {
			t.Errorf("call %d project_id = %q, want proj-1", i, p)
		}
		if fake.gotDiscoveryID[i] != "disc-1" {
			t.Errorf("call %d discovery_id = %q, want disc-1", i, fake.gotDiscoveryID[i])
		}
		if fake.gotRunIDs[i] != "run-1" {
			t.Errorf("call %d run_id = %q, want run-1", i, fake.gotRunIDs[i])
		}
	}

	if len(fake.gotExploration) != 2 || fake.gotExploration[0].Step != 1 {
		t.Errorf("exploration payload not forwarded verbatim: %+v", fake.gotExploration)
	}
	if len(fake.gotAnalysis) != 1 || fake.gotAnalysis[0].AreaID != "churn" {
		t.Errorf("analysis payload not forwarded verbatim: %+v", fake.gotAnalysis)
	}
	if len(fake.gotValidation) != 1 || fake.gotValidation[0].InsightID != "i1" {
		t.Errorf("validation payload not forwarded verbatim: %+v", fake.gotValidation)
	}
	if fake.gotRecommend != rec {
		t.Errorf("recommendation pointer not forwarded verbatim")
	}
}

func TestPersistSplitLogs_NilRepoIsNoOp(t *testing.T) {
	// No persister wired and no payloads — must not panic and must do
	// nothing observable. Mirrors the production path where unit-test
	// orchestrators don't bring up MongoDB.
	o := &Orchestrator{projectID: "p", runID: "r"}
	o.persistSplitLogs(context.Background(), "d", nil, nil, nil, nil)
}

func TestPersistSplitLogs_ContinuesAfterEachError(t *testing.T) {
	// Per the contract documented on persistSplitLogs: a Save* failure is
	// logged and swallowed, and the next Save* still runs. We assert that
	// every Save* method got called even when each one returns its own
	// synthetic error — the parent DiscoveryResult is already saved by
	// the time we get here, so dropping later log writes on the floor
	// would silently lose telemetry.
	fake := &fakeDiscoveryLogPersister{
		saveExplorationErr:    errors.New("boom-exploration"),
		saveAnalysisErr:       errors.New("boom-analysis"),
		saveValidationErr:     errors.New("boom-validation"),
		saveRecommendationErr: errors.New("boom-recommendation"),
	}
	o := &Orchestrator{
		projectID:        "p",
		runID:            "r",
		discoveryLogRepo: fake,
	}

	o.persistSplitLogs(context.Background(), "d", nil, nil, nil, nil)

	if fake.explorationCalls != 1 || fake.analysisCalls != 1 || fake.validationCalls != 1 || fake.recommendationCall != 1 {
		t.Fatalf("each Save* must run exactly once even when prior ones error; got E=%d A=%d V=%d R=%d",
			fake.explorationCalls, fake.analysisCalls, fake.validationCalls, fake.recommendationCall)
	}
}

// Compile-time assertions — the production *database.DiscoveryLogRepository
// and the test fake both have to satisfy discoveryLogPersister. If a method
// signature ever drifts (e.g. adds an arg to SaveExplorationSteps), this
// catches it before production wiring breaks.
var (
	_ discoveryLogPersister = (*database.DiscoveryLogRepository)(nil)
	_ discoveryLogPersister = (*fakeDiscoveryLogPersister)(nil)
)

func TestNewOrchestrator_TypedNilDiscoveryLogRepoNormalizes(t *testing.T) {
	// Regression — a caller passing a typed-nil
	// *database.DiscoveryLogRepository must not produce a non-nil
	// interface field on the orchestrator. Without the guard, the
	// `o.discoveryLogRepo == nil` check in persistSplitLogs returns
	// false (interface holds a nil concrete pointer), and the next
	// call dereferences it and panics.
	var typedNil *database.DiscoveryLogRepository
	o := NewOrchestrator(OrchestratorOptions{
		DiscoveryLogRepo: typedNil,
		ProjectID:        "p",
		RunID:            "r",
	})

	if o.discoveryLogRepo != nil {
		t.Fatalf("typed-nil DiscoveryLogRepo must be normalized to nil interface; got %#v", o.discoveryLogRepo)
	}

	// Sanity — the guarded path must be a clean no-op (no panic, no
	// dereference of the nil concrete pointer).
	o.persistSplitLogs(context.Background(), "d", nil, nil, nil, nil)
}
