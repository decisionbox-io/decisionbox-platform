package askserve

import (
	"testing"

	commonmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/testutil"
)

// The routing telemetry already recorded what a turn QUERIED. These pin the
// separate fact the epic's routing work needs: what the router was OFFERED and
// what it PICKED.
//
// Without the ballot, a connected datasource the router never selects is
// indistinguishable in the record from one that was correctly irrelevant — so
// "is the router blind to this source?" has no answer, for every question.

func TestRouter_RecordsBallotAndVerdict(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":["wh_b"],"reason":"about CRM users","confidence":0.9}`,
		`{"query":"select count(*) from crm.users"}`,
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "how many flagged CRM users?")
	if store.final.Status != commonmodels.AskTurnStatusDone {
		t.Fatalf("status = %q (%s), want done", store.final.Status, store.final.Error)
	}

	// Both datasources were on the ballot, even though only one was picked.
	if got := store.final.RoutingCandidateIDs; len(got) != 2 {
		t.Fatalf("candidate ids = %v, want both datasources", got)
	}
	if !containsStr(store.final.RoutingCandidateIDs, "wh_a") || !containsStr(store.final.RoutingCandidateIDs, "wh_b") {
		t.Errorf("candidate ids = %v, want to contain wh_a and wh_b", store.final.RoutingCandidateIDs)
	}
	if got := store.final.RoutingChosenIDs; len(got) != 1 || got[0] != "wh_b" {
		t.Errorf("chosen ids = %v, want [wh_b]", got)
	}
}

// TestRouter_BallotIsRecordedWhenTheRouterClarifies is the case the ballot
// exists for: nothing was chosen and nothing was queried, so without the
// candidate set the record would show only that a question was asked, with no
// trace of what the router was looking at when it gave up.
func TestRouter_BallotIsRecordedWhenTheRouterClarifies(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"clarify":true,"question":"Do you mean ticket sales or CRM accounts?","confidence":0.3}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "show me the users")
	if store.final.Disposition != commonmodels.AskTurnDispositionClarify {
		t.Fatalf("disposition = %q, want clarify", store.final.Disposition)
	}
	if got := store.final.RoutingCandidateIDs; len(got) != 2 {
		t.Errorf("candidate ids = %v, want both datasources recorded on a clarify", got)
	}
	if got := store.final.RoutingChosenIDs; len(got) != 0 {
		t.Errorf("chosen ids = %v, want none — the router picked nothing", got)
	}
	if !store.final.RoutingClarify {
		t.Error("RoutingClarify should be true")
	}
}

// TestRouter_BallotRecordedEvenWhenAChoiceIsUnknown pins that an id the model
// invents is not silently laundered into the record: the ballot is what the
// router was shown, and the verdict is what survived validation.
func TestRouter_BallotRecordedEvenWhenAChoiceIsUnknown(t *testing.T) {
	whA := testutil.NewMockWarehouseProvider("sales")
	whB := testutil.NewMockWarehouseProvider("crm")
	rt := twoDatasourceRuntime(&scriptedProvider{responses: []string{
		`{"datasources":["wh_b","wh_does_not_exist"],"reason":"crm plus something","confidence":0.9}`,
		`{"query":"select count(*) from crm.users"}`,
		`{"answer":"done"}`,
	}}, whA, whB, nil, nil)

	store := runRouted(t, rt, "how many flagged CRM users?")
	if got := store.final.RoutingChosenIDs; len(got) != 1 || got[0] != "wh_b" {
		t.Errorf("chosen ids = %v, want only the real datasource [wh_b]", got)
	}
	if containsStr(store.final.RoutingCandidateIDs, "wh_does_not_exist") {
		t.Errorf("candidate ids = %v must be the offered set, never a model-invented id",
			store.final.RoutingCandidateIDs)
	}
}

// TestSingleDatasourceTurn_HasNoBallot pins that a turn with no routing
// decision records none — the fields mean "the router ran and saw this", so
// filling them on a pinned single-datasource turn would invent a decision that
// never happened.
func TestSingleDatasourceTurn_HasNoBallot(t *testing.T) {
	wh := testutil.NewMockWarehouseProvider("sales")
	rt := testRuntime(&scriptedProvider{responses: []string{
		`{"query":"select 1"}`,
		`{"answer":"done"}`,
	}}, wh, nil, "")

	store := runRouted(t, rt, "how many sales?")
	if got := store.final.RoutingCandidateIDs; len(got) != 0 {
		t.Errorf("candidate ids = %v, want none on a single-datasource turn", got)
	}
	if got := store.final.RoutingChosenIDs; len(got) != 0 {
		t.Errorf("chosen ids = %v, want none on a single-datasource turn", got)
	}
}
