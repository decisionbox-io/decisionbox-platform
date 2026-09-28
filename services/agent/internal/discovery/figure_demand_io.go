package discovery

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The prompt, the reply shape and the parse for the declaration demand.
//
// Kept beside the pass rather than in repair_prompt.go because the two prompts ask
// for opposite things: a repair may change the prose and must not change the
// declarations' subject, while a demand may change only the declarations and must not
// touch a word of the prose.

type figureDemandDeclaration struct {
	Insight int                `json:"insight"`
	Claim   models.FigureClaim `json:"-"`
	Raw     json.RawMessage    `json:"-"`
}

type figureDemandLabel struct {
	Insight int    `json:"insight"`
	Figure  string `json:"figure"`
	Why     string `json:"why"`
}

type figureDemandReply struct {
	Declarations    []figureDemandDeclaration
	NotMeasurements []figureDemandLabel
}

// wireDemandReply is the shape on the wire. The declaration entries carry an
// `insight` index alongside the ordinary FigureClaim fields, flat rather than nested,
// because a flat list with an explicit index survives a model that loses track of
// nesting and a nested one does not.
type wireDemandReply struct {
	Declarations    []map[string]json.RawMessage `json:"declarations"`
	NotMeasurements []figureDemandLabel          `json:"not_measurements"`
}

func parseFigureDemandReply(response string) (figureDemandReply, error) {
	var out figureDemandReply
	vals, err := jsonValues(cleanJSONResponse(response))
	if err != nil {
		return out, err
	}
	if len(vals) == 0 {
		return out, fmt.Errorf("the reply held no JSON value")
	}
	var wire wireDemandReply
	var firstErr error
	for i, v := range vals {
		var w wireDemandReply
		if derr := json.Unmarshal(v, &w); derr != nil {
			if i == 0 {
				firstErr = derr
			}
			continue
		}
		if len(w.Declarations) > 0 || len(w.NotMeasurements) > 0 {
			wire = w
			firstErr = nil
			break
		}
		if i == 0 {
			wire = w
		}
	}
	if firstErr != nil {
		return out, firstErr
	}

	for _, d := range wire.Declarations {
		idxRaw, ok := d["insight"]
		if !ok {
			continue
		}
		var idx int
		if err := json.Unmarshal(idxRaw, &idx); err != nil {
			continue
		}
		// The rest of the object is an ordinary FigureClaim. Re-marshalled and
		// decoded through the real type so the demand path and the analysis path
		// cannot drift into reading the same JSON differently.
		delete(d, "insight")
		body, merr := json.Marshal(d)
		if merr != nil {
			continue
		}
		var claim models.FigureClaim
		if err := json.Unmarshal(body, &claim); err != nil {
			continue
		}
		if strings.TrimSpace(claim.Figure) == "" || claim.Step == 0 || strings.TrimSpace(claim.Kind) == "" {
			continue
		}
		out.Declarations = append(out.Declarations, figureDemandDeclaration{Insight: idx, Claim: claim})
	}
	for _, l := range wire.NotMeasurements {
		if strings.TrimSpace(l.Figure) == "" {
			continue
		}
		out.NotMeasurements = append(out.NotMeasurements, l)
	}
	return out, nil
}

// parseDemandValue reads a numeral out of the text a label names, so a label can be
// matched against the demand list by value rather than by string.
func parseDemandValue(s string) (float64, bool) {
	sel, ok := selectNumeralAny(s)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(sel.digits, ",", ""), 64)
	if err != nil {
		return 0, false
	}
	if scale, scaled := numeralScale[sel.suffix]; scaled {
		v *= scale
	}
	return v, true
}

// selectNumeralAny reads the first numeral in a string, for the case where there is
// no authoritative value to match against. Used only for label text, where the model
// is echoing a numeral back and there is nothing to disambiguate it with.
func selectNumeralAny(s string) (selectedNumeral, bool) {
	m := reNumeral.FindStringSubmatchIndex(s)
	if m == nil {
		return selectedNumeral{}, false
	}
	digits := group(s, m, 3)
	if digits == "" {
		return selectedNumeral{}, false
	}
	suffix := strings.ToLower(strings.TrimSpace(strings.Trim(group(s, m, 4), "^$")))
	return selectedNumeral{digits: digits, suffix: strings.TrimRight(suffix, ".,;:)")}, true
}

func figureDemandResponseFormat() *gollm.ResponseFormat {
	num := func(d string) map[string]interface{} {
		return map[string]interface{}{"type": "number", "description": d}
	}
	str := func(d string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": d}
	}
	return &gollm.ResponseFormat{
		Name:   "figure_declarations",
		Strict: false,
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"declarations": map[string]interface{}{
					"type":        "array",
					"description": "One entry per listed figure you can name the arithmetic for",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"insight": map[string]interface{}{"type": "integer", "description": "The numbered finding this figure appears in"},
							"figure":  str("The figure as listed"),
							"value":   num("The figure in the units of the step's own column"),
							"step":    map[string]interface{}{"type": "integer", "description": "Exploration step whose rows produced it"},
							"kind":    str(`One of "cell", "sum", "count", "ratio", "diff"`),
							"column":  str("Column the arithmetic runs over"),
							"row":     str("Terms selecting one row, same grammar as filter"),
							"other":   str("Terms selecting a second row, for diff or a ratio denominator"),
							"scope":   str("Which rows a sum, count or ratio denominator covers"),
							"pct":     map[string]interface{}{"type": "boolean", "description": "True when written as a percentage rather than a fraction"},
						},
						"required": []string{"insight", "figure", "value", "step", "kind"},
					},
				},
				"not_measurements": map[string]interface{}{
					"type":        "array",
					"description": "One entry per listed figure that measures nothing — a bucket edge, a top-N bound, an identifier",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"insight": map[string]interface{}{"type": "integer", "description": "The numbered finding this figure appears in"},
							"figure":  str("The figure as listed"),
							"why":     str("What it is instead, in a few words"),
						},
						"required": []string{"insight", "figure", "why"},
					},
				},
			},
			"required": []string{"declarations", "not_measurements"},
		},
	}
}

