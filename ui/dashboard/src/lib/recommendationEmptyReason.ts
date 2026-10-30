/**
 * Why the recommendations section is empty.
 *
 * Extracted from the run page so it can be tested. The mapping is the only
 * thing standing between a real failure and the sentence a reader takes to
 * mean "the data had nothing in it": every branch here exists because some
 * failure was once indistinguishable from an ordinary empty result.
 *
 * Statuses are checked before counters. A status is stamped by the phase that
 * failed and names one cause; a counter can be non-zero in a run that still
 * produced recommendations for other reasons.
 */
export type RecommendationLogForEmptyReason = {
  status?: string;
  recommendations_dropped_parse?: number;
  recommendations_dropped_unresolved_figures?: number;
  error?: string;
} | null | undefined;

export const RECOMMENDATION_EMPTY_FALLBACK =
  'No actionable recommendations for the insights found.';

export function recommendationEmptyReason(recLog: RecommendationLogForEmptyReason): string {
  if (recLog?.status === 'recommendation_parse_error') {
    return 'The recommendation model returned a response that could not be parsed, so none were saved. This has been logged for investigation.';
  }
  if (recLog?.status === 'skipped_no_eligible_insights') {
    return 'No insights passed validation, so no recommendations were generated.';
  }
  if (recLog?.status === 'recommendation_figures_unresolved') {
    // The model wrote figure references and never declared the figures, so
    // every number it promised resolved to nothing. Shipping those would put
    // a literal "{{f1}}" where an amount belongs, so they were dropped.
    return 'The recommendations referenced figures the model did not declare, so their numbers could not be verified and none were saved. This has been logged for investigation.';
  }
  if ((recLog?.recommendations_dropped_parse ?? 0) > 0) {
    return 'Some recommendations could not be parsed and were dropped. See the recommendation log for details.';
  }
  if ((recLog?.recommendations_dropped_unresolved_figures ?? 0) > 0) {
    // Reached when the gate dropped some but a status was already set by an
    // earlier, more specific failure — the counter is still the useful detail.
    return 'Some recommendations referenced figures the model did not declare and were dropped. See the recommendation log for details.';
  }
  if (recLog?.error) {
    // Generation failed (e.g. LLM timeout / rate limit) before parsing —
    // don't present it as a valid empty result.
    return 'Recommendation generation did not complete (see the recommendation log). This has been logged for investigation.';
  }
  return RECOMMENDATION_EMPTY_FALLBACK;
}
