package discovery

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

// The routing contract's reviewed-correlation-keys block: what it says, when
// it says nothing, and the one thing in it that must not depend on the model
// choosing to call a tool.

func confirmedKey() agentplugin.CorrelationKey {
	return agentplugin.CorrelationKey{
		DatasourceID: "wh_analytics", SourceField: "transactionId",
		WithDatasourceID: "default", AnchorColumns: []string{"orders.order_id"},
		Grain: "transaction", State: agentplugin.CorrelationConfirmed,
	}
}

func rejectedKey(field string) agentplugin.CorrelationKey {
	return agentplugin.CorrelationKey{
		DatasourceID: "wh_analytics", SourceField: field,
		WithDatasourceID: "default", AnchorColumns: []string{"orders.customer_id"},
		Grain: "customer", State: agentplugin.CorrelationRejected,
		Reason: "a reviewer checked " + field + " and the two sides do not hold the same values",
	}
}

// TestCorrelationContract_NothingDecidedRendersNothing is the empty-safe
// invariant, stated directly rather than only through the golden: a project
// that has never had a pairing reviewed reads exactly as it did before this
// existed.
func TestCorrelationContract_NothingDecidedRendersNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		keys []agentplugin.CorrelationKey
	}{
		{"nil", nil},
		{"empty", []agentplugin.CorrelationKey{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildDatasourcesPromptSection(sqlOnlyContext(), correlationGuidance{keys: tc.keys})
			for _, forbidden := range []string{"get_correlations", "Reviewed correlation keys", "REJECTED"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("unreviewed project must not see %q:\n%s", forbidden, got)
				}
			}
		})
	}
}

func TestCorrelationContract_TeachesTheActionAndMandatesTheCall(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(),
		correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}})

	// Nothing upstream teaches this action — the domain packs do not carry it
	// — so the shape has to be here or the model cannot emit it.
	if !strings.Contains(got, `"get_correlations": {"a"`) {
		t.Errorf("want the action shape taught:\n%s", got)
	}
	// Mandated, not suggested. The hop rules beside it are imperative too.
	if !strings.Contains(got, "MUST call `get_correlations`") {
		t.Errorf("want the call mandated:\n%s", got)
	}
	if !strings.Contains(got, "stronger evidence than a name match") {
		t.Errorf("want the reason a reviewed key outranks a name match:\n%s", got)
	}
}

// TestCorrelationContract_ExampleUsesThisRunsDatasources: a model shown an id
// that does not exist has been taught, in the same breath, that the ids here
// are illustrative.
func TestCorrelationContract_ExampleUsesThisRunsDatasources(t *testing.T) {
	dc := sqlOnlyContext()
	got := buildDatasourcesPromptSection(dc,
		correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}})

	want := fmt.Sprintf(`{"a": "%s", "b": "%s"}`, dc.descriptors[0].id, dc.descriptors[1].id)
	if !strings.Contains(got, want) {
		t.Errorf("want the worked call to name this run's datasources (%s):\n%s", want, got)
	}
}

// TestCorrelationContract_RejectionsAreNamedInline is the point of rendering
// anything here at all. A prohibition that lives only inside the tool's answer
// is a prohibition that depends on the model choosing to call the tool.
func TestCorrelationContract_RejectionsAreNamedInline(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(),
		correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey(), rejectedKey("userId")}})

	if !strings.Contains(got, "REJECTED PAIRINGS") {
		t.Fatalf("want the rejections named inline:\n%s", got)
	}
	if !strings.Contains(got, "`wh_analytics`.`userId` ↔ `default`.`orders.customer_id`") {
		t.Errorf("want the pairing named on both sides:\n%s", got)
	}
	if !strings.Contains(got, "do not hold the same values") {
		t.Errorf("want the rejection's reason carried:\n%s", got)
	}
	if !strings.Contains(got, "in either direction") {
		t.Errorf("a rejection is about two id-spaces, not a hop direction:\n%s", got)
	}
	if !strings.Contains(got, "spelled") {
		t.Errorf("want the neighbouring-spelling escape closed:\n%s", got)
	}
	if !strings.Contains(got, "leave the correlation unmade") {
		t.Errorf("want an instruction for what to do when no reviewed key exists:\n%s", got)
	}
	// A confirmed key is a preference and the tool serves it; only the
	// prohibition earns space in the contract itself.
	if strings.Contains(got, "transactionId") {
		t.Errorf("confirmed keys belong in the tool's answer, not the contract:\n%s", got)
	}
}

