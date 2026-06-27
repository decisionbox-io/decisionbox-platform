# Plan — Insight descriptions as formatted Markdown content (#293)

## 1. Problem restatement

An insight's `description` is the core of the insight: what was found, the numbers
behind it, why it matters, who is affected. Today it is **generated as flat prose**
and **rendered as a single plain-text block** (`<Text size="sm">{insight.description}</Text>`).
Rich findings become a wall of text that buries the takeaway and slows the skim-to-act
loop the dashboard is built around.

The reporter's clarifying comment is decisive on scope:

> This is not just a visual enhancement. Insight detail page should gain markdown
> rendering capability, but **we need to generate insight content in markdown as
> well**. Let's scope it accordingly.

So this is a **two-sided** change, not a CSS pass:

1. **Generation (agent):** instruct the analysis LLM to author `description` as a small,
   tasteful subset of Markdown with a consistent anatomy.
2. **Rendering (dashboard):** render that Markdown as clean, well-structured content in
   the full insight view, and reduce it to a clean plain-text snippet in every compact /
   preview context — never leaking raw `**`, `##`, or table pipes.

The change must be backward compatible: existing plain-text descriptions are already
valid Markdown and must render as a tidy paragraph with no stray symbols (AC4).

## 2. Scope and key decisions (answering the issue's open questions)

These are decisions I made to keep the run moving; each has an off-ramp the reviewer can
veto on this plan PR.

- **Q1 — Recommendations too?** **Yes, in this same PR.** Recommendation `description`
  shares the exact same code paths on both sides: the renderer is one shared component
  (used by two detail pages instead of one) and the generation guidance lives in the
  already-parallel `discipline.RecommendationsRules()`. The spec's *Consistency*
  requirement and Open Question 1 both lean "together," and shipping insights formatted
  while the recommendation right beside it stays a wall of text would look broken. Marginal
  cost is near-zero. **Off-ramp:** if the reviewer prefers insights-only, drop the
  recommendation renderer wiring + the recommendation discipline-rule block; nothing else
  changes.
- **Q2 — Anatomy: requirement or recommendation?** **Strong recommendation, not enforced.**
  The discipline rule asks the model to structure the description as *takeaway → what's
  happening → why it matters → who's affected → contributing factors* **where the content
  supports it**, but does not reject insights that don't fit the mold. Hard-enforcing a
  shape would fight "older and plainer insights still look good" and findings that are
  genuinely one sentence. No validation/verifier change rejects on structure.
- **Q3 — Formatting set.** **In:** bold/italic emphasis, bulleted + numbered lists, small
  sub-headings (rendered at h3/h4 scale only — never page-size), GFM tables, emphasized
  inline numbers (bold). **Out:** images, raw HTML, arbitrary embeds, oversized headings.
  **Links:** rendered as **plain, non-navigable text** in v1 — descriptions are
  product-generated from the user's own warehouse, external links shouldn't appear, and
  this fully satisfies the *Trust* requirement ("no surprising or unsafe links") with zero
  link surface. The model is also told not to author links.

### Data model: no change

`Insight.Description` / `Recommendation.Description` are already `string` in
`services/agent/internal/models/discovery.go`, `services/api/models/discovery.go`, and
`libs/go-common/models`. Markdown is just text in that same field, so:

- **No schema migration, no new field.** Plain-text legacy descriptions are valid Markdown
  and render as a paragraph (AC4 for free).
- Embeddings / semantic search are unaffected (a few `**`/`#` characters don't move vectors
  meaningfully); the spec's non-goal "not changing what an insight is / how they're
  discovered" is respected.

Reusing the field (vs. adding `description_md`) is the chosen approach precisely because it
needs no migration and is automatically backward/forward compatible.

## 3. Architecture overview

```
GENERATION                                   RENDERING
services/agent/internal/discipline/rules.go  ui/dashboard/src/components/common/Markdown.tsx   (new, full render)
  + analysisRulesText: Markdown-authoring     ui/dashboard/src/lib/markdown.ts                  (new, toPlainText)
  + recommendationsRulesText: same             ├─ insight detail page      → <Markdown>
  + envelope clarification ("no markdown"      ├─ recommendation detail    → <Markdown>
    means no code fence, not no-formatting-    └─ preview/snippet contexts → toPlainText()
    in-string-fields)
        │ appended at runtime by orchestrator.go
        │ (buildAnalysisAreaPrompt / buildRecommendationsPrompt)
        ▼
  reaches every domain pack + custom area, no per-pack edits
```

