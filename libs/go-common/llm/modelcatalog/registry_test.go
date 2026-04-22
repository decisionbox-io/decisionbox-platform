package modelcatalog

import (
	"strings"
	"sync"
	"testing"
)

func TestWire_Valid(t *testing.T) {
	tests := []struct {
		w    Wire
		want bool
	}{
		{Anthropic, true},
		{OpenAICompat, true},
		{GoogleNative, true},
		{Unknown, false},
		{Wire("bogus"), false},
		{Wire(""), false},
	}
	for _, tt := range tests {
		if got := tt.w.Valid(); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.w, got, tt.want)
		}
	}
}

func TestParseWire(t *testing.T) {
	tests := []struct {
		in   string
		want Wire
	}{
		{"anthropic", Anthropic},
		{"ANTHROPIC", Anthropic},
		{" Anthropic ", Anthropic},
		{"openai-compat", OpenAICompat},
		{"openai_compat", OpenAICompat},
		{"openai compat", OpenAICompat},
		{"openai-compatible", OpenAICompat},
		{"openai", OpenAICompat},
		{"google-native", GoogleNative},
		{"google_native", GoogleNative},
		{"google", GoogleNative},
		{"gemini", GoogleNative},
		{"", Unknown},
		{"bogus", Unknown},
	}
	for _, tt := range tests {
		if got := ParseWire(tt.in); got != tt.want {
			t.Errorf("ParseWire(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEntry_Key(t *testing.T) {
	e := Entry{Cloud: "bedrock", ID: "anthropic.claude-sonnet-4-20250514-v1:0"}
	if got := e.Key(); got != "bedrock/anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Errorf("Key() = %q", got)
	}
}

func TestRegister_Panics(t *testing.T) {
	cases := []struct {
		name string
		e    Entry
		want string
	}{
		{"empty Cloud", Entry{ID: "x", Wire: Anthropic}, "empty Cloud"},
		{"empty ID", Entry{Cloud: "x", Wire: Anthropic}, "empty ID"},
		{"invalid wire", Entry{Cloud: "x", ID: "y", Wire: Wire("bogus")}, "invalid wire"},
		{"unknown wire (empty)", Entry{Cloud: "x", ID: "y", Wire: Unknown}, "invalid wire"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic")
				}
				msg, ok := r.(string)
				if !ok {
					t.Fatalf("panic value is not a string: %v", r)
				}
				if !strings.Contains(msg, tc.want) {
					t.Errorf("panic = %q, should contain %q", msg, tc.want)
				}
			}()
			Register(tc.e)
		})
	}
}

func TestRegister_DefaultsDisplayName(t *testing.T) {
	cloud := "test-display-name"
	Register(Entry{Cloud: cloud, ID: "mymodel-v1", Wire: Anthropic})
	e, ok := Lookup(cloud, "mymodel-v1")
	if !ok {
		t.Fatal("entry not found")
	}
	if e.DisplayName != "mymodel-v1" {
		t.Errorf("DisplayName = %q, want %q (default to ID)", e.DisplayName, "mymodel-v1")
	}
}

func TestRegister_ReplacesExisting(t *testing.T) {
	cloud := "test-replace"
	Register(Entry{Cloud: cloud, ID: "m", Wire: Anthropic, DisplayName: "v1", MaxOutputTokens: 1000})
	Register(Entry{Cloud: cloud, ID: "m", Wire: OpenAICompat, DisplayName: "v2", MaxOutputTokens: 2000})
	e, ok := Lookup(cloud, "m")
	if !ok {
		t.Fatal("not found")
	}
	if e.Wire != OpenAICompat {
		t.Errorf("wire = %q, want re-registered value", e.Wire)
	}
	if e.MaxOutputTokens != 2000 {
		t.Errorf("max tokens = %d, want re-registered value", e.MaxOutputTokens)
	}
	if e.DisplayName != "v2" {
		t.Errorf("display name = %q", e.DisplayName)
	}
}

func TestLookup_Miss(t *testing.T) {
	_, ok := Lookup("nonexistent", "nothing")
	if ok {
		t.Error("expected miss for unregistered entry")
	}
}

