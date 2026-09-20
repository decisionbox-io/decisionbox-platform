package agentplugin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func keyProvider(keys ...CorrelationKey) CorrelationProviderFunc {
	return func(context.Context, CorrelationRequest) ([]CorrelationKey, error) { return keys, nil }
}

func confirmedKey() CorrelationKey {
	return CorrelationKey{
		DatasourceID: "wh_a", SourceField: "transactionId",
		WithDatasourceID: "wh_b", AnchorColumns: []string{"orders.order_id"},
		Grain: "transaction", State: CorrelationConfirmed,
	}
}

func TestCorrelations_NoProvider_IsEmptyNotAnError(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	// An unwired seam must not look like a failure: a run would then have to
	// decide whether to stop over knowledge nobody ever recorded.
	keys, err := Correlations(context.Background(), CorrelationRequest{ProjectID: "p1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %+v, want none", keys)
	}
}

func TestCorrelations_UsesTheRegisteredProvider(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	var got CorrelationRequest
	want := confirmedKey()
	RegisterCorrelationProvider("test-decisions", func(_ context.Context, req CorrelationRequest) ([]CorrelationKey, error) {
		got = req
		return []CorrelationKey{want}, nil
	})

	req := CorrelationRequest{ProjectID: "p1", DatasourceIDs: []string{"wh_a", "wh_b"}}
	keys, err := Correlations(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("provider saw %+v, want %+v", got, req)
	}
	if len(keys) != 1 || !reflect.DeepEqual(keys[0], want) {
		t.Fatalf("keys = %+v, want the provider's own answer verbatim", keys)
	}
}

// TestCorrelations_RejectionWithoutAReasonGetsOne is the one substitution this
// seam makes. A rejection is quoted to a model as an instruction not to do
// something, and an instruction with no reason attached is the kind a model
// talks itself out of.
func TestCorrelations_RejectionWithoutAReasonGetsOne(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	RegisterCorrelationProvider("silent", keyProvider(CorrelationKey{
		DatasourceID: "wh_a", SourceField: "userId",
		WithDatasourceID: "wh_b", AnchorColumns: []string{"orders.customer_id"},
		Grain: "customer", State: CorrelationRejected,
	}))

	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want one", keys)
	}
	if !strings.Contains(keys[0].Reason, "do not hold the same values") {
		t.Fatalf("Reason = %q, want the generic sentence", keys[0].Reason)
	}
}

func TestCorrelations_KeepsAProvidersOwnReason(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	const reason = "Dana rejected this on 2026-09-01: the ids are from different tenants"
	RegisterCorrelationProvider("wordy", keyProvider(CorrelationKey{
		DatasourceID: "wh_a", SourceField: "userId",
		WithDatasourceID: "wh_b", State: CorrelationRejected, Reason: reason,
	}))

	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys[0].Reason != reason {
		t.Fatalf("Reason = %q, want the provider's own sentence untouched", keys[0].Reason)
	}
}

// TestCorrelations_DropsAnUnrecognisedState guards the one thing a caller
// cannot render: everything downstream turns State into an instruction, and a
// state nobody recognises is neither "use this" nor "do not".
func TestCorrelations_DropsAnUnrecognisedState(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	good := confirmedKey()
	RegisterCorrelationProvider("mixed", keyProvider(
		good,
		CorrelationKey{DatasourceID: "wh_a", SourceField: "x", State: CorrelationState("probably")},
		CorrelationKey{DatasourceID: "wh_a", SourceField: "y", State: ""},
	))

	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 || keys[0].SourceField != good.SourceField {
		t.Fatalf("keys = %+v, want only the recognised one", keys)
	}
}

// TestCorrelations_AllStatesDroppedIsNothing pins that a filtered-to-empty
// answer reads the same as no answer. A caller decides whether to render a
// block by whether anything came back, and an empty non-nil slice would render
// a heading over nothing.
func TestCorrelations_AllStatesDroppedIsNothing(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	RegisterCorrelationProvider("junk", keyProvider(
		CorrelationKey{DatasourceID: "wh_a", SourceField: "x", State: CorrelationState("maybe")},
	))

	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys != nil {
		t.Fatalf("keys = %+v, want nil", keys)
	}
}