Both sides hang off a **single source of truth**: one Go rules file for authoring, one
React component + one util for display. This mirrors the existing design intent documented
at the top of `discipline/rules.go` (rules live in code "to eliminate cross-repo drift
across every prompt template") and avoids editing 27 `analysis_*.md` + 4 `recommendations.md`
pack files.

## 4. Detailed changes

### 4A. Generation — `services/agent/internal/discipline/rules.go`

The discipline package is appended to every writer prompt at runtime by the orchestrator
(`buildAnalysisAreaPrompt` → `AppendAnalysisRules`, `buildRecommendationsPrompt` →
`AppendRecommendationsRules`, confirmed at `orchestrator.go:1280-1301`). It already reaches
**user-added custom analysis areas** that carry no pack content — so it is the correct,
drift-proof injection point.

Add a new rule block to **`analysisRulesText`** (and the parallel one to
`recommendationsRulesText`):

- **`STRUCTURED MARKDOWN DESCRIPTION`** — the `description` field is **GitHub-Flavored
  Markdown**. Reconcile the conflicting envelope instruction explicitly, because 58 pack
  lines say *"Respond with ONLY valid JSON (no markdown, no explanations)"*:
  > The "no markdown" instruction above refers to the **response envelope** — do not wrap
  > the JSON in ``` code fences and emit nothing outside the JSON object. It does **not**
  > restrict the *content of string fields*. The `description` value MUST be GitHub-Flavored
  > Markdown.
  Because the discipline block is appended **last**, it has recency advantage over the
  pack's earlier phrasing — one clarification fixes all 58 sites without touching them.
- **Supported elements** (the Q3 set): a short bold lead/takeaway line with the headline
  number in **bold**; short paragraphs; `**bold**` / `*italic*`; `-`/`1.` lists for
  contributing factors; small sub-headings using `###`/`####` only (never `#`/`##`); GFM
  tables for small numeric comparisons. **Forbidden:** images, raw HTML, links, top-level
  `#`/`##` headings.
- **Anatomy (strong recommendation)** — where the finding supports it: one-line takeaway →
  what's happening (exact numbers) → why it matters → who's affected (segment + size) →
  optional contributing-factors list.
- **JSON-safety note** — newlines inside the `description` string MUST be JSON-escaped
  (`\n`); the response must remain a single valid JSON object. (This protects the strict
  `json.Unmarshal` in `parseInsights` — see Risks.)
- **No conflict with existing rules** — the new rule explicitly defers to base-context
  Rule 8 (non-dramatic language): Markdown controls *structure*, not *tone*; bold is for
  numbers/keywords, never for shouting, and the no-emoji / no-`!` / no-all-caps / "critical
  only in the severity field" constraints still hold. This keeps the verifier's V4
  editorial-language check happy (it scans for dramatic words / `!` / emoji / all-caps —
  none of which are Markdown structural symbols, so `**67%**` and `### Why it matters` are
  not falsely rejected).

The recommendation block is the same, framed for `title`/`description`/`actions` and
preserving the existing `R. RELATED_INSIGHT_IDS` and non-dramatic-language clauses.

**Optional, low-value, deliberately NOT doing (Rule 8):** rewriting the `"description":`
example string inside all 27+4 pack prompt files to show Markdown. The runtime append is
authoritative and reaches custom areas; editing every pack invites drift and is exactly the
kind of duplication `discipline/rules.go` exists to avoid. Left out unless a reviewer asks.

### 4B. Rendering — new shared pieces

**New `ui/dashboard/src/lib/markdown.ts`** — `toPlainText(md: string): string` and a small
`snippet(md, max)` helper. `toPlainText` strips the supported Markdown to readable plain
text for preview contexts: removes emphasis markers, heading hashes, list bullets/numbers,
collapses GFM tables to space/dash-separated cells, unwraps `[text](url)` to `text`, and
normalizes whitespace. Pure, dependency-free, unit-tested against every supported element +
plain text + empty/undefined. Satisfies AC5 (no raw formatting chars, no oversized headings,
no layout breakage in compact contexts).

**New `ui/dashboard/src/components/common/Markdown.tsx`** — a thin, safe wrapper over
`react-markdown` + `remark-gfm` (both already deps: `react-markdown@^10`, `remark-gfm@^4`),
with a component map styled from `src/styles/tokens.css` custom properties (no inline magic
colors — Rule 2 / TS standards). Follows the precedent already in the repo at
`ask/page.tsx:341-384` (which maps `p/li/h1..h3/strong/ul/ol/table/th/td/code`), minus the
citation processing. Key specifics:

- **Safety/Trust:** react-markdown v10 renders **no raw HTML** by default (no `rehype-raw`
  anywhere in the repo — confirmed) and sanitizes URLs, so it is XSS-safe. We go further and
  map `a → <span>` (plain text, non-navigable) so even a stray link can't navigate (Q3 /
  Trust).