func TestLookupWire_MissReturnsUnknown(t *testing.T) {
	if w := LookupWire("bedrock", "totally-made-up-model"); w != Unknown {
		t.Errorf("got %q, want Unknown", w)
	}
}

func TestLookupWire_Hit(t *testing.T) {
	Register(Entry{Cloud: "test-hit", ID: "m-1", Wire: Anthropic})
	if w := LookupWire("test-hit", "m-1"); w != Anthropic {
		t.Errorf("got %q, want Anthropic", w)
	}
}

func TestListByCloud_SortedAndFiltered(t *testing.T) {
	cloud := "test-list-cloud"
	Register(Entry{Cloud: cloud, ID: "zzz", Wire: Anthropic})
	Register(Entry{Cloud: cloud, ID: "aaa", Wire: Anthropic})
	Register(Entry{Cloud: cloud, ID: "mmm", Wire: OpenAICompat})
	Register(Entry{Cloud: "other-list-cloud", ID: "x", Wire: Anthropic})

	list := ListByCloud(cloud)
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	want := []string{"aaa", "mmm", "zzz"}
	for i, e := range list {
		if e.ID != want[i] {
			t.Errorf("list[%d].ID = %q, want %q", i, e.ID, want[i])
		}
		if e.Cloud != cloud {
			t.Errorf("list[%d].Cloud = %q, want filtered to %q", i, e.Cloud, cloud)
		}
	}
}

func TestListByCloud_Empty(t *testing.T) {
	if list := ListByCloud("never-registered"); len(list) != 0 {
		t.Errorf("len = %d, want 0", len(list))
	}
}

func TestClouds_UniqueAndSorted(t *testing.T) {
	Register(Entry{Cloud: "zebra", ID: "m", Wire: Anthropic})
	Register(Entry{Cloud: "zebra", ID: "m2", Wire: Anthropic})
	Register(Entry{Cloud: "alpha", ID: "m", Wire: Anthropic})

	clouds := Clouds()
	// Seed clouds will also be present; just assert ours and order.
	seenAlpha, seenZebra := false, false
	prev := ""
	for _, c := range clouds {
		if prev != "" && c < prev {
			t.Errorf("not sorted: %q before %q", prev, c)
		}
		prev = c
		if c == "alpha" {
			seenAlpha = true
		}
		if c == "zebra" {
			seenZebra = true
		}
	}
	if !seenAlpha || !seenZebra {
		t.Errorf("expected both test clouds, got %v", clouds)
	}
}

func TestAll_SortedByCloudThenID(t *testing.T) {
	all := All()
	for i := 1; i < len(all); i++ {
		a, b := all[i-1], all[i]
		if a.Cloud > b.Cloud {
			t.Errorf("not sorted by cloud at %d: %q > %q", i, a.Cloud, b.Cloud)
		}
		if a.Cloud == b.Cloud && a.ID > b.ID {
			t.Errorf("not sorted by id within %q at %d: %q > %q", a.Cloud, i, a.ID, b.ID)
		}
	}
}

func TestResolveWire_HitNoOverride(t *testing.T) {
	Register(Entry{Cloud: "test-resolve-hit", ID: "m", Wire: Anthropic})
	w, err := ResolveWire("test-resolve-hit", "m", Unknown)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != Anthropic {
		t.Errorf("w = %q, want Anthropic", w)
	}
}

func TestResolveWire_HitIgnoresOverride(t *testing.T) {
	// Catalog hit should win over the override (catalog is authoritative).
	Register(Entry{Cloud: "test-hit-over-override", ID: "m", Wire: Anthropic})
	w, err := ResolveWire("test-hit-over-override", "m", OpenAICompat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != Anthropic {
		t.Errorf("catalog hit did not win over override: got %q", w)
	}
}

func TestResolveWire_MissWithOverride(t *testing.T) {
	w, err := ResolveWire("bedrock", "some.newer-model-2099", OpenAICompat)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w != OpenAICompat {
		t.Errorf("w = %q, want OpenAICompat", w)
	}
}

func TestResolveWire_MissNoOverrideReturnsActionableError(t *testing.T) {
	_, err := ResolveWire("bedrock", "does-not-exist", Unknown)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bedrock") {
		t.Errorf("error %q should name the cloud", msg)
	}
	if !strings.Contains(msg, "does-not-exist") {
		t.Errorf("error %q should name the model", msg)
	}
	if !strings.Contains(msg, "wire_override") {
		t.Errorf("error %q should mention wire_override", msg)
	}
	for _, w := range []Wire{Anthropic, OpenAICompat, GoogleNative} {
		if !strings.Contains(msg, string(w)) {
			t.Errorf("error should list wire %q", w)
		}
	}
}