func TestCorrelationContract_NoRejectionsMeansNoProhibitionSection(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(),
		correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}})

	if strings.Contains(got, "REJECTED PAIRINGS") {
		t.Errorf("nothing was rejected, so there is nothing to prohibit:\n%s", got)
	}
	if !strings.Contains(got, "get_correlations") {
		t.Errorf("the action is still taught:\n%s", got)
	}
}

// TestCorrelationContract_OverflowPointsAtTheTool: dropping a prohibition
// silently would be the worst thing this block could do, so the line that
// replaces the dropped ones says where the rest are.
func TestCorrelationContract_OverflowPointsAtTheTool(t *testing.T) {
	t.Setenv(maxRejectedPairingsRenderedEnv, "2")

	keys := []agentplugin.CorrelationKey{
		rejectedKey("userId"), rejectedKey("clientId"), rejectedKey("sessionId"), rejectedKey("visitorId"),
	}
	got := buildDatasourcesPromptSection(sqlOnlyContext(), correlationGuidance{keys: keys})

	if !strings.Contains(got, "`userId`") || !strings.Contains(got, "`clientId`") {
		t.Errorf("want the first two rendered:\n%s", got)
	}
	if strings.Contains(got, "`sessionId`") {
		t.Errorf("want the third dropped under the cap:\n%s", got)
	}
	if !strings.Contains(got, "…and 2 more rejected pairings") {
		t.Errorf("want the overflow counted:\n%s", got)
	}
	if !strings.Contains(got, "call `get_correlations` for the full list") {
		t.Errorf("want the overflow to point at the tool:\n%s", got)
	}
}

func TestCorrelationContract_UnderTheCapHasNoOverflowLine(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(),
		correlationGuidance{keys: []agentplugin.CorrelationKey{rejectedKey("userId")}})

	if strings.Contains(got, "more rejected pairings") {
		t.Errorf("nothing was dropped, so nothing should say so:\n%s", got)
	}
}

// TestCorrelationContract_SitsAfterTheHopRules places the block where the
// model has just been told HOW to hop, which is where it should learn which
// keys are known.
func TestCorrelationContract_SitsAfterTheHopRules(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(),
		correlationGuidance{keys: []agentplugin.CorrelationKey{rejectedKey("userId")}})

	hop := strings.Index(got, "RIGHT (two steps)")
	block := strings.Index(got, "Reviewed correlation keys")
	list := strings.Index(got, "Available datasources:")
	if hop < 0 || block < 0 || list < 0 {
		t.Fatalf("missing a landmark (hop=%d block=%d list=%d):\n%s", hop, block, list, got)
	}
	if hop >= block || block >= list {
		t.Errorf("want the block between the hop example and the datasource list:\n%s", got)
	}
}

// TestCorrelationContract_RendersOnAMixedLanguageRunToo: the block is about
// which keys were reviewed, which has nothing to do with what language a
// datasource is queried in.
func TestCorrelationContract_RendersOnAMixedLanguageRunToo(t *testing.T) {
	got := buildDatasourcesPromptSection(withDatasource(sqlOnlyContext(), cubeDescriptor()),
		correlationGuidance{keys: []agentplugin.CorrelationKey{rejectedKey("userId")}})

	if !strings.Contains(got, "REJECTED PAIRINGS") {
		t.Errorf("want the prohibition on a mixed-language run:\n%s", got)
	}
	if !strings.Contains(got, "NOT all queried in the same language") {
		t.Errorf("want the mixed-language contract kept:\n%s", got)
	}
}