- **No oversized headings:** map `h1/h2/h3 → h4-scale`, `h4 → smaller` — sub-headings
  *separate* ideas but never dominate, matching "small sub-headings" + AC2/AC8.
- **Typography native to the product:** font sizes, line-height (~1.6–1.7), list indent,
  table borders all from tokens, matching the Ask renderer and the surrounding Mantine
  cards (AC8).
- **Long content (AC6):** no `max-height`/clamp on the full renderer; tables wrapped in an
  `overflow-x:auto` div so wide tables scroll instead of breaking the layout on small
  screens.

### 4C. Wire the full renderer into the two detail views

| File | Line | From | To |
|------|------|------|----|
| `app/projects/[id]/discoveries/[runId]/insights/[insightId]/page.tsx` | 196-198 | `<Text size="sm">{insight.description}</Text>` | `<Markdown>{insight.description}</Markdown>` (with a quiet "No description" when empty — AC *No description* state) |
| `app/projects/[id]/discoveries/[runId]/recommendations/[recommendationId]/page.tsx` | 156-159 | `<Text size="sm">{recommendation.description}</Text>` | `<Markdown>{recommendation.description}</Markdown>` |

The insight detail page already orders content title → at-a-glance badges (severity / area /
affected) → description card → Assessment → Indicators → Metrics → supporting detail
(AC7 already satisfied structurally); we only upgrade the description card's body.

### 4D. Reduce Markdown to plain text in every preview/snippet context (AC5)

Wrap the description with `toPlainText(...)` (then slice/clamp as today) at each compact
site so raw Markdown never leaks:

| File | Line(s) | Context |
|------|---------|---------|
| `components/search/ResultCard.tsx` | 54-56 | global search result card snippet (`slice(0,200)`) |
| `components/lists/SimilarItems.tsx` | 60-63 | "Similar Insights" card (`lineClamp={2}`) |
| `components/common/SpotlightSearch.tsx` | 250-257 | spotlight quick-search row (`slice(0,80)`) |
| `app/projects/[id]/recommendations/page.tsx` | 166-169 | recommendations semantic-search snippet (`slice(0,200)`) |
| `app/projects/[id]/recommendations/page.tsx` | 277-280 | recommendations list card body (currently full text) → plain-text snippet |

`app/projects/[id]/insights/page.tsx:123` filters on `description` for client-side text
search; it lowercases+`includes`, so matching against raw Markdown still works. Left as-is
(filtering on the underlying text is correct; stripping there is unnecessary churn).

### 4E. Docs (Rule 4)

- `docs/reference/data-models.md` — Insight (line 37) and Recommendation (line 74)
  `description` rows: note the field is **GitHub-Flavored Markdown** (small subset: emphasis,
  lists, small sub-headings, simple tables; rendered in the dashboard, reduced to plain text
  in previews).
- `docs/concepts/discovery-lifecycle.md` — where the analysis output schema is shown
  (~line 169 / 250), add a sentence that `description` is authored as Markdown with a
  suggested takeaway-first anatomy.
- `docs/guides/customizing-prompts.md` and/or `docs/guides/creating-domain-packs.md` — note
  that the platform appends Markdown-authoring guidance to every analysis/recommendation
  prompt (so custom areas get it automatically) and that the `description` example values
  may use Markdown.
- `docs/concepts/ask.md` — only if it asserts descriptions are plain text (verify; touch
  only if stale).
- `CHANGELOG.md` — one `### Added` entry under `[Unreleased]` listing the agent rules file,
  the new dashboard component + util, and the wired pages.

All docs stay `md` (not `mdx`), one sentence per line, links validated (per repo doc rules).

## 5. Implementation phases

1. **Agent rules + tests.** Add the Markdown-authoring block to `analysisRulesText` and
   `recommendationsRulesText`; extend `discipline/rules_test.go`. Add a `parseInsights`
   test proving an escaped-newline Markdown description round-trips and a plain description
   still parses. `make test-go`.
2. **Dashboard util + component + tests.** Add `lib/markdown.ts` (`toPlainText`) and
   `components/common/Markdown.tsx`; Jest tests for both. `make test-ui`.
3. **Wire renderer.** Swap the two detail-page description blocks to `<Markdown>`; handle
   empty description.
4. **Wire previews.** Apply `toPlainText` at the 5 snippet sites; extend `ResultCard` test
   to assert no raw `**`/`#` leaks.
