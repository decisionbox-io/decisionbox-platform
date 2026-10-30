import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import {
  recommendationEmptyReason,
  RECOMMENDATION_EMPTY_FALLBACK,
} from '@/lib/recommendationEmptyReason';

describe('recommendationEmptyReason', () => {
  it('falls back to the plain empty sentence when nothing went wrong', () => {
    expect(recommendationEmptyReason(null)).toBe(RECOMMENDATION_EMPTY_FALLBACK);
    expect(recommendationEmptyReason({})).toBe(RECOMMENDATION_EMPTY_FALLBACK);
  });

  it('explains a response that could not be parsed', () => {
    expect(recommendationEmptyReason({ status: 'recommendation_parse_error' }))
      .toMatch(/could not be parsed/);
  });

  it('explains a run with no eligible insights', () => {
    expect(recommendationEmptyReason({ status: 'skipped_no_eligible_insights' }))
      .toMatch(/No insights passed validation/);
  });

  // The failure this file was extracted for. The agent drops every
  // recommendation whose prose references figures it never declared, and
  // without this branch the page said "No actionable recommendations for the
  // insights found" -- the one sentence a reader takes to mean the data was
  // uninteresting.
  it('explains recommendations dropped for unresolved figures', () => {
    const reason = recommendationEmptyReason({ status: 'recommendation_figures_unresolved' });
    expect(reason).not.toBe(RECOMMENDATION_EMPTY_FALLBACK);
    expect(reason).toMatch(/figures/i);
  });

  it('mentions the figure drops when some were dropped but a status named another cause', () => {
    expect(recommendationEmptyReason({ recommendations_dropped_unresolved_figures: 2 }))
      .toMatch(/figures/i);
  });

  it('prefers the status over a counter, because a status names the cause', () => {
    expect(recommendationEmptyReason({
      status: 'recommendation_parse_error',
      recommendations_dropped_unresolved_figures: 3,
    })).toMatch(/could not be parsed/);
  });

  it('prefers any known cause over a bare generation error', () => {
    expect(recommendationEmptyReason({
      status: 'recommendation_figures_unresolved',
      error: 'context deadline exceeded',
    })).toMatch(/figures/i);
    expect(recommendationEmptyReason({ error: 'context deadline exceeded' }))
      .toMatch(/did not complete/);
  });
});

// The class test. Each branch above covers one status; this covers the next
// one someone adds.
//
// A status is stamped in Go and read here, in another language, in another
// module, reached through two hand-maintained mirrors (the API read model and
// the api.ts type). Every link has already been forgotten at least once: the
// figure counters were mirrored into the API struct only after someone
// noticed, and `recommendation_figures_unresolved` shipped stamped and
// unhandled -- the producer written, the consumer not -- which is what this
// test would have caught.
describe('every recommendation status the agent can stamp is handled here', () => {
  const orchestrator = resolve(process.cwd(), '../../services/agent/internal/discovery/orchestrator.go');

  it('reads the agent source it is asserting against', () => {
    const src = readFileSync(orchestrator, 'utf8');
    expect(src).toContain('RecommendationStep');
  });

  it('handles each one with something other than the fallback', () => {
    const src = readFileSync(orchestrator, 'utf8');

    // Two shapes the agent uses: a named constant, and a literal set inline
    // on a RecommendationStep.
    const fromConstants = [...src.matchAll(/statusRecommendation\w*\s*=\s*"([^"]+)"/g)].map((m) => m[1]);
    const fromLiterals = [...src.matchAll(/RecommendationStep\{[^}]*Status:\s*"([^"]+)"/g)].map((m) => m[1]);
    const statuses = [...new Set([...fromConstants, ...fromLiterals])];

    expect(statuses.length).toBeGreaterThan(0);

    const unhandled = statuses.filter(
      (status) => recommendationEmptyReason({ status }) === RECOMMENDATION_EMPTY_FALLBACK,
    );
    expect(unhandled).toEqual([]);
  });
});
