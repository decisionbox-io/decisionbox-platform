package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	goconfig "github.com/decisionbox-io/decisionbox/libs/go-common/config"
	gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Generating the recommendation set in two passes: decide what it is, then
// write one at a time.
//
// The phase used to ask for the whole set as a single JSON document under one
// output cap. A run with ten or twelve eligible insights asks for several
// recommendations, each carrying a description, actions, an impact object and
// figures, and the document does not fit: generation stops at the cap, the
// document is cut mid-structure, and the strict envelope parse then discards
// everything -- including the recommendations the response had already
// finished. The phase ended with none, the run still reported completed, and
// the self-heal retry re-sent the identical request and reproduced the
// identical cut. Seen on a self-hosted small model, where both attempts spend
// the whole output budget and the phase produces nothing.
//
// Splitting the *insights* across calls would have been the wrong axis. The
// cap is on output, and most recommendations cite more than one insight --
// batching the input is how you lose that. So the insights stay whole in every
// call and the output is what gets split:
//
//  1. One plan call. All the insights, and an output that is only the shape of
//     the set -- a title, a category, a priority and the insight ids each
//     entry is built on. A dozen of those is a few hundred tokens against a
//     budget measured in thousands, so it cannot be cut off. This is the call
//     that does the work the single call did globally: deciding what the set
//     covers, that no two entries say the same thing, and what each one's
//     priority is relative to the others.
//
//  2. One expansion call per planned entry. Each one is shown the insights and
//     the whole plan, and writes exactly one recommendation. The output is one
//     object, so the cap is no longer something a long answer can reach, and a
//     call that does fail costs one recommendation instead of the set.
//
// Both instruction blocks are appended in code rather than added to the pack
// templates, for the reason the figure contract is: a pack file can be edited
// and a custom template skips pack content, so a contract that only lives in
// templates is one some runs do not have.
//
// When the plan call cannot be read, or the expansions produce nothing at all,
// the phase falls back to the single-document call it used to make. That path
// is no better than it was -- but it is no worse either, so a model that
// cannot follow the two-pass contract is left exactly where it started
// instead of worse off.

// Plan/expand tunables. Every cap here is a bound on cost, not a product
// decision: how many recommendations to produce is the pack's instruction to
// the model, and the plan simply must not be allowed to fan out without limit.
const (
	// defaultRecommendationPlanMaxItems bounds the planned set, and so the
	// number of expansion calls the phase will make. Packs ask for a handful;
	// the bound is well above that so it never silently trims a legitimate
	// plan, and exists only so a runaway plan cannot turn into a runaway
	// number of calls.
	defaultRecommendationPlanMaxItems = 20

	// recommendationPlanMaxItemsEnv overrides defaultRecommendationPlanMaxItems.
	recommendationPlanMaxItemsEnv = "RECOMMENDATION_PLAN_MAX_ITEMS"

	// defaultRecommendationPlanOutputTokens caps the plan call's output. A
	// plan entry is a title and three small fields; twenty of them do not
	// approach this. It is deliberately far below the model's output cap so
	// the plan call is the one call in the phase that cannot be truncated.
	defaultRecommendationPlanOutputTokens = 2048

	// recommendationPlanOutputTokensEnv overrides defaultRecommendationPlanOutputTokens.
	recommendationPlanOutputTokensEnv = "RECOMMENDATION_PLAN_OUTPUT_TOKENS"

	// defaultRecommendationExpandOutputTokens caps one expansion's output.
	// One recommendation with its description, actions, impact and figures
	// runs to a few hundred tokens; this leaves several times that, so
	// reaching it means the model is repeating itself rather than writing a
	// longer answer, and the retry asks for a terser one.
	defaultRecommendationExpandOutputTokens = 4096

	// recommendationExpandOutputTokensEnv overrides defaultRecommendationExpandOutputTokens.
	recommendationExpandOutputTokensEnv = "RECOMMENDATION_EXPAND_OUTPUT_TOKENS"

	// defaultRecommendationCallMinOutputTokens is the floor the plan and
	// expansion output budgets never drop below, so a near-window input still
	// gets a usable generation instead of a zero request. Far smaller than the
	// analysis floor because these calls ask for far less.
	defaultRecommendationCallMinOutputTokens = 1024

	// recommendationCallMinOutputTokensEnv overrides defaultRecommendationCallMinOutputTokens.
	recommendationCallMinOutputTokensEnv = "RECOMMENDATION_CALL_MIN_OUTPUT_TOKENS"

	// defaultRecommendationExpandMaxRetries bounds the corrective re-asks for
	// one expansion. One extra shot: the retry is reason-aware (terser on a
	// truncated or degenerate reply, shape-corrective on a parse failure), and
	// a second identical-shaped round is what the old single call already
	// proved does not help.
	defaultRecommendationExpandMaxRetries = 1

	// recommendationExpandMaxRetriesEnv overrides defaultRecommendationExpandMaxRetries.
	recommendationExpandMaxRetriesEnv = "RECOMMENDATION_EXPAND_MAX_RETRIES"

	// recommendationPlanShrinkDivisor is how much a truncated or unreadable
	// plan shrinks by on the retry. Halving is the standard response to "the
	// answer did not fit": it converges in a couple of rounds and cannot
	// oscillate.
	recommendationPlanShrinkDivisor = 2

	// recommendationPlanMinItemsOnRetry is the floor the shrinking plan stops
	// at. Below a handful there is no set left to plan.
	recommendationPlanMinItemsOnRetry = 3

	// recommendationTerseMaxActions is the number of implementation steps the
	// terse retry asks for, when a full-length expansion did not fit.
	recommendationTerseMaxActions = 3

	// estimatedRecommendationsPerRun is how many recommendations the cost
	// estimate (EstimateCost) assumes a run will produce, and so how many
	// expansion calls it budgets for. The packs ask for a handful; this is not
	// a limit on anything, only the estimator's assumption, and it is bounded
	// by recommendationPlanMaxItems at the point of use.
	estimatedRecommendationsPerRun = 6

	// recommendationReasoningHeadroomTokens is the extra output budget a
	// reasoning-effective model gets on top of a call's own ceiling.
	//
	// On a thinking model the requested output covers the hidden reasoning as
	// well as the answer -- on Ollama it becomes num_predict for the whole
	// generation -- so a ceiling sized from the visible JSON alone can be
	// spent before the answer begins. That is not something asking for shorter
	// prose can fix, and it is the failure that cost the exploration engine
	// its short runs on Qwen3 and DeepSeek-R1 (issue #341), where the same
	// headroom was the fix.
	//
	// Additive rather than a flat ceiling, because the plan call and an
	// expansion ask for genuinely different amounts of visible answer and a
	// flat ceiling would erase the difference. The sum lands the expansion on
	// the exploration engine's own reasoning ceiling. Still bounded by the
	// model's output cap and budgeted against the window, so it cannot
	// overflow; a non-reasoning model never reaches it and its budget is
	// unchanged. Not env-tunable on its own -- the operator-facing dials are
	// the per-call ceilings, which this is added to.
	recommendationReasoningHeadroomTokens = 12288

	// recommendationPlanTitleMaxRunes is the title length the plan call is
	// asked to stay under. Long enough for the action-and-context titles the
	// packs ask for, short enough that a plan of twenty stays small.
	recommendationPlanTitleMaxRunes = 120
)

