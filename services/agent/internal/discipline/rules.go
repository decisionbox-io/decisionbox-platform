// Package discipline carries the platform-enforced claim-discipline and
// editorial-language rules that the agent appends to every writer and
// verifier prompt at runtime.
//
// The rules live in code rather than in domain-pack files for three reasons:
//
//  1. User-supplied custom analysis areas (added via the dashboard or
//     PUT /api/v1/projects/{id}/prompts) do not carry pack content; if the
//     rules lived in pack files, custom areas would skip them.
//  2. Generator-produced packs and customer-edited prompts can drop the
//     rules by accident. A runtime append cannot be edited away.
//  3. A single Go source of truth eliminates cross-repo drift between the
//     filesystem packs and the enterprise pack-generator meta-prompts.
//
// Rule text is intentionally language-neutral. Examples are written in
// English for clarity, but every rule block carries an explicit LANGUAGE
// NOTE instructing the LLM to apply the same constraints to its
// configured output language — judging by meaning, not by literal English
// triggers. This is independent of the upstream {{LANGUAGE}} substitution
// the orchestrator already applies to the base context: the LANGUAGE NOTE
// is about the rule's *reach*, not the prose's *target* language.
//
// The four exported functions return the rule text for their respective
// injection sites. See PLAN-PROMPT-CLAIM-DISCIPLINE.md in the plans repo
// for the full design.
package discipline

// BaseContextRules returns the rule text appended to the rendered base
// context. Cascades to every downstream prompt because the orchestrator
// prepends the base context to exploration, analysis, and recommendations.
//
// Carries:
//   - Rule 1: scope-bind every superlative
//   - Rule 2: no claim beyond query window
//   - Rule 7: partial-period hygiene
//   - Rule 8: objective, non-dramatic language (principle + worked
//     examples; no enumerated banned-words list)
func BaseContextRules() string {
	return baseContextRulesText
}

// AnalysisRules returns the rule text appended to each analysis-area
// prompt, after the area-specific content is assembled. Framed for the
// insight JSON schema (description, indicators, metrics, name,
// source_steps).
//
// Carries:
//   - Rule 3: re-rank from raw result rows, not prior prose
//   - Rule 4: address counter-evidence explicitly
//   - Rule 5: self-consistency across description, indicators, metrics, name
//   - Rule 6: cite the step for every number
func AnalysisRules() string {
	return analysisRulesText
}

// RecommendationsRules returns the rule text appended to the
// recommendations prompt. Reframes rules 3, 4, 5, 6 against the
// recommendation JSON schema (title, description, actions,
// related_insight_ids) and reiterates the non-dramatic-language
// principle from rule 8 for recommendation prose.
func RecommendationsRules() string {
	return recommendationsRulesText
}

// VerifierRules returns the rule text inlined into the verifier's
// buildVerificationPrompt output. Each rule funnels through the verifier's
// existing count-comparison pipeline: V1 instructs the LLM to construct
// ranking SQL whose returned `count` encodes claim correctness;
// V2/V3/V4 instruct the LLM to emit SELECT 0 AS count with a SQL comment
// naming the offense, so the existing `count == 0 → rejected` branch fires
// with a logged reason.
//
// Carries:
//   - V1: re-verify the headline superlative
//   - V2: reject quantifier-scope mismatch
//   - V3: check cross-field consistency
//   - V4: reject editorial language
func VerifierRules() string {
	return verifierRulesText
}

// ruleSeparator is the gap inserted between an upstream prompt body and
// the appended rule text. Two newlines render as a blank line in the
// markdown body the LLM receives, which keeps the discipline section
// visually distinct from the pack-supplied content above it.
const ruleSeparator = "\n\n"

// AppendBaseContextRules returns prompt with the base-context discipline
// rules appended after a blank-line separator. Called by the orchestrator
// once per run, immediately after the base context's template-variable
// substitutions finish. Pure: no I/O, no global state, deterministic.
func AppendBaseContextRules(prompt string) string {
	return prompt + ruleSeparator + BaseContextRules()
}

