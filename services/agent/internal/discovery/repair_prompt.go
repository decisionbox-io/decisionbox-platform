package discovery

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// buildInsightRepairPrompt asks for one insight back, rewritten so that every
// statement it makes is true of the rows it already cited.
//
// It carries the evaluator's own reason string verbatim, including the names of
// the rows that refuted the claim. That detail is the whole value of the prompt:
// told only "2 rows, not 1", the model has to re-find the counter-examples, and
// re-finding them over a table sorted by the wrong column is precisely the step
// it got wrong when it wrote the claim. Told "Bookcases, Tables", it has nothing
// left to search for.
//
// The prompt is scoped to one insight and to the steps its failing claims cite,
// not the whole area. An area prompt is the large one by construction, and
// re-running it would regenerate the sound insights beside the refuted one --
// making the change unattributable and the measurement meaningless.
//
// Within the insight it is scoped again, to the contradicted sentences. That
// narrowing is the one thing the first repair measurement found wrong: fixing a
// false superlative, the model also rewrote an untouched neighbour, turning
// "the fourth-highest sales among the top-10 sub-categories by revenue" into
// "substantial revenue". Tables really is rank 4, so a true and specific claim
// was discarded while a false one was corrected -- and nothing downstream can
// catch it, because a vague claim is not a refuted one. Hence the instruction is
// stated three times over: as the opening constraint, in the list of what may
// not be touched, and by naming that exact substitution as the failure.
func buildInsightRepairPrompt(ins models.Insight, failed []models.QuantifierVerdict, steps []models.ExplorationStep) string {
	var b strings.Builder

	b.WriteString("## A claim in this finding is contradicted by its own evidence\n\n")
	b.WriteString("You wrote the finding below, and declared what each of its quantifier claims rests on. ")
	b.WriteString("The platform evaluated each declared predicate over the **full rows** of the step it cited. ")
	b.WriteString("These claims do not hold:\n\n")
	for i, v := range failed {
		b.WriteString(fmt.Sprintf("%d. %q — declared as `%s` against step %d\n", i+1, v.Claim, v.Kind, v.Step))
		b.WriteString(fmt.Sprintf("   → %s\n", v.Reason))
	}
	b.WriteString("\nThe rows are not in question. They are the rows you were given, complete and correct, ")
	b.WriteString("and the arithmetic above was done over all of them. What is in question is the sentence.\n\n")

	b.WriteString("### The finding, as you wrote it\n\n```json\n")
	b.WriteString(repairInsightJSON(ins))
	b.WriteString("\n```\n\n")

	b.WriteString("### The rows the claims were evaluated over\n\n")
	// The legend is written as a top-level section for the area prompt, where it
	// is one. Here it sits inside one, so its heading is demoted to keep the
	// document's levels consistent -- the text is identical either way.
	b.WriteString(strings.Replace(digestLegend, "## Reading", "#### Reading", 1))
	b.WriteString("```json\n")
	b.WriteString(RenderCompactedSteps(steps))
	b.WriteString("\n```\n\n")

	b.WriteString("### Rewrite it\n\nReturn this one finding with **only the contradicted sentences changed**. ")
	b.WriteString("Every other sentence must come back word for word as you wrote it — same figures, same names, same ")
	b.WriteString("wording. A sentence not listed above was not questioned, and shortening it, generalising it or ")
	b.WriteString("dropping a figure out of it throws away something that was right.\n\nFor the sentences that are ")
	b.WriteString("contradicted, you may:\n\n")
	b.WriteString("- correct a number, a name, a rank or a direction to what the rows say;\n")
	b.WriteString("- narrow the claim to a scope that is true — `top_n` / `top_n_column` if the claim is about the largest few;\n")
	b.WriteString("- weaken it to a claim that holds (\"one of the loss-making lines\" rather than \"the only\");\n")
	b.WriteString("- delete the sentence and keep the rest of the finding.\n\n")
	b.WriteString("You may not:\n\n")
	b.WriteString("- change `source_steps`. The evidence is fixed; the sentence is what changes. A claim moved to a different step is a different claim.\n")
	b.WriteString("- keep a contradicted sentence and drop its `quantifier_claims` entry. An undeclared claim is not thereby true, and a rewrite that removes the check instead of the error is rejected and does not count as a round.\n")
	b.WriteString("- invent a figure no step returned.\n")
	b.WriteString("- touch a sentence that is not listed above. Replacing a specific figure with a vague phrase " +
		"(\"substantial revenue\" for \"the fourth-highest sales\") loses a true claim while fixing a false one, " +
		"and is the one failure this instruction exists to prevent.\n\n")
	// The contract goes in the repair prompt too. Without it the rewrite is asked
	// to re-declare its claims under rules it cannot see: the area prompt carried
	// them, this call is a fresh conversation, and a model reaching for the
	// nearest kind it can remember is how the misdeclared `monotonic` for "ran a
	// loss in every year" arose in the first place. Re-stating it is ~1.5KB
	// against a prompt whose whole purpose is that a declaration be right.
	b.WriteString(quantifierContract)
	b.WriteString("Re-declare `quantifier_claims` for the text you actually write. ")
	b.WriteString("Respond with ONLY a single JSON object of the form ")
	b.WriteString("`{\"insights\": [ <the one rewritten finding> ]}` — no prose, no markdown fences.\n")

	return b.String()
}

// repairInsightJSON renders the insight as the model authored it: the fields it
// is allowed to change, and nothing else.
//
// The derived fields are withheld on purpose. `evidence_checks` would show the
// model the verdicts in a shape it could then emit back, and the prompt above
// already states each failure in prose. `evidence_quality` and `id` are not
// things a rewrite may touch, and a field shown in the input is a field the model
// reasonably assumes it should return.
func repairInsightJSON(ins models.Insight) string {
	body := map[string]any{
		"name":         ins.Name,
		"severity":     ins.Severity,
		"source_steps": ins.SourceSteps,
	}
	// Prefer the Markdown rendition the model authored; Description is the
	// plain-text reduction derived from it, so echoing that back would ask the
	// model to re-author its own formatting from a stripped copy.
	if ins.DescriptionMd != "" {
		body["description"] = ins.DescriptionMd
	} else {
		body["description"] = ins.Description
	}
	if ins.AffectedCount != 0 {
		body["affected_count"] = ins.AffectedCount
	}
	if ins.RiskScore != 0 {
		body["risk_score"] = ins.RiskScore
	}
	if ins.Confidence != 0 {
		body["confidence"] = ins.Confidence
	}
	if ins.TargetSegment != "" {
		body["target_segment"] = ins.TargetSegment
	}
	if len(ins.Indicators) > 0 {
		body["indicators"] = ins.Indicators
	}
	if len(ins.Metrics) > 0 {
		body["metrics"] = ins.Metrics
	}
	if len(ins.QuantifierClaims) > 0 {
		body["quantifier_claims"] = ins.QuantifierClaims
	}
	out, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		// Metrics is free-form and could in principle hold something
		// unmarshalable. The name alone still identifies which finding is
		// being repaired, and the failures above still say what is wrong.
		return fmt.Sprintf("{\n  %q: %q\n}", "name", ins.Name)
	}
	return string(out)
}
