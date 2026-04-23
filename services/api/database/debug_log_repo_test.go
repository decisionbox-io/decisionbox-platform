package database

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	// The debug-logs endpoint caps LLM responses at 4KB to keep 2s polls
	// cheap. Responses from agents exploring Turkish / emoji-rich data
	// commonly contain multi-byte runes, and a naive byte slice can cut
	// in the middle of a rune — producing a string that `json.Marshal`
	// happily encodes but Next.js's `fetch().json()` rejects on the
	// client side. The helper must always return valid UTF-8.
	suffix := "…"

	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"under cap unchanged", "hello", 100, "hello"},
		{"at cap unchanged", "hello", 5, "hello"},
		{"simple ascii truncation", "helloworld", 5, "hello" + suffix},
		// "İ" in UTF-8 is 2 bytes (0xC4 0xB0). Cutting at 1 byte would
		// produce an invalid sequence; truncateUTF8 must retreat to the
		// previous rune boundary.
		// "İ" is 2 bytes (0xC4 0xB0). At max=2 the cut lands inside the
		// rune (byte 2 is a continuation byte), so truncateUTF8 must
		// retreat to byte 1 — yielding "a" + suffix.
		{"turkish İ mid-rune", "aİbc", 2, "a" + suffix},
		// At max=3 the cut lands cleanly after the İ rune (byte 3 is 'b'),
		// so no retreat is needed — "aİ" fits.
		{"turkish İ clean boundary", "aİbc", 3, "aİ" + suffix},
		// Rocket 🚀 is a 4-byte rune. Cutting at 2 bytes should retreat
		// to before the emoji entirely.
		{"emoji mid-rune", "x🚀y", 3, "x" + suffix},
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
		})
	}
}