// AppendAnalysisRules returns prompt with the insight-writing discipline
// rules appended after a blank-line separator. Called by the orchestrator
// once per analysis area (including user-added custom areas), after the
// per-area template-variable substitutions finish.
func AppendAnalysisRules(prompt string) string {
	return prompt + ruleSeparator + AnalysisRules()
}

// AppendRecommendationsRules returns prompt with the recommendation
// discipline rules appended after a blank-line separator. Called by the
// orchestrator once per run, after the recommendations prompt's
// template-variable substitutions finish.
func AppendRecommendationsRules(prompt string) string {
	return prompt + ruleSeparator + RecommendationsRules()
}

const baseContextRulesText = `## Claim discipline

Read before writing any insight or recommendation. These rules are
platform-enforced and apply regardless of pack content, per-project
prompt edits, or the configured output language.

LANGUAGE NOTE — read this once and apply to every rule below.
The trigger words, bad/good examples, and forbidden vocabulary in
these rules are written in English for clarity. The rules
themselves are language-agnostic: when you produce insight or
recommendation prose in the project's configured output language
(which may be any language, not English), apply the same
constraints to that language's equivalent words and phrases. If
the output language uses different morphology, idiom, or word
order, judge by meaning — a translation of "catastrophic" carries
the same prohibition; a translation of "all-time" carries the same
scope-binding requirement. Never quote the English examples
verbatim in non-English output; translate the principle.

1. SCOPE-BIND EVERY SUPERLATIVE
   Any superlative or universal claim ("highest", "lowest", "peak",
   "best", "worst", "first", "only", "always", "never", "all-time",
   "ever", "record", "unique" — and any translation thereof in the
   project's output language) MUST name the exact data window it is
   derived from, in the same sentence. The window must match the
   WHERE clause of the step that supports the claim.
     Good: "Week 19 is the highest week in the queried window
            (2026-03-01 to 2026-05-11)."
     Bad:  "Week 19 is the all-time highest week."
   If no executed step covers the full history, you may not use
   "all-time" or any of its translations.

2. NO CLAIM BEYOND QUERY WINDOW
   You cannot make any claim about a period your queries did not
   cover. If your widest query starts on 2026-03-01, do not speak of
   Q1, last year, lifetime, or "ever". To make a wider claim, first
   run a wider query.

7. PARTIAL-PERIOD HYGIENE
   For time-series results, check the first and last row for partial
   periods (days_in_week < 7, or equivalent). A partial period MUST
   NOT enter a ranking unless either (a) it is annualized /
   extrapolated to a comparable length, or (b) all other rows are
   normalized the same way. Mark partial rows explicitly when
   referenced.

8. OBJECTIVE, NON-DRAMATIC LANGUAGE
   Describe findings using numbers and observable facts. Do not
   editorialize severity in prose — that is what the structured
   ` + "`severity`" + ` field is for. The reader will judge importance from the
   data; your job is to expose it accurately, not to amplify it.

   Avoid in prose: dramatic vocabulary (catastrophic, disaster,
   crisis, collapse, dramatic, severe, terrible, alarming,
   devastating, dire, urgent-as-adjective, skyrocketing, plummeting,
   record-breaking, shocking, dangerous, emergency — and their
   translations), emoji, exclamation marks (!), all-caps for
   emphasis, comparative metaphors. The same constraint applies to
   any translation of these words into the project's output
   language.

   The word "critical" (and any translation thereof) may appear ONLY
   as the value of the structured ` + "`severity`" + ` field — never inside
   prose.

   Replace dramatic language with the underlying number or ratio:
     Bad : "Online Return Disaster"
     Good: "Online return rate 51% — 6-day-old product"

     Bad : "Catastrophic Overstock"
     Good: "103 days of cover — 3x the network average"

     Bad : "Critical Acceleration — Spiked to 28.9% in Last 7 Days"
     Good: "Last 7-day return rate 28.9% (lifetime average 20.3%)"

   Adverb hygiene: avoid "significantly", "substantially",
   "drastically", "sharply" (and translations) when a number can
   replace them. "Sales fell sharply" should become "Sales fell 23%
   week over week".

   The headline should answer "what is the measurement, and how does
   it compare to its benchmark" — never "how should the reader feel
   about it".
`