func TestRegister_ThreadSafe(t *testing.T) {
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "concurrent-" + strings.Repeat("x", i%5+1)
			Register(Entry{Cloud: "test-concurrent", ID: id, Wire: Anthropic})
		}(i)
	}
	wg.Wait()
	list := ListByCloud("test-concurrent")
	if len(list) == 0 {
		t.Error("no entries registered")
	}
}

// --- Seed catalog content tests ---
// These protect the default seed list shipped in catalog.go from silent
// regressions — if a model is removed or its wire changes, these tests
// fail loudly (the dashboard and agent both rely on specific entries).

func TestSeed_BedrockClaudeOpus46Anthropic(t *testing.T) {
	e, ok := Lookup("bedrock", "global.anthropic.claude-opus-4-6-v1")
	if !ok {
		t.Fatal("claude-opus-4-6 global not seeded on bedrock")
	}
	if e.Wire != Anthropic {
		t.Errorf("wire = %q, want Anthropic", e.Wire)
	}
	if e.MaxOutputTokens != 128000 {
		t.Errorf("max_output_tokens = %d, want 128000", e.MaxOutputTokens)
	}
}

func TestSeed_BedrockQwenOpenAICompat(t *testing.T) {
	e, ok := Lookup("bedrock", "qwen.qwen3-next-80b-a3b")
	if !ok {
		t.Fatal("qwen.qwen3-next-80b-a3b not seeded on bedrock")
	}
	if e.Wire != OpenAICompat {
		t.Errorf("wire = %q, want OpenAICompat", e.Wire)
	}
}

func TestSeed_BedrockDeepSeek(t *testing.T) {
	e, ok := Lookup("bedrock", "deepseek.r1-v1:0")
	if !ok {
		t.Fatal("deepseek.r1-v1:0 not seeded on bedrock")
	}
	if e.Wire != OpenAICompat {
		t.Errorf("wire = %q, want OpenAICompat", e.Wire)
	}
}

func TestSeed_VertexGeminiGoogleNative(t *testing.T) {
	e, ok := Lookup("vertex-ai", "gemini-2.0-flash")
	if !ok {
		t.Fatal("gemini-2.0-flash not seeded on vertex-ai")
	}
	if e.Wire != GoogleNative {
		t.Errorf("wire = %q, want GoogleNative", e.Wire)
	}
}

func TestSeed_VertexClaudeAnthropic(t *testing.T) {
	e, ok := Lookup("vertex-ai", "claude-opus-4@20250514")
	if !ok {
		t.Fatal("claude-opus-4@20250514 not seeded on vertex-ai")
	}
	if e.Wire != Anthropic {
		t.Errorf("wire = %q, want Anthropic", e.Wire)
	}
}

func TestSeed_VertexLlamaOpenAICompat(t *testing.T) {
	e, ok := Lookup("vertex-ai", "meta/llama-3.3-70b-instruct-maas")
	if !ok {
		t.Fatal("meta/llama-3.3-70b-instruct-maas not seeded on vertex-ai")
	}
	if e.Wire != OpenAICompat {
		t.Errorf("wire = %q, want OpenAICompat", e.Wire)
	}
}

func TestSeed_AzureGpt5OpenAICompat(t *testing.T) {
	e, ok := Lookup("azure-foundry", "gpt-5")
	if !ok {
		t.Fatal("gpt-5 not seeded on azure-foundry")
	}
	if e.Wire != OpenAICompat {
		t.Errorf("wire = %q, want OpenAICompat", e.Wire)
	}
}

func TestSeed_AzureClaudeAnthropic(t *testing.T) {
	e, ok := Lookup("azure-foundry", "claude-opus-4-6")
	if !ok {
		t.Fatal("claude-opus-4-6 not seeded on azure-foundry")
	}
	if e.Wire != Anthropic {
		t.Errorf("wire = %q, want Anthropic", e.Wire)
	}
}

