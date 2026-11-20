package agentplugin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
)

// The scoring-configuration registry, tested the way the discovery-policy one
// is: by what a RUN sees, not by what the registry stores.
//
// Every case here is a case where the provider fails to answer, because that
// is the whole contract. A severity weight is a preference; the result of a
// discovery run is hours of warehouse queries and model calls. So the registry
// is allowed to lose a preference and is never allowed to lose a run, and the
// four ways it can lose one -- nothing registered, an error, a panic, a
// provider that cannot even name itself -- each get their own assertion.

type fakeScoringProvider struct {
	name string
	out  ScoringOverride
	err  error
	// panicWith, when non-nil, is raised instead of answering.
	panicWith any
	// gotProject and gotWarehouse record what was asked for.
	gotProject   string
	gotWarehouse string
}

func (f *fakeScoringProvider) Name() string { return f.name }

func (f *fakeScoringProvider) ScoringConfig(_ context.Context, projectID, warehouseID string) (ScoringOverride, error) {
	f.gotProject, f.gotWarehouse = projectID, warehouseID
	if f.panicWith != nil {
		panic(f.panicWith)
	}
	return f.out, f.err
}

// TestNoProviderResolvesToNothingConfigured is the community case, and the
// state every deployment is in until someone saves a weight.
//
// The zero override and a NIL ERROR, which together are what make the
// compatibility guarantee hold: ApplyOverride leaves a calibration untouched
// when handed this value, so an unconfigured deployment scores exactly as it
// did before any of this existed, and nothing logs a line about it.
func TestNoProviderResolvesToNothingConfigured(t *testing.T) {
	resetScoringForTest()

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, ScoringOverride{}) {
		t.Errorf("override = %+v, want the zero value", got)
	}
}

// TestTheProviderIsAskedForTheProjectAndTheDatasource pins the signature's
// second half. The money thresholds are per datasource because the currency
// is, so a registry that dropped the warehouse id would hand every datasource
// the first one's thresholds.
func TestTheProviderIsAskedForTheProjectAndTheDatasource(t *testing.T) {
	resetScoringForTest()
	p := &fakeScoringProvider{name: "fake"}
	RegisterScoringConfigProvider(p)
	t.Cleanup(resetScoringForTest)

	if _, err := ResolveScoringConfig(context.Background(), "proj-7", "wh_snowflake"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.gotProject != "proj-7" || p.gotWarehouse != "wh_snowflake" {
		t.Errorf("asked for (%q, %q), want (%q, %q)", p.gotProject, p.gotWarehouse, "proj-7", "wh_snowflake")
	}
}

// TestAProviderErrorFallsBackAndReports: the caller gets the default AND the
// error, so it can log what went wrong and still score the run. Returning only
// the error would leave the pin with nothing to write; swallowing it would
// leave a deployment whose Mongo is down silently unweighted.
func TestAProviderErrorFallsBackAndReports(t *testing.T) {
	resetScoringForTest()
	want := errors.New("mongo is down")
	RegisterScoringConfigProvider(&fakeScoringProvider{
		name: "fake",
		out:  ScoringOverride{CategoryWeights: map[string]float64{"compliance": 3}},
		err:  want,
	})
	t.Cleanup(resetScoringForTest)

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if !reflect.DeepEqual(got, ScoringOverride{}) {
		t.Errorf("override = %+v, want the zero value — a half-answer must not reach the engine", got)
	}
}

// TestAPanickingProviderCannotTakeTheRunWithIt. A plugin's nil map or failed
// type assertion is the panic this is actually about, and it happens inside
// the provider's own goroutine -- the run's.
func TestAPanickingProviderCannotTakeTheRunWithIt(t *testing.T) {
	resetScoringForTest()
	RegisterScoringConfigProvider(&fakeScoringProvider{name: "exploding", panicWith: "boom"})
	t.Cleanup(resetScoringForTest)

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err == nil {
		t.Fatal("a panicking provider reported no error")
	}
	// The provider is NAMED, because an operator reading the log has to know
	// which plugin to turn off.
	if !strings.Contains(err.Error(), "exploding") {
		t.Errorf("error does not name the provider: %v", err)
	}
	if !reflect.DeepEqual(got, ScoringOverride{}) {
		t.Errorf("override = %+v, want the zero value", got)
	}
}

// TestAProviderWhoseNamePanicsIsStillReported. The recovery path asks the
// provider what it is called, and a provider broken enough to panic in
// ScoringConfig can be broken enough to panic in Name() -- which would turn a
// handled panic into an unhandled one inside the recover.
func TestAProviderWhoseNamePanicsIsStillReported(t *testing.T) {
	resetScoringForTest()
	// Installed directly rather than registered: registration asks for the
	// name and would panic there instead, which is the guard the test below
	// covers. The path under test is the RECOVERY asking a broken provider
	// what it is called.
	scoringMu.Lock()
	scoringProvider = namePanics{}
	scoringMu.Unlock()
	t.Cleanup(resetScoringForTest)

	_, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Name() panicked") {
		t.Errorf("error = %v, want it to report the unusable name", err)
	}
}

