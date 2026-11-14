package agentplugin

import (
	"context"
	"errors"
	"testing"
)

type stubConsolidator struct {
	name      string
	verdicts  []ConsolidationVerdict
	err       error
	panicWith string
	calls     int
	gotPID    string
	gotCount  int
}

func (s *stubConsolidator) Name() string {
	if s.panicWith == "name" {
		panic("Name() exploded")
	}
	return s.name
}

func (s *stubConsolidator) Consolidate(_ context.Context, projectID string, clusters []DuplicateCluster) ([]ConsolidationVerdict, error) {
	s.calls++
	s.gotPID = projectID
	s.gotCount = len(clusters)
	if s.panicWith == "consolidate" {
		panic("Consolidate() exploded")
	}
	return s.verdicts, s.err
}

func oneCluster() []DuplicateCluster {
	return []DuplicateCluster{{
		Candidates: []ConsolidationDoc{{ID: "a"}, {ID: "b"}},
		Proposal:   ProposalCosine,
	}}
}

func TestResolveConsolidation_NoProviderIsNotAnError(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	verdicts, err := ResolveConsolidation(context.Background(), "proj", oneCluster())
	if err != nil {
		t.Fatalf("no provider must not be an error: %v", err)
	}
	if verdicts != nil {
		t.Fatalf("no provider must return no verdicts, got %d", len(verdicts))
	}
	if HasConsolidationProvider() {
		t.Error("HasConsolidationProvider() = true with nothing registered")
	}
	if n := ConsolidationProviderName(); n != "" {
		t.Errorf("ConsolidationProviderName() = %q, want empty", n)
	}
}

func TestResolveConsolidation_PassesClustersThrough(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	stub := &stubConsolidator{name: "stub", verdicts: []ConsolidationVerdict{{CanonicalID: "a"}}}
	RegisterConsolidationProvider(stub)

	verdicts, err := ResolveConsolidation(context.Background(), "proj-9", oneCluster())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].CanonicalID != "a" {
		t.Fatalf("verdicts = %+v", verdicts)
	}
	if stub.gotPID != "proj-9" || stub.gotCount != 1 {
		t.Errorf("provider saw project=%q clusters=%d", stub.gotPID, stub.gotCount)
	}
	if !HasConsolidationProvider() || ConsolidationProviderName() != "stub" {
		t.Errorf("registry does not report the registered provider")
	}
}

// An empty cluster list must not reach the provider at all: a run with nothing
// to adjudicate should cost nothing, and a provider that charges per call
// would otherwise be billed for every area that found no duplicates.
func TestResolveConsolidation_EmptyClustersSkipTheProvider(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	stub := &stubConsolidator{name: "stub"}
	RegisterConsolidationProvider(stub)

	if _, err := ResolveConsolidation(context.Background(), "proj", nil); err != nil {
		t.Fatalf("empty clusters: %v", err)
	}
	if stub.calls != 0 {
		t.Errorf("provider called %d times for an empty cluster list", stub.calls)
	}
}

// An error returns NO verdicts, not the partial set. A half-applied
// adjudication is the one state the write guard cannot reason about: a
// canonical chosen in one cluster and suppressed in another is how a fold
// chain gets built.
func TestResolveConsolidation_ErrorReturnsNothing(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	boom := errors.New("provider down")
	RegisterConsolidationProvider(&stubConsolidator{
		name:     "stub",
		verdicts: []ConsolidationVerdict{{CanonicalID: "a"}},
		err:      boom,
	})

	verdicts, err := ResolveConsolidation(context.Background(), "proj", oneCluster())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the provider's error", err)
	}
	if verdicts != nil {
		t.Fatalf("an error must discard the partial verdicts, got %d", len(verdicts))
	}
}

func TestResolveConsolidation_PanicIsRecovered(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	RegisterConsolidationProvider(&stubConsolidator{name: "stub", panicWith: "consolidate"})

	verdicts, err := ResolveConsolidation(context.Background(), "proj", oneCluster())
	if err == nil {
		t.Fatal("a panicking provider must return an error, not unwind into the run")
	}
	if verdicts != nil {
		t.Fatalf("verdicts = %+v after a panic", verdicts)
	}
}

// A provider whose Name() panics must still be nameable for the error
// message: the recover path calls it, and a panic there would escape the
// deferred recover that was handling the first one.
func TestResolveConsolidation_PanickingNameIsSafe(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	consolidationMu.Lock()
	consolidationProvider = &stubConsolidator{name: "stub", panicWith: "name"}
	consolidationMu.Unlock()

	if n := ConsolidationProviderName(); n == "" {
		t.Error("ConsolidationProviderName() must not return empty for a panicking Name()")
	}
}

func TestRegisterConsolidationProvider_RejectsNilAndNameless(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	for _, tc := range []struct {
		name string
		p    ConsolidationProvider
	}{
		{"nil", nil},
		{"empty name", &stubConsolidator{name: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("expected a panic at registration")
				}
			}()
			RegisterConsolidationProvider(tc.p)
		})
	}
}

// Last registrant wins, as with DiscoveryPolicyProvider — a single-provider
// registry, so a second registration replaces rather than panicking.
func TestRegisterConsolidationProvider_LastWins(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	RegisterConsolidationProvider(&stubConsolidator{name: "first"})
	RegisterConsolidationProvider(&stubConsolidator{name: "second"})
	if got := ConsolidationProviderName(); got != "second" {
		t.Errorf("provider = %q, want second", got)
	}
}

// The contract is one verdict per cluster, POSITIONALLY. A response of any
// other length is discarded whole, because reading it by position assigns one
// cluster's verdict to another: a provider that answers only the second of two
// clusters has that answer read against the FIRST, where it is rejected as
// naming a stranger — and the second cluster then gets the community rule,
// which may fold it the way the provider refused to. A caller cannot tell that
// apart from a provider that answered the first cluster.
func TestResolveConsolidation_RejectsANonPositionalResponse(t *testing.T) {
	resetConsolidationForTest()
	t.Cleanup(resetConsolidationForTest)

	two := []DuplicateCluster{
		{Candidates: []ConsolidationDoc{{ID: "a"}, {ID: "b"}}, Proposal: ProposalCosine},
		{Candidates: []ConsolidationDoc{{ID: "c"}, {ID: "d"}}, Proposal: ProposalCosine},
	}

	RegisterConsolidationProvider(&stubConsolidator{
		name:     "shortish",
		verdicts: []ConsolidationVerdict{{CanonicalID: "d", Duplicates: map[string]float64{"c": 0.9}}},
	})
	verdicts, err := ResolveConsolidation(context.Background(), "proj", two)
	if err == nil {
		t.Fatal("a one-verdict answer for two clusters must be refused, not read by position")
	}
	if verdicts != nil {
		t.Fatalf("verdicts = %+v, want none", verdicts)
	}

	// The positional answer — including the zero verdict for a cluster the
	// provider declined — is accepted.
	resetConsolidationForTest()
	RegisterConsolidationProvider(&stubConsolidator{
		name:     "positional",
		verdicts: []ConsolidationVerdict{{}, {CanonicalID: "d", Duplicates: map[string]float64{"c": 0.9}}},
	})
	verdicts, err = ResolveConsolidation(context.Background(), "proj", two)
	if err != nil {
		t.Fatalf("a positional answer must be accepted: %v", err)
	}
	if len(verdicts) != 2 || verdicts[0].CanonicalID != "" || verdicts[1].CanonicalID != "d" {
		t.Fatalf("verdicts = %+v", verdicts)
	}
}