// TestCorrelationContract_RestrictionIsScopedToTheRejectedPairs.
//
// The record-grain fallback used to be stated unconditionally, so on a run with
// three or more datasources one rejection on A↔B told the model not to
// correlate B↔C record by record either. That is a prohibition nobody
// recorded, and it contradicts the answer `get_correlations` gives for an
// unreviewed pair — which says in as many words that silence is neutral.
func TestCorrelationContract_RestrictionIsScopedToTheRejectedPairs(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(),
		correlationGuidance{keys: []agentplugin.CorrelationKey{rejectedKey("userId")}})

	if !strings.Contains(got, "ONE OF THE PAIRS ABOVE") {
		t.Errorf("the record-grain restriction must name what it applies to:\n%s", got)
	}
	// And say what it does not apply to, since the contract is the only place
	// a run that never calls the action reads about any of this.
	if !strings.Contains(got, "unreviewed, which is not the same as rejected") {
		t.Errorf("the contract must keep silence neutral:\n%s", got)
	}
}

// TestCorrelationLookup_WiredOnlyWhenTheActionWasTaught.
//
// The engine announces a per-run budget for this action whenever the lookup is
// wired, so wiring it on a run whose contract never taught the action changes
// that run's opening message to advertise something it was not offered — which
// is every deployment with no provider at all, and every project that has
// curated nothing.
func TestCorrelationLookup_WiredOnlyWhenTheActionWasTaught(t *testing.T) {
	o := &Orchestrator{projectID: "p1"}
	dc := sqlOnlyContext()

	if o.correlationLookup(nil, correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}}) != nil {
		t.Error("a single-datasource run has nothing to correlate with")
	}
	if o.correlationLookup(dc, correlationGuidance{}) != nil {
		t.Error("a run whose contract taught no action must not be given the lookup")
	}
	if o.correlationLookup(dc, correlationGuidance{keys: []agentplugin.CorrelationKey{}}) != nil {
		t.Error("an empty decision set is the same as none")
	}
	if o.correlationLookup(dc, correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}}) == nil {
		t.Error("a run that was taught the action must be able to serve it")
	}
}

// TestCorrelationLookup_AsksTheSeamAboutThePairItIsGiven.
func TestCorrelationLookup_AsksTheSeamAboutThePairItIsGiven(t *testing.T) {
	defer agentplugin.ResetCorrelationProviderForTest()
	agentplugin.ResetCorrelationProviderForTest()

	var got agentplugin.CorrelationRequest
	agentplugin.RegisterCorrelationProvider("test-decisions",
		func(_ context.Context, req agentplugin.CorrelationRequest) ([]agentplugin.CorrelationKey, error) {
			got = req
			return []agentplugin.CorrelationKey{confirmedKey()}, nil
		})

	o := &Orchestrator{projectID: "p1"}
	lookup := o.correlationLookup(sqlOnlyContext(), correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}})
	keys, err := lookup(context.Background(), "wh_analytics", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ProjectID != "p1" {
		t.Errorf("ProjectID = %q, want the run's project", got.ProjectID)
	}
	if len(got.DatasourceIDs) != 2 || got.DatasourceIDs[0] != "wh_analytics" || got.DatasourceIDs[1] != "default" {
		t.Errorf("DatasourceIDs = %v, want the pair the action named", got.DatasourceIDs)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want the provider's answer", keys)
	}
}