type namePanics struct{}

func (namePanics) Name() string { panic("no name") }

func (namePanics) ScoringConfig(context.Context, string, string) (ScoringOverride, error) {
	panic("boom")
}

// TestRegistrationRefusesWhatCannotBeUsed. Both are programmer errors in an
// init(), which is exactly where a panic is the right answer: the alternative
// is a deployment that boots fine and silently has no provider.
func TestRegistrationRefusesWhatCannotBeUsed(t *testing.T) {
	resetScoringForTest()
	t.Cleanup(resetScoringForTest)

	t.Run("nil provider", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("registering nil did not panic")
			}
		}()
		RegisterScoringConfigProvider(nil)
	})

	t.Run("empty name", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("registering an unnamed provider did not panic")
			}
		}()
		RegisterScoringConfigProvider(&fakeScoringProvider{name: ""})
	})
}

// TestTheLastRegistrantWins — one provider, as with the discovery policy. The
// enterprise build registers exactly one; this pins which answer a second
// would give rather than leaving it to map iteration or to whoever happened to
// import first.
func TestTheLastRegistrantWins(t *testing.T) {
	resetScoringForTest()
	RegisterScoringConfigProvider(&fakeScoringProvider{
		name: "first", out: ScoringOverride{CategoryWeights: map[string]float64{"compliance": 1.5}},
	})
	RegisterScoringConfigProvider(&fakeScoringProvider{
		name: "second", out: ScoringOverride{CategoryWeights: map[string]float64{"compliance": 2.5}},
	})
	t.Cleanup(resetScoringForTest)

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.CategoryWeights["compliance"] != 2.5 {
		t.Errorf("compliance = %v, want 2.5 from the second registrant", got.CategoryWeights["compliance"])
	}
}

// TestAnOverrideThatChangesABandSurvivesTheRegistry is the case the feature
// exists for, asserted at this seam rather than at the engine's: a weight and
// a threshold handed over by a plugin arrive intact.
func TestAnOverrideThatChangesABandSurvivesTheRegistry(t *testing.T) {
	resetScoringForTest()
	want := ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3},
		AmountBands:     []gomodels.MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}},
		AmountCurrency:  "₺",
	}
	RegisterScoringConfigProvider(&fakeScoringProvider{name: "fake", out: want})
	t.Cleanup(resetScoringForTest)

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("override = %+v, want %+v", got, want)
	}
}

