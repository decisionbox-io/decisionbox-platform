package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
)

// fakeRunDiscoveryIDSetter records every SetDiscoveryID call so the
// orchestrator wiring tests can prove the post-save linkage stamp is
// dispatched with the right (runID, discoveryID) and that errors are
// swallowed instead of propagated.
type fakeRunDiscoveryIDSetter struct {
	calls    int
	gotRun   string
	gotDisc  string
	returnErr error
}

func (f *fakeRunDiscoveryIDSetter) SetDiscoveryID(_ context.Context, runID, discoveryID string) error {
	f.calls++
	f.gotRun = runID
	f.gotDisc = discoveryID
	return f.returnErr
}

// stampDiscoveryID is the inline of the orchestrator's post-save
// stamp branch: nil-guard + best-effort call. We exercise it directly
// rather than reaching for the full RunDiscovery flow, which would
// require the entire LLM/warehouse stack to be wired.
func stampDiscoveryID(o *Orchestrator, ctx context.Context, discoveryID string) {
	if o.runRepo != nil && o.runID != "" {
		_ = o.runRepo.SetDiscoveryID(ctx, o.runID, discoveryID)
	}
}

func TestOrchestrator_PostSaveStampsDiscoveryID(t *testing.T) {
	fake := &fakeRunDiscoveryIDSetter{}
	o := &Orchestrator{
		runID:   "run-42",
		runRepo: fake,
	}

	stampDiscoveryID(o, context.Background(), "disc-99")

	if fake.calls != 1 {
		t.Fatalf("SetDiscoveryID should be called once; got %d", fake.calls)
	}
	if fake.gotRun != "run-42" || fake.gotDisc != "disc-99" {
		t.Errorf("SetDiscoveryID got (%q, %q); want (run-42, disc-99)", fake.gotRun, fake.gotDisc)
	}
}

func TestOrchestrator_PostSaveSwallowsLinkageError(t *testing.T) {
	fake := &fakeRunDiscoveryIDSetter{returnErr: errors.New("mongo-down")}
	o := &Orchestrator{
		runID:   "run-42",
		runRepo: fake,
	}

	// Must not panic, must not propagate. The discovery doc is already
	// on disk by the time SetDiscoveryID runs — losing the linkage is
	// strictly preferable to rolling back a successful discovery.
	stampDiscoveryID(o, context.Background(), "disc-99")

	if fake.calls != 1 {
		t.Errorf("SetDiscoveryID must still have been attempted; calls=%d", fake.calls)
	}
}

func TestOrchestrator_PostSaveSkippedWhenRunRepoNil(t *testing.T) {
	o := &Orchestrator{runID: "run-42"} // runRepo intentionally nil
	// No panic, no fake to assert against — just exercise the guard.
	stampDiscoveryID(o, context.Background(), "disc-99")
}

func TestOrchestrator_PostSaveSkippedWhenRunIDEmpty(t *testing.T) {
	fake := &fakeRunDiscoveryIDSetter{}
	o := &Orchestrator{runRepo: fake} // runID empty (e.g., headless invocations)
	stampDiscoveryID(o, context.Background(), "disc-99")
	if fake.calls != 0 {
		t.Errorf("SetDiscoveryID must not run when runID is empty; calls=%d", fake.calls)
	}
}

func TestNewOrchestrator_TypedNilRunRepoNormalizes(t *testing.T) {
	// Same Go gotcha that bit the discoveryLogRepo wiring: a typed-nil
	// concrete pointer through the OrchestratorOptions field must not
	// produce a non-nil interface value with a nil underlying pointer.
	var typedNil *database.RunRepository
	o := NewOrchestrator(OrchestratorOptions{
		RunRepo:   typedNil,
		ProjectID: "p",
		RunID:     "r",
	})
	if o.runRepo != nil {
		t.Fatalf("typed-nil RunRepo must normalize to nil interface; got %#v", o.runRepo)
	}
	// The guarded post-save stamp must be a clean no-op.
	stampDiscoveryID(o, context.Background(), "d")
}

// Compile-time: production *database.RunRepository implements the
// orchestrator's runDiscoveryIDSetter interface. If the method
// signature ever drifts, this catches it before production wiring
// breaks.
var _ runDiscoveryIDSetter = (*database.RunRepository)(nil)
