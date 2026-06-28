package mdtext

import (
	"strings"
	"testing"
)

// markerChars are Markdown syntax characters that must never survive in the
// plain reduction of formatted input.
const markerChars = "*#`~"

func TestToPlainText_PlainPassthrough(t *testing.T) {
	in := "Players are leaving at Level 45 due to a steep difficulty curve."
	if got := ToPlainText(in); got != in {
		t.Errorf("plain text should pass through unchanged.\n got: %q\nwant: %q", got, in)
	}
}

func TestToPlainText_Empty(t *testing.T) {
	if got := ToPlainText(""); got != "" {
		t.Errorf("empty input should yield empty output, got %q", got)
	}
	if got := ToPlainText("   \n\n  "); got != "" {
		t.Errorf("whitespace-only input should yield empty output, got %q", got)
	}
}

func TestToPlainText_Emphasis(t *testing.T) {
	cases := map[string]string{
		"**67%** of users churned":     "67% of users churned",
		"__bold__ and _italic_":        "bold and italic",
		"*italic* word":                "italic word",
		"***both*** at once":           "both at once",
		"a `code` token":               "a code token",
		"~~struck~~ through":           "struck through",
		"mix **bold** and *italic* ok": "mix bold and italic ok",
	}
	for in, want := range cases {
		if got := ToPlainText(in); got != want {
			t.Errorf("ToPlainText(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestToPlainText_CodeSpanContentsPreserved guards that emphasis/strikethrough
// rules do not reach inside an inline-code span — identifiers and operators
// that happen to use Markdown delimiters survive verbatim, so the plain
// `description` stays a faithful reduction of `description_md`.
func TestToPlainText_CodeSpanContentsPreserved(t *testing.T) {
	cases := map[string]string{
		"use `__typename__` here":  "use __typename__ here",
		"compute `a * b * c` now":  "compute a * b * c now",
		"the `user_id` column":     "the user_id column",
		"two `*x*` and `~y~` spans": "two *x* and ~y~ spans",
	}
	for in, want := range cases {
		if got := ToPlainText(in); got != want {
			t.Errorf("ToPlainText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToPlainText_Headings(t *testing.T) {
	in := "# Takeaway\n## What's happening\n### Why it matters\n#### Who's affected"
	got := ToPlainText(in)
	want := "Takeaway\nWhat's happening\nWhy it matters\nWho's affected"
	if got != want {
		t.Errorf("headings:\n got: %q\nwant: %q", got, want)
	}
	if strings.ContainsAny(got, "#") {
		t.Errorf("heading hashes leaked: %q", got)
	}
}

func TestToPlainText_Lists(t *testing.T) {
	in := "- first driver\n- second driver\n* third\n+ fourth\n1. step one\n2. step two"
	got := ToPlainText(in)
	want := "first driver\nsecond driver\nthird\nfourth\nstep one\nstep two"
	if got != want {
		t.Errorf("lists:\n got: %q\nwant: %q", got, want)
	}
}

func TestToPlainText_Table(t *testing.T) {
	in := strings.Join([]string{
		"| Segment | Rate |",
		"|---------|------|",
		"| iOS     | 28%  |",
		"| Android | 19%  |",
	}, "\n")
	got := ToPlainText(in)
	want := "Segment | Rate\niOS | 28%\nAndroid | 19%"
	if got != want {
		t.Errorf("table:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "---") {
		t.Errorf("table separator row leaked: %q", got)
	}
}

func TestToPlainText_LinksAndImages(t *testing.T) {
	cases := map[string]string{
		"see [the dashboard](https://example.com) for detail": "see the dashboard for detail",
		"![alt text](https://example.com/x.png) caption":      "alt text caption",
		"[bare label](javascript:alert(1)) stays text":        "bare label stays text",
	}
	for in, want := range cases {
		if got := ToPlainText(in); got != want {
			t.Errorf("ToPlainText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToPlainText_Blockquote(t *testing.T) {
	if got := ToPlainText("> quoted line"); got != "quoted line" {
		t.Errorf("blockquote: got %q", got)
	}
}

func TestToPlainText_HorizontalRuleDropped(t *testing.T) {
	in := "above\n\n---\n\nbelow"
	got := ToPlainText(in)
	want := "above\n\nbelow"
	if got != want {
		t.Errorf("hr:\n got: %q\nwant: %q", got, want)
	}
}

func TestToPlainText_CodeFence(t *testing.T) {
	in := "before\n```sql\nSELECT 1\n```\nafter"
	got := ToPlainText(in)
	want := "before\nSELECT 1\nafter"
	if got != want {
		t.Errorf("code fence:\n got: %q\nwant: %q", got, want)
	}
}

func TestToPlainText_CollapsesBlankLines(t *testing.T) {
	in := "para one\n\n\n\npara two"
	got := ToPlainText(in)
	want := "para one\n\npara two"
	if got != want {
		t.Errorf("blank-line collapse:\n got: %q\nwant: %q", got, want)
	}
}

// TestToPlainText_NoMarkersSurvive is the property the caller relies on to
// decide whether a description carried formatting: a fully-formatted insight
// body must reduce to text with no Markdown markers left.
func TestToPlainText_NoMarkersSurvive(t *testing.T) {
	in := strings.Join([]string{
		"### Day-1 churn is **67%**",
		"",
		"Most new players leave after the *tutorial*. Key drivers:",
		"",
		"- `onboarding` length",
		"- difficulty spike",
		"",
		"| Cohort | Churn |",
		"|--------|-------|",
		"| new    | 67%   |",
	}, "\n")
	got := ToPlainText(in)
	if strings.ContainsAny(got, markerChars) {
		t.Errorf("markers leaked into plain reduction: %q", got)
	}
	if strings.Contains(got, "|---") || strings.Contains(got, "--|") {
		t.Errorf("table separator leaked: %q", got)
	}
	for _, want := range []string{"Day-1 churn is 67%", "tutorial", "onboarding length", "Cohort | Churn"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in reduction, got: %q", want, got)
		}
	}
}

// TestToPlainText_UnformattedEqualsInput documents the signal the orchestrator
// uses: when the authored description has no formatting, the reduction equals
// the input, so the caller knows to leave DescriptionMd empty.
func TestToPlainText_UnformattedEqualsInput(t *testing.T) {
	plain := "A single clean sentence with numbers like 28.9% and a count of 8,298."
	if got := ToPlainText(plain); got != plain {
		t.Errorf("unformatted text should equal its reduction.\n got: %q\nwant: %q", got, plain)
	}
}
