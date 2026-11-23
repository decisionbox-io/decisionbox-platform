package agentplugin

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

type fakeScoringProvider struct {
	override   ScoringOverride
	err        error
	panics     bool
	namePanics bool
	name       string
	gotProject string
}

func (f *fakeScoringProvider) ScoringConfig(_ context.Context, projectID string) (ScoringOverride, error) {
	f.gotProject = projectID
	if f.panics {
		panic("boom")
	}
	return f.override, f.err
}

func (f *fakeScoringProvider) Name() string {
	if f.namePanics {
		panic("no name")
	}
	if f.name != "" {
		return f.name
	}
	return "fake-scoring"
}

func useScoringProvider(t *testing.T, p ScoringConfigProvider) {
	t.Helper()
	resetScoringForTest()
	t.Cleanup(resetScoringForTest)
	if p != nil {
		RegisterScoringConfigProvider(p)
	}
}

func TestResolveScoringConfig_NothingRegisteredIsTheZeroOverrideAndNoError(t *testing.T) {
	useScoringProvider(t, nil)
	got, err := ResolveScoringConfig(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CategoryWeights != nil {
		t.Errorf("CategoryWeights = %v, want nil with no provider registered", got.CategoryWeights)
	}
}

func TestResolveScoringConfig_TheProviderIsAskedForThisProject(t *testing.T) {
	p := &fakeScoringProvider{override: ScoringOverride{CategoryWeights: map[string]float64{"compliance": 3}}}
	useScoringProvider(t, p)
	got, err := ResolveScoringConfig(context.Background(), "p-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.gotProject != "p-42" {
		t.Errorf("provider asked about %q, want p-42", p.gotProject)
	}
	if want := map[string]float64{"compliance": 3}; !reflect.DeepEqual(got.CategoryWeights, want) {
		t.Errorf("CategoryWeights = %v, want %v", got.CategoryWeights, want)
	}
}

func TestResolveScoringConfig_AnErrorIsTheZeroOverrideAndNamesTheProvider(t *testing.T) {
	cause := errors.New("db down")
	useScoringProvider(t, &fakeScoringProvider{
		override: ScoringOverride{CategoryWeights: map[string]float64{"compliance": 3}},
		err:      cause,
		name:     "settings-store",
	})
	got, err := ResolveScoringConfig(context.Background(), "p1")
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want it to wrap the provider's error", err)
	}
	if !strings.Contains(err.Error(), "settings-store") {
		t.Errorf("err = %q, want it to name the provider", err)
	}
	// What the provider returned beside its error is not trusted: a provider
	// that failed has not answered.
	if got.CategoryWeights != nil {
		t.Errorf("CategoryWeights = %v, want nil on error", got.CategoryWeights)
	}
}

func TestResolveScoringConfig_APanicIsRecoveredIntoAnError(t *testing.T) {
	useScoringProvider(t, &fakeScoringProvider{panics: true})
	got, err := ResolveScoringConfig(context.Background(), "p1")
	if err == nil {
		t.Fatal("a panicking provider must surface an error, not crash")
	}
	if !strings.Contains(err.Error(), "fake-scoring") {
		t.Errorf("err = %q, want it to name the provider", err)
	}
	if got.CategoryWeights != nil {
		t.Errorf("CategoryWeights = %v, want nil after a panic", got.CategoryWeights)
	}
}

// TestResolveScoringConfig_APanickingNameDoesNotEscapeTheRecovery -- the
// recovery names the provider, so a Name() that panics too must not turn the
// recovered panic into a second, unrecovered one.
func TestResolveScoringConfig_APanickingNameDoesNotEscapeTheRecovery(t *testing.T) {
	p := &fakeScoringProvider{panics: true}
	useScoringProvider(t, p)
	// Registration reads the name, so it only starts panicking afterwards.
	p.namePanics = true

	_, err := ResolveScoringConfig(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "<unknown:Name() panicked>") {
		t.Errorf("err = %v, want the placeholder name", err)
	}
}

func TestResolveScoringConfig_TheLastRegistrantWins(t *testing.T) {
	useScoringProvider(t, &fakeScoringProvider{override: ScoringOverride{CategoryWeights: map[string]float64{"compliance": 1}}})
	RegisterScoringConfigProvider(&fakeScoringProvider{override: ScoringOverride{CategoryWeights: map[string]float64{"compliance": 2}}})
	got, _ := ResolveScoringConfig(context.Background(), "p1")
	if got.CategoryWeights["compliance"] != 2 {
		t.Errorf("compliance = %v, want 2 from the second registrant", got.CategoryWeights["compliance"])
	}
}

func TestRegisterScoringConfigProvider_RejectsProgrammerErrors(t *testing.T) {
	resetScoringForTest()
	t.Cleanup(resetScoringForTest)
	for _, tc := range []struct {
		name string
		p    ScoringConfigProvider
	}{
		{"nil", nil},
		{"an empty name", emptyNameProvider{&fakeScoringProvider{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("registration did not panic")
				}
			}()
			RegisterScoringConfigProvider(tc.p)
		})
	}
}

// emptyNameProvider is a provider whose Name() really is empty: the fake falls
// back to a default name when its own is unset.
type emptyNameProvider struct{ *fakeScoringProvider }

func (emptyNameProvider) Name() string { return "" }

// TestResolveScoringConfig_NormalisesWhatTheEngineWouldRefuse -- every entry
// the engine would not apply, or that the run document could not store, is
// dropped, and the ones beside it survive.
func TestResolveScoringConfig_NormalisesWhatTheEngineWouldRefuse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		id     string
		weight float64
		keep   bool
	}{
		{"a legal weight", "compliance", 3, true},
		{"the bottom of the range", "compliance", 0.01, true},
		{"zero", "compliance", 0, false},
		{"negative", "compliance", -1, false},
		{"just over the ceiling", "compliance", 3.0001, false},
		{"NaN", "compliance", math.NaN(), false},
		{"+Inf", "compliance", math.Inf(1), false},
		{"-Inf", "compliance", math.Inf(-1), false},
		{"an empty id", "", 2, false},
		{"a NUL byte in the id", "comp\x00liance", 2, false},
		{"an uppercase id", "Compliance", 2, false},
		{"a dotted id", "a.b", 2, false},
		{"a dollar id", "$where", 2, false},
		{"an underscore id", "data_quality", 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useScoringProvider(t, &fakeScoringProvider{override: ScoringOverride{CategoryWeights: map[string]float64{
				tc.id:       tc.weight,
				"retention": 1.5,
			}}})
			got, err := ResolveScoringConfig(context.Background(), "p1")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			_, kept := got.CategoryWeights[tc.id]
			if kept != tc.keep {
				t.Errorf("%q = %v kept %v, want %v", tc.id, tc.weight, kept, tc.keep)
			}
			if got.CategoryWeights["retention"] != 1.5 {
				t.Errorf("the legal entry beside it was lost: %v", got.CategoryWeights)
			}
		})
	}
}

func TestResolveScoringConfig_NothingLegalIsNil(t *testing.T) {
	useScoringProvider(t, &fakeScoringProvider{override: ScoringOverride{CategoryWeights: map[string]float64{
		"compliance": 0, "": 2,
	}}})
	got, err := ResolveScoringConfig(context.Background(), "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CategoryWeights != nil {
		t.Errorf("CategoryWeights = %v, want nil when nothing legal was supplied", got.CategoryWeights)
	}
}