5. **Docs + CHANGELOG.**
6. **Local gates:** `make build`, `make test-go`, `make lint-go` (after
   `export PATH=$PATH:$(go env GOPATH)/bin`), `make test-ui`, `make lint-ui`, and
   `cd ui/dashboard && npm run build`.

## 6. Test strategy (Rule 9 — failure & edge cases, not just happy path)

**Go — `services/agent/internal/discipline/rules_test.go`:**
- `AnalysisRules()` / `RecommendationsRules()` contain the new section header + the key
  clauses (Markdown-in-`description`, the envelope clarification, the supported-set, the
  anatomy, the JSON-escape note). Mirrors the existing `assertContainsAll` table style.
- Guard test: the envelope-clarification phrasing is present so the "no markdown" pack lines
  can't be read as "no formatting in fields."
- Negative: the new block must not introduce emoji / `!` / dramatic words (keeps it
  consistent with Rule 8 and the existing `assertNotContains` discipline).

**Go — `services/agent/internal/discovery/orchestrator_*_test.go` (parse):**
- `parseInsights` with a description containing escaped-newline Markdown
  (`"## Takeaway\n\n**67%** of ...\n\n| a | b |\n|---|---|\n| 1 | 2 |"`) → parses, field
  preserved verbatim. **Failure/edge:** a plain one-line description still parses (AC4);
  an empty description is accepted.

**Dashboard — Jest:**
- `lib/markdown.toPlainText`: bold/italic/headings/lists/tables/links all reduce to clean
  text; plain text passes through unchanged; `undefined`/empty → `''`; long input + slice
  still has no stray symbols (AC5).
- `components/common/Markdown`: renders `**x**`→`<strong>`, `*x*`→`<em>`, `- a`→list item,
  `### H`→small heading (assert it is *not* an `<h1>`), a GFM table → `<table>` (AC1/AC2);
  a plain paragraph renders as one `<p>` with no literal `*`/`#` (AC4); an HTML-injection
  string (`<img onerror=...>` / `<script>`) is rendered inert as text, not as a live element
  (Trust); a `[text](javascript:...)` link renders as plain text, non-navigable (Q3/Trust).
- Extend `ResultCard.test.tsx`: a Markdown description (`**Bold** finding`) shows `Bold
  finding` in the snippet, not `**Bold**` (AC5).