// TestCorrelationContract_AFailedReadIsNotAnUncuratedProject.
//
// A decision store that was briefly unreachable has not established that
// nobody has reviewed anything. Treating the two alike would drop every
// recorded rejection for the length of a run, silently — the outcome this
// whole feature exists to stop.
func TestCorrelationContract_AFailedReadIsNotAnUncuratedProject(t *testing.T) {
	got := buildDatasourcesPromptSection(sqlOnlyContext(), correlationGuidance{unread: true})

	if !strings.Contains(got, "MUST call `get_correlations`") {
		t.Errorf("the action must still be offered so it can be retried per pair:\n%s", got)
	}
	if !strings.Contains(got, "failed read, NOT a project with nothing reviewed") {
		t.Errorf("the contract must say the list is missing, not empty:\n%s", got)
	}
	if strings.Contains(got, "REJECTED PAIRINGS") {
		t.Errorf("there is nothing to name, so nothing may be named:\n%s", got)
	}
}

func TestCorrelationLookup_WiredAfterAFailedRead(t *testing.T) {
	o := &Orchestrator{projectID: "p1"}
	// The store may answer the next time it is asked, and a per-pair call
	// during the run is the only way to find out.
	if o.correlationLookup(sqlOnlyContext(), correlationGuidance{unread: true}) == nil {
		t.Error("a failed startup read must not disable the action for the whole run")
	}
}

// TestCorrelationGuidance_Offered pins the one predicate both the contract and
// the wiring read, so they cannot drift into disagreeing about which runs get
// the action.
func TestCorrelationGuidance_Offered(t *testing.T) {
	for _, tc := range []struct {
		name string
		g    correlationGuidance
		want bool
	}{
		{"nothing known", correlationGuidance{}, false},
		{"decisions exist", correlationGuidance{keys: []agentplugin.CorrelationKey{confirmedKey()}}, true},
		{"read failed", correlationGuidance{unread: true}, true},
	} {
		if got := tc.g.offered(); got != tc.want {
			t.Errorf("%s: offered() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCuratedCorrelations_AHangingProviderDegradesRatherThanStalling.
//
// The error path already degrades a failed read to "unread". A provider that
// HANGS never fails, so without a bound it would hold the run for the whole
// run context — which defaults to 24 hours and can be turned off entirely.
// The timeout is what turns a hang back into the failure this code handles.
func TestCuratedCorrelations_AHangingProviderDegradesRatherThanStalling(t *testing.T) {
	t.Setenv(correlationLookupTimeoutEnv, "1")
	defer agentplugin.ResetCorrelationProviderForTest()
	agentplugin.ResetCorrelationProviderForTest()

	released := make(chan struct{})
	defer close(released)
	agentplugin.RegisterCorrelationProvider("hangs",
		func(ctx context.Context, _ agentplugin.CorrelationRequest) ([]agentplugin.CorrelationKey, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-released:
				return nil, nil
			}
		})

	o := &Orchestrator{projectID: "p1"}
	start := time.Now()
	got := o.curatedCorrelations(context.Background(), sqlOnlyContext())
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("took %s; the provider call is not bounded", elapsed)
	}
	if !got.unread {
		t.Error("a timed-out read must report as unread, not as a project with nothing reviewed")
	}
	if got.offered() != true {
		t.Error("the action must still be offered so the run can retry per pair")
	}
}

// TestCorrelationLookupTimeout_HonoursItsOverride keeps the knob real: Rule 2
// wants a value an operator can change, not one that only looks configurable.
func TestCorrelationLookupTimeout_HonoursItsOverride(t *testing.T) {
	if got := correlationLookupTimeout(); got != defaultCorrelationLookupTimeout {
		t.Errorf("unset = %s, want the default %s", got, defaultCorrelationLookupTimeout)
	}
	t.Setenv(correlationLookupTimeoutEnv, "42")
	if got := correlationLookupTimeout(); got != 42*time.Second {
		t.Errorf("override = %s, want 42s", got)
	}
	// Nonsense falls back rather than producing a zero timeout, which would
	// cancel every call before it started.
	t.Setenv(correlationLookupTimeoutEnv, "nope")
	if got := correlationLookupTimeout(); got != defaultCorrelationLookupTimeout {
		t.Errorf("invalid override = %s, want the default", got)
	}
}
