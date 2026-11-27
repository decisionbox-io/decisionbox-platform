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
//
//	{{DIALECT}}                       — warehouse SQL dialect name
//	{{LANGUAGE}}                      — discovery output language (defaults to "English" when empty)
//	{{FILTER_CLAUSE}}                 — rendered SQL predicate (or empty)
//	{{FILTER_FIELD}}                  — name of the filter column
//	{{FILTER_VALUE}}                  — value of the filter
//	{{SOURCE_STEPS_TRUNCATION_NOTICE}} — non-empty only when SourceStepsTruncated
//	{{NUMERIC_TOLERANCE_PCT}}         — relative tolerance as an integer percentage (e.g. "20")
//	{{MIN_SAMPLE_SIZE}}               — min row population for refuter counter-evidence (e.g. "30")
//	{{QUERY_CONTRACT}}                — the query_warehouse example and its shape rule, in the source's language
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
		"{{QUERY_CONTRACT}}", queryContract(mode, b.Warehouse.QueryLanguage, b.Warehouse.QueryGuide),
	}
	out := template
	// `replacements` is built as alternating key/value pairs immediately
	// above; len is always even. gosec G602's "slice index out of range"
	// is a false positive — the i+=2 stride keeps i+1 in range — but a
	// defensive guard is cheaper than a long nolint comment.
	for i := 0; i+1 < len(replacements); i += 2 {
		out = strings.ReplaceAll(out, replacements[i], replacements[i+1])
	}
	return out
}

// queryContract is the query_warehouse example and its shape rule.
//
// For a SQL warehouse it is the text each frame always carried, to the byte.
// For a source whose queries are not SQL, a SELECT example would be the most
// concrete instruction in the frame and the model would follow it into a
// query the source rejects, so the example names the language instead.
func queryContract(mode valmodels.AgentMode, nonSQLLanguage, guide string) string {
	if nonSQLLanguage == "" {
		if mode == valmodels.ModeRefuter {
			return "```json\n{\"query_warehouse\": \"SELECT * FROM `dataset.table` WHERE …\"}\n```\n\n" +
				"`query_warehouse` is a BARE SQL STRING. Not an object, not nested. Just the SQL."
		}
		return "```json\n{\"query_warehouse\": \"SELECT col, COUNT(*) AS n FROM `dataset.table` GROUP BY col\"}\n```\n\n" +
			"`query_warehouse` is a BARE SQL STRING. NOT `{\"sql\": \"...\"}`, NOT `{\"query\": \"...\"}`. Just the SQL."
	}
	contract := "```json\n{\"query_warehouse\": \"<one request written in " + nonSQLLanguage + ">\"}\n```\n\n" +
		"`query_warehouse` is a BARE STRING holding one request written in " + nonSQLLanguage + " — not SQL, which this source rejects. " +
		"The exploration steps in the evidence show requests in this language; adapt them. " +
		"If the request is a JSON object, put it inside the string with its quotes escaped; never nest it as an object."
	if guide != "" {
		contract += "\n\nHow to write a request in " + nonSQLLanguage + ":\n\n" + guide
	}
	return contract
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