// recommendationPlanResponseFormatName is the schema identifier sent to
// providers that name their structured-output schema.
const recommendationPlanResponseFormatName = "recommendation_plan"

// recommendationPlanEnvelopeKey is the key the plan call answers under. Kept
// distinct from "recommendations" on purpose: a model that ignores the plan
// instruction and writes the full set anyway produces a response this parser
// reports as missing its key, rather than one it silently half-accepts.
const recommendationPlanEnvelopeKey = "plan"

// recommendationPlanItem is one entry of the planned set: what the
// recommendation will be, without its prose.
type recommendationPlanItem struct {
	Title             string   `json:"title"`
	Category          string   `json:"category"`
	Priority          int      `json:"priority"`
	RelatedInsightIDs []string `json:"related_insight_ids"`
}

// UnmarshalJSON decodes a plan entry as tolerantly as a recommendation is
// decoded.
//
// The priority is the field that matters. A model that answers "high", "P2" or
// "2" is answering the pack's own priority scale in the pack's own words, and
// models.Recommendation has accepted that for as long as it has existed -- the
// pack even has to tell the model to emit an integer, which is the tell that
// they do not. A plan entry that insisted on a number would drop the entry,
// and a dropped plan entry is a recommendation lost before it was ever
// written: the exact loss this phase exists to stop, arriving one entry at a
// time instead of all at once.
//
// models.CoercePriority is called rather than copied, because two tables of
// severity words are two answers to one question.
func (i *recommendationPlanItem) UnmarshalJSON(data []byte) error {
	type alias recommendationPlanItem
	aux := &struct {
		Priority json.RawMessage `json:"priority"`
		*alias
	}{alias: (*alias)(i)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	i.Priority = models.CoercePriority(aux.Priority)
	return nil
}

// recommendationPlanMaxItems returns the configured fan-out bound.
func recommendationPlanMaxItems() int {
	if v := goconfig.GetEnvAsInt(recommendationPlanMaxItemsEnv, defaultRecommendationPlanMaxItems); v > 0 {
		return v
	}
	return defaultRecommendationPlanMaxItems
}

// recommendationPlanOutputTokens returns the configured plan output cap.
func recommendationPlanOutputTokens() int {
	if v := goconfig.GetEnvAsInt(recommendationPlanOutputTokensEnv, defaultRecommendationPlanOutputTokens); v > 0 {
		return v
	}
	return defaultRecommendationPlanOutputTokens
}

// recommendationExpandOutputTokens returns the configured per-expansion output cap.
func recommendationExpandOutputTokens() int {
	if v := goconfig.GetEnvAsInt(recommendationExpandOutputTokensEnv, defaultRecommendationExpandOutputTokens); v > 0 {
		return v
	}
	return defaultRecommendationExpandOutputTokens
}

// recommendationCallMinOutputTokens returns the configured output floor for
// the plan and expansion calls.
func recommendationCallMinOutputTokens() int {
	if v := goconfig.GetEnvAsInt(recommendationCallMinOutputTokensEnv, defaultRecommendationCallMinOutputTokens); v > 0 {
		return v
	}
	return defaultRecommendationCallMinOutputTokens
}

// recommendationExpandMaxRetries returns the configured per-expansion retry
// bound, clamped at zero so a negative value disables the retry rather than
// looping.
func recommendationExpandMaxRetries() int {
	v := goconfig.GetEnvAsInt(recommendationExpandMaxRetriesEnv, defaultRecommendationExpandMaxRetries)
	if v < 0 {
		return 0
	}
	return v
}

// recommendationPlanResponseFormat returns the structured-output request for
// the plan call, attached where the provider supports it so the model is
// decode-constrained into the plan envelope rather than the full
// recommendation shape it has just been shown.
func recommendationPlanResponseFormat() *gollm.ResponseFormat {
	return &gollm.ResponseFormat{
		Name:   recommendationPlanResponseFormatName,
		Schema: recommendationPlanResponseSchema(),
		Strict: false,
	}
}

// recommendationPlanResponseSchema is the JSON Schema for the plan envelope.
// Deliberately tiny: the plan carries no prose, so every field a schema could
// describe here is one the expansion calls are responsible for instead.
func recommendationPlanResponseSchema() map[string]interface{} {
	str := func(desc string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc}
	}
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			recommendationPlanEnvelopeKey: map[string]interface{}{
				"type":        "array",
				"description": "The recommendations this run will produce, as a set, without their prose",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"title":    str("Specific, action-oriented recommendation title"),
						"category": str("Recommendation category, from the categories described above"),
						"priority": map[string]interface{}{"type": "integer", "description": "Priority on the scale described above, set relative to the other entries"},
						"related_insight_ids": map[string]interface{}{
							"type":        "array",
							"description": "UUIDs of the insights this entry is built on, copied verbatim from the input insights",
							"items":       str("An insight UUID from the input"),
						},
					},
					"required": []interface{}{"title", "related_insight_ids"},
				},
			},
		},
		"required": []interface{}{recommendationPlanEnvelopeKey},
	}
}

