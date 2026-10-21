import { recommendationEmptyReason } from './recommendationEmptyReason';

describe('recommendationEmptyReason', () => {
  it('tells a truncated response apart from an unparseable one', () => {
    // The whole point of the two statuses: the fixes are different, and
    // before this the budget case fell through to the generic message and
    // sent the investigation down the model-quality path.
    const truncated = recommendationEmptyReason({ status: 'recommendation_truncated' });
    const unparseable = recommendationEmptyReason({ status: 'recommendation_parse_error' });

    expect(truncated).not.toEqual(unparseable);
    expect(truncated).toMatch(/output budget/);
    expect(truncated).toMatch(/RECOMMENDATION_PLAN_MAX_ITEMS/);
    expect(unparseable).toMatch(/could not be parsed/);
  });

  it('explains a run where nothing was eligible', () => {
    expect(recommendationEmptyReason({ status: 'skipped_no_eligible_insights' })).toMatch(
      /No insights passed validation/
    );
  });

  it('reports planned entries that could not be written', () => {
    expect(recommendationEmptyReason({ recommendations_expand_failed: 2 })).toMatch(
      /planned but could not be written/
    );
  });

  it('reports recommendations dropped by the parser', () => {
    expect(recommendationEmptyReason({ recommendations_dropped_parse: 1 })).toMatch(
      /could not be parsed and were dropped/
    );
  });

  it('does not present a failed generation as a valid empty result', () => {
    expect(recommendationEmptyReason({ error: 'context deadline exceeded' })).toMatch(
      /did not complete/
    );
  });

  it('falls back to a plain empty result when nothing went wrong', () => {
    for (const log of [null, {}, { recommendations_dropped_parse: 0, recommendations_expand_failed: 0 }]) {
      expect(recommendationEmptyReason(log)).toEqual('No actionable recommendations for the insights found.');
    }
  });

  it('prefers the status over the counters when both are present', () => {
    // A truncated phase can also have dropped items; the status is the more
    // specific statement and must win.
    expect(
      recommendationEmptyReason({ status: 'recommendation_truncated', recommendations_dropped_parse: 3 })
    ).toMatch(/output budget/);
  });
});
