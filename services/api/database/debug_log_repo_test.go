package database

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	// The debug-logs endpoint caps LLM responses at ~4KB to keep 2s polls
	// cheap. Responses from agents exploring Turkish / emoji-rich data
	// commonly contain multi-byte runes, and a naive byte slice can cut
	// in the middle of a rune — producing a string that `json.Marshal`
	// happily encodes but Next.js's `fetch().json()` rejects on the
	// client side. The helper must:
	//   1. Never return invalid UTF-8 (no mid-rune cuts).
	//   2. Never exceed `max` bytes total, including the suffix
	//      (unless `max` is smaller than the suffix itself, which no
	//      production caller will ever do — `maxLLMResponseBytes` is 4096).
	suffix := "…" // 3 bytes in UTF-8

	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under cap unchanged", "hello", 100, "hello"},
		{"at cap unchanged", "hello", 5, "hello"},
		// ASCII truncation: 10 bytes, max=8 leaves a 5-byte budget for
		// content (8 − len("…")=3) → "hello" + "…" = 8 bytes total.
		{"simple ascii truncation with budget", "helloworld", 8, "hello" + suffix},
		// "aİbcdef" is 7 bytes: a(1) İ(2) b(1) c(1) d(1) e(1). max=5 →
		// budget=2. Cut at byte 2 is a continuation byte of İ, so
		// truncateUTF8 retreats to byte 1 → "a" + suffix = 4 bytes.
		{"turkish İ mid-rune retreats", "aİbcdef", 5, "a" + suffix},
		// Same string with max=6 → budget=3. Cut at byte 3 is 'b', a
		// clean rune-start; "aİ" fits and no retreat is needed.
		{"turkish İ clean boundary", "aİbcdef", 6, "aİ" + suffix},
		// "x🚀y" is 6 bytes: x(1) 🚀(4) y(1). max=4 → budget=1. Cut at
		// byte 1 is the 0xF0 lead byte of 🚀, which IS a rune-start;
		// but we want to keep "x" only (the rest of 🚀 would follow
		// after byte 1). Since cut=1 corresponds to the byte AFTER 'x',
		// s[:1] = "x" → "x" + suffix.
		{"emoji stays before rune", "x🚀y", 4, "x" + suffix},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateUTF8(tc.in, tc.max, suffix)
			if got != tc.want {
				t.Errorf("truncateUTF8(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateUTF8 returned invalid UTF-8: %q (bytes %x)", got, []byte(got))
			}
			// Guarantee the documented cap: once triggered, the returned
			// string never exceeds `max` bytes.
			if len(tc.in) > tc.max && len(got) > tc.max {
				t.Errorf("truncateUTF8 returned %d bytes, exceeds cap %d", len(got), tc.max)
			}
		})
	}
}