// parseRecommendationPlan decodes the plan response tolerantly and per-item,
// the same way parseRecommendations reads the full envelope: every top-level
// JSON value is offered to the item decoder and the first that produces a
// plan wins, so prose around the answer costs nothing.
//
// Returns the kept entries, the number of per-item drops, and a non-nil error
// only when the response is not recognisable as a plan at all. An entry with
// no title is dropped: the title is what the expansion call is asked to write,
// and an untitled entry would ask the model to expand nothing.
func parseRecommendationPlan(response string) ([]recommendationPlanItem, int, error) {
	vals, ferr := jsonValues(cleanJSONResponse(response))
	if len(vals) == 0 {
		if ferr == nil {
			ferr = fmt.Errorf("no JSON value in response")
		}
		return nil, 0, ferr
	}
	var (
		firstItems   []recommendationPlanItem
		firstDropped int
		firstErr     error
	)
	for i, val := range vals {
		raws, err := envelopeItems(val, recommendationPlanEnvelopeKey)
		if err != nil {
			if i == 0 {
				firstErr = err
			}
			continue
		}
		kept, dropped := decodeRecommendationPlanItems(raws)
		if len(kept) > 0 {
			return kept, dropped, nil
		}
		if i == 0 {
			firstItems, firstDropped = kept, dropped
		}
	}
	if firstErr != nil {
		return nil, 0, firstErr
	}
	return firstItems, firstDropped, nil
}

// decodeRecommendationPlanItems decodes the entries of one plan array,
// dropping what cannot be expanded and counting each drop.
func decodeRecommendationPlanItems(raws []json.RawMessage) ([]recommendationPlanItem, int) {
	items := make([]recommendationPlanItem, 0, len(raws))
	dropped := 0
	for i, raw := range raws {
		if strings.TrimSpace(string(raw)) == "null" {
			dropped++
			applog.WithFields(applog.Fields{"index": i}).
				Warn("Dropping a null recommendation plan entry; keeping the rest of the plan")
			continue
		}
		var item recommendationPlanItem
		if err := json.Unmarshal(raw, &item); err != nil {
			dropped++
			applog.WithFields(applog.Fields{
				"index":  i,
				"reason": err.Error(),
			}).Warn("Dropping unparseable recommendation plan entry; keeping the rest of the plan")
			continue
		}
		item.Title = strings.TrimSpace(item.Title)
		if item.Title == "" {
			dropped++
			applog.WithFields(applog.Fields{"index": i}).
				Warn("Dropping a recommendation plan entry with no title; keeping the rest of the plan")
			continue
		}
		items = append(items, item)
	}
	return items, dropped
}

// recommendationPlanSuffix is the plan call's instruction block, appended to
// the recommendation prompt the pack built. It overrides the output format the
// template just described, because this call is not writing recommendations.
func recommendationPlanSuffix(maxItems int) string {
	return fmt.Sprintf(`

---

## This response: the plan only

Ignore the output format described above for this response. You are not
writing the recommendations yet — you are deciding, as one decision, what the
set of recommendations will be. Each one is then written in its own later
response, one at a time, so you do not have to fit them all here.

Respond with ONLY a single JSON object of this form:

{"%s":[{"title":"...","category":"...","priority":1,"related_insight_ids":["<insight id>"]}]}

For this response:
- At most %d entries. Decide the whole set here: cover what the insights
  actually support, do not plan two entries that would say the same thing in
  different words, and set each priority relative to the others using the
  priority scale described above.
- `+"`related_insight_ids`"+` holds the ids of the insights the entry is built on,
  copied verbatim from the insights above. One entry may cite several, and
  should when the recommendation rests on several.
- `+"`category`"+` comes from the categories described above.
- Keep each title under %d characters.
- No `+"`description`"+`, no `+"`actions`"+`, no `+"`expected_impact`"+`, no `+"`success_metrics`"+`,
  no `+"`figures`"+` — every one of those belongs to the later response that writes
  that entry, and anything you put here is discarded.
- No prose, no markdown fences, nothing before or after the JSON object.`,
		recommendationPlanEnvelopeKey, maxItems, recommendationPlanTitleMaxRunes)
}

// recommendationExpandSuffix is one expansion call's instruction block: the
// whole plan for context, and the single entry this response writes.
//
// The plan is restated in full so the recommendation knows the set it belongs
// to -- which entry is higher priority, and what the others already cover, so
// it does not re-make their argument. The planned insight ids are restated
// verbatim because they are the citation the platform resolves figures
// against, and a re-worded id resolves to nothing.
func recommendationExpandSuffix(plan []recommendationPlanItem, idx int) string {
	var sb strings.Builder
	sb.WriteString("\n\n---\n\n## This response: one recommendation\n\n")
	sb.WriteString("The set has already been planned, as a set. The full plan, in order:\n\n")
	for i, item := range plan {
		marker := ""
		if i == idx {
			marker = "   <- write this one"
		}
		category := item.Category
		if category == "" {
			category = "uncategorised"
		}
		fmt.Fprintf(&sb, "%d. [priority %d, %s] %s%s\n", i+1, item.Priority, category, item.Title, marker)
	}
	target := plan[idx]
	fmt.Fprintf(&sb, "\nWrite entry %d — %q — and nothing else.\n\n", idx+1, target.Title)
	sb.WriteString("Respond with ONLY a single JSON object of the form " +
		`{"recommendations":[ ... ]} holding exactly one recommendation, in the ` +
		"format described above, with:\n")
	fmt.Fprintf(&sb, "- `title`: the planned title. Refine the wording if it reads badly, keep the meaning.\n")
	if target.Category != "" {
		fmt.Fprintf(&sb, "- `category`: %q, as planned.\n", target.Category)
	}
	if target.Priority > 0 {
		fmt.Fprintf(&sb, "- `priority`: %d, as planned.\n", target.Priority)
	}
	if len(target.RelatedInsightIDs) > 0 {
		fmt.Fprintf(&sb, "- `related_insight_ids`: exactly these ids, copied verbatim — %s\n",
			strings.Join(target.RelatedInsightIDs, ", "))
	}
	sb.WriteString("- every other field the format above requires, in full. " +
		"This is the only response that writes this recommendation, so it is the " +
		"one place its description, actions, impact and figures are produced.\n\n")
	sb.WriteString("Do not write any of the other planned entries — each is written in its " +
		"own response. Do not re-make another entry's argument. No prose, no markdown " +
		"fences, nothing before or after the JSON object.")
	return sb.String()
}

// recommendationBriefThinkingSuffix is the corrective block for a reply that
// spent its budget without the answer reaching the caller. Asking for a
// shorter answer would be answering the wrong question: there was no answer,
// and what the next attempt needs is for the model to get to one. Paired with
// a raised ceiling, so the room is there as well as asked for.
func recommendationBriefThinkingSuffix(reason string) string {
	return fmt.Sprintf(`

Your previous response could not be used: %s. Whatever you spent that response
on, it did not reach us. Do not plan or deliberate at length this time: decide
quickly and spend the response on the JSON object itself, complete and
parseable, with nothing before or after it.`, reason)
}

