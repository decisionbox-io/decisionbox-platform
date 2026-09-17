package ai

// The get_correlations action: what somebody has already decided about
// correlating two of this run's datasources.
//
// A hop between datasources is only sound if the field the values came from
// identifies the same thing on both sides, and the exploration agent chooses
// that field from names alone. Two systems agreeing on a name and disagreeing
// on everything else is how a confidently wrong correlation gets made, and it
// is not a mistake the agent can see from where it sits.
//
// Somebody may have looked. This action asks, before the hop rather than after
// it, and the answer is written to be acted on: a rejected pairing arrives as a
// prohibition with its reason, not as a data point.
//
// Advisory in the sense that a tool is only consulted if the model calls it —
// nothing here refuses a query. What the wording can do, it does.

import (
	"context"
	"fmt"
	"strings"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	logger "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// DefaultMaxCorrelationLookupsPerRun caps get_correlations calls across a run.
//
// Low next to the schema budgets on purpose: the answer for a pair does not
// change during a run, and there are only so many pairs. A model re-asking
// past this has stopped using the answer, and the budget line tells it so.
const DefaultMaxCorrelationLookupsPerRun = 12

// CorrelationLookupFunc answers what has been decided about correlating two
// datasources. Supplied by the caller that knows the project, so this package
// neither reaches for a plugin registry nor carries a project id.
//
// Optional, like SchemaProvider: nil is a wiring where nobody can answer, and
// the engine says so rather than pretending the answer was "nothing decided".
// An error means the answer could not be read — never a reason to fail a run.
type CorrelationLookupFunc func(ctx context.Context, a, b string) ([]agentplugin.CorrelationKey, error)

// CorrelationPair is the get_correlations payload: the two datasources to ask
// about. An object rather than a positional array because a model that
// transposes two bare ids produces a question nobody can see is the wrong one.
type CorrelationPair struct {
	A string `json:"a"`
	B string `json:"b"`
}

// executeGetCorrelations serves a get_correlations action. The result string
// becomes the next user message; its shape is part of the prompt contract
// written by the multi-warehouse routing section.
func (e *ExplorationEngine) executeGetCorrelations(
	ctx context.Context,
	action *ExplorationAction,
	step *models.ExplorationStep,
) string {
	step.QueryPurpose = "get_correlations"

	if e.correlationLookup == nil {
		step.Error = "correlation lookup not configured"
		return "Correlation lookup is unavailable on this run, so there is nothing recorded to check " +
			"against. Treat any cross-datasource key as unverified: prefer one whose values you have " +
			"seen line up on both sides, and say so when you report a correlation built on it."
	}

	if e.maxCorrelationLookupsPerRun > 0 && e.correlationLookupsUsed >= e.maxCorrelationLookupsPerRun {
		step.Error = fmt.Sprintf("correlation lookup budget exhausted (%d/%d)",
			e.correlationLookupsUsed, e.maxCorrelationLookupsPerRun)
		return fmt.Sprintf(
			"Correlation lookup budget exhausted — you have used %d of %d this run. "+
				"The rejected pairings named in the system prompt still stand for the rest of the run.",
			e.correlationLookupsUsed, e.maxCorrelationLookupsPerRun,
		)
	}

	pair := action.GetCorrelations
	if pair == nil {
		step.Error = "get_correlations with no pair"
		return `get_correlations needs two datasources. Use {"thinking": "...", "get_correlations": {"a": "<datasource_id>", "b": "<datasource_id>"}}.`
	}

	a, aOK := e.knownDatasource(pair.A)
	b, bOK := e.knownDatasource(pair.B)
	if !aOK || !bOK {
		unknown := pair.A
		if aOK {
			unknown = pair.B
		}
		step.Error = fmt.Sprintf("unknown datasource_id %q", unknown)
		return fmt.Sprintf("get_correlations: unknown datasource_id %q. This run's datasources are: %s.",
			unknown, strings.Join(e.datasourceIDs, ", "))
	}
	if a == b {
		step.Error = "get_correlations with one datasource"
		return fmt.Sprintf("get_correlations compares TWO datasources, and both ids resolved to %q. "+
			"Correlation is between datasources; a field does not need one to be joined within its own.", a)
	}

	keys, err := e.correlationLookup(ctx, a, b)
	e.correlationLookupsUsed++

	if err != nil {
		step.Error = err.Error()
		logger.WithError(err).Warn("get_correlations failed")
		// Deliberately not "nothing is recorded": a read that failed has not
		// established that a pairing is unreviewed, and a rejection that went
		// missing behind an error is the exact failure this action exists to
		// prevent.
		return fmt.Sprintf(
			"Correlation lookup failed: %s. This is NOT the same as no decision being recorded — "+
				"a reviewed rejection could exist and be unreadable right now. Do not treat a "+
				"cross-datasource key between %s and %s as verified in this run.",
			err.Error(), a, b,
		)
	}

	return formatCorrelationResult(a, b, keys, e.correlationLookupsUsed, e.maxCorrelationLookupsPerRun)
}

// knownDatasource resolves an id the model supplied against this run's
// datasources, treating "" as the primary the way every other action does.
func (e *ExplorationEngine) knownDatasource(id string) (string, bool) {
	want := strings.TrimSpace(id)
	if want == "" {
		want = e.primaryDatasource
	}
	want = normDatasourceID(want)
	for _, known := range e.datasourceIDs {
		if known == want {
			return want, true
		}
	}
	return want, false
}

// formatCorrelationResult renders the answer for one pair.
//
// Rejections come first and under their own heading, because they are the only
// part of this answer that changes what the agent must NOT do, and an
// instruction buried under a list is an instruction that gets skimmed. Each one
// carries the provider's reason: a model follows "these two do not hold the
// same values" where it talks itself out of "do not use this".
//
// The substitution section is never silent. The failure mode of a bare
// prohibition is a model reaching for the neighbouring key, so the answer
// either names the reviewed alternatives or states outright that there are
// none and what to do instead.
//
// Input order is preserved within each group: the provider answers
// deterministically, and re-sorting here would be a second ordering to keep in
// step with it.
func formatCorrelationResult(a, b string, keys []agentplugin.CorrelationKey, used, max int) string {
	var rejected, good []agentplugin.CorrelationKey
	for _, k := range keys {
		if k.State == agentplugin.CorrelationRejected {
			rejected = append(rejected, k)
			continue
		}
		good = append(good, k)
	}

	var sb strings.Builder
	switch {
	case len(keys) == 0:
		fmt.Fprintf(&sb, "No reviewed correlation keys are recorded between `%s` and `%s`.\n", a, b)
		// One-sided on purpose. Nobody having looked is not evidence that no
		// key exists, and a model told otherwise would stop looking for one.
		sb.WriteString("That says nothing either way about whether a key exists — nobody has reviewed this pair. " +
			"Judge any key you use here on the values themselves.\n")
	case len(rejected) > 0:
		fmt.Fprintf(&sb, "Reviewed correlation keys between `%s` and `%s` (%d):\n\n", a, b, len(keys))
		sb.WriteString("DO NOT CORRELATE ON:\n")
		for _, k := range rejected {
			writeCorrelationLine(&sb, k)
			fmt.Fprintf(&sb, "  %s.\n", strings.TrimRight(k.Reason, "."))
		}
		sb.WriteString("This is not a preference. Do not use these pairings, in either direction, and do not " +
			"substitute a different spelling of the same field — the decision is about the field, not how it " +
			"is spelled.\n\n")
		writeCorrelationAlternatives(&sb, a, b, good)
	default:
		fmt.Fprintf(&sb, "Reviewed correlation keys between `%s` and `%s` (%d) — prefer these over any pairing "+
			"you infer from matching names:\n", a, b, len(good))
		for _, k := range good {
			writeCorrelationLine(&sb, k)
		}
	}

	if max > 0 {
		fmt.Fprintf(&sb, "\n%d of %d correlation lookups used this run.\n", used, max)
	}
	return sb.String()
}

// writeCorrelationAlternatives writes what to use instead of a rejected
// pairing — including when the honest answer is "nothing".
func writeCorrelationAlternatives(sb *strings.Builder, a, b string, good []agentplugin.CorrelationKey) {
	if len(good) == 0 {
		fmt.Fprintf(sb, "USE INSTEAD: nothing. No reviewed key links `%s` and `%s`. Do not correlate them "+
			"record by record in this run — compare them only at a grain both sides genuinely share, or "+
			"leave the correlation unmade and say in your findings that it could not be made.\n", a, b)
		return
	}
	sb.WriteString("USE INSTEAD — reviewed and known good:\n")
	for _, k := range good {
		writeCorrelationLine(sb, k)
	}
}

// writeCorrelationLine renders one pairing, naming the datasource on both
// sides.
//
// Qualified even though the pair is in the heading: a key can be stated from
// either side, and a field attributed to the wrong datasource is the same class
// of error this whole action exists to prevent.
func writeCorrelationLine(sb *strings.Builder, k agentplugin.CorrelationKey) {
	fmt.Fprintf(sb, "- `%s`.`%s` ↔ `%s`.`%s`%s\n",
		k.DatasourceID, k.SourceField,
		k.WithDatasourceID, strings.Join(k.AnchorColumns, "`, `"),
		correlationQualifier(k))
}

// correlationQualifier is the parenthesised grain + provenance on a pairing
// line. Both are omitted when absent rather than rendered empty, so a provider
// that reports no grain does not produce "( grain)".
func correlationQualifier(k agentplugin.CorrelationKey) string {
	var parts []string
	if k.Grain != "" {
		parts = append(parts, k.Grain+" grain")
	}
	if p := correlationProvenance(k.State); p != "" {
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// correlationProvenance says how a pairing came to be known, in the words the
// dashboard uses for the same decision.
//
// A rejection carries none: its line is already under a heading saying what it
// is, and its reason follows on the next line.
func correlationProvenance(s agentplugin.CorrelationState) string {
	switch s {
	case agentplugin.CorrelationConfirmed:
		return "confirmed by a reviewer"
	case agentplugin.CorrelationManual:
		return "declared by a reviewer"
	}
	return ""
}
