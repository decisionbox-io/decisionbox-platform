/**
 * The pure copy helpers behind the severity and priority surfaces.
 *
 * Separate from SeverityAndPriority.test.tsx, which drives the rendered
 * components: these are the fallback arms a component test reaches only by
 * accident, and each one answers "what does a reader see when the document
 * does not carry what this function wants". An empty string on screen is the
 * shape all of them exist to avoid.
 */
import {
  driverSummary,
  figureWords,
  isLegacySeverityWord,
  notQuantified,
  severityBandOrder,
  severityMeta,
} from '@/components/scoring/severityMeta';
import {
  isTrustedConfidence,
  matrixCellWords,
  priorityMeta,
  priorityTierOrder,
} from '@/components/scoring/priorityMeta';
import { confidenceMeta } from '@/components/scoring/confidenceMeta';
import type { SeverityDriver } from '@/lib/api';

describe('driverSummary', () => {
  const amount = { figure_id: 'rev', display: '$2.40M', value: 2400000, unit: 'currency', currency: '$', verdict: 'holds' };
  const share = { figure_id: 'pct', display: '41.2%', value: 41.2, unit: 'percent', verdict: 'holds' };

  it('leads with the amount when the document carries one', () => {
    const d = { kind: 'share', quantified: true, figure: share, amount } as unknown as SeverityDriver;
    expect(driverSummary(d)).toBe('$2.40M');
  });

  // The share-banded case with no amount: the row shows the number that
  // actually ranked the finding, which is the only number there is.
  it('falls back to the banding figure when there is no amount', () => {
    const d = { kind: 'share', quantified: true, figure: share } as unknown as SeverityDriver;
    expect(driverSummary(d)).toBe('41.2%');
  });

  // A driver with neither display is a document an older engine wrote, or one
  // whose verdict carried no rendition. An empty string is correct here: the
  // row renders nothing rather than the word "undefined".
  it('returns empty when neither figure has a rendition', () => {
    expect(driverSummary({ kind: 'share', quantified: true } as unknown as SeverityDriver)).toBe('');
    expect(driverSummary(undefined)).toBe('');
  });

  // The baseline cases are the pair that must not read alike: an amount that
  // exists but could not rank the finding, and no figure at all.
  it('marks a baseline driver not quantified, with its amount when it has one', () => {
    const withAmount = { kind: 'baseline', quantified: false, amount } as unknown as SeverityDriver;
    expect(driverSummary(withAmount)).toBe('$2.40M · not quantified');
    const bare = { kind: 'baseline', quantified: false } as unknown as SeverityDriver;
    expect(driverSummary(bare)).toBe('not quantified');
  });
});

describe('notQuantified', () => {
  it('is true only for an explicit false', () => {
    expect(notQuantified({ kind: 'baseline', quantified: false } as unknown as SeverityDriver)).toBe(true);
    expect(notQuantified({ kind: 'share', quantified: true } as unknown as SeverityDriver)).toBe(false);
    // Absent, not false: a document with no driver has not told us it could
    // not be measured, so the marker stays off.
    expect(notQuantified(undefined)).toBe(false);
  });
});

describe('figureWords', () => {
  it('prefers the rendition, falls back to the reference, then to nothing', () => {
    expect(figureWords({ display: '41.2%', verdict: 'holds' } as never)).toBe('41.2% (holds)');
    expect(figureWords({ display: '41.2%' } as never)).toBe('41.2%');
    expect(figureWords({ figure_id: 'pct', verdict: 'fails' } as never)).toBe('figure pct (fails)');
    expect(figureWords({} as never)).toBe('');
    expect(figureWords(undefined)).toBe('');
  });
});

// isLegacySeverityWord gates the legacy badge. `critical` is the single word
// the vocabulary change moved, so nothing else may claim to be legacy -- a
// false positive here renders a current band as an old one.
describe('isLegacySeverityWord', () => {
  it('is true for critical and nothing else', () => {
    expect(isLegacySeverityWord('critical')).toBe(true);
    for (const w of ['high', 'medium', 'low', 'info', '', undefined]) {
      expect(isLegacySeverityWord(w)).toBe(false);
    }
  });
});