const analysisRulesText = `## Insight-writing discipline

Apply before emitting each insight. Platform-enforced. These rules
apply in the project's configured output language — the field
references below are English schema names (do not translate them),
but the prose constraints apply to whatever language the LLM is
emitting in.

3. RE-RANK FROM THE RAW RESULT ROWS — DO NOT REUSE PRIOR PROSE
   When writing a "top N" / "highest" / "lowest" claim, re-derive the
   ranking directly from the most recent supporting step's full
   result set. Do not trust your own earlier thinking-note summary —
   it may have dropped or re-labeled rows. Walk the rows, pick the
   extreme, then write the claim.

4. ADDRESS COUNTER-EVIDENCE EXPLICITLY
   Before finalizing, scan every supporting step's result for any row
   that contradicts your headline (a higher value where you claim a
   peak, an older date where you claim a first, etc.). If one
   exists, you MUST either:
     a) drop or weaken the claim to fit the data, OR
     b) explicitly name the counter-row in the description and
        justify why it is excluded — and apply the same exclusion
        rule symmetrically to the row you are championing.
   Forbidden: silently dismissing a contradicting row as "outlier",
   "holiday", "partial", or "anomaly" while letting your favored row
   keep its holiday or campaign tailwind.

5. SELF-CONSISTENCY CHECK ACROSS FIELDS
   The quantifier and window used in ` + "`description`" + `, ` + "`indicators`" + `,
   ` + "`metrics`" + `, and the headline ` + "`name`" + ` must match each other. If the
   description says "all time", the indicator cannot say "last 10
   weeks". Re-read the four fields together before finalizing.

6. CITE THE STEP FOR EVERY NUMBER
   Every quantitative figure that appears in ` + "`description`" + ` or
   ` + "`indicators`" + ` must be traceable to a row in one of the steps listed
   in ` + "`source_steps`" + `. If a number is not in any step's result, remove
   it or run a query that produces it. Do not fabricate, round-trip
   from memory, or interpolate.
`

const recommendationsRulesText = `## Recommendation discipline

Apply before emitting each recommendation. Platform-enforced. As
with the insight-writing rules above, these apply in the project's
configured output language: field references are English schema
names (do not translate), but the prose constraints follow the
language the recommendation is being written in.

3. RE-RANK FROM THE UNDERLYING INSIGHT
   When a recommendation references a "top N" pattern (e.g. "target
   the 3 highest-churn segments"), re-derive the ranking from the
   insight's ` + "`metrics`" + ` or the step rows in its ` + "`source_steps`" + ` — not
   from earlier prose. The recommendation must agree with the
   insight it cites.

4. ADDRESS COUNTER-EVIDENCE EXPLICITLY
   If the underlying insight had counter-evidence rows, the
   recommendation must either incorporate the counter-evidence into
   its ` + "`target_segment`" + ` definition or explicitly justify why those
   rows are excluded from the recommended action. No silent
   dismissal.

5. SELF-CONSISTENCY CHECK ACROSS FIELDS
   The quantifier and window used in ` + "`title`" + `, ` + "`description`" + `, and the
   ` + "`target_segment`" + ` definition must match each other and must match
   the cited insight's quantifier and window. A title that says
   "last 30 days" cannot have a description that says "last quarter".

6. CITE THE INSIGHT FOR EVERY NUMBER
   Every quantitative figure that appears in ` + "`title`" + `, ` + "`description`" + `,
   ` + "`actions`" + `, or ` + "`expected_impact`" + ` must be traceable either to the cited
   insight's own values or to a row in one of that insight's
   ` + "`source_steps`" + `. ` + "`related_insight_ids`" + ` must point at the insights the
   numbers actually come from.

NON-DRAMATIC LANGUAGE (recommendation prose)
   Recommendation ` + "`title`" + `, ` + "`description`" + `, and ` + "`actions`" + ` are subject to
   the same non-dramatic-language rule as insight prose: describe
   the action and the measurement, not how the reader should feel.
   No dramatic vocabulary (or its translations), no emoji, no
   exclamation marks, no all-caps emphasis, no metaphors. The word
   "critical" (and any translation thereof) may NOT appear in
   recommendation prose — encode urgency in the structured
   ` + "`priority`" + ` field instead.
`