// buildFigureDemandPrompt lists, per finding, the numerals whose origin the model did
// not state, and asks for arithmetic or an explicit dismissal.
//
// It shows the sentences the numerals appear in and the rows of the cited steps, and
// it says plainly that the prose is fixed. That last point is the prompt's whole
// safety property: a model allowed to edit the text would answer a demand for
// provenance by deleting the number, which converts an unexplained figure into no
// figure and reads downstream as a clean document.
func buildFigureDemandPrompt(insights []models.Insight, wanted map[int][]numeralHit, steps []models.ExplorationStep) string {
	var b strings.Builder

	b.WriteString("## Some numbers in these findings have no stated origin\n\n")
	b.WriteString("You wrote the findings below and declared the arithmetic behind some of their figures. ")
	b.WriteString("The numbers listed here appear in the prose with no declaration, so nothing can check them. ")
	b.WriteString("For each one, either name the arithmetic over the rows you cited, or say that it measures nothing.\n\n")
	b.WriteString("This is not a request to change the findings. **The prose is fixed — every sentence stays exactly ")
	b.WriteString("as written.** Only the declarations are being added. A figure you cannot account for should be ")
	b.WriteString("reported as such rather than explained away: an honest gap is useful and a wrong declaration is not.\n\n")

	idxs := make([]int, 0, len(wanted))
	for i := range wanted {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	for _, idx := range idxs {
		if idx < 1 || idx > len(insights) {
			continue
		}
		ins := insights[idx-1]
		fmt.Fprintf(&b, "### Finding %d — %s\n\n", idx, ins.Name)
		fmt.Fprintf(&b, "Cited steps: %v\n\n", ins.SourceSteps)
		b.WriteString("Undeclared figures:\n\n")
		for _, h := range wanted[idx] {
			fmt.Fprintf(&b, "- `%s`", h.raw)
			if ctx := sentenceContaining(ins, h.raw); ctx != "" {
				fmt.Fprintf(&b, " — in: %q", ctx)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("### The rows you cited\n\n")
	b.WriteString(strings.Replace(digestLegend, "## Reading", "#### Reading", 1))
	b.WriteString("```json\n")
	b.WriteString(RenderCompactedSteps(steps))
	b.WriteString("\n```\n\n")

	b.WriteString("### What to send back\n\n")
	b.WriteString("A single JSON object with two arrays and nothing else:\n\n")
	b.WriteString("```json\n{\n")
	b.WriteString("  \"declarations\": [\n")
	b.WriteString("    {\"insight\": 1, \"figure\": \"131,800\", \"value\": 131800, \"step\": 25, \"kind\": \"diff\",\n")
	b.WriteString("     \"column\": \"revenue\", \"row\": \"yr = 1997\", \"other\": \"yr = 1996\"}\n")
	b.WriteString("  ],\n")
	b.WriteString("  \"not_measurements\": [\n")
	b.WriteString("    {\"insight\": 1, \"figure\": \"30\", \"why\": \"the upper edge of the 0-30 day bucket I named\"}\n")
	b.WriteString("  ]\n}\n```\n\n")
	b.WriteString("`insight` is the finding's number above. Use the same `kind` grammar as the analysis contract:\n\n")
	b.WriteString("- `cell` — one cell: `column` plus a `row` selecting exactly one row.\n")
	b.WriteString("- `sum` — the total of `column` over `scope`, or over every row.\n")
	b.WriteString("- `count` — how many rows are in `scope`.\n")
	b.WriteString("- `ratio` — `column` in `row`, over the same column in `other`, or over the column total across `scope`.\n")
	b.WriteString("- `diff` — `column` in `row` minus `column` in `other`. Both selectors required.\n\n")
	b.WriteString("Put every listed figure in exactly one of the two arrays. ")
	b.WriteString("Do not declare a figure that is not on a list — those are already accounted for. ")
	b.WriteString("Do not return the findings themselves.\n")

	return b.String()
}

// sentenceContaining finds the sentence a numeral appears in, so the demand quotes the
// prose back rather than asking about a bare number. Shortened, because the point is
// to locate the figure and not to re-send the document.
func sentenceContaining(ins models.Insight, token string) string {
	fields := append([]string{ins.Name, ins.Description}, ins.Indicators...)
	for _, f := range fields {
		for _, line := range strings.Split(f, "\n") {
			for _, sentence := range strings.SplitAfter(line, ". ") {
				if len(standaloneNumber(sentence, token)) > 0 {
					return clip(strings.TrimSpace(sentence), 180)
				}
			}
		}
	}
	return ""
}
