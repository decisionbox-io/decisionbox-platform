package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// mockProvider is a minimal Provider implementation for registry tests.
type mockProvider struct{}

func (m *mockProvider) Chat(_ context.Context, _ ChatRequest) (*ChatResponse, error) {
	return &ChatResponse{Content: "test"}, nil
}
func (m *mockProvider) Validate(_ context.Context) error { return nil }

func mockFactory(_ ProviderConfig) (Provider, error) { return &mockProvider{}, nil }

// Each test that registers a provider uses a sync.Once so the test file
// is safe to run with -count=N and -parallel.
var (
	onceMeta         sync.Once
	onceCatalog      sync.Once
	onceValidate     sync.Once
	onceResolveWire  sync.Once
	onceMaxTokens    sync.Once
	oncePricing      sync.Once
	onceJSONMarshal  sync.Once
	onceFamilyInfer  sync.Once
	onceSingleWire   sync.Once
)

// --- Wire ---

func TestWire_Valid(t *testing.T) {
	cases := []struct {
		w    Wire
		want bool
	}{
		{WireAnthropic, true},
		{WireOpenAICompat, true},
		{WireGoogleNative, true},
		{WireUnknown, false},
		{Wire("bogus"), false},
		{Wire(""), false},
	}
	for _, c := range cases {
		if c.w.Valid() != c.want {
			t.Errorf("Wire(%q).Valid() = %v, want %v", c.w, c.w.Valid(), c.want)
		}
	}
}

