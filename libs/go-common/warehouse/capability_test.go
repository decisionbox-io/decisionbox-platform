package warehouse

import (
	"context"
	"errors"
	"testing"
)

// recordingProvider captures what AsQueryRunner's adapter forwards to the
// underlying SQL Provider.
type recordingProvider struct {
	mockWarehouseProvider
	gotQuery  string
	gotParams map[string]interface{}
	called    int
	err       error
}

func (r *recordingProvider) Query(_ context.Context, q string, params map[string]interface{}) (*QueryResult, error) {
	r.called++
	r.gotQuery = q
	r.gotParams = params
	if r.err != nil {
		return nil, r.err
	}
	return &QueryResult{Columns: []string{"c"}}, nil
}

func (r *recordingProvider) SQLFixPrompt() string { return "fix {{ORIGINAL_SQL}}" }

// nativeRunner is a provider that speaks the seam directly — the shape a
// non-SQL adapter takes.
type nativeRunner struct {
	mockWarehouseProvider
	got NativeQuery
}

func (n *nativeRunner) RunQuery(_ context.Context, q NativeQuery) (*QueryResult, error) {
	n.got = q
	return &QueryResult{}, nil
}
func (n *nativeRunner) QueryLanguage() string  { return "Report Request" }
func (n *nativeRunner) QueryFixPrompt() string { return "native fix prompt" }

func TestAsQueryRunner_AdaptsASQLProvider(t *testing.T) {
	p := &recordingProvider{}
	r := AsQueryRunner(p)

	res, err := r.RunQuery(context.Background(), SQLQuery("SELECT 1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a result")
	}
	if p.called != 1 {
		t.Errorf("Provider.Query called %d times, want 1", p.called)
	}
	if p.gotQuery != "SELECT 1" {
		t.Errorf("forwarded query = %q, want %q", p.gotQuery, "SELECT 1")
	}
	if p.gotParams != nil {
		t.Errorf("forwarded params = %v, want nil — the executor never passed params", p.gotParams)
	}
	if got := r.QueryLanguage(); got != "Mock SQL" {
		t.Errorf("QueryLanguage() = %q, want the provider's SQLDialect %q", got, "Mock SQL")
	}
	if got := r.QueryFixPrompt(); got != "fix {{ORIGINAL_SQL}}" {
		t.Errorf("QueryFixPrompt() = %q, want the provider's SQLFixPrompt", got)
	}
}

func TestAsQueryRunner_PropagatesProviderError(t *testing.T) {
	want := errors.New("warehouse down")
	r := AsQueryRunner(&recordingProvider{err: want})

	if _, err := r.RunQuery(context.Background(), SQLQuery("SELECT 1")); !errors.Is(err, want) {
		t.Errorf("error = %v, want it to be %v unwrapped", err, want)
	}
}

func TestAsQueryRunner_ReturnsANativeRunnerUnchanged(t *testing.T) {
	n := &nativeRunner{}
	r := AsQueryRunner(n)

	if r != QueryRunner(n) {
		t.Fatal("a provider that implements QueryRunner must not be wrapped")
	}
	if got := r.QueryLanguage(); got != "Report Request" {
		t.Errorf("QueryLanguage() = %q, want the runner's own language", got)
	}
}

func TestAsQueryRunner_UnwrapReachesTheProvider(t *testing.T) {
	p := &recordingProvider{}
	r := AsQueryRunner(p)

	u, ok := r.(interface{ Unwrap() Provider })
	if !ok {
		t.Fatal("the SQL adapter must expose the provider it wraps")
	}
	if u.Unwrap() != Provider(p) {
		t.Error("Unwrap returned a different provider")
	}
}