// recommendationTerseSuffix is the corrective block for an expansion whose
// reply was cut off at the output limit, or spent its budget on a repeating
// run. Naming the length as the problem is the whole point: the previous
// suffixes asked for a complete object and got one that did not fit, so the
// re-ask has to be for a shorter one, not a better-formed one.
func recommendationTerseSuffix(reason string) string {
	return fmt.Sprintf(`

Your previous response could not be used: %s, so the JSON object was never
finished. Write the same recommendation again, shorter: keep every field the
format requires, but hold the description to a few sentences, give at most %d
actions, and do not repeat yourself. A complete short object is usable; a
longer one that stops mid-way is not.`, reason, recommendationTerseMaxActions)
}

// recommendationFailure is why a pass of the recommendation phase produced
// nothing, carried out of the pass so the phase can stamp the status that
// names the actual problem instead of folding everything onto "parse error".
type recommendationFailure struct {
	// err is what went wrong, as the operator should read it.
	err error

	// status is the RecommendationStep status to stamp. Empty for a call that
	// never returned a response: there is no response to blame, and the old
	// phase reported a transport failure as a bare error with no status.
	status string

	// prefix labels err in the step's Error string.
	prefix string

	// missingOutput is set when the reply spent its budget without the answer
	// reaching the caller — the one failure whose repair is a *larger* request
	// rather than a smaller one.
	missingOutput bool
}

// newRecommendationParseFailure reports a response the parsers could not read.
func newRecommendationParseFailure(err error) *recommendationFailure {
	return &recommendationFailure{err: err, status: statusRecommendationParseError, prefix: "parse error"}
}

// newRecommendationTruncationFailure reports a response that was cut off at
// the output limit, or spent its budget without producing content.
func newRecommendationTruncationFailure(reason string, missingOutput bool) *recommendationFailure {
	return &recommendationFailure{
		err:           errors.New(reason),
		status:        statusRecommendationTruncated,
		prefix:        "response unusable",
		missingOutput: missingOutput,
	}
}

// newRecommendationTransportFailure reports a call that failed outright.
func newRecommendationTransportFailure(err error) *recommendationFailure {
	return &recommendationFailure{err: err}
}

// recommendationFailureFor turns one unusable response into the failure that
// describes it, preferring the health verdict: "the response was cut off" is
// both truer and more actionable than the decoder's "unexpected end of JSON
// input", which is only ever the symptom.
func recommendationFailureFor(health responseHealth, parseErr error, emptyErr error) *recommendationFailure {
	switch {
	case health.unusable():
		return newRecommendationTruncationFailure(health.reason, health.missingOutput)
	case parseErr != nil:
		return newRecommendationParseFailure(parseErr)
	default:
		return newRecommendationParseFailure(emptyErr)
	}
}

// message renders the failure for RecommendationStep.Error.
func (f *recommendationFailure) message() string {
	if f == nil || f.err == nil {
		return ""
	}
	if f.prefix == "" {
		return f.err.Error()
	}
	return f.prefix + ": " + f.err.Error()
}

// reason renders the failure for a log field, including the nil case so
// callers do not each have to guard it.
func (f *recommendationFailure) reason() string {
	if f == nil || f.err == nil {
		return "no recommendations were produced"
	}
	return f.err.Error()
}

// retrySuffix is the corrective instruction this failure calls for. A
// truncated response needs a shorter answer; a malformed one needs a
// better-formed answer. Asking for the wrong one is what made the old
// single-document retry a second copy of the first failure.
func (f *recommendationFailure) retrySuffix() string {
	if f == nil || f.err == nil {
		return ""
	}
	if f.missingOutput {
		return recommendationBriefThinkingSuffix(f.err.Error())
	}
	if f.status == statusRecommendationTruncated {
		return recommendationTerseSuffix(f.err.Error())
	}
	return recommendationRepairSuffix(f.err)
}

// recommendationParseMaxRetries returns the configured bound on corrective
// re-asks for the plan call and the single-document fallback, clamped at zero
// so a negative value disables the retry rather than looping.
func recommendationParseMaxRetries() int {
	v := goconfig.GetEnvAsInt(recommendationParseMaxRetriesEnv, defaultRecommendationParseMaxRetries)
	if v < 0 {
		return 0
	}
	return v
}

// shrinkPlanItems halves the planned-set bound for a re-ask, stopping at the
// floor below which there is no set left to plan.
func shrinkPlanItems(maxItems int) int {
	shrunk := maxItems / recommendationPlanShrinkDivisor
	if shrunk < recommendationPlanMinItemsOnRetry {
		shrunk = recommendationPlanMinItemsOnRetry
	}
	if shrunk > maxItems {
		return maxItems
	}
	return shrunk
}

// recommendationPlanRepairSuffix is the corrective block for a plan call whose
// response could not be read.
func recommendationPlanRepairSuffix(reason string, maxItems int) string {
	return fmt.Sprintf(`

Your previous response could not be used: %s.
Respond with ONLY a single JSON object of the form {"%s":[ ... ]} — no prose, no
markdown fences, and not a bare top-level array. At most %d entries, and each
entry needs only a `+"`title`"+` and a `+"`related_insight_ids`"+` array.`,
		reason, recommendationPlanEnvelopeKey, maxItems)
}