func TestParseWire(t *testing.T) {
	cases := []struct {
		in   string
		want Wire
	}{
		{"anthropic", WireAnthropic},
		{"ANTHROPIC", WireAnthropic},
		{"  Anthropic  ", WireAnthropic},
		{"openai-compat", WireOpenAICompat},
		{"openai_compat", WireOpenAICompat},
		{"openai compat", WireOpenAICompat},
		{"openai-compatible", WireOpenAICompat},
		{"openai", WireOpenAICompat},
		{"google-native", WireGoogleNative},
		{"google_native", WireGoogleNative},
		{"google", WireGoogleNative},
		{"gemini", WireGoogleNative},
		{"", WireUnknown},
		{"bogus", WireUnknown},
	}
	for _, c := range cases {
		if got := ParseWire(c.in); got != c.want {
			t.Errorf("ParseWire(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- Catalog lookup (FindModel, MaxOutputTokensFor, PricingFor, ResolveWire) ---

func registerCatalogProvider(t *testing.T) ProviderMeta {
	t.Helper()
	name := "test-catalog"
	onceCatalog.Do(func() {
		RegisterWithMeta(name, mockFactory, ProviderMeta{
			Name: "Catalog test",
			Models: []ModelEntry{
				{
					ID:              "claude-opus-4-7",
					Aliases:         []string{"opus-4-7", "us.anthropic.claude-opus-4-7-v1:0"},
					Wire:            WireAnthropic,
					MaxOutputTokens: 128000,
					Pricing:         TokenPricing{InputPerMillion: 5, OutputPerMillion: 25},
				},
				{
					ID:              "claude-sonnet-4-6",
					Aliases:         []string{"sonnet-4-6"},
					Wire:            WireAnthropic,
					MaxOutputTokens: 64000,
					Pricing:         TokenPricing{InputPerMillion: 3, OutputPerMillion: 15},
				},
				{
					ID:              "gpt-5",
					Wire:            WireOpenAICompat,
					MaxOutputTokens: 16384,
					Pricing:         TokenPricing{InputPerMillion: 5, OutputPerMillion: 15},
				},
			},
			DefaultMaxOutputTokens: 8192,
			SupportsTools:          true,
		})
	})
	meta, _ := GetProviderMeta(name)
	return meta
}

func TestProviderMeta_FindModel_CanonicalAndAlias(t *testing.T) {
	meta := registerCatalogProvider(t)
	cases := []struct {
		model string
		want  string // canonical ID
	}{
		{"claude-opus-4-7", "claude-opus-4-7"},
		{"opus-4-7", "claude-opus-4-7"},
		{"us.anthropic.claude-opus-4-7-v1:0", "claude-opus-4-7"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"sonnet-4-6", "claude-sonnet-4-6"},
		{"gpt-5", "gpt-5"},
	}
	for _, c := range cases {
		got, ok := meta.FindModel(c.model)
		if !ok {
			t.Errorf("FindModel(%q): not found", c.model)
			continue
		}
		if got.ID != c.want {
			t.Errorf("FindModel(%q).ID = %q, want %q", c.model, got.ID, c.want)
		}
	}
}

func TestProviderMeta_FindModel_Miss(t *testing.T) {
	meta := registerCatalogProvider(t)
	for _, miss := range []string{"", "unknown-model", "OPUS-4-7" /* case-sensitive */} {
		if _, ok := meta.FindModel(miss); ok {
			t.Errorf("FindModel(%q) should miss but matched", miss)
		}
	}
}

func TestProviderMeta_MaxOutputTokensFor_HitsDefault(t *testing.T) {
	meta := registerCatalogProvider(t)
	if got := meta.MaxOutputTokensFor("claude-opus-4-7"); got != 128000 {
		t.Errorf("opus-4-7 = %d, want 128000", got)
	}
	if got := meta.MaxOutputTokensFor("opus-4-7"); got != 128000 {
		t.Errorf("opus-4-7 alias = %d, want 128000", got)
	}
	// Unknown model → DefaultMaxOutputTokens.
	if got := meta.MaxOutputTokensFor("vendor.unknown"); got != 8192 {
		t.Errorf("unknown = %d, want default 8192", got)
	}
}

func TestProviderMeta_MaxOutputTokensFor_GlobalFallback(t *testing.T) {
	// Provider with no DefaultMaxOutputTokens set must fall back to
	// the package-level 8192 floor when a model misses the catalog.
	name := "test-no-default"
	RegisterWithMeta(name, mockFactory, ProviderMeta{
		Name: "no default",
		Models: []ModelEntry{
			{ID: "fixed", Wire: WireAnthropic, MaxOutputTokens: 1234},
		},
	})
	got := GetMaxOutputTokens(name, "missing")
	if got != 8192 {
		t.Errorf("global fallback = %d, want 8192", got)
	}
	got = GetMaxOutputTokens(name, "fixed")
	if got != 1234 {
		t.Errorf("hit = %d, want 1234", got)
	}
}

func TestGetMaxOutputTokens_UnknownProvider(t *testing.T) {
	if got := GetMaxOutputTokens("really-not-a-provider", "anything"); got != 8192 {
		t.Errorf("unknown provider = %d, want 8192", got)
	}
}

func TestProviderMeta_PricingFor(t *testing.T) {
	meta := registerCatalogProvider(t)
	p, ok := meta.PricingFor("claude-opus-4-7")
	if !ok || p.InputPerMillion != 5 || p.OutputPerMillion != 25 {
		t.Errorf("PricingFor(opus-4-7) = (%+v, %v)", p, ok)
	}
	// Alias resolves to the same pricing as canonical.
	p2, _ := meta.PricingFor("opus-4-7")
	if p2 != p {
		t.Errorf("alias pricing %+v != canonical %+v", p2, p)
	}
	// Miss returns zero + false.
	if p, ok := meta.PricingFor("missing"); ok || p.InputPerMillion != 0 {
		t.Errorf("PricingFor(missing) = (%+v, %v)", p, ok)
	}
}

func TestProviderMeta_ResolveWire_FromCatalog(t *testing.T) {
	meta := registerCatalogProvider(t)
	w, err := meta.ResolveWire("claude-opus-4-7", WireUnknown)
	if err != nil || w != WireAnthropic {
		t.Errorf("ResolveWire(claude-opus-4-7) = (%q, %v)", w, err)
	}
	w, err = meta.ResolveWire("us.anthropic.claude-opus-4-7-v1:0", WireUnknown)
	if err != nil || w != WireAnthropic {
		t.Errorf("ResolveWire(alias) = (%q, %v)", w, err)
	}
	w, err = meta.ResolveWire("gpt-5", WireUnknown)
	if err != nil || w != WireOpenAICompat {
		t.Errorf("ResolveWire(gpt-5) = (%q, %v)", w, err)
	}
}

func TestProviderMeta_ResolveWire_Override(t *testing.T) {
	meta := registerCatalogProvider(t)
	w, err := meta.ResolveWire("vendor.uncatalogued", WireOpenAICompat)
	if err != nil || w != WireOpenAICompat {
		t.Errorf("ResolveWire(uncatalogued, override) = (%q, %v)", w, err)
	}
	// Catalog hit beats override.
	w, err = meta.ResolveWire("claude-opus-4-7", WireOpenAICompat)
	if err != nil || w != WireAnthropic {
		t.Errorf("catalog should win over override, got (%q, %v)", w, err)
	}
}

func TestProviderMeta_ResolveWire_FamilyInferrer(t *testing.T) {
	name := "test-inferrer"
	onceFamilyInfer.Do(func() {
		RegisterWithMeta(name, mockFactory, ProviderMeta{
			Name: "with inferrer",
			Models: []ModelEntry{
				{ID: "fam-1", Wire: WireAnthropic, MaxOutputTokens: 1000},
			},
			FamilyInferrer: func(model string) Wire {
				if strings.HasPrefix(model, "fam-") {
					return WireAnthropic
				}
				return WireUnknown
			},
		})
	})
	meta, _ := GetProviderMeta(name)
	// fam-2 not in catalog but inferrer recognises the prefix.
	w, err := meta.ResolveWire("fam-2", WireUnknown)
	if err != nil || w != WireAnthropic {
		t.Errorf("ResolveWire via inferrer = (%q, %v)", w, err)
	}
	// No inferrer match + no override → actionable error.
	if _, err := meta.ResolveWire("vendor.unknown", WireUnknown); err == nil {
		t.Error("expected actionable error for unrecognised model with no override")
	} else {
		for _, want := range []string{name, "vendor.unknown", "wire_override"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q missing %q", err.Error(), want)
			}
		}
	}
}

func TestProviderMeta_CatalogModels_Sorted(t *testing.T) {
	meta := registerCatalogProvider(t)
	cm := meta.CatalogModels()
	if len(cm) != 3 {
		t.Fatalf("CatalogModels len = %d, want 3", len(cm))
	}
	// Sorted by ID — claude-opus-4-7 < claude-sonnet-4-6 < gpt-5.
	for i := 1; i < len(cm); i++ {
		if cm[i-1].ID >= cm[i].ID {
			t.Errorf("not sorted: %q before %q", cm[i-1].ID, cm[i].ID)
		}
	}
	// Aliases never leak into ModelInfo output.
	for _, m := range cm {
		if m.ID == "opus-4-7" || m.ID == "us.anthropic.claude-opus-4-7-v1:0" {
			t.Errorf("alias %q surfaced into combobox", m.ID)
		}
	}
}

// --- validateMeta panics ---

func TestRegisterWithMeta_PanicsOnEmptyEntryID(t *testing.T) {
	defer expectPanic(t, "empty ID")
	RegisterWithMeta("test-bad-empty-id", mockFactory, ProviderMeta{
		Models: []ModelEntry{{Wire: WireAnthropic, MaxOutputTokens: 1000}},
	})
}

func TestRegisterWithMeta_PanicsOnInvalidWire(t *testing.T) {
	defer expectPanic(t, "invalid wire")
	RegisterWithMeta("test-bad-wire", mockFactory, ProviderMeta{
		Models: []ModelEntry{{ID: "x", Wire: Wire("bogus"), MaxOutputTokens: 1000}},
	})
}

func TestRegisterWithMeta_PanicsOnZeroMaxTokens(t *testing.T) {
	defer expectPanic(t, "non-positive MaxOutputTokens")
	RegisterWithMeta("test-zero-max", mockFactory, ProviderMeta{
		Models: []ModelEntry{{ID: "x", Wire: WireAnthropic, MaxOutputTokens: 0}},
	})
}

func TestRegisterWithMeta_PanicsOnDuplicateID(t *testing.T) {
	defer expectPanic(t, "duplicate ID")
	RegisterWithMeta("test-dup-id", mockFactory, ProviderMeta{
		Models: []ModelEntry{
			{ID: "x", Wire: WireAnthropic, MaxOutputTokens: 100},
			{ID: "x", Wire: WireAnthropic, MaxOutputTokens: 200},
		},
	})
}

func TestRegisterWithMeta_PanicsOnAliasCollidingWithID(t *testing.T) {
	defer expectPanic(t, "alias")
	RegisterWithMeta("test-alias-id-collision", mockFactory, ProviderMeta{
		Models: []ModelEntry{
			{ID: "x", Wire: WireAnthropic, MaxOutputTokens: 100},
			{ID: "y", Aliases: []string{"x"}, Wire: WireAnthropic, MaxOutputTokens: 200},
		},
	})
}

func TestRegisterWithMeta_PanicsOnDuplicateAlias(t *testing.T) {
	defer expectPanic(t, "alias")
	RegisterWithMeta("test-dup-alias", mockFactory, ProviderMeta{
		Models: []ModelEntry{
			{ID: "x", Aliases: []string{"shared"}, Wire: WireAnthropic, MaxOutputTokens: 100},
			{ID: "y", Aliases: []string{"shared"}, Wire: WireAnthropic, MaxOutputTokens: 200},
		},
	})
}

func TestRegisterWithMeta_PanicsOnAliasEqualToID(t *testing.T) {
	defer expectPanic(t, "alias duplicates ID")
	RegisterWithMeta("test-self-alias", mockFactory, ProviderMeta{
		Models: []ModelEntry{
			{ID: "x", Aliases: []string{"x"}, Wire: WireAnthropic, MaxOutputTokens: 100},
		},
	})
}

func TestRegisterWithMeta_PanicsOnEmptyAlias(t *testing.T) {
	defer expectPanic(t, "empty alias")
	RegisterWithMeta("test-empty-alias", mockFactory, ProviderMeta{
		Models: []ModelEntry{
			{ID: "x", Aliases: []string{""}, Wire: WireAnthropic, MaxOutputTokens: 100},
		},
	})
}

func TestRegisterWithMeta_AcceptsWireUnknownForSingleWireProvider(t *testing.T) {
	// Single-wire providers (Ollama) leave entry.Wire blank because
	// they have no dispatch step. Validation must accept that.
	onceSingleWire.Do(func() {
		RegisterWithMeta("test-single-wire", mockFactory, ProviderMeta{
			Models: []ModelEntry{
				{ID: "no-wire", Wire: WireUnknown, MaxOutputTokens: 100},
			},
		})
	})
	meta, ok := GetProviderMeta("test-single-wire")
	if !ok {
		t.Fatal("not registered")
	}
	if got, ok := meta.FindModel("no-wire"); !ok || got.Wire != WireUnknown {
		t.Errorf("entry = %+v ok=%v", got, ok)
	}
}

// --- Register / NewProvider basics ---

func TestRegister_PanicOnDuplicate(t *testing.T) {
	name := "test-dup"
	Register(name, mockFactory)
	defer expectPanic(t, "Register called twice")
	Register(name, mockFactory)
}

func TestRegister_PanicOnNilFactory(t *testing.T) {
	defer expectPanic(t, "factory is nil")
	Register("test-nil-factory", nil)
}

func TestNewProvider_UnknownReturnsActionableError(t *testing.T) {
	_, err := NewProvider("nonexistent-provider", ProviderConfig{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error %q missing 'unknown provider'", err.Error())
	}
}

func TestRegisterWithMeta_AssignsID(t *testing.T) {
	name := "test-assigns-id"
	onceMeta.Do(func() {
		RegisterWithMeta(name, mockFactory, ProviderMeta{Name: "x"})
	})
	meta, _ := GetProviderMeta(name)
	if meta.ID != name {
		t.Errorf("ID = %q, want %q", meta.ID, name)
	}
}

func TestRegisteredProvidersMeta_Sorted(t *testing.T) {
	registerCatalogProvider(t)
	metas := RegisteredProvidersMeta()
	for i := 1; i < len(metas); i++ {
		if metas[i-1].ID >= metas[i].ID {
			t.Errorf("RegisteredProvidersMeta not sorted: %q before %q", metas[i-1].ID, metas[i].ID)
		}
	}
}

// --- JSON marshalling ---

func TestProviderMeta_MarshalJSON(t *testing.T) {
	name := "test-json-marshal"
	onceJSONMarshal.Do(func() {
		RegisterWithMeta(name, mockFactory, ProviderMeta{
			Name:        "JSON test",
			Description: "for marshal",
			Models: []ModelEntry{
				{
					ID:              "model-a",
					Aliases:         []string{"alias-a-1", "alias-a-2"},
					DisplayName:     "Model A",
					Wire:            WireAnthropic,
					MaxOutputTokens: 1234,
					Pricing:         TokenPricing{InputPerMillion: 1, OutputPerMillion: 2},
				},
			},
			DefaultMaxOutputTokens: 999,
			SupportsTools:          true,
		})
	})
	meta, _ := GetProviderMeta(name)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// Canonical ID surfaces; aliases are hidden.
	if !strings.Contains(got, `"model-a"`) {
		t.Errorf("missing canonical ID in %q", got)
	}
	if strings.Contains(got, "alias-a-1") || strings.Contains(got, "alias-a-2") {
		t.Errorf("aliases leaked into JSON: %q", got)
	}
	// DisplayName + max_output_tokens + pricing carried across.
	for _, want := range []string{`"display_name":"Model A"`, `"max_output_tokens":1234`, `"input_price_per_million":1`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in JSON %q", want, got)
		}
	}
	// supports_tools serialised.
	if !strings.Contains(got, `"supports_tools":true`) {
		t.Errorf("supports_tools missing in %q", got)
	}
}

var onceAuthMethodsMarshal sync.Once

func TestProviderMeta_AuthMethodsMarshal(t *testing.T) {
	name := "test-auth-methods-marshal"
	onceAuthMethodsMarshal.Do(func() {
		RegisterWithMeta(name, mockFactory, ProviderMeta{
			Name: "auth methods test",
			AuthMethods: []AuthMethod{
				{
					ID:          "iam_role",
					Name:        "IAM Role",
					Description: "uses pod role",
				},
				{
					ID:          "access_keys",
					Name:        "Access Keys",
					Description: "static keys",
					Fields: []ConfigField{
						{Key: "credentials_json", Label: "Access Keys", Required: true, Type: "credential"},
					},
				},
			},
		})
	})
	meta, _ := GetProviderMeta(name)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		`"auth_methods"`,
		`"id":"iam_role"`,
		`"id":"access_keys"`,
		`"name":"IAM Role"`,
		`"name":"Access Keys"`,
		`"credentials_json"`,
		`"type":"credential"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in JSON %q", want, got)
		}
	}
}

func TestProviderMeta_AuthMethodsOmittedWhenEmpty(t *testing.T) {
	// Providers without AuthMethods (Ollama-style) should not emit an
	// auth_methods key — the dashboard renders nothing in that case.
	meta := ProviderMeta{
		ID:   "no-auth-provider",
		Name: "no auth",
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "auth_methods") {
		t.Errorf("auth_methods key leaked into JSON: %q", string(raw))
	}
}

// --- helpers ---

func expectPanic(t *testing.T, wantSubstring string) {
	t.Helper()
	r := recover()
	if r == nil {
		t.Fatal("expected panic, got none")
	}
	msg, ok := r.(string)
	if !ok {
		t.Fatalf("panic value not string: %v (%T)", r, r)
	}
	if !strings.Contains(msg, wantSubstring) {
		t.Fatalf("panic %q missing %q", msg, wantSubstring)
	}
}

// Compile-time check: ensure mockProvider implements Provider.
var _ Provider = (*mockProvider)(nil)

// Sanity: NewProvider returns the same factory's output.
func TestNewProvider_ReturnsFactoryOutput(t *testing.T) {
	name := "test-factory-roundtrip"
	onceValidate.Do(func() {
		Register(name, func(cfg ProviderConfig) (Provider, error) {
			if cfg["api_key"] != "abc" {
				return nil, fmt.Errorf("bad key %q", cfg["api_key"])
			}
			return &mockProvider{}, nil
		})
	})
	if _, err := NewProvider(name, ProviderConfig{"api_key": "abc"}); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if _, err := NewProvider(name, ProviderConfig{"api_key": "wrong"}); err == nil {
		t.Fatal("expected factory error to surface")
	}
}

// Reference unused once* variables to silence the linter when individual
// tests are skipped — keeps the sync.Once allocations meaningful on
// re-runs without forcing a global re-registration.
var _ = []*sync.Once{
	&onceMeta, &onceCatalog, &onceValidate, &onceResolveWire,
	&onceMaxTokens, &oncePricing, &onceJSONMarshal, &onceFamilyInfer, &onceSingleWire,
}
