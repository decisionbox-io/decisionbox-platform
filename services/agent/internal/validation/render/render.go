// Package render builds the "verification context" block — the column-grounded
// evidence the insight verifier (and, in a later PR, the SQL fixer) inserts
// into LLM prompts so the model sees real warehouse column names instead of
// having to guess them.
//
// Background: the verification phase used to receive only a table-level catalog
// (one line per table, no column names). On warehouses with non-English /
// non-obvious column conventions (e.g. an MSSQL Netsis-style ERP schema where
// every insight came back with `validation.status = "error"` and `Invalid
// column name 'TARIiH' / 'STHAR_SUBE' / …`), the model invented columns. This
// package renders the authoritative grounding evidence — the SQL of the
// exploration steps the analyst LLM cited as `source_steps` — so the verifier
// can adapt the existing, already-successfully-executed SQL into a
// `SELECT COUNT(...)` shape without inventing names.
//
// The full design lives in plans/PLAN-INSIGHT-VERIFICATION-GROUNDING.md; this
// package implements §4.1 (Layer 1) and is reused by §4.2 (Layer 2 fixer) and
// §4.3 (Layer 3 lookup_schema results).
package render

import (
	"fmt"
	"sort"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// DefaultBudgetChars is the soft cap for the rendered VerificationContext
// section. Pathological CTE-heavy ERP SQL is ~5–10k chars per step; 32k fits
// 4–6 steps comfortably plus framing overhead.
const DefaultBudgetChars = 32_000

// SectionHeader is the header that prefixes the source-queries block. Exported
// so tests (and the surrounding prompt) can detect the section boundary
// deterministically.
const SectionHeader = "## Source Exploration Queries"

// LookupSectionHeader is the header that prefixes the on-demand schema lookup
// block — populated by the verifier's tool loop in Layer 3 when the LLM asks
// `lookup_schema` to fetch column detail for tables the source-step queries
// did not touch. Rendered AFTER the source-queries section because Layer 1's
// evidence is more current than the cache (see §6.10 / §6.12 of the plan).
const LookupSectionHeader = "## On-Demand Schema Lookups"

// RuleInstruction is the column-grounding rule that follows the rendered
// evidence in the verifier's generation prompt. Exported so the verifier
// generation prompt can include it verbatim and tests can assert it didn't
// drift. Phrasing reconciles "use only columns from source queries" with
// the standard `COUNT(DISTINCT user_id)` shape when the project's filter
// field is documented elsewhere — the model gets a clear precedence rule.
const RuleInstruction = `**COLUMN GROUNDING — STRICT**:
- Prefer column names that appear in the source exploration queries above. Those queries already executed successfully against this warehouse, so their column references are authoritative.
- Do not invent new column names. If a column you would like to reference is not present in the source queries, fall back to a column documented in the table schemas section.
- Adapt one of the source queries into a SELECT COUNT(...) AS count over the same table(s). Do not query a table that does not appear above unless it is required for an aggregation that is impossible against the source tables alone.`

// RenderVerificationContext returns a markdown-formatted block of the source
// exploration queries that the insight cites, in the order the insight cited
// them. Returns the empty string when no cited step has executable SQL — the
// caller is expected to omit the surrounding section in that case.
//
// budgetChars caps the total rendered length. Pass DefaultBudgetChars unless
// the caller has a reason to tighten or relax the budget. When the rendered
// block would exceed the budget, drop the OLDEST cited step first (keeping the
// most recent — typically the LLM's narrowest-focused query that produced the
// numeric finding).
//
// Steps that don't appear in `log` are silently skipped (the analysis-step
// picker may have dropped a step but the insight still cited the index;
// silently skipping mirrors what `models.Insight.SourceSteps` is documented to
// allow). Steps whose Action is not "query_data" or whose Query is empty are
// also skipped — there's nothing to verify from a lookup_schema cycle.
func RenderVerificationContext(
	log []models.ExplorationStep,
	sourceStepIDs []int,
	budgetChars int,
) string {
	return RenderVerificationContextWithLookups(log, sourceStepIDs, nil, budgetChars)
}

// RenderVerificationContextWithLookups extends RenderVerificationContext with
// on-demand `lookup_schema` results gathered by the verifier's tool loop
// (Layer 3). The lookup block follows the source-queries block — Layer 1's
// SQL is more current than the cache, so when both are present the verifier
// is told to prefer source-query columns. Either or both inputs may be empty;
// when both are empty the function returns "" and the caller is expected to
// strip the surrounding section header.
//
// budgetChars caps the source-queries block (Layer 1). Lookup detail is
// rendered after with its own framing — its size is bounded indirectly by
// MaxLookupTablesPerCall on the SchemaProvider side, which keeps single
// lookups within ~1–2k tokens. We do not re-budget the lookup block here.
func RenderVerificationContextWithLookups(
	log []models.ExplorationStep,
	sourceStepIDs []int,
	lookups []ai.LookupTable,
	budgetChars int,
) string {
	if budgetChars <= 0 {
		budgetChars = DefaultBudgetChars
	}

	indexed := indexLog(log)
	steps := pickCitedSteps(indexed, sourceStepIDs)
	steps = trimToBudget(steps, budgetChars)

	sourceBlock := ""
	if len(steps) > 0 {
		sourceBlock = renderSection(steps)
	}

	lookupBlock := renderLookupSection(lookups)

	switch {
	case sourceBlock == "" && lookupBlock == "":
		return ""
	case sourceBlock == "":
		return lookupBlock
	case lookupBlock == "":
		return sourceBlock
	default:
		return sourceBlock + lookupBlock
	}
}

func renderLookupSection(lookups []ai.LookupTable) string {
	if len(lookups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(LookupSectionHeader)
	b.WriteString("\n\n")
	for _, t := range lookups {
		fmt.Fprintf(&b, "### `%s`\n\n", t.Table)
		if len(t.Columns) > 0 {
			b.WriteString("Columns: ")
			cols := make([]string, 0, len(t.Columns))
			for _, c := range t.Columns {
				if c.Type != "" {
					cols = append(cols, fmt.Sprintf("%s %s", c.Name, c.Type))
				} else {
					cols = append(cols, c.Name)
				}
			}
			b.WriteString(strings.Join(cols, ", "))
			b.WriteString(".\n\n")
		}
		if t.RowCount >= 0 {
			fmt.Fprintf(&b, "Approximate row count: %d.\n\n", t.RowCount)
		}
	}
	return b.String()
}

func indexLog(log []models.ExplorationStep) map[int]models.ExplorationStep {
	out := make(map[int]models.ExplorationStep, len(log))
	for _, s := range log {
		out[s.Step] = s
	}
	return out
}

// pickCitedSteps walks `sourceStepIDs` (the order the insight cited them) and
// returns matching log entries that actually executed a query. Order is the
// citation order — duplicates are de-duped, the first occurrence wins.
func pickCitedSteps(indexed map[int]models.ExplorationStep, ids []int) []models.ExplorationStep {
	seen := make(map[int]bool, len(ids))
	out := make([]models.ExplorationStep, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s, ok := indexed[id]
		if !ok {
			continue
		}
		if !isExecutableQueryStep(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func isExecutableQueryStep(s models.ExplorationStep) bool {
	if strings.TrimSpace(s.Query) == "" {
		return false
	}
	if s.Action != "" && s.Action != "query_data" {
		return false
	}
	return true
}

// trimToBudget returns the subset of `steps` whose total rendered size is
// within budget, dropping the OLDEST step number first (lowest Step value)
// when the budget is exceeded. Citation order within the kept set is
// preserved.
//
// The size accounting is precomputed once per step and the running total is
// updated in place each time a step is dropped, so the loop runs in O(n log n)
// (the sort) instead of re-rendering the entire section per drop iteration.
func trimToBudget(steps []models.ExplorationStep, budgetChars int) []models.ExplorationStep {
	if len(steps) == 0 {
		return steps
	}

	headerSize := len(SectionHeader) + len("\n\n")
	perStepSize := make(map[int]int, len(steps))
	total := headerSize
	for _, s := range steps {
		var b strings.Builder
		writeStep(&b, s)
		sz := b.Len()
		perStepSize[s.Step] = sz
		total += sz
	}
	if total <= budgetChars {
		return steps
	}

	// Drop oldest Step first. Sort a copy ascending; citation order in the
	// returned slice is preserved by walking the original `steps`.
	byStepAsc := make([]models.ExplorationStep, len(steps))
	copy(byStepAsc, steps)
	sort.SliceStable(byStepAsc, func(i, j int) bool {
		return byStepAsc[i].Step < byStepAsc[j].Step
	})

	dropped := make(map[int]bool, len(steps))
	for total > budgetChars && len(dropped) < len(steps) {
		for _, candidate := range byStepAsc {
			if dropped[candidate.Step] {
				continue
			}
			dropped[candidate.Step] = true
			total -= perStepSize[candidate.Step]
			break
		}
	}

	out := make([]models.ExplorationStep, 0, len(steps)-len(dropped))
	for _, s := range steps {
		if dropped[s.Step] {
			continue
		}
		out = append(out, s)
	}
	return out
}

func renderSection(steps []models.ExplorationStep) string {
	if len(steps) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(SectionHeader)
	b.WriteString("\n\n")
	for _, s := range steps {
		writeStep(&b, s)
	}
	return b.String()
}

func writeStep(b *strings.Builder, s models.ExplorationStep) {
	purpose := strings.TrimSpace(s.QueryPurpose)
	if purpose == "" {
		purpose = "(no purpose recorded)"
	}
	fmt.Fprintf(b, "### Step %d — %s\n\n", s.Step, purpose)
	b.WriteString("```sql\n")
	b.WriteString(strings.TrimRight(s.Query, "\n"))
	b.WriteString("\n```\n\n")
	if s.Error != "" {
		fmt.Fprintf(b, "Returned an error: %s\n\n", s.Error)
		return
	}
	fmt.Fprintf(b, "Returned %d row(s).\n\n", s.RowCount)
}