describe('the band and tier fallbacks', () => {
  it('severityMeta answers for an absent or unknown band', () => {
    expect(severityMeta(undefined).label).toBe('Unknown severity');
    expect(severityMeta('severe-ish').label).toBe('Unknown severity');
    expect(severityMeta('high').label).toBe('High severity');
    // The gloss says WHY it is unknown, so a reader on an older dashboard is
    // not left thinking the grade is missing.
    expect(severityMeta('severe-ish').gloss).toMatch(/newer version/);
  });

  it('confidenceMeta answers for an absent or unknown band', () => {
    expect(confidenceMeta(undefined).label).toBe('Unknown');
    expect(confidenceMeta('quite-sure').label).toBe('Unknown');
    expect(confidenceMeta(undefined).gloss).toMatch(/newer version/);
  });

  it('priorityMeta answers for an absent or unknown tier', () => {
    expect(priorityMeta(undefined).label).toBe('Unknown');
    expect(priorityMeta('urgent').label).toBe('Unknown');
    expect(priorityMeta('critical').label).toBe('Act now');
  });

  // Sort helpers put anything they do not recognise last rather than first:
  // an unrecognised grade must not outrank a real one.
  it('sorts unknown values last', () => {
    expect(severityBandOrder(undefined)).toBeGreaterThan(severityBandOrder('low'));
    expect(severityBandOrder('severe-ish')).toBeGreaterThan(severityBandOrder('low'));
    expect(priorityTierOrder(undefined)).toBeGreaterThan(priorityTierOrder('low'));
    expect(priorityTierOrder('urgent')).toBeGreaterThan(priorityTierOrder('low'));
    // `critical` ties with `high` so a legacy document does not sort below a
    // new one that uses the newer word for the same thing.
    expect(severityBandOrder('critical')).toBe(severityBandOrder('high'));
  });
});

describe('isTrustedConfidence', () => {
  it('is the two-group collapse, and an absent band is not trusted', () => {
    expect(isTrustedConfidence('high')).toBe(true);
    expect(isTrustedConfidence('medium')).toBe(true);
    for (const b of ['low', 'unverifiable', 'not_confirmed']) {
      expect(isTrustedConfidence(b)).toBe(false);
    }
    // Absent means nothing has checked it, which is the untrusted column --
    // defaulting the other way would promote an ungraded document.
    expect(isTrustedConfidence(undefined)).toBe(false);
  });
});

describe('matrixCellWords', () => {
  it('names both halves of the cell', () => {
    expect(matrixCellWords('high', 'high')).toBe('high severity × evidence that held up');
    expect(matrixCellWords('high', 'unverifiable'))
      .toBe('high severity × evidence that did not hold up, or was never checked');
  });

  // Without a severity band there is no cell, and a half-sentence about
  // confidence alone would describe a grade the document does not have.
  it('says nothing without a severity band', () => {
    expect(matrixCellWords(undefined, 'high')).toBe('');
    expect(matrixCellWords('', 'high')).toBe('');
  });
});

// The Investigate tier is specialised by what the evidence actually did,
// because its three untrusted bands got there for three different reasons.
describe('the Investigate gloss', () => {
  it('distinguishes refuted, unverifiable and never-checked', () => {
    expect(priorityMeta('investigate', 'low').gloss).toMatch(/weak or contradicts/);
    expect(priorityMeta('investigate', 'unverifiable').gloss).toMatch(/nothing in the cited evidence/);
    expect(priorityMeta('investigate', 'not_confirmed').gloss).toMatch(/nothing has checked it yet/);
  });

  it('falls back to the wording that covers all three', () => {
    // No band in hand -- the run header's tally renders a tier with no
    // document behind it.
    expect(priorityMeta('investigate').gloss).toMatch(/does not back it/);
    // A band this build does not know must not silently pick one of the three.
    expect(priorityMeta('investigate', 'mysterious').gloss).toMatch(/does not back it/);
  });

  it('does not specialise a tier that is not Investigate', () => {
    expect(priorityMeta('critical', 'low').gloss).toMatch(/warehouse rows back it up/);
  });
});
