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

1. **Generation (agent):** instruct the analysis LLM to author the description as a small,
   tasteful subset of Markdown with a consistent anatomy.
2. **Rendering (dashboard):** render that Markdown as clean, well-structured content in
   the full insight view, while every compact / preview context keeps showing clean
   plain text — never leaking raw `**`, `##`, or table pipes.

## 2. Data model: add `description_md` alongside `description` (team direction)

Per the team's direction, we do **not** overload the existing field. We keep `description`
exactly as it is today — **raw plain text** — and add a **new sibling field**
`description_md` that holds the Markdown-formatted content.

| Field | Content | Consumers |
|-------|---------|-----------|
| `description` (existing) | **Plain text** (raw) | API integrators / external tools, semantic-search snippets, list/preview UIs, embeddings — everything that wants clean text |
| `description_md` (new) | **GitHub-Flavored Markdown** (small subset) | The full insight/recommendation detail views, which render it as structured content |

Why two fields (the team's rationale, which holds up technically):

- **Backward compatibility.** Existing API consumers and existing documents keep working
  untouched — `description` still means "plain text." Legacy docs simply lack `description_md`.
- **Integration-friendly.** Customers wiring DecisionBox into other tools read `description`
  and get clean, formatting-free text with no Markdown to strip.
- **Clean previews for free.** Because `description` stays plain, every compact/preview
  surface (search cards, spotlight, similar-items, list snippets) already shows clean text —
  **no client-side Markdown stripping is needed anywhere** (this removes a whole class of
  "raw `**` leaked into a card" bugs that a single-field design would have created). AC5 is
  satisfied structurally.
- **Embeddings stay clean.** `StandaloneInsight.BuildEmbeddingText()` keeps using
  `Description` (now guaranteed plain), so vectors are built over prose, not Markdown noise.

`description_md` is `omitempty` on every struct, so it is **purely additive** on the wire and
in BSON — no migration, and old documents deserialize unchanged.

### How both fields get populated (generation mechanism)

**Recommended: author once, derive the plain.** The analysis LLM keeps authoring into the
`description` field it already writes (every pack prompt's JSON schema example uses
`"description"`), but now authors it as Markdown — driven by the centralized discipline rule
(§4A), so **no per-pack prompt edits and custom analysis areas are covered too**. The agent
then splits it at parse time (`parseInsights` / recommendation parse):

```go
authored := ins.Description                 // Markdown, as instructed by the discipline rule
plain := mdtext.ToPlainText(authored)       // faithful plain reduction
ins.Description = plain                      // raw/plain — backward-compatible, integration-friendly
if plain != authored {                       // only when formatting was actually present
    ins.DescriptionMd = authored             // rich source for the detail view
}                                            // else leave DescriptionMd empty → detail view falls back to `description`
```

This guarantees the two fields are **always consistent** (the plain is literally the rich
content with formatting removed), needs **zero new output fields** from the LLM (most reliable
path), and yields a `description` that is a faithful, clean reduction of what's displayed.

**Alternative considered (reviewer's call):** instruct the LLM to emit **both** `description`
(plain) and `description_md` (Markdown) directly. Pro: `description` keeps the model's natural
plain phrasing; no Go stripper. Con: two independently-authored strings can drift, ~doubles
description tokens, and reliably emitting a second field not shown in the pack schema examples
is weaker for custom areas. I recommend the derive approach but will switch if the reviewer
prefers the LLM to author the plain version itself.

Both approaches **degrade gracefully**: if no Markdown is produced, `description_md` is empty
and the detail view renders `description` as a tidy paragraph (AC4).

## 3. Scope and key decisions (answering the issue's open questions)

Decisions made to keep the run moving; each has an off-ramp the reviewer can veto on this PR.

- **Q1 — Recommendations too?** **Yes, same PR.** `Recommendation.description` gets the same
  `description_md` sibling and the same rendering, because both sides are shared code (one
  renderer used on two detail pages; the already-parallel `discipline.RecommendationsRules()`).
  Shipping insights formatted while the recommendation beside it stays a wall of text would
  look broken. **Off-ramp:** drop the recommendation field + renderer wiring + rec rule block;
  nothing else changes.
- **Q2 — Anatomy: requirement or recommendation?** **Strong recommendation, not enforced.**
  The discipline rule asks for *takeaway → what's happening → why it matters → who's affected →
  contributing factors* **where the content supports it**; it does not reject insights that
  don't fit. Hard-enforcing a shape fights "older/plainer insights still look good" and
  one-sentence findings. No validation/verifier change rejects on structure.
- **Q3 — Formatting set.** **In:** bold/italic, bulleted + numbered lists, small sub-headings
  (rendered at h3/h4 scale only — never page-size), GFM tables, emphasized inline numbers
  (bold). **Out:** images, raw HTML, arbitrary embeds, oversized headings. **Links:** rendered
  as **plain, non-navigable text** in v1 and the model is told not to author them — fully
  satisfies the *Trust* requirement ("no surprising or unsafe links") with zero link surface.

## 4. Detailed changes

### 4A. Generation — `services/agent/internal/discipline/rules.go`

The discipline package is appended to every writer prompt at runtime by the orchestrator
(`buildAnalysisAreaPrompt` → `AppendAnalysisRules`, `buildRecommendationsPrompt` →
`AppendRecommendationsRules`, confirmed at `orchestrator.go:1280-1301`). It already reaches
**user-added custom analysis areas** that carry no pack content — the correct, drift-proof
injection point. Add a new rule block to **`analysisRulesText`** and the parallel one to
**`recommendationsRulesText`**:

- **`STRUCTURED MARKDOWN DESCRIPTION`** — the `description` field is authored as
  **GitHub-Flavored Markdown**. Reconcile the conflicting envelope phrasing explicitly,
  because 58 pack lines say *"Respond with ONLY valid JSON (no markdown, no explanations)"*:
  > The "no markdown" instruction above governs the **response envelope** — do not wrap the
  > JSON in ``` code fences and emit nothing outside the JSON object. It does **not** restrict
  > the *content of string fields*. The `description` value MUST be GitHub-Flavored Markdown.
  Because the discipline block is appended **last**, it has recency advantage over the pack's
  earlier phrasing — one clarification fixes all 58 sites without touching them.
- **Supported elements** (the Q3 set): a short bold lead/takeaway line with the headline
  number in **bold**; short paragraphs; `**bold**` / `*italic*`; `-`/`1.` lists; small
  sub-headings using `###`/`####` only (never `#`/`##`); GFM tables for small numeric
  comparisons. **Forbidden:** images, raw HTML, links, top-level `#`/`##` headings.
- **Anatomy (strong recommendation)** — where the finding supports it: one-line takeaway →
  what's happening (exact numbers) → why it matters → who's affected (segment + size) →
  optional contributing-factors list.
- **JSON-safety note** — newlines inside the `description` string MUST be JSON-escaped (`\n`);
  the response stays a single valid JSON object (protects the strict `json.Unmarshal`).
- **Defers to tone rules** — Markdown controls *structure*, not *tone*: bold is for
  numbers/keywords, never shouting; base-context Rule 8 (no emoji / `!` / all-caps; "critical"
  only in the `severity` field) still governs. The verifier's V4 editorial-language scan keys
  on dramatic *words* / `!` / emoji / all-caps — none are Markdown structural tokens, so
  `**67%**` and `### Why it matters` are **not** falsely rejected (verified against
  `verifierRulesText`).

The recommendation block mirrors this for `title`/`description`/`actions`, preserving the
existing `R. RELATED_INSIGHT_IDS` and non-dramatic-language clauses.

**Deliberately NOT doing (Rule 8):** editing the `"description"` example string in the 27
`analysis_*.md` + 4 `recommendations.md` pack files. The runtime append is authoritative and
reaches custom areas; per-pack edits invite drift — exactly what `discipline/rules.go` exists
to prevent.

### 4B. Generation — split into plain + Markdown (new Go helper)

- **New package `services/agent/internal/mdtext`** — `ToPlainText(md string) string`: a small,
  dependency-free reducer for the supported subset. Removes emphasis markers, heading hashes,
  list bullets/numbers, inline-code backticks and blockquote markers; flattens GFM tables to
  readable cell text; unwraps `[text](url)` → `text`; normalizes whitespace. License-free
  (no new dependency → no `.grant.yaml` churn). Thoroughly unit-tested.
- **`orchestrator.go` `parseInsights` (≈1116-1130)** and the recommendation parse
  (≈1204-1217): after the existing UUID/`DiscoveredAt` defaults, apply the split shown in §2
  — set `Description = ToPlainText(authored)` and populate `DescriptionMd` only when formatting
  was present. Done **at parse time**, so the stored & validated `description` is plain and
  `description_md` is the rich source; validation (which attaches a verdict, never rewrites the
  text) sees the plain `description` and behaves unchanged.

### 4C. Model fields (`description_md`, `omitempty` everywhere)

| File | Struct(s) | Add |
|------|-----------|-----|
| `services/agent/internal/models/discovery.go` | `Insight` (≈81), `Recommendation` (≈110) | `DescriptionMd string \`bson:"description_md,omitempty" json:"description_md,omitempty"\`` |
| `services/api/models/discovery.go` | `Insight` (≈44), `Recommendation` (≈61) | same |
| `libs/go-common/models/insight.go` | `StandaloneInsight` (≈29) | same |
| `libs/go-common/models/recommendation.go` | `StandaloneRecommendation` (≈23) | same |
| `services/agent/internal/discovery/phase_embed_index.go` | `denormalizeInsights` (≈72), `denormalizeRecommendations` | copy `DescriptionMd: ins.DescriptionMd` so the standalone collection carries it |
| `ui/dashboard/src/lib/api.ts` | `Insight` (≈382), `Recommendation` (≈471) | `description_md?: string;` |

`StandaloneInsight.BuildEmbeddingText()` is left using `Description` (now plain) — no change.
A JSON marshal/unmarshal round-trip test is added per the repo's "new model field" rule.

### 4D. Rendering — new shared component + wire the two detail views

**New `ui/dashboard/src/components/common/Markdown.tsx`** — a thin, safe wrapper over
`react-markdown` + `remark-gfm` (both already deps: `react-markdown@^10`, `remark-gfm@^4`),
with a component map styled from `src/styles/tokens.css` custom properties (no inline magic
colors — Rule 2 / TS standards), following the existing precedent at `ask/page.tsx:341-384`
minus the citation processing. Specifics:

- **Safety/Trust:** react-markdown v10 renders **no raw HTML** by default (no `rehype-raw`
  anywhere in the repo — confirmed) and sanitizes URLs, so it is XSS-safe. We map `a → <span>`
  (plain text, non-navigable) so even a stray link can't navigate (Q3 / Trust).
- **No oversized headings:** `h1/h2/h3 → h4-scale`, `h4 → smaller` (AC2/AC8).
- **Native typography:** sizes, ~1.6–1.7 line-height, list indent, table borders all from
  tokens, matching the Ask renderer and the surrounding Mantine cards (AC8).
- **Long content (AC6):** no `max-height`/clamp; tables wrapped in `overflow-x:auto` so wide
  tables scroll instead of breaking on small screens.

**Wire it in** (render `description_md` when present, else fall back to `description`):

| File | Line | Change |
|------|------|--------|
| `app/projects/[id]/discoveries/[runId]/insights/[insightId]/page.tsx` | 196-198 | `<Markdown>{insight.description_md || insight.description}</Markdown>`; quiet "No description" when both empty |
| `app/projects/[id]/discoveries/[runId]/recommendations/[recommendationId]/page.tsx` | 156-159 | `<Markdown>{recommendation.description_md || recommendation.description}</Markdown>` |

The insight detail page already orders content title → at-a-glance badges → description card →
Assessment → Indicators → Metrics → supporting detail (AC7 satisfied structurally); we only
upgrade the description card's body.

### 4E. Preview / compact contexts — no change needed

Because `description` stays plain, the snippet surfaces keep reading it and stay clean with
**no edits and no client-side stripping**: `components/search/ResultCard.tsx:54-56`,
`components/lists/SimilarItems.tsx:60-63`, `components/common/SpotlightSearch.tsx:250-257`,
`app/projects/[id]/recommendations/page.tsx:166-169` & `277-280`, and the
`app/projects/[id]/insights/page.tsx:123` text filter. This is the main simplification the
two-field design buys us. (A regression test still pins that a Markdown-bearing insight shows
plain text in a card — see §6.)

### 4F. Docs (Rule 4)

- `docs/reference/data-models.md` — Insight (line 37) and Recommendation (line 74): keep
  `description` as "plain text" and **add a `description_md` row** ("GitHub-Flavored Markdown
  rendition rendered in the dashboard; absent on plain/legacy insights").
- `docs/reference/api.md` — if it documents the insight/recommendation response shape, add
  `description_md`.
- `docs/concepts/discovery-lifecycle.md` (≈169 / 250) — note the description is authored as
  Markdown with a takeaway-first anatomy and stored split into `description` (plain) +
  `description_md` (Markdown).
- `docs/guides/customizing-prompts.md` and/or `creating-domain-packs.md` — note the platform
  appends Markdown-authoring guidance to every analysis/recommendation prompt (custom areas
  included) and that the agent derives the plain `description`.
- `CHANGELOG.md` — one `### Added` entry under `[Unreleased]`.

All docs stay `md` (not `mdx`), one sentence per line, links validated.

## 5. Implementation phases

1. **Model fields + round-trip tests.** Add `description_md` to the four Go structs +
   `lib/api.ts`; wire the two denormalize mappings. `make test-go`.
2. **`mdtext.ToPlainText` + tests.** New package, thorough unit tests.
3. **Agent rules + split + tests.** Markdown block in `analysisRulesText` /
   `recommendationsRulesText`; the parse-time split in `parseInsights` + recommendation parse;
   extend `discipline/rules_test.go`; add parse tests. `make test-go`.
4. **Dashboard component + tests.** `components/common/Markdown.tsx` + Jest tests. `make test-ui`.
5. **Wire renderer.** Swap the two detail-page description blocks; handle empty.
6. **Docs + CHANGELOG.**
7. **Local gates:** `make build`, `make test-go`, `make lint-go` (after
   `export PATH=$PATH:$(go env GOPATH)/bin`), `make test-ui`, `make lint-ui`,
   `cd ui/dashboard && npm run build`, and `make test-integration` (confirm the Mongo
   round-trip still serves insights with the new field).

## 6. Test strategy (Rule 9 — failure & edge cases, not just happy path)

**Go — `internal/mdtext`:** bold/italic/headings/lists/tables/inline-code/blockquote/links all
reduce to clean text with no residual `*`/`#`/`|`/`` ` ``; plain text passes through unchanged;
empty string → empty; a "no formatting" input is detected as equal to its reduction (so the
caller leaves `description_md` empty); a multi-paragraph input keeps readable spacing.

**Go — `discipline/rules_test.go`:** `AnalysisRules()` / `RecommendationsRules()` contain the
new section header + key clauses (Markdown-in-`description`, the envelope clarification, the
supported-set, the anatomy, the JSON-escape note); a guard test that the envelope
clarification is present; negative test that the new block introduces no emoji / `!` / dramatic
words (keeps it consistent with Rule 8, via the existing `assertNotContains`).

**Go — `internal/discovery` parse:** `parseInsights` with a Markdown description (escaped
newlines, bold, a small table) → `DescriptionMd` holds the Markdown verbatim and `Description`
holds the plain reduction (no `*`/`#`/`|`). **Edge:** a plain one-line description → `Description`
unchanged and `DescriptionMd` empty (AC4); empty description accepted. Same for the
recommendation parse. A model JSON round-trip test (marshal → unmarshal) asserts `description_md`
survives and is omitted when empty.

**Dashboard — Jest:**
- `components/common/Markdown`: `**x**`→`<strong>`, `*x*`→`<em>`, `- a`→list item, `### H`→
  small heading (assert *not* `<h1>`), GFM table → `<table>` (AC1/AC2); a plain paragraph →
  one `<p>` with no literal `*`/`#` (AC4); an HTML-injection string (`<img onerror>` /
  `<script>`) renders inert as text (Trust); a `[t](javascript:...)` link renders as
  non-navigable text (Q3/Trust).
- Extend `ResultCard.test.tsx`: with `description` plain (as it now always is) the card shows
  clean text — pins that previews never show Markdown even as the model starts emitting it
  (AC5).

**Integration (real testcontainer — Rule 9):** the change adds no new DB/warehouse/API
behavior, only a new string field, so no new integration target is warranted (Rule 8). The
existing `make test-integration` (agent + API Mongo round-trip) persists and serves the insight
struct — now with `description_md` — and must stay green; I will run it rather than add a
redundant container test.

## 7. Risks & mitigations

- **Stripper fidelity.** `mdtext.ToPlainText` must produce clean plain text for the controlled
  subset. *Mitigation:* the subset is small and we instruct exactly what the model emits; the
  reducer is line-oriented and exhaustively unit-tested; worst case a slightly awkward plain
  line, never broken UI. *Not* pulling in a full Markdown parser dependency (Rule 8 / license
  surface) for a reduction this small.
- **JSON parse fragility on multi-line strings.** Strict `json.Unmarshal` (`orchestrator.go:1112,1198`)
  needs `\n`-escaped newlines. *Mitigation:* the new rule requires escaped newlines + a single
  valid JSON object; modern models escape inside JSON; a parse test pins the escaped form. A
  custom newline-repair pass is out of scope (tracked follow-up if real failures appear — not a
  silent TODO).
- **Verifier false-rejects on Markdown symbols.** None: V4 keys on dramatic words / `!` / emoji
  / all-caps, and it scans the plain `description` (post-split) anyway. Covered above + by tests.
- **Field drift between `description` and `description_md`.** Eliminated by the derive approach
  (plain is computed from the Markdown). The LLM-authors-both alternative would reintroduce it —
  noted in §2.
- **Enterprise dashboard overlay.** The new `Markdown.tsx` is additive (never overlaid). The two
  deep route detail pages are unlikely to be overlaid (the documented example is `AppShell.tsx`),
  but I can't inspect the enterprise repo from this container. **Flagging for reviewer:** confirm
  whether `insights/[insightId]/page.tsx` or `recommendations/[recommendationId]/page.tsx` are
  overlaid; if so, port the one-line render edit.

## 8. Alternatives considered

- **Reuse the single `description` field for Markdown (no new field).** This was the original
  draft; **superseded by team direction** to keep `description` raw for backward compatibility +
  integration and add `description_md`. The two-field design is also strictly simpler on the UI
  (previews need no stripping).
- **LLM authors both `description` and `description_md` directly** (vs. derive). Trade-offs in
  §2 — recommended approach is derive; reviewer may flip it.
- **Edit every pack prompt instead of the central rule.** Rejected: 27+4 files, drift, misses
  custom areas.
- **Server-side render Markdown → HTML in the API.** Rejected: pushes HTML sanitization into Go;
  the dashboard owns presentation and react-markdown is safe by default on the client.
- **Reuse the Ask page's inline renderer directly.** Rejected: coupled to citation processing;
  a clean shared component is cheaper and leaves Ask untouched (Rule 8).
- **Hard-enforce the anatomy via the verifier.** Rejected per Q2.

## 9. Files touched (summary)

| Area | File | Change |
|------|------|--------|
| Model | `services/agent/internal/models/discovery.go` | + `DescriptionMd` on Insight + Recommendation |
| Model | `services/api/models/discovery.go` | + `DescriptionMd` on Insight + Recommendation |
| Model | `libs/go-common/models/insight.go`, `.../recommendation.go` | + `DescriptionMd` on the two Standalone structs |
| Model | `ui/dashboard/src/lib/api.ts` | + `description_md?: string` on Insight + Recommendation |
| Agent | `services/agent/internal/mdtext/` (+ test) | **new** — `ToPlainText` reducer |
| Agent | `services/agent/internal/discovery/orchestrator.go` | parse-time split (insights + recommendations) |
| Agent | `services/agent/internal/discovery/phase_embed_index.go` | copy `DescriptionMd` in both denormalize mappings |
| Agent | `services/agent/internal/discipline/rules.go` (+ test) | + Markdown-authoring block in analysis & recommendation rules |
| Agent tests | `services/agent/internal/discovery/*_test.go` | parse split + model round-trip tests |
| UI | `ui/dashboard/src/components/common/Markdown.tsx` (+ test) | **new** — safe GFM renderer |
| UI | `.../insights/[insightId]/page.tsx`, `.../recommendations/[recommendationId]/page.tsx` | description card → `<Markdown>` with `description_md || description` |
| UI tests | extend `src/__tests__/ResultCard.test.tsx` | preview stays plain |
| Docs | `docs/reference/data-models.md`, `docs/reference/api.md`, `docs/concepts/discovery-lifecycle.md`, `docs/guides/customizing-prompts.md` (+/or `creating-domain-packs.md`) | document `description_md` + Markdown authoring |
| Docs | `CHANGELOG.md` | `### Added` entry under `[Unreleased]` |

## 10. Acceptance-criteria traceability

| AC | Covered by |
|----|-----------|
| 1 — full view renders emphasis/lists/sub-headings/table, no raw symbols | 4D `Markdown.tsx` over `description_md`; component tests |
| 2 — lists indented, emphasis bold/italic, sub-headings separate, tables legible | 4D component map + tests |
| 3 — takeaway + headline numbers prominent in seconds | 4A anatomy (bold lead/number) + 4D typography |
| 4 — plain description → clean paragraph, no leftover symbols | fallback to plain `description`; empty `description_md`; component + parse tests |
| 5 — previews are clean plain-text snippets | 4E previews read plain `description` (no stripping); `ResultCard` test |
| 6 — very long description fully readable, not clipped | 4D no clamp; table overflow-scroll |
| 7 — detail view organized: title → key facts → description → detail | existing layout; description card upgraded |
| 8 — formatting matches product typography/spacing | 4D token-based styling, mirrors Ask renderer |

## 11. Out of scope / follow-ups

- Custom newline-repair in the JSON parser (only if real parse failures appear — tracked issue,
  not a TODO).
- Back-filling historical insights with a `description_md` (non-goal; they render fine as
  paragraphs via the fallback).
- Rich-text editing by end users (explicit non-goal).
- Images / embeds / navigable links (explicit non-goal / deferred per Q3).

---

This is a **PLAN for review** — no implementation is included in this PR. Once approved, the
build step implements it per the phases above, deletes this plan file, marks the PR ready, and
runs the Codex + Copilot review loop.

Closes #293

— Co-coded with Jale 🤖
