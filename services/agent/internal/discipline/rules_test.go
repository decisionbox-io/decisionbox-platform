package discipline

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// substringChecker pairs a human-readable label with a substring the rule
// text must contain. Using labels in test failure messages lets a future
// maintainer see which intent was missing without re-reading the rule
// body. Substrings are kept short and stable so harmless rewordings of
// the surrounding prose do not flake the tests.
type substringChecker struct {
	label string
	want  string
}

func assertContainsAll(t *testing.T, name, text string, checks []substringChecker) {
	t.Helper()
	for _, c := range checks {
		if !strings.Contains(text, c.want) {
			t.Errorf("%s: missing %s — expected to find %q in:\n%s", name, c.label, c.want, text)
		}
	}
}

func assertNotContains(t *testing.T, name, text string, substrings []string) {
	t.Helper()
	for _, s := range substrings {
		if strings.Contains(text, s) {
			t.Errorf("%s: forbidden substring present — %q must NOT appear in:\n%s", name, s, text)
		}
	}
}

func TestBaseContextRules_ContainsAllExpectedRules(t *testing.T) {
	text := BaseContextRules()
	assertContainsAll(t, "BaseContextRules", text, []substringChecker{
		{"section header", "## Claim discipline"},
		{"rule 1 superlative-binding", "SCOPE-BIND EVERY SUPERLATIVE"},
		{"rule 1 enforcement phrase", "MUST name the"},
		{"rule 2 no-claim-beyond-window", "NO CLAIM BEYOND QUERY WINDOW"},
		{"rule 7 partial-period hygiene", "PARTIAL-PERIOD HYGIENE"},
		{"rule 8 non-dramatic header", "OBJECTIVE, NON-DRAMATIC LANGUAGE"},
		{"rule 8 severity-only-in-field clause", "structured `severity` field"},
		{"rule 8 critical-only-in-severity clause", `The word "critical"`},
		{"rule 8 translation-extension clause", "any translation thereof"},
		{"rule 8 adverb hygiene clause", "Adverb hygiene"},
	})
}

func TestBaseContextRules_ContainsWorkedExamples(t *testing.T) {
	text := BaseContextRules()
	// Worked Bad → Good examples for rule 8. Fragments are sampled short
	// enough to be unique and stable across harmless rewordings of the
	// surrounding prose.
	wantFragments := []string{
		"Online return rate 51%",       // rule 8 Bad → Good first row
		"103 days of cover",            // rule 8 Bad → Good second row
		"Last 7-day return rate 28.9%", // rule 8 Bad → Good third row
		"Sales fell sharply",           // adverb-hygiene Bad example anchor
	}
	for _, m := range wantFragments {
		if !strings.Contains(text, m) {
			t.Errorf("BaseContextRules: missing worked-example fragment %q", m)
		}
	}
}

func TestRulesAreLanguageNeutral(t *testing.T) {
	// The rule prose must not name a specific human language other than
	// English. The rules apply regardless of the project's configured
	// output language; we describe principles + give English-only
	// examples and tell the LLM to apply the same constraint to
	// translations.
	//
	// Sentinel non-English fragments — if anything matching these
	// appears in the rules, someone has re-introduced a language-specific
	// example.
	bannedFragments := []string{
		"tüm zamanların",
		"Felaket",
		"Katastrofik",
		"Online Kalıp",
		"ciddi şekilde",
		"önemli ölçüde",
	}
	allTexts := map[string]string{
		"BaseContextRules":     BaseContextRules(),
		"AnalysisRules":        AnalysisRules(),
		"RecommendationsRules": RecommendationsRules(),
		"VerifierRules":        VerifierRules(),
	}
	for name, text := range allTexts {
		for _, banned := range bannedFragments {
			if strings.Contains(text, banned) {
				t.Errorf("%s contains non-English fragment %q — rules must be language-neutral with English-only examples", name, banned)
			}
		}
	}
}

