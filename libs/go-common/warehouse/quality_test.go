package warehouse

import (
	"strings"
	"testing"
)

// TestQueryResultDegraded_DefaultsToSound pins the property every existing SQL
// provider depends on: a result built the way they build one — columns and rows,
// nothing else — is not degraded.
//
// The default matters more than it looks. Degraded() is the check a consumer
// uses to decide whether to trust a number, so a default of "degraded" would
// make every warehouse answer suspect, and a nil-safety bug here would panic on
// a path that only runs when a query failed to produce a result at all.
func TestQueryResultDegraded_DefaultsToSound(t *testing.T) {
	tests := []struct {
		name string
		r    *QueryResult
		want bool
	}{
		{
			name: "nil result",
			r:    nil,
			want: false,
		},
		{
			name: "zero value",
			r:    &QueryResult{},
			want: false,
		},
		{
			name: "rows but no caveat — how every SQL provider answers",
			r:    &QueryResult{Columns: []string{"n"}, Rows: []map[string]interface{}{{"n": 1}}},
			want: false,
		},
		{
			name: "explicitly empty caveat slice",
			r:    &QueryResult{Quality: []QualityCaveat{}},
			want: false,
		},
		{
			name: "one caveat",
			r:    &QueryResult{Quality: []QualityCaveat{{Kind: QualityWithheld}}},
			want: true,
		},
		{
			name: "caveat on an empty row set — withholding can empty a result",
			r:    &QueryResult{Rows: nil, Quality: []QualityCaveat{{Kind: QualityWithheld, Detail: "all rows below the reporting threshold"}}},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Degraded(); got != tt.want {
				t.Errorf("Degraded() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestQualityCaveatString covers the rendering that reaches logs and prompts.
// A caveat whose Detail was dropped still has to name its kind: an empty string
// would read as "no caveat" at exactly the moment there is one.
func TestQualityCaveatString(t *testing.T) {
	tests := []struct {
		name   string
		caveat QualityCaveat
		want   string
	}{
		{
			name:   "kind and detail",
			caveat: QualityCaveat{Kind: QualitySampled, Detail: "12,000 of 480,000 events"},
			want:   "sampled: 12,000 of 480,000 events",
		},
		{
			name:   "kind only",
			caveat: QualityCaveat{Kind: QualityTruncated},
			want:   "truncated",
		},
		{
			name:   "zero value still renders something",
			caveat: QualityCaveat{},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.caveat.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestQualityKindsAreDistinct guards against two constants collapsing onto the
// same wire value in a future edit. They are persisted and branched on, so a
// duplicate would silently merge two degradation classes — a consumer that
// refuses sampled results would start refusing truncated ones too, or worse,
// stop refusing either.
func TestQualityKindsAreDistinct(t *testing.T) {
	kinds := []QualityKind{QualityWithheld, QualitySampled, QualityTruncated, QualityRestricted}
	seen := make(map[QualityKind]bool, len(kinds))
	for _, k := range kinds {
		if k == "" {
			t.Error("a quality kind is empty; the zero value must not be a valid kind")
		}
		if seen[k] {
			t.Errorf("quality kind %q is declared twice", k)
		}
		seen[k] = true
	}
	if len(seen) != len(kinds) {
		t.Errorf("got %d distinct kinds, want %d", len(seen), len(kinds))
	}
}

// The instruction is what a model acts on, so it has to carry the source's own
// words: "withheld" alone is a category, "37 of 412 rows withheld" is a fact
// someone can decide against.
func TestCaveatInstruction_CarriesTheDetailAndTellsTheModelWhatToDo(t *testing.T) {
	got := CaveatInstruction([]QualityCaveat{
		{Kind: QualityWithheld, Detail: "37 of 412 rows withheld"},
		{Kind: QualitySampled},
	})
	for _, want := range []string{
		"not a faithful answer",
		"withheld: 37 of 412 rows withheld",
		"sampled",
		"Do not present a total, share or ranking from these rows as exact",
		"say so rather than answering from what is here",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("instruction is missing %q:\n%s", want, got)
		}
	}
}

// Nothing said means nothing added. A result with no caveat must render
// character-identically to how it did before caveats existed.
func TestCaveatInstruction_IsEmptyWhenTheSourceSaidNothing(t *testing.T) {
	if got := CaveatInstruction(nil); got != "" {
		t.Errorf("CaveatInstruction(nil) = %q, want empty", got)
	}
	if got := CaveatInstruction([]QualityCaveat{}); got != "" {
		t.Errorf("CaveatInstruction([]) = %q, want empty", got)
	}
}
