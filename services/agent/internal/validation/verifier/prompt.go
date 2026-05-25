package verifier

import (
	"embed"
	"fmt"
	"strings"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
)

//go:embed prompts/verifier.md prompts/refuter.md
var promptFS embed.FS

// renderSystemPrompt substitutes per-bundle placeholders into the
// mode's system frame. The system frame stays constant across rounds
// for a single bundle; only the user message (history + next-turn
// instructions) changes between rounds.
//
// Placeholders handled:
//   {{DIALECT}}                       — warehouse SQL dialect name
//   {{LANGUAGE}}                      — discovery output language (defaults to "English" when empty)
//   {{FILTER_CLAUSE}}                 — rendered SQL predicate (or empty)
//   {{FILTER_FIELD}}                  — name of the filter column
//   {{FILTER_VALUE}}                  — value of the filter
//   {{SOURCE_STEPS_TRUNCATION_NOTICE}} — non-empty only when SourceStepsTruncated
//   {{NUMERIC_TOLERANCE_PCT}}         — relative tolerance as an integer percentage (e.g. "20")
//   {{MIN_SAMPLE_SIZE}}               — min row population for refuter counter-evidence (e.g. "30")
func renderSystemPrompt(template string, mode valmodels.AgentMode, b Bundle, numericTolerance float64, minSampleSize int) string {
	language := b.Discovery.Language
	if language == "" {
		language = "English"
	}
	filterClause := b.Warehouse.Filter
	if filterClause == "" && b.Warehouse.FilterField != "" && b.Warehouse.FilterValue != "" {
		filterClause = b.Warehouse.FilterField + " = '" + b.Warehouse.FilterValue + "'"
	}
	notice := ""
	if b.SourceStepsTruncated {
		docKind := "insight"
		if b.Doc.Kind == valmodels.DocRecommendation {
			docKind = "recommendation"
		}
		notice = fmt.Sprintf("**Important — Evidence omitted**: %d source step(s) cited by this %s were omitted from this bundle to stay within token budget. For any claim that depends on omitted steps, mark `unverifiable`.", b.SourceStepsOmitted, docKind)
	}
	tol := int(numericTolerance * 100)
	if tol < 0 {
		tol = 0
	}
	if minSampleSize < 0 {
		minSampleSize = 0
	}
	replacements := []string{
		"{{DIALECT}}", b.Warehouse.Dialect,
		"{{LANGUAGE}}", language,
		"{{SOURCE_STEPS_TRUNCATION_NOTICE}}", notice,
		"{{FILTER_CLAUSE}}", filterClause,
		"{{FILTER_FIELD}}", b.Warehouse.FilterField,
		"{{FILTER_VALUE}}", b.Warehouse.FilterValue,
		"{{NUMERIC_TOLERANCE_PCT}}", fmt.Sprintf("%d", tol),
		"{{MIN_SAMPLE_SIZE}}", fmt.Sprintf("%d", minSampleSize),
	}
	out := template
	for i := 0; i < len(replacements); i += 2 {
		out = strings.ReplaceAll(out, replacements[i], replacements[i+1])
	}
	return out
}

// loadSystemPrompts reads and returns the two embedded system frames
// keyed by AgentMode. Called once at Agent construction; the frames
// are immutable thereafter.
func loadSystemPrompts() (map[valmodels.AgentMode]string, error) {
	verifier, err := promptFS.ReadFile("prompts/verifier.md")
	if err != nil {
		return nil, fmt.Errorf("read verifier prompt: %w", err)
	}
	refuter, err := promptFS.ReadFile("prompts/refuter.md")
	if err != nil {
		return nil, fmt.Errorf("read refuter prompt: %w", err)
	}
	return map[valmodels.AgentMode]string{
		valmodels.ModeVerifier: string(verifier),
		valmodels.ModeRefuter:  string(refuter),
	}, nil
}