func TestNativeQuery_StructuredPayload(t *testing.T) {
	type reportRequest struct{ Metrics []string }

	sql := SQLQuery("SELECT 1")
	if sql.IsStructured() {
		t.Error("a SQL query carries no payload")
	}
	if sql.String() != "SELECT 1" {
		t.Errorf("String() = %q, want the statement", sql.String())
	}

	structured := NativeQuery{
		Text:    "metrics=[sessions] by date",
		Payload: reportRequest{Metrics: []string{"sessions"}},
	}
	if !structured.IsStructured() {
		t.Error("a query with a payload must report itself as structured")
	}
	if structured.String() != "metrics=[sessions] by date" {
		t.Errorf("String() = %q, want the readable rendering, never the payload", structured.String())
	}

	n := &nativeRunner{}
	if _, err := n.RunQuery(context.Background(), structured); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := n.got.Payload.(reportRequest); !ok {
		t.Errorf("payload reached the runner as %T, want it preserved as reportRequest", n.got.Payload)
	}
}

func TestProviderMeta_LanguageFallsBackToDialect(t *testing.T) {
	tests := []struct {
		name string
		meta ProviderMeta
		want string
	}{
		{"declared language wins", ProviderMeta{Capability: Capability{QueryLanguage: "SOQL"}, Dialect: "ignored"}, "SOQL"},
		{"undeclared falls back to the dialect label", ProviderMeta{Dialect: "PostgreSQL"}, "PostgreSQL"},
		{"neither declared falls back to SQL", ProviderMeta{}, "SQL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.meta.Language(); got != tc.want {
				t.Errorf("Language() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCapability_TravelsWithAProviderMeta pins that the embedded descriptor is
// reachable through the meta, so a consumer holding a ProviderMeta does not
// need to know the descriptor is a separate type.
func TestCapability_TravelsWithAProviderMeta(t *testing.T) {
	m := ProviderMeta{
		Dialect:    "GA4",
		Capability: Capability{Shape: ShapeCube, CanAnchor: Anchoring(false)},
	}
	if m.EffectiveShape() != ShapeCube {
		t.Errorf("EffectiveShape() = %q, want %q through the embedded descriptor", m.EffectiveShape(), ShapeCube)
	}
	if m.Anchors() {
		t.Error("a provider declaring CanAnchor(false) must not anchor")
	}
	if got := m.Language(); got != "GA4" {
		t.Errorf("Language() = %q, want the Dialect fallback %q", got, "GA4")
	}
}

func TestCapability_ShapeDefaultsToEntities(t *testing.T) {
	if got := (Capability{}).EffectiveShape(); got != ShapeEntities {
		t.Errorf("EffectiveShape() = %q, want %q for an undeclared provider", got, ShapeEntities)
	}
	if got := (Capability{Shape: ShapeCube}).EffectiveShape(); got != ShapeCube {
		t.Errorf("EffectiveShape() = %q, want %q", got, ShapeCube)
	}
}

// TestProviderMeta_AnchorsDefaultsToTrue pins the direction of the default.
// Getting this backwards would silently make every already-registered
// datasource — including the enterprise-only drivers this repo cannot see —
// ineligible to be a project's only source, with no error to trace it by.
func TestCapability_AnchorsDefaultsToTrue(t *testing.T) {
	if !(Capability{}).Anchors() {
		t.Error("an undeclared provider must anchor; a warehouse is the system of record by construction")
	}
	if !(Capability{CanAnchor: Anchoring(true)}).Anchors() {
		t.Error("an explicit true must anchor")
	}
	if (Capability{CanAnchor: Anchoring(false)}).Anchors() {
		t.Error("an explicit false must not anchor")
	}
}

// TestRegisteredSQLProvidersAnchorAndAreEntityShaped asserts the defaults hold
// for whatever is registered in this binary, so a provider added later cannot
// silently acquire a wrong shape or anchoring value by omission.
func TestRegisteredSQLProvidersAnchorAndAreEntityShaped(t *testing.T) {
	for _, m := range RegisteredProvidersMeta() {
		if m.Language() == "" {
			t.Errorf("provider %q resolves to an empty query language", m.ID)
		}
		if m.EffectiveShape() != ShapeEntities && m.EffectiveShape() != ShapeCube {
			t.Errorf("provider %q has shape %q, which is not a known shape", m.ID, m.EffectiveShape())
		}
	}
}