func TestCorrelations_ProviderErrorIsWrappedAndNamed(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	sentinel := errors.New("decision store unreachable")
	RegisterCorrelationProvider("the-store", func(context.Context, CorrelationRequest) ([]CorrelationKey, error) {
		return []CorrelationKey{confirmedKey()}, sentinel
	})

	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the provider's own error", err)
	}
	if !strings.Contains(err.Error(), "the-store") {
		t.Fatalf("error = %q, want it to name the provider", err.Error())
	}
	// Keys returned alongside an error must not be usable: a provider that
	// errored did not answer the question, and a partial list of decisions is
	// how a rejection goes missing.
	if keys != nil {
		t.Fatalf("keys = %+v, want none from an errored provider", keys)
	}
}

func TestRegisterCorrelationProvider_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func()
	}{
		{"empty name", func() { RegisterCorrelationProvider("", keyProvider()) }},
		{"nil fn", func() { RegisterCorrelationProvider("x", nil) }},
		{"double registration", func() {
			RegisterCorrelationProvider("first", keyProvider())
			RegisterCorrelationProvider("second", keyProvider())
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetCorrelationProviderForTest()
			ResetCorrelationProviderForTest()
			defer func() {
				if recover() == nil {
					t.Fatalf("%s must panic", tc.name)
				}
			}()
			tc.call()
		})
	}
}

func TestResetCorrelationProviderForTest_AllowsReRegistration(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	first := confirmedKey()
	second := confirmedKey()
	second.SourceField = "orderRef"

	RegisterCorrelationProvider("first", keyProvider(first))
	ResetCorrelationProviderForTest()
	RegisterCorrelationProvider("second", keyProvider(second))

	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 1 || keys[0].SourceField != "orderRef" {
		t.Fatalf("keys = %+v, want the re-registered provider's answer", keys)
	}
}

func TestCorrelations_ProviderPanicBecomesAnError(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	// Stands in for the accident a real provider would have while reading a
	// project's decisions. What is under test is the recover, not which
	// runtime fault reached it.
	RegisterCorrelationProvider("exploding", func(context.Context, CorrelationRequest) ([]CorrelationKey, error) {
		panic("nil map in the decision reader")
	})

	// The point is that this call RETURNS at all: a discovery run has no
	// recover of its own, so an escaping panic takes the agent process down —
	// and every other run on it — over a question whose honest answer is
	// "nothing is known".
	keys, err := Correlations(context.Background(), CorrelationRequest{})
	if err == nil {
		t.Fatal("a panicking provider must surface as an error")
	}
	if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "exploding") {
		t.Fatalf("error = %q, want it to name the provider and say it panicked", err.Error())
	}
	if keys != nil {
		t.Fatalf("keys = %+v, want none", keys)
	}
}

func TestCorrelations_SurvivesAPanicAndKeepsWorking(t *testing.T) {
	defer ResetCorrelationProviderForTest()
	ResetCorrelationProviderForTest()

	calls := 0
	RegisterCorrelationProvider("flaky", func(_ context.Context, req CorrelationRequest) ([]CorrelationKey, error) {
		calls++
		if req.ProjectID == "bad" {
			panic("boom")
		}
		return []CorrelationKey{confirmedKey()}, nil
	})

	if _, err := Correlations(context.Background(), CorrelationRequest{ProjectID: "bad"}); err == nil {
		t.Fatal("want an error for the panicking call")
	}
	// The registry must not be left broken by the recover.
	keys, err := Correlations(context.Background(), CorrelationRequest{ProjectID: "good"})
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%+v err=%v; a later call must still be answered", keys, err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestCallCorrelationProvider_PanicYieldsNoAnswer pins the invariant at the
// layer that catches the panic, not only at the exported one.
//
// Correlations discards the keys whenever an error comes back, so a test there
// cannot tell that apart from a half-built answer escaping the recover. This
// one can, and it has to: a partial list of decisions is how a rejection
// silently goes missing.
func TestCallCorrelationProvider_PanicYieldsNoAnswer(t *testing.T) {
	keys, err := callCorrelationProvider(context.Background(),
		func(context.Context, CorrelationRequest) ([]CorrelationKey, error) {
			panic("boom")
		}, CorrelationRequest{})

	if err == nil {
		t.Fatal("a panic must become an error")
	}
	if keys != nil {
		t.Fatalf("keys = %+v, want nil — nothing was computed", keys)
	}
}