// planRecommendations runs pass 1: one call that decides what the set of
// recommendations is, without writing any of them.
//
// Returns the planned entries, or nil and the failure that explains why there
// are none. A re-ask shrinks the bound as well as naming the fault, so a plan
// that did not fit is asked for smaller rather than asked for again.
func (o *Orchestrator) planRecommendations(
	ctx context.Context,
	step *models.RecommendationStep,
	prompt string,
	insights []models.Insight,
) ([]recommendationPlanItem, *recommendationFailure) {
	format := recommendationPlanResponseFormat()
	maxItems := recommendationPlanMaxItems()
	maxRetries := recommendationParseMaxRetries()

	var fail *recommendationFailure
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			step.RecommendationPlanRetries = attempt
			maxItems = shrinkPlanItems(maxItems)
			applog.WithFields(applog.Fields{
				"attempt":   attempt,
				"max_items": maxItems,
				"reason":    fail.reason(),
			}).Warn("Re-planning recommendations after an unusable response")
		}

		attemptPrompt := prompt + recommendationPlanSuffix(maxItems)
		if attempt > 0 {
			attemptPrompt += recommendationPlanRepairSuffix(fail.reason(), maxItems)
		}

		maxTokens := o.recommendationCallMaxTokens(ctx, attemptPrompt, retryCeiling(recommendationPlanOutputTokens(), fail))
		res, err := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if err != nil {
			recordRecommendationCall(step, models.RecommendationCallPlan, attempt, 0, nil, responseHealth{}, err)
			applog.WithError(err).Warn("Recommendation plan call failed")
			return nil, newRecommendationTransportFailure(err)
		}

		health := classifyResponse(o.recommendationResponseFacts(res, maxTokens))
		recordRecommendationCall(step, models.RecommendationCallPlan, attempt, 0, res, health, nil)
		// The plan response is what the step records: it is the one call that
		// describes the whole set, and so the one worth reading back.
		step.Response = res.Content

		items, dropped, perr := parseRecommendationPlan(res.Content)
		step.RecommendationsDroppedParse += dropped
		if perr == nil && len(items) > 0 {
			// Keep only the ids that name an eligible insight, here rather
			// than at the point of use. The expansion prompt restates the
			// planned ids verbatim for the entry to copy, so an id the model
			// invented in the plan would be handed back to it as the citation
			// to use -- teaching the slug-shaped failure of issue #237 instead
			// of catching it. The entry survives without ids; the expansion's
			// own citation, and the downstream recovery, are the net.
			dropped := 0
			for i := range items {
				kept := eligibleInsightIDsAmong(items[i].RelatedInsightIDs, insights)
				dropped += len(items[i].RelatedInsightIDs) - len(kept)
				items[i].RelatedInsightIDs = kept
			}
			if dropped > 0 {
				applog.WithFields(applog.Fields{
					"dropped": dropped,
					"planned": len(items),
				}).Warn("Recommendation plan cited ids that name no eligible insight; dropping them from the plan")
			}
			if len(items) > maxItems {
				applog.WithFields(applog.Fields{
					"planned":   len(items),
					"max_items": maxItems,
				}).Warn("Recommendation plan exceeded the fan-out bound; keeping the first entries")
				items = items[:maxItems]
			}
			return items, nil
		}
		// An empty plan the model actually meant is an answer: it decided the
		// insights support nothing actionable. The single document has always
		// been allowed to say that as {"recommendations":[]} and is not
		// re-asked for it, so the plan is not either -- re-asking would spend
		// another generation telling the model its valid answer was unusable,
		// and then spend a third on the fallback. "Meant it" is a complete
		// response that dropped no entries: an empty plan that lost every
		// entry to the decoder, or stopped at the output limit, is a failure
		// like any other.
		//
		// Not conditioned on the health verdict. A plan cut off before it
		// wrote an entry does not parse at all -- for this to read as an empty
		// plan the model had to close the array -- and the verdict is a
		// statement about the tokens, not about the answer in hand. Letting it
		// veto a parsed answer is how a structured-output provider's perfectly
		// good empty plan ended up being argued with, since the fold discards
		// the tool scaffolding its token count was spent on.
		if perr == nil && dropped == 0 {
			applog.WithFields(applog.Fields{"health": health.reason}).
				Info("Recommendation plan is empty: the model found nothing to recommend")
			return nil, nil
		}
		fail = recommendationFailureFor(health, perr, fmt.Errorf("the plan held no usable entries"))
	}
	return nil, fail
}

// expandRecommendationPlan runs pass 2: one call per planned entry, each
// writing that entry in full.
//
// An entry that cannot be written costs that entry and nothing else, which is
// the whole point of the split — so the loop records the failure and carries
// on. It returns the failure only when every entry failed, because a partial
// set is a result, not an error. A finished context stops the loop: the
// remaining calls would each fail the same way after a full generation's wait.
func (o *Orchestrator) expandRecommendationPlan(
	ctx context.Context,
	step *models.RecommendationStep,
	prompt string,
	plan []recommendationPlanItem,
	insights []models.Insight,
) ([]models.Recommendation, *recommendationFailure) {
	format := recommendationResponseFormat()
	maxRetries := recommendationExpandMaxRetries()

	recs := make([]models.Recommendation, 0, len(plan))
	var fail *recommendationFailure
	for idx := range plan {
		if err := ctx.Err(); err != nil {
			applog.WithFields(applog.Fields{
				"written":   len(recs),
				"planned":   len(plan),
				"remaining": len(plan) - idx,
			}).Warn("Context finished; stopping recommendation expansion")
			if len(recs) == 0 {
				fail = newRecommendationTransportFailure(err)
			}
			break
		}

		rec, expandFail := o.expandOneRecommendation(ctx, step, prompt, plan, idx, insights, format, maxRetries)
		if rec == nil {
			step.RecommendationsExpandFailed++
			fail = expandFail
			applog.WithFields(applog.Fields{
				"entry":  idx + 1,
				"reason": expandFail.reason(),
			}).Warn("Could not write one planned recommendation; keeping the rest of the set")
			continue
		}
		recs = append(recs, *rec)
	}

	step.RecommendationsExpanded = len(recs)
	if len(recs) > 0 {
		return recs, nil
	}
	return recs, fail
}

// expandOneRecommendation writes one planned entry, re-asking once (by
// default) in the way the failure calls for.
func (o *Orchestrator) expandOneRecommendation(
	ctx context.Context,
	step *models.RecommendationStep,
	prompt string,
	plan []recommendationPlanItem,
	idx int,
	insights []models.Insight,
	format *gollm.ResponseFormat,
	maxRetries int,
) (*models.Recommendation, *recommendationFailure) {
	base := prompt + recommendationExpandSuffix(plan, idx)

	var fail *recommendationFailure
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptPrompt := base
		if attempt > 0 {
			attemptPrompt += fail.retrySuffix()
			applog.WithFields(applog.Fields{
				"entry":   idx + 1,
				"attempt": attempt,
				"reason":  fail.reason(),
			}).Warn("Re-asking for one recommendation after an unusable response")
		}

		maxTokens := o.recommendationCallMaxTokens(ctx, attemptPrompt, retryCeiling(recommendationExpandOutputTokens(), fail))
		res, err := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if err != nil {
			recordRecommendationCall(step, models.RecommendationCallExpand, attempt, idx, nil, responseHealth{}, err)
			return nil, newRecommendationTransportFailure(err)
		}

		health := classifyResponse(o.recommendationResponseFacts(res, maxTokens))
		recordRecommendationCall(step, models.RecommendationCallExpand, attempt, idx, res, health, nil)

		rec, perr := adoptExpansionResponse(step, res.Content, plan, idx, insights)
		if perr == nil {
			return o.repairExpansionCitation(ctx, step, base, rec, plan, idx, insights, format), nil
		}
		fail = recommendationFailureFor(health, perr, fmt.Errorf("the response held no usable recommendation"))
	}
	return nil, fail
}

