/**
 * Why the recommendations section is empty, so it is never silent (issue #342).
 *
 * Extracted from the run page to be testable on its own. The branches are the
 * point: an empty section with no reason sent every investigation to the same
 * place, and the reasons are not interchangeable — a response that could not be
 * parsed is a prompt or model-quality question, a response that ran out of
 * output budget is a budget one, and a run that generated nothing because
 * nothing passed validation is neither.
 */
export type RecommendationLogSummary = {
  status?: string;
  recommendations_dropped_parse?: number;
  recommendations_expand_failed?: number;
  error?: string;
} | null;

export function recommendationEmptyReason(recLog: RecommendationLogSummary): string {
  if (recLog?.status === 'recommendation_parse_error') {
    return 'The recommendation model returned a response that could not be parsed, so none were saved. This has been logged for investigation.';
  }
  if (recLog?.status === 'recommendation_truncated') {
    // Distinct from the parse error on purpose: the response was not
    // malformed, it ran out of room. The operator-facing fix is a budget one,
    // and the generic message below sent that investigation down the
    // model-quality path instead.
    return 'The recommendation model ran out of output budget before it finished a response, so none were saved. Raising the model’s output limit, or lowering RECOMMENDATION_PLAN_MAX_ITEMS, is the usual fix.';
  }
  if (recLog?.status === 'skipped_no_eligible_insights') {
    return 'No insights passed validation, so no recommendations were generated.';
  }
  if ((recLog?.recommendations_dropped_parse ?? 0) > 0) {
    return 'Some recommendations could not be parsed and were dropped. See the recommendation log for details.';
  }
  if ((recLog?.recommendations_expand_failed ?? 0) > 0) {
    return 'Some recommendations were planned but could not be written. See the recommendation log for details.';
  }
  if (recLog?.error) {
    // Generation failed (e.g. LLM timeout / rate limit) before parsing —
    // don't present it as a valid empty result.
    return 'Recommendation generation did not complete (see the recommendation log). This has been logged for investigation.';
  }
  return 'No actionable recommendations for the insights found.';
}