**Integration (real, testcontainer — Rule 9):** the change is prompt-text + presentation;
it adds no new DB/warehouse/API behavior, so no new integration target is warranted. The
existing `make test-integration` (agent + API Mongo round-trip) already exercises persisting
and serving an insight `description` string and continues to pass unchanged with Markdown
content (it's still just a string). I will run it to confirm no regression rather than add a
redundant container test (avoids Rule 8 gold-plating).

## 7. Risks & mitigations

- **JSON parse fragility on multi-line strings.** `parseInsights`/recommendation parsing use
  strict `json.Unmarshal` (`orchestrator.go:1112,1198`); a description with *literal*
  (unescaped) newlines would fail and drop that area's insights. *Mitigation:* the new rule
  explicitly requires `\n`-escaped newlines and a single valid JSON object; modern models
  reliably escape inside JSON; a parse test pins the escaped form. *Not* adding a custom
  newline-repair pass in this PR — that's a separate hardening concern (Rule 8); if the team
  wants it, it's a tracked follow-up, not silent tech debt.
- **Verifier false-rejects on Markdown symbols.** V4 scans `description` for dramatic words /
  `!` / emoji / all-caps — none are Markdown structural tokens, so `**`, `###`, `|` are safe.
  Confirmed against `verifierRulesText`. No change needed; covered by reasoning above.
- **Enterprise dashboard overlay.** Per `automation/CLAUDE.md`, if the enterprise repo
  overlays a community UI file we edit, the overlay must be synced. The new `Markdown.tsx` /
  `markdown.ts` are **additive** (never overlaid). The deep route detail pages and the
  preview components are unlikely to be overlaid (the documented example is `AppShell.tsx`),
  but I cannot inspect the enterprise repo from this container. **Flagging for reviewer:**
  confirm whether `insights/[insightId]/page.tsx`, `recommendations/[recommendationId]/page.tsx`,
  `ResultCard.tsx`, `SimilarItems.tsx`, or `SpotlightSearch.tsx` are overlaid; if so, port the
  one-line edits.
- **Preview leakage regressions.** Easy to miss a snippet site. *Mitigation:* the table in
  4D enumerates all five; a `ResultCard` test pins the behavior; grep for `.description` over
  `src/` was used to build the list.
- **Tone drift.** Giving the model bold/headings could tempt dramatic emphasis. *Mitigation:*
  the new rule subordinates itself to Rule 8 and the verifier still enforces it.

## 8. Alternatives considered

- **New `description_md` field + keep `description` plain.** Rejected: needs a migration and
  dual-write, and the same field already round-trips Markdown losslessly. More moving parts,
  no benefit.
- **Edit every pack prompt's `description` example instead of the central rule.** Rejected:
  27+4 files, cross-pack drift, and misses custom analysis areas — the exact failure mode
  `discipline/rules.go` was created to prevent.
- **Server-side render Markdown → HTML in the API.** Rejected: pushes an HTML-sanitization
  burden into Go, and the dashboard already owns presentation; react-markdown is safe by
  default on the client.
- **Reuse the Ask page's inline renderer directly.** Rejected: it's coupled to citation
  processing; extracting a clean shared component is cheaper than retrofitting, and we leave
  the Ask page untouched (Rule 8).
- **Hard-enforce the anatomy via the verifier.** Rejected per Q2 — fights plain/simple
  findings and "older insights still look good."

## 9. Files touched (summary)

| Area | File | Change |
|------|------|--------|
| Agent | `services/agent/internal/discipline/rules.go` | + Markdown-authoring block in `analysisRulesText` & `recommendationsRulesText` + envelope clarification |
| Agent | `services/agent/internal/discipline/rules_test.go` | + assertions for the new rule text |
| Agent | `services/agent/internal/discovery/orchestrator_*_test.go` | + Markdown-in-`description` parse test (escaped newlines, plain, empty) |
| UI | `ui/dashboard/src/lib/markdown.ts` | **new** — `toPlainText` / `snippet` |
| UI | `ui/dashboard/src/components/common/Markdown.tsx` | **new** — safe GFM renderer (token-styled, small headings, links→text) |
| UI | `.../insights/[insightId]/page.tsx` | description card → `<Markdown>` + empty state |
| UI | `.../recommendations/[recommendationId]/page.tsx` | description card → `<Markdown>` |
| UI | `components/search/ResultCard.tsx` | snippet → `toPlainText` |
| UI | `components/lists/SimilarItems.tsx` | snippet → `toPlainText` |
| UI | `components/common/SpotlightSearch.tsx` | snippet → `toPlainText` |
| UI | `app/projects/[id]/recommendations/page.tsx` | two snippet sites → `toPlainText` |
| UI tests | `src/__tests__/Markdown.test.tsx`, `src/__tests__/markdown.test.ts`, extend `ResultCard.test.tsx` | **new/extended** |
| Docs | `docs/reference/data-models.md`, `docs/concepts/discovery-lifecycle.md`, `docs/guides/customizing-prompts.md` (+/or `creating-domain-packs.md`) | note Markdown descriptions |
| Docs | `CHANGELOG.md` | `### Added` entry under `[Unreleased]` |

## 10. Acceptance-criteria traceability

| AC | Covered by |
|----|-----------|
| 1 — full view renders emphasis/lists/sub-headings/table, no raw symbols | 4B/4C `Markdown.tsx`; component tests |
| 2 — lists indented, emphasis bold/italic, sub-headings separate, tables legible | 4B component map + tests |
| 3 — takeaway + headline numbers prominent in seconds | 4A anatomy (bold lead/number) + 4B typography |
| 4 — plain description → clean paragraph, no leftover symbols | field reuse + react-markdown paragraph; component test |
| 5 — previews are clean plain-text snippets | 4B `toPlainText` + 4D five sites + `ResultCard` test |
| 6 — very long description fully readable, not clipped | 4B no clamp on full render; table overflow-scroll |
| 7 — detail view organized: title → key facts → description → detail | existing layout; description card upgraded |
| 8 — formatting matches product typography/spacing | 4B token-based styling, mirrors Ask renderer |

## 11. Out of scope / follow-ups

- Custom newline-repair in the JSON parser (only if real-world parse failures appear — would
  be a tracked issue, not a TODO).
- Back-filling/re-generating historical insight descriptions into Markdown (non-goal; they
  already render fine as paragraphs).
- Rich-text editing by end users (explicit non-goal).
- Images / embeds / navigable links (explicit non-goal / deferred per Q3).

---

This is a **PLAN for review** — no implementation is included in this PR. Once approved, the
build step implements it per the phases above, deletes this plan file, marks the PR ready,
and runs the Codex + Copilot review loop.

Closes #293

— Co-coded with Jale 🤖