const verifierRulesText = `## Headline-claim verification (in addition to count checks)

Apply when constructing the verification SQL. Each rule funnels through
the verifier's existing count-comparison pipeline: emit SQL whose
returned ` + "`count`" + ` reflects whether the claim holds. A returned 0
will trigger the existing rejected branch.

LANGUAGE NOTE. The insight you are verifying may be written in any
language the project configured. The trigger words listed below are
English for clarity, but the rules apply to their semantic
equivalents in the insight's actual language. Judge by meaning: a
non-English word that means "highest" / "all-time" / "catastrophic"
triggers the same rule as the English term would.

V1. RE-VERIFY THE HEADLINE SUPERLATIVE, NOT JUST THE COUNT
    If the insight's description or name contains a superlative or
    universal quantifier ("highest", "lowest", "first", "all-time",
    "ever", "record", "unique" — or any translation thereof), the
    verification query MUST test BOTH the ranking AND the affected
    count in a single statement. Construct the SQL so the returned
    ` + "`count`" + ` is:
      - the actual aggregate (e.g. COUNT(DISTINCT user_id)) over the
        claimed entity's rows when the claimed entity IS the
        ranking winner, AND
      - 0 when the claimed entity is NOT the ranking winner.
    Do NOT substitute the literal claimed-count value as the THEN
    branch — that would make the verification self-confirming (the
    ratio check would compare the claimed number against itself and
    always pass when the ranking is true). The THEN branch must run
    an independent aggregate over the warehouse so the existing
    ratio check (verified vs. claimed, ±20%) still catches an
    inflated or deflated count even when the ranking is correct.
    Example pattern:
      SELECT CASE
        WHEN (SELECT week_id FROM dataset.table
              WHERE <filter>
              GROUP BY week_id
              ORDER BY <metric> DESC
              LIMIT 1) = '<claimed_week>'
        THEN (SELECT COUNT(DISTINCT user_id) FROM dataset.table
              WHERE <filter> AND week_id = '<claimed_week>')
        ELSE 0
      END AS count
    Outcomes:
      - Claimed entity is the ranking winner AND its count matches
        the claim within 20%: existing pipeline confirms.
      - Claimed entity is the ranking winner BUT its count differs
        materially: existing pipeline marks adjusted with the
        verified count.
      - Claimed entity is NOT the ranking winner: returned 0 →
        existing pipeline rejects.

V2. REJECT QUANTIFIER-SCOPE MISMATCH
    If the description uses "all-time" / "lifetime" / "ever" /
    "first time" (or any translation) but no supporting step queried
    more than the last 90 days (or the appropriate horizon for the
    metric), the framing is wrong regardless of whether the numbers
    themselves are correct within the narrow window. Emit:
      SELECT 0 AS count  -- V2: description claims all-time, source
                         -- steps cover <N> days
    so the existing pipeline rejects the insight with a logged
    reason.

V3. CHECK CROSS-FIELD CONSISTENCY
    Compare the quantifier and window in ` + "`description`" + ` against
    ` + "`indicators`" + ` and against the WHERE clauses of ` + "`source_steps`" + `. Any
    mismatch (e.g. description says "all-time", indicators say "last
    10 weeks", queries cover 11 weeks) is a failure. Emit:
      SELECT 0 AS count  -- V3: description/indicators/source_steps
                         -- mismatch on <field>
    so the existing pipeline rejects the insight.

V4. REJECT EDITORIAL LANGUAGE
    Scan ` + "`name`" + `, ` + "`description`" + `, ` + "`indicators`" + `, ` + "`target_segment`" + `, and any
    recommendation ` + "`title`" + ` / ` + "`description`" + ` / ` + "`actions`" + ` for dramatic
    vocabulary (catastrophic, disaster, crisis, collapse, dramatic,
    severe, terrible, alarming, devastating — or any translation
    thereof), exclamation marks, emoji, all-caps emphasis, and
    comparative metaphors. The word "critical" (and any translation)
    is allowed only as a ` + "`severity`" + ` value, never in prose. On any
    hit, emit:
      SELECT 0 AS count  -- V4: editorial language in <field>:
                         -- "<offending phrase>"
    so the existing pipeline rejects the insight.
`