func TestAllRulesCarryOutputLanguageGuidance(t *testing.T) {
	// The rules describe English examples but explicitly tell the LLM that
	// the constraints apply in the project's configured output language.
	// Every rule block must carry a clause that extends the principle
	// beyond English, otherwise a project configured to emit insights in
	// another language would interpret the rules as English-only.
	cases := map[string]string{
		"BaseContextRules":     BaseContextRules(),
		"AnalysisRules":        AnalysisRules(),
		"RecommendationsRules": RecommendationsRules(),
		"VerifierRules":        VerifierRules(),
	}
	// At least one of these phrases must appear in each rule block. Each
	// expresses the same intent ("rules extend to non-English output");
	// any one is sufficient to confirm the LLM is told to translate the
	// principle, not just the literal English examples.
	languageClausePhrases := []string{
		"configured output language",
		"insight's actual language",
		"any language",
	}
	for name, text := range cases {
		found := false
		lower := strings.ToLower(text)
		for _, p := range languageClausePhrases {
			if strings.Contains(lower, strings.ToLower(p)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: missing output-language guidance — rule block must explicitly extend the principle to non-English output", name)
		}
	}
}

func TestBaseContextRules_NoEnumeratedBannedWordList(t *testing.T) {
	// Rule 8 ships as principle + worked examples, not an enumerated
	// banned-words list. A regression here would mean someone re-introduced
	// the explicit list — fail loudly so the design decision is preserved.
	// The phrase "Banned words" used to appear in earlier drafts of the
	// rules; ensure it does not regress.
	text := BaseContextRules()
	assertNotContains(t, "BaseContextRules", text, []string{
		"Banned words in",
		"Banned vocabulary",
	})
}

func TestAnalysisRules_ContainsExpectedRules(t *testing.T) {
	text := AnalysisRules()
	assertContainsAll(t, "AnalysisRules", text, []substringChecker{
		{"section header", "## Insight-writing discipline"},
		{"rule 3 re-rank from raw rows", "RE-RANK FROM THE RAW RESULT ROWS"},
		{"rule 4 address counter-evidence", "ADDRESS COUNTER-EVIDENCE EXPLICITLY"},
		{"rule 4 forbidden-silent-dismissal", "silently dismissing"},
		{"rule 5 self-consistency", "SELF-CONSISTENCY CHECK ACROSS FIELDS"},
		{"rule 5 names insight schema fields", "`description`"},
		{"rule 5 names indicators field", "`indicators`"},
		{"rule 5 names metrics field", "`metrics`"},
		{"rule 5 names name field", "`name`"},
		{"rule 6 cite the step", "CITE THE STEP FOR EVERY NUMBER"},
		{"rule 6 references source_steps", "`source_steps`"},
	})
}

func TestRecommendationsRules_ContainsExpectedRules(t *testing.T) {
	text := RecommendationsRules()
	assertContainsAll(t, "RecommendationsRules", text, []substringChecker{
		{"section header", "## Recommendation discipline"},
		{"rule 3 re-rank from underlying insight", "RE-RANK FROM THE UNDERLYING INSIGHT"},
		{"rule 4 counter-evidence", "ADDRESS COUNTER-EVIDENCE EXPLICITLY"},
		{"rule 5 self-consistency", "SELF-CONSISTENCY CHECK ACROSS FIELDS"},
		{"rule 5 names title field", "`title`"},
		{"rule 5 names description field", "`description`"},
		{"rule 5 names target_segment field", "`target_segment`"},
		{"rule 6 cite the insight", "CITE THE INSIGHT FOR EVERY NUMBER"},
		{"rule 6 references related_insight_ids", "`related_insight_ids`"},
		{"non-dramatic reiteration", "NON-DRAMATIC LANGUAGE"},
		{"critical banned in prose", "may NOT appear in"},
		{"priority field reference", "`priority`"},
	})
}

func TestVerifierRules_ContainsV1V4(t *testing.T) {
	text := VerifierRules()
	assertContainsAll(t, "VerifierRules", text, []substringChecker{
		{"section header", "## Headline-claim verification"},
		{"V1 header", "V1. RE-VERIFY THE HEADLINE SUPERLATIVE"},
		{"V1 references SQL CASE construct", "CASE"},
		{"V1 references claimed_week placeholder", "claimed_week"},
		{"V2 header", "V2. REJECT QUANTIFIER-SCOPE MISMATCH"},
		{"V2 SELECT 0 mechanism", "SELECT 0 AS count"},
		{"V3 header", "V3. CHECK CROSS-FIELD CONSISTENCY"},
		{"V3 names source_steps comparison", "`source_steps`"},
		{"V4 header", "V4. REJECT EDITORIAL LANGUAGE"},
		{"V4 names severity carve-out", `The word "critical"`},
		{"V4 allowed only in severity field", "`severity` value"},
	})
}

func TestVerifierRules_V1RequiresActualAggregateNotClaimedValue(t *testing.T) {
	// V1's THEN branch must run an independent aggregate against the
	// warehouse — substituting the literal claimed-count number would
	// make the verification self-confirming (the ratio check compares
	// claimed against itself → always 1.0). This test pins the
	// anti-pattern callout so a future edit that "simplifies" the
	// example back to <claimed_affected_count> in the THEN branch is
	// caught by failing tests.
	text := VerifierRules()
	requiredCallouts := []string{
		"BOTH the ranking AND the affected",
		"actual aggregate",
		"self-confirming",
		"COUNT(DISTINCT user_id)",
	}
	for _, want := range requiredCallouts {
		if !strings.Contains(text, want) {
			t.Errorf("V1 framing regressed — missing required callout %q. V1 must instruct the LLM to run an independent aggregate in the THEN branch, not substitute the claimed value.", want)
		}
	}
	// The Bad-shape — using the literal <claimed_affected_count>
	// placeholder in the THEN branch — must NOT appear, because that
	// is the failure mode V1 is supposed to prevent.
	forbiddenShape := "THEN <claimed_affected_count>"
	if strings.Contains(text, forbiddenShape) {
		t.Errorf("V1 example regressed to the self-confirming shape %q — must use a subquery returning the actual aggregate", forbiddenShape)
	}
}

func TestAllRulesReturnNonEmpty(t *testing.T) {
	cases := map[string]string{
		"BaseContextRules":     BaseContextRules(),
		"AnalysisRules":        AnalysisRules(),
		"RecommendationsRules": RecommendationsRules(),
		"VerifierRules":        VerifierRules(),
	}
	for name, text := range cases {
		if text == "" {
			t.Errorf("%s returned empty string", name)
		}
	}
}

func TestAllRulesAreValidUTF8(t *testing.T) {
	// The rule text contains Turkish characters (ş, ç, ü, ğ, ö) which are
	// outside ASCII. A future edit that accidentally introduces a malformed
	// byte sequence (e.g., by mis-pasting from a non-UTF-8 source) would
	// silently produce a broken prompt. Catch it here.
	cases := map[string]string{
		"BaseContextRules":     BaseContextRules(),
		"AnalysisRules":        AnalysisRules(),
		"RecommendationsRules": RecommendationsRules(),
		"VerifierRules":        VerifierRules(),
	}
	for name, text := range cases {
		if !utf8.ValidString(text) {
			t.Errorf("%s returned invalid UTF-8", name)
		}
	}
}

func TestAllRulesAreDeterministic(t *testing.T) {
	// The four accessors must return the same string on repeated invocation.
	// They're trivial today (const returns) but the test guards against a
	// future refactor that, e.g., interpolates time.Now() or a counter.
	cases := map[string]func() string{
		"BaseContextRules":     BaseContextRules,
		"AnalysisRules":        AnalysisRules,
		"RecommendationsRules": RecommendationsRules,
		"VerifierRules":        VerifierRules,
	}
	for name, fn := range cases {
		first := fn()
		for i := 0; i < 3; i++ {
			again := fn()
			if again != first {
				t.Errorf("%s returned a different value on call %d: drift detected", name, i+1)
				break
			}
		}
	}
}

func TestAllRulesHaveNoLeftoverTemplateVariables(t *testing.T) {
	// The rule text is final at compile time — no {{...}} placeholders.
	// If a future edit accidentally pastes a template variable from a pack
	// file, the orchestrator's per-site substitution will NOT touch it
	// (substitutions happen before our append), so it would leak into the
	// LLM prompt verbatim. Forbid the syntax entirely.
	cases := map[string]string{
		"BaseContextRules":     BaseContextRules(),
		"AnalysisRules":        AnalysisRules(),
		"RecommendationsRules": RecommendationsRules(),
		"VerifierRules":        VerifierRules(),
	}
	for name, text := range cases {
		if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
			t.Errorf("%s contains a template variable marker — rule text must be final at compile time", name)
		}
	}
}