func TestSeed_OpenAIDirect(t *testing.T) {
	e, ok := Lookup("openai", "gpt-4o")
	if !ok {
		t.Fatal("gpt-4o not seeded on openai")
	}
	if e.Wire != OpenAICompat {
		t.Errorf("wire = %q, want OpenAICompat", e.Wire)
	}
}

func TestSeed_ClaudeDirect(t *testing.T) {
	e, ok := Lookup("claude", "claude-opus-4-6")
	if !ok {
		t.Fatal("claude-opus-4-6 not seeded on claude")
	}
	if e.Wire != Anthropic {
		t.Errorf("wire = %q, want Anthropic", e.Wire)
	}
}

// seedClouds is the set of clouds covered by the shipped init() seed;
// used to filter test scope away from pollution caused by other tests in
// this file that Register() into ad-hoc cloud names.
var seedClouds = map[string]bool{
	"bedrock":       true,
	"vertex-ai":     true,
	"azure-foundry": true,
	"openai":        true,
	"claude":        true,
}

func TestSeed_AllClaudeModelsOnAllCloudsMatchAnthropicWire(t *testing.T) {
	// Guard against a future editor adding a Claude entry with the wrong wire.
	for _, e := range All() {
		if !seedClouds[e.Cloud] {
			continue
		}
		if strings.Contains(e.ID, "claude") && e.Wire != Anthropic {
			t.Errorf("%s on %s has wire %q, every Claude model must use Anthropic wire",
				e.ID, e.Cloud, e.Wire)
		}
	}
}

func TestSeed_AllGeminiOnVertexUsesGoogleNative(t *testing.T) {
	for _, e := range All() {
		if e.Cloud == "vertex-ai" && strings.HasPrefix(e.ID, "gemini-") && e.Wire != GoogleNative {
			t.Errorf("%s on vertex has wire %q, want GoogleNative", e.ID, e.Wire)
		}
	}
}

func TestSeed_NoDeprecatedModels(t *testing.T) {
	// Deprecated families should stay out — users can add via wire_override.
	bannedSubstrings := []string{
		"claude-3-haiku",
		"claude-3-opus",
		"claude-3-sonnet",
		"claude-3-5-sonnet",
		"gpt-3.5",
		"gpt-4-0314",
	}
	for _, e := range All() {
		if !seedClouds[e.Cloud] {
			continue
		}
		for _, banned := range bannedSubstrings {
			if strings.Contains(e.ID, banned) {
				t.Errorf("deprecated model %q leaked into seed catalog on %s", e.ID, e.Cloud)
			}
		}
	}
}

func TestSeed_CloudsCovered(t *testing.T) {
	want := []string{"azure-foundry", "bedrock", "claude", "openai", "vertex-ai"}
	clouds := Clouds()
	have := make(map[string]struct{}, len(clouds))
	for _, c := range clouds {
		have[c] = struct{}{}
	}
	for _, c := range want {
		if _, ok := have[c]; !ok {
			t.Errorf("seed does not cover cloud %q", c)
		}
	}
}

func TestSeed_EntriesHaveDisplayNames(t *testing.T) {
	for _, e := range All() {
		if !seedClouds[e.Cloud] {
			continue
		}
		if e.DisplayName == "" {
			t.Errorf("entry %s/%s has empty display name", e.Cloud, e.ID)
		}
	}
}

func TestSeed_EntriesHaveValidWire(t *testing.T) {
	for _, e := range All() {
		if !seedClouds[e.Cloud] {
			continue
		}
		if !e.Wire.Valid() {
			t.Errorf("entry %s/%s has invalid wire %q", e.Cloud, e.ID, e.Wire)
		}
	}
}

func TestSeed_EntriesHavePositiveMaxTokens(t *testing.T) {
	for _, e := range All() {
		if !seedClouds[e.Cloud] {
			continue
		}
		if e.MaxOutputTokens <= 0 {
			t.Errorf("entry %s/%s has non-positive max_output_tokens = %d",
				e.Cloud, e.ID, e.MaxOutputTokens)
		}
	}
}