// recommendationResponseFacts gathers what is known about one reply for
// classifyResponse, including whether this run's model thinks -- which decides
// whether tokens the text does not account for are a reply that went missing
// or a reply that was thinking.
func (o *Orchestrator) recommendationResponseFacts(res *ai.ChatResult, requestedMaxTokens int) responseFacts {
	return responseFacts{
		stopReason:         res.StopReason,
		outputTokens:       res.TokensOut,
		requestedMaxTokens: requestedMaxTokens,
		content:            res.Content,
		reasoning:          o.effectiveReasoning(),
	}
}

// repairExpansionCitation re-asks one expansion that ended up citing no
// eligible insight, with the eligible ids spelled out.
//
// The plan is normally the answer here -- an expanded recommendation takes its
// citation from the entry it was written for. This is for when the plan has
// none either, because the plan cited nothing or cited ids that name no
// insight: without it the only thing left is the downstream backfill, which
// links the recommendation to every eligible insight and so says nothing about
// where it actually came from. The single-document path has always had this
// re-ask (#347); the two-pass path needs its own, per entry, because that is
// the unit it works in.
//
// Bounded by the same knob, and the re-ask is only adopted when it actually
// recovers a citation. On failure the uncited recommendation is kept: it is
// still a recommendation, and the backfill grounds it.
func (o *Orchestrator) repairExpansionCitation(
	ctx context.Context,
	step *models.RecommendationStep,
	base string,
	rec *models.Recommendation,
	plan []recommendationPlanItem,
	idx int,
	insights []models.Insight,
	format *gollm.ResponseFormat,
) *models.Recommendation {
	retries := recommendationCitationMaxRetries()
	if retries <= 0 || anyRecCitesEligible([]models.Recommendation{*rec}, insights) {
		return rec
	}

	for attempt := 1; attempt <= retries; attempt++ {
		step.RecommendationCitationRetries = attempt
		applog.WithFields(applog.Fields{"entry": idx + 1, "attempt": attempt}).Warn(
			"Re-asking for one recommendation: it cited no eligible insight id")

		attemptPrompt := base + recommendationCitationRepairSuffix(insights)
		maxTokens := o.recommendationCallMaxTokens(ctx, attemptPrompt, recommendationExpandOutputTokens())
		res, err := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if err != nil {
			recordRecommendationCall(step, models.RecommendationCallExpand, attempt, idx, nil, responseHealth{}, err)
			break // keep the uncited recommendation; the backfill grounds it
		}
		health := classifyResponse(o.recommendationResponseFacts(res, maxTokens))
		recordRecommendationCall(step, models.RecommendationCallExpand, attempt, idx, res, health, nil)

		// Through the same adoption path as the first attempt. A re-ask is
		// just as able to answer with the whole set, and adopting whatever
		// carried a valid citation would save another entry twice and lose
		// this one -- which is the protection selectPlannedRecommendation
		// exists to give, so there is one way in and not two.
		recovered, perr := adoptExpansionResponse(step, res.Content, plan, idx, insights)
		if perr != nil {
			continue
		}
		if !anyRecCitesEligible([]models.Recommendation{*recovered}, insights) {
			continue
		}
		return recovered
	}
	return rec
}

// recommendationCitationMaxRetries returns the configured bound on the
// citation re-ask, clamped at zero so a negative value disables it.
func recommendationCitationMaxRetries() int {
	v := goconfig.GetEnvAsInt(recommendationCitationMaxRetriesEnv, defaultRecommendationCitationMaxRetries)
	if v < 0 {
		return 0
	}
	return v
}

// generateRecommendationsSingleDocument is the fallback: the whole set asked
// for as one JSON document, which is what this phase did before the two-pass
// split. It is reached only when the plan could not be read or no expansion
// produced anything, so it is strictly a second chance — on a model that
// handles the old shape and not the new one, the run ends where it used to
// rather than worse.
//
// It keeps the citation self-heal (#347), which the expansion path does not
// need: an expanded recommendation takes its citation from the plan entry it
// was written for, while a single document has to be asked again.
func (o *Orchestrator) generateRecommendationsSingleDocument(
	ctx context.Context,
	step *models.RecommendationStep,
	prompt string,
	insights []models.Insight,
) ([]models.Recommendation, *recommendationFailure) {
	format := recommendationResponseFormat()
	maxRetries := recommendationParseMaxRetries()

	var (
		recs []models.Recommendation
		fail *recommendationFailure
	)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptPrompt := prompt
		if attempt > 0 {
			step.RecommendationParseRetries = attempt
			attemptPrompt = prompt + fail.retrySuffix()
			applog.WithFields(applog.Fields{
				"attempt": attempt,
				"reason":  fail.reason(),
			}).Warn("Re-prompting for the recommendation document after an unusable response")
		}

		// The output budget the single call has always used: the model's own
		// cap, measured against the input rather than assumed (issue #347).
		// Re-resolved per attempt for the reason recommendationCallMaxTokens
		// re-resolves — the window may have been learned since.
		recWindow, recOutputCap := o.resolveModelBudget()
		maxTokens := budgetedMaxOutputTokens(recWindow, approxTokens(ctx, attemptPrompt), recOutputCap, analysisMinOutputTokens())

		res, err := o.aiClient.ChatWithFormat(ctx, attemptPrompt, "", maxTokens, format)
		if err != nil {
			recordRecommendationCall(step, models.RecommendationCallSingleDocument, attempt, 0, nil, responseHealth{}, err)
			applog.WithError(err).Warn("Failed to generate recommendations")
			return make([]models.Recommendation, 0), newRecommendationTransportFailure(err)
		}

		health := classifyResponse(o.recommendationResponseFacts(res, maxTokens))
		recordRecommendationCall(step, models.RecommendationCallSingleDocument, attempt, 0, res, health, nil)
		step.Response = res.Content

		parsed, dropped, perr := parseRecommendations(res.Content)
		step.RecommendationsDroppedParse += dropped
		if perr == nil && (len(parsed) > 0 || dropped == 0) {
			// >=1 kept, or a legitimately empty result — accept either.
			recs, fail = parsed, nil
			break
		}
		fail = recommendationFailureFor(health, perr, fmt.Errorf("all %d recommendation(s) failed to parse", dropped))
	}

	if len(recs) == 0 {
		return make([]models.Recommendation, 0), fail
	}

	// Layer 3b (citation self-heal, #347): the document parsed, but if NO
	// recommendation cites an eligible insight id, re-ask once with the
	// eligible UUIDs spelled out — real citations beat the downstream blunt
	// backfill. Fires only when zero recs are cited (the small/open-model
	// case, e.g. DeepSeek-V3.1 which omits related_insight_ids); a big model
	// cites on the first try and skips this. Bounded + env-overridable (Rule
	// 2). On failure we keep the uncited document — recoverRelatedInsightIDs
	// backfills it, so a recommendation is never lost.
	citationRetries := recommendationCitationMaxRetries()
	if citationRetries > 0 && !anyRecCitesEligible(recs, insights) {
		for cAttempt := 1; cAttempt <= citationRetries; cAttempt++ {
			step.RecommendationCitationRetries = cAttempt
			applog.WithFields(applog.Fields{"attempt": cAttempt}).Warn(
				"Re-prompting for recommendations: response cited no eligible insight id")
			citationPrompt := prompt + recommendationCitationRepairSuffix(insights)
			citWindow, citOutputCap := o.resolveModelBudget()
			citMaxTokens := budgetedMaxOutputTokens(citWindow, approxTokens(ctx, citationPrompt), citOutputCap, analysisMinOutputTokens())
			res, err := o.aiClient.ChatWithFormat(ctx, citationPrompt, "", citMaxTokens, format)
			if err != nil {
				recordRecommendationCall(step, models.RecommendationCallSingleDocument, cAttempt, 0, nil, responseHealth{}, err)
				break // keep the uncited document; backfill grounds it downstream
			}
			health := classifyResponse(o.recommendationResponseFacts(res, citMaxTokens))
			recordRecommendationCall(step, models.RecommendationCallSingleDocument, cAttempt, 0, res, health, nil)
			parsed, _, perr := parseRecommendations(res.Content)
			if perr == nil && len(parsed) > 0 && anyRecCitesEligible(parsed, insights) {
				// Adopt the re-prompt only when it actually recovered a citation.
				recs = parsed
				step.Response = res.Content
				break
			}
		}
	}

	return recs, nil
}