func TestAllRulesStartWithMarkdownSectionHeader(t *testing.T) {
	// The orchestrator appends each rule block with a "\n\n" separator,
	// expecting the rule text to render as a fresh section. A missing
	// leading "## " header would visually merge the rules into the
	// surrounding prose. Enforce the shape.
	cases := map[string]string{
		"BaseContextRules":     BaseContextRules(),
		"AnalysisRules":        AnalysisRules(),
		"RecommendationsRules": RecommendationsRules(),
		"VerifierRules":        VerifierRules(),
	}
	for name, text := range cases {
		if !strings.HasPrefix(text, "## ") {
			t.Errorf("%s does not start with a markdown H2 header (got: %q)", name, firstLine(text))
		}
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- Append* helpers (the public seam used by the orchestrator) ---

func TestAppendBaseContextRules_NormalInput(t *testing.T) {
	prompt := "## Store Profile\n\nSome content here."
	got := AppendBaseContextRules(prompt)
	if !strings.HasPrefix(got, prompt) {
		t.Errorf("AppendBaseContextRules dropped the upstream prompt; got prefix %q", firstLine(got))
	}
	if !strings.Contains(got, BaseContextRules()) {
		t.Errorf("AppendBaseContextRules did not append the rule text")
	}
	// Blank-line separator: exactly two newlines between upstream prompt and
	// rule text. Critical because markdown renders the rules as a fresh
	// section only with a blank line above.
	want := prompt + "\n\n" + BaseContextRules()
	if got != want {
		t.Errorf("AppendBaseContextRules produced incorrect separator/order")
	}
}

func TestAppendBaseContextRules_EmptyInput(t *testing.T) {
	got := AppendBaseContextRules("")
	want := "\n\n" + BaseContextRules()
	if got != want {
		t.Errorf("AppendBaseContextRules(\"\") = %q, want leading separator + rule text", firstLine(got))
	}
}

func TestAppendBaseContextRules_AlreadyHasRulesIsIdempotentByDocumentation(t *testing.T) {
	// The helpers are NOT idempotent: a second call appends the rule
	// text again. The orchestrator calls each helper exactly once per
	// site, so duplication never happens in production. This test
	// documents the contract — if you call twice, you get the rules
	// twice — so a future caller who mistakenly double-calls catches
	// the behavior in their own tests rather than being surprised at
	// runtime.
	once := AppendBaseContextRules("body")
	twice := AppendBaseContextRules(once)
	if !strings.HasPrefix(twice, once) {
		t.Errorf("double-append should retain the single-append output as a prefix")
	}
	if strings.Count(twice, BaseContextRules()) != 2 {
		t.Errorf("double-append should produce two rule blocks, got %d", strings.Count(twice, BaseContextRules()))
	}
}

func TestAppendAnalysisRules_AppendsRuleText(t *testing.T) {
	prompt := "# Revenue analysis\n{{QUERY_RESULTS}} block already substituted"
	got := AppendAnalysisRules(prompt)
	if !strings.Contains(got, AnalysisRules()) {
		t.Errorf("AppendAnalysisRules did not append the rule text")
	}
	if !strings.HasPrefix(got, prompt) {
		t.Errorf("AppendAnalysisRules dropped the upstream prompt")
	}
}

func TestAppendAnalysisRules_AppliesToCustomAreaPromptBody(t *testing.T) {
	// Regression test for the design decision: code-level injection
	// must cover user-added custom analysis areas, which carry
	// arbitrary user-supplied prompt bodies. A custom area's prompt
	// could be anything — empty, malformed, multi-line, with or
	// without markdown — and the rules must still append.
	customAreaPrompts := []string{
		"",
		"single line",
		"multi\nline\nprompt",
		"# Heading\n\nPara.\n\nAnother para with `code`.",
		"Prompt with {{UNSUBSTITUTED}} placeholder that orchestrator missed",
		"Prompt with non-Latin chars: ürün, иванов, 你好",
	}
	for _, p := range customAreaPrompts {
		got := AppendAnalysisRules(p)
		if !strings.Contains(got, AnalysisRules()) {
			t.Errorf("AppendAnalysisRules omitted rule text for custom area input %q", firstLine(p))
		}
	}
}

func TestAppendRecommendationsRules_AppendsRuleText(t *testing.T) {
	prompt := "# Recommendations\n\nGenerate based on insights."
	got := AppendRecommendationsRules(prompt)
	if !strings.Contains(got, RecommendationsRules()) {
		t.Errorf("AppendRecommendationsRules did not append the rule text")
	}
	if !strings.HasPrefix(got, prompt) {
		t.Errorf("AppendRecommendationsRules dropped the upstream prompt")
	}
}

func TestAppendHelpers_AllUseDoubleNewlineSeparator(t *testing.T) {
	// Cross-cutting check: every helper inserts exactly "\n\n" between
	// upstream prompt and rule text. Drift here would produce subtle
	// rendering bugs (single-newline = inline continuation, three+
	// newlines = unnecessary whitespace).
	type fixture struct {
		name   string
		fn     func(string) string
		rules  string
		prompt string
	}
	fixtures := []fixture{
		{"AppendBaseContextRules", AppendBaseContextRules, BaseContextRules(), "upstream"},
		{"AppendAnalysisRules", AppendAnalysisRules, AnalysisRules(), "upstream"},
		{"AppendRecommendationsRules", AppendRecommendationsRules, RecommendationsRules(), "upstream"},
	}
	for _, f := range fixtures {
		got := f.fn(f.prompt)
		want := f.prompt + "\n\n" + f.rules
		if got != want {
			t.Errorf("%s separator/order mismatch — must be `upstream + \\n\\n + rules`", f.name)
		}
	}
}

func TestRulesDoNotOverlap(t *testing.T) {
	// Each rule block targets a distinct injection site. They should not
	// share their *headers* — overlapping headers in the final rendered
	// prompt would suggest the model that the same instruction is being
	// repeated, which dilutes attention. This is a structural sanity
	// check, not a content check.
	headers := map[string]string{
		"BaseContextRules":     firstLine(BaseContextRules()),
		"AnalysisRules":        firstLine(AnalysisRules()),
		"RecommendationsRules": firstLine(RecommendationsRules()),
		"VerifierRules":        firstLine(VerifierRules()),
	}
	seen := make(map[string]string, len(headers))
	for name, h := range headers {
		if prev, ok := seen[h]; ok {
			t.Errorf("duplicate section header %q in %s and %s", h, prev, name)
		}
		seen[h] = name
	}
}
