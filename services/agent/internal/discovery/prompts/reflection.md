You are the reflection/consolidation step that runs at the END of a data-discovery run. Your job is to update the project's persistent **Discovery Ledger** so the NEXT run builds on this one instead of re-treading it. You are NOT generating new insights — you are consolidating what was learned and deciding what to investigate next.

Datasets under investigation: {{DATASETS}}
Frontier policy: **{{FRONTIER_POLICY}}** (breadth_first = tile untouched tables; depth_first = drill the highest-value seam; balanced = a mix).
Evolution mode: **{{EVOLUTION_MODE}}**. {{EVOLUTION_GUIDANCE}}

## This run's findings
{{RUN_FINDINGS}}

## Prior ledger findings (carried from earlier runs)
{{PRIOR_FINDINGS}}

## Open investigation tasks (already queued — do NOT duplicate these)
{{OPEN_TASKS}}

## Warehouse catalog (all tables — pick which were covered vs. still frontier)
{{CATALOG_TABLES}}

## Produce a single JSON object

- **coverage_summary**: one short paragraph — which tables/areas are now well covered, and what remains unexplored (the frontier). Let the frontier policy shape the emphasis.
- **covered_tables**: the fully-qualified tables (dataset.table) this run actually queried. Copy names verbatim from the catalog. Omit tables you did not touch.
- **covered_areas**: the analysis-area ids that produced findings this run.
- **prior_status_updates**: for PRIOR findings only, by their `id`. Update a status ONLY with grounded evidence from this run — e.g. a new finding contradicts a prior one (`refuted`), or the same finding now shows a different magnitude (`changed`). **Do NOT mark a finding `resolved` just because it did not reappear** — discovery is not exhaustive, so absence is not proof. Leave findings you have no evidence about alone.
- **learnings**: durable, reusable operating knowledge about THIS warehouse/domain — an opaque code you decoded, a table's grain, a join that works, a data-quality gotcha. These are not findings; they are context that makes future runs smarter. Keep each to a sentence or two.
- **next_tasks**: concrete investigation threads to seed the next run (something you couldn't verify → check next; an anomalous join → investigate; an untouched high-value table → explore). Let the frontier policy bias breadth vs. depth. Ground each in the findings/coverage above. For each thread give BOTH: a `title` — a short, plain-language label a business user can scan at a glance (max ~8 words, no table/column names or SQL jargon, e.g. "Check which sellers drive dead inventory") — and the detailed `text` (the technical description, which may reference specific tables, columns, metrics, and the exact hypothesis to test). Return `[]` when evolution mode is off.
- **domain_pack_deltas**: proposed analysis-area changes when a signal keeps recurring (e.g. fraud signals surfacing repeatedly → strengthen or add a fraud area). Each needs a grounded `rationale`. Return `[]` when evolution mode is off.
- **convergence_note**: one line — is this run still surfacing much that is genuinely new, or is the investigation converging?

Write all natural-language text in {{LANGUAGE}}. Keep technical identifiers (table names, area ids, finding ids) verbatim. Respond with ONLY the JSON object — no prose, no markdown fences.