// retryCeiling is the output ceiling the next attempt should ask for, given
// how the last one failed.
//
// Everything else gets the same ceiling again, because the problem was the
// answer and the re-ask names it. A reply whose budget went somewhere the
// caller cannot see is the exception: the answer never started, so asking for
// the same room again asks for the same outcome. That is what an uncatalogued
// thinking model looks like from the outside — the OpenAI catalog flags no
// model as reasoning, so o-series and GPT-5 projects reach the phase with
// effectiveReasoning() false — and reacting to the evidence covers a model
// nothing has classified, including one that does not exist yet.
func retryCeiling(base int, fail *recommendationFailure) int {
	if fail != nil && fail.missingOutput {
		return base + recommendationReasoningHeadroomTokens
	}
	return base
}

// recommendationCallMaxTokens budgets one call's output: the measured input
// against the model's window, capped by what this call actually needs rather
// than by what the model would allow.
//
// The cap is the point. The old single call asked for the model's whole output
// cap and the model used all of it; a plan is a few hundred tokens and one
// recommendation a few hundred more, so a ceiling in that range makes reaching
// it mean "this model is repeating itself", which is a thing the retry can act
// on. The window arithmetic underneath is the same one the analysis phase uses,
// so a large input still shrinks the output rather than overflowing (#347).
//
// The window is resolved here, per call, rather than once for the phase. The
// adaptive context-overflow retry in internal/ai learns the model's true
// window from a rejected request and hands it to the orchestrator, and a phase
// that now makes a dozen calls should spend that lesson on the next one rather
// than re-learning it on every entry.
func (o *Orchestrator) recommendationCallMaxTokens(ctx context.Context, prompt string, callCap int) int {
	window, modelOutputCap := o.resolveModelBudget()
	limit := callCap
	if o.effectiveReasoning() {
		limit += recommendationReasoningHeadroomTokens
	}
	if modelOutputCap > 0 && modelOutputCap < limit {
		limit = modelOutputCap
	}
	floor := recommendationCallMinOutputTokens()
	if floor > limit {
		floor = limit
	}
	return budgetedMaxOutputTokens(window, approxTokens(ctx, prompt), limit, floor)
}

// recordRecommendationCall appends one call to the step's per-call record and
// accumulates its cost onto the step totals.
func recordRecommendationCall(
	step *models.RecommendationStep,
	kind string,
	attempt, index int,
	res *ai.ChatResult,
	health responseHealth,
	callErr error,
) {
	call := models.RecommendationCall{
		Kind:       kind,
		Index:      index,
		Attempt:    attempt,
		Truncated:  health.truncated,
		Degenerate: health.degenerate,
	}
	if res != nil {
		step.TokensIn += res.TokensIn
		step.TokensOut += res.TokensOut
		step.DurationMs += res.DurationMs

		call.TokensIn = res.TokensIn
		call.TokensOut = res.TokensOut
		call.DurationMs = res.DurationMs
		call.StopReason = res.StopReason
		call.ResponseChars = len([]rune(res.Content))
	}
	if callErr != nil {
		call.Error = callErr.Error()
	}
	step.Calls = append(step.Calls, call)
}

// adoptExpansionResponse reads one expansion response into the planned
// recommendation it answers for: the tolerant parse, the entry match, and the
// fields the plan already decided.
//
// Every path that takes a recommendation out of a response goes through here.
// There were three such paths and each one that did its own selection got it
// wrong in its own way -- taking the first object, accepting a sole object
// unchecked, adopting whichever object carried a citation -- all with the same
// consequence: another entry saved twice, this one lost, and this one's
// citation stamped on the duplicate.
func adoptExpansionResponse(
	step *models.RecommendationStep,
	content string,
	plan []recommendationPlanItem,
	idx int,
	insights []models.Insight,
) (*models.Recommendation, error) {
	parsed, dropped, perr := parseRecommendations(content)
	step.RecommendationsDroppedParse += dropped
	if perr != nil {
		return nil, perr
	}
	if len(parsed) == 0 {
		return nil, errors.New("the response held no recommendation")
	}

	rec, extra, serr := selectPlannedRecommendation(parsed, plan, idx)
	if serr != nil {
		return nil, serr
	}
	// One entry per call. A response that writes more is answering for entries
	// whose own calls will answer for them, so the extras are dropped rather
	// than duplicated into the set.
	if extra > 0 {
		step.RecommendationsDroppedParse += extra
		applog.WithFields(applog.Fields{
			"entry": idx + 1,
			"extra": extra,
		}).Warn("Expansion wrote more than the one planned recommendation; keeping the planned one")
	}
	applyPlanToRecommendation(rec, plan[idx], insights)
	return rec, nil
}