// TestTheRegistryDropsWhatTheEngineWouldRefuse. Normalising here means no
// caller re-validates, and DROPPING rather than clamping means nothing acts on
// a number nobody wrote -- the rule ResolveSeverity already follows for a
// weight that reached it illegal.
func TestTheRegistryDropsWhatTheEngineWouldRefuse(t *testing.T) {
	resetScoringForTest()
	RegisterScoringConfigProvider(&fakeScoringProvider{name: "fake", out: ScoringOverride{
		CategoryWeights: map[string]float64{
			"compliance": 3,      // legal, the top of the range
			"zeroed":     0,      // would delete every finding in it from the ranking
			"negative":   -1,     // nonsense
			"too-big":    3.0001, // past the ceiling
			"":           2,      // no category to weight
		},
		AmountBands: []gomodels.MagnitudeBand{
			{At: 2_000_000, Band: gomodels.SeverityBandHigh},
			{At: 0, Band: gomodels.SeverityBandMedium}, // every amount is >= 0
			{At: 500, Band: "catastrophic"},            // a band with no magnitude behind it
		},
		AmountCurrency: "₺",
	}})
	t.Cleanup(resetScoringForTest)

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantWeights := map[string]float64{"compliance": 3}
	if !reflect.DeepEqual(got.CategoryWeights, wantWeights) {
		t.Errorf("weights = %+v, want %+v", got.CategoryWeights, wantWeights)
	}
	wantBands := []gomodels.MagnitudeBand{{At: 2_000_000, Band: gomodels.SeverityBandHigh}}
	if !reflect.DeepEqual(got.AmountBands, wantBands) {
		t.Errorf("bands = %+v, want %+v", got.AmountBands, wantBands)
	}
}

// TestNothingLegalLeftIsIndistinguishableFromNothingConfigured. A deployment
// whose only weight was hand-edited to something illegal must not look
// configured while behaving unconfigured -- and a currency with no surviving
// threshold is a threshold set nobody can reach.
func TestNothingLegalLeftIsIndistinguishableFromNothingConfigured(t *testing.T) {
	resetScoringForTest()
	RegisterScoringConfigProvider(&fakeScoringProvider{name: "fake", out: ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 0},
		AmountBands:     []gomodels.MagnitudeBand{{At: -5, Band: gomodels.SeverityBandHigh}},
		AmountCurrency:  "₺",
	}})
	t.Cleanup(resetScoringForTest)

	got, err := ResolveScoringConfig(context.Background(), "p1", "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got, ScoringOverride{}) {
		t.Errorf("override = %+v, want the zero value", got)
	}
}

// TestALadderNobodyCanReadInOrderIsDropped.
//
// The engine takes the first rung an amount reaches, so `100,000 -> medium`
// before `1,000,000 -> high` bands a $2M amount MEDIUM and makes every rung
// after the first unreachable. The settings API refuses that, but this seam
// also carries what a hand-edited document or another provider supplies.
//
// Dropped whole, not sorted: reordering would apply a ladder nobody wrote. The
// weights survive, because they are a separate statement about the same
// project.
func TestALadderNobodyCanReadInOrderIsDropped(t *testing.T) {
	got := normaliseScoringOverride(ScoringOverride{
		CategoryWeights: map[string]float64{"compliance": 3},
		AmountBands: []gomodels.MagnitudeBand{
			{At: 100_000, Band: gomodels.SeverityBandMedium},
			{At: 1_000_000, Band: gomodels.SeverityBandHigh},
		},
		AmountCurrency: "$",
	})

	if got.AmountBands != nil {
		t.Errorf("bands = %+v, want them dropped", got.AmountBands)
	}
	if got.AmountCurrency != "" {
		t.Errorf("currency = %q, want it dropped with the bands it described", got.AmountCurrency)
	}
	if got.CategoryWeights["compliance"] != 3 {
		t.Errorf("weights = %+v, want them kept — a ladder and a weight are different statements", got.CategoryWeights)
	}

	// A descending ladder is kept, including one with a single rung.
	ok := normaliseScoringOverride(ScoringOverride{
		AmountBands: []gomodels.MagnitudeBand{
			{At: 1_000_000, Band: gomodels.SeverityBandHigh},
			{At: 100_000, Band: gomodels.SeverityBandMedium},
		},
		AmountCurrency: "$",
	})
	if len(ok.AmountBands) != 2 {
		t.Errorf("bands = %+v, want both kept", ok.AmountBands)
	}

	// Two rungs at the same amount are not an order either: the second can
	// never be reached.
	dup := normaliseScoringOverride(ScoringOverride{
		AmountBands: []gomodels.MagnitudeBand{
			{At: 1_000_000, Band: gomodels.SeverityBandHigh},
			{At: 1_000_000, Band: gomodels.SeverityBandMedium},
		},
		AmountCurrency: "$",
	})
	if dup.AmountBands != nil {
		t.Errorf("bands = %+v, want a duplicated rung dropped", dup.AmountBands)
	}
}