// selectPlannedRecommendation picks, out of one expansion response, the
// recommendation that response was asked for.
//
// A response holding one object is that object. The expansion is told it may
// refine the planned title's wording, so a title that does not match verbatim
// is expected there rather than suspicious.
//
// A response holding several is answering for entries whose own calls will
// answer for them, and taking the first would be wrong exactly where it costs
// most: asked for the second entry and handed [first, second], it would save
// the first twice, lose the second, and stamp the second's planned citations
// onto the duplicate. So the target is matched by title -- first on the
// planned title itself, then as the one object that is not some other planned
// entry's -- and an answer where that cannot be decided is refused and
// re-asked rather than guessed at.
func selectPlannedRecommendation(parsed []models.Recommendation, plan []recommendationPlanItem, idx int) (*models.Recommendation, int, error) {
	if len(parsed) == 0 {
		return nil, 0, errors.New("the response held no recommendation")
	}

	target := normalizeTitleForMatch(plan[idx].Title)
	// The other planned titles, which this response is not the place for. The
	// target is excluded in case a plan repeated a title, so a repeat can
	// never make an entry unanswerable.
	others := make(map[string]struct{}, len(plan))
	for j := range plan {
		if j == idx {
			continue
		}
		if t := normalizeTitleForMatch(plan[j].Title); t != target {
			others[t] = struct{}{}
		}
	}

	matches := make([]int, 0, 1)
	for i := range parsed {
		if normalizeTitleForMatch(parsed[i].Title) == target {
			matches = append(matches, i)
		}
	}
	if len(matches) > 1 {
		return nil, 0, fmt.Errorf("the response wrote %d recommendations and %d of them carry the planned title, so which one was asked for cannot be decided", len(parsed), len(matches))
	}
	if len(matches) == 0 {
		// The title was reworded, which the expansion is allowed to do. Fall
		// back to elimination: the one object that is not some other planned
		// entry restated.
		//
		// This is the check a sole object needs too, and it is the reason
		// there is no shortcut for one: a response that wrote a *different*
		// planned entry -- because it answered for the wrong one, or because
		// the decoder dropped the right one out of a response that held both
		// -- would otherwise be accepted as this entry, saving that entry
		// twice, losing this one, and stamping this one's citation onto the
		// duplicate. A reworded title is unrecognisable; another entry's
		// title is recognisably not this one.
		for i := range parsed {
			if _, isOther := others[normalizeTitleForMatch(parsed[i].Title)]; !isOther {
				matches = append(matches, i)
			}
		}
		if len(matches) != 1 {
			return nil, 0, fmt.Errorf("the response wrote %d recommendation(s) and none of them is unambiguously the planned entry", len(parsed))
		}
	}

	chosen := parsed[matches[0]]
	return &chosen, len(parsed) - 1, nil
}

// normalizeTitleForMatch reduces a title to what two spellings of the same
// title share: case and run-of-whitespace differences are not a different
// recommendation.
func normalizeTitleForMatch(title string) string {
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// applyPlanToRecommendation settles the fields the plan already decided.
//
// related_insight_ids is the one that matters: it is the citation the platform
// resolves figures and insight links against, and the plan entry already names
// the insights this recommendation was planned from. Taking them from the plan
// when the expansion omitted them — or answered with ids that are none of them
// — is a citation the run can stand behind, rather than the blunt downstream
// backfill.
//
// Two ways that could make things worse, both avoided. An expansion that cites
// an eligible insight is never overruled, even by a plan that named different
// ones: narrowing or re-choosing while writing is a judgement, and a real
// citation is the thing being protected here. And the plan's own ids are only
// adopted if they are real — a plan entry whose ids the model invented (the
// slug-shaped failure of issue #237) would otherwise replace a good citation
// with one the downstream check discards, which then backfills every eligible
// insight and loses the provenance entirely.
//
// Category and priority are only filled when the expansion left them empty. A
// model that reconsidered them in the writing is not overruled.
func applyPlanToRecommendation(rec *models.Recommendation, item recommendationPlanItem, eligible []models.Insight) {
	planned := eligibleInsightIDsAmong(item.RelatedInsightIDs, eligible)
	if len(planned) > 0 && !anyRecCitesEligible([]models.Recommendation{*rec}, eligible) {
		rec.RelatedInsightIDs = planned
	}
	if strings.TrimSpace(rec.Category) == "" {
		rec.Category = item.Category
	}
	if rec.Priority == 0 {
		rec.Priority = item.Priority
	}
}

// eligibleInsightIDsAmong returns the ids that name an insight in the eligible
// set, in the order given, with blanks and duplicates removed. Anything else
// is not an id this run can resolve, whatever it looks like.
func eligibleInsightIDsAmong(ids []string, eligible []models.Insight) []string {
	if len(ids) == 0 || len(eligible) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(eligible))
	for i := range eligible {
		if id := strings.TrimSpace(eligible[i].ID); id != "" {
			known[id] = struct{}{}
		}
	}
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if _, ok := known[id]; !ok {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// recommendationPhaseTokens estimates the recommendation phase's token usage
// for the pre-run cost estimate: one plan call, then one expansion call per
// planned recommendation (see the note at the top of this file).
//
// contextTokens is what every call in the phase sends — the base context, the
// recommendation prompt, and the eligible insights. It is the dominant term
// and each call pays it in full, so an estimate that counts it once understates
// the phase by the whole fan-out, and understates it further the richer the
// prompt. The expansion count is the shape the packs ask for, held under the
// configured fan-out bound so a lowered bound lowers the estimate with it and a
// raised one cannot inflate the estimate past what the packs would produce.
//
// Upper-bound arithmetic, like the rest of the estimator: a planning aid, not a
// billing record.
func recommendationPhaseTokens(contextTokens int) (inputTokens, outputTokens int) {
	expansions := estimatedRecommendationsPerRun
	if bound := recommendationPlanMaxItems(); bound < expansions {
		expansions = bound
	}
	if expansions < 0 {
		expansions = 0
	}

	// The plan carries four small fields per entry; one written recommendation
	// carries a description, implementation steps, an impact object and
	// figures.
	const (
		planOutputPerEntryTokens     = 50
		outputPerRecommendationToken = 600
	)

	inputTokens = contextTokens * (1 + expansions)
	outputTokens = planOutputPerEntryTokens*expansions + outputPerRecommendationToken*expansions
	return inputTokens, outputTokens
}
