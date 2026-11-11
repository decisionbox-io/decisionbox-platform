// Decision-maker copy for the four priority tiers, and the words for the
// matrix cell that selected one.
//
// Labels, tone and the matrix wording only. Which cell a document is in comes
// off the document's own two bands, and the cutoffs behind those come from the
// engine the document says scored it — so nothing here is a second copy of a
// number.
//
// `critical` is a tier, not a severity. It is the cell where a severe finding
// meets evidence that held up, which is what the word always meant and what
// the severity field could never say on its own.

import type { StatusTone } from '@/components/validation/statusMeta';
import type { ConfidenceBand } from './confidenceMeta';
import type { PriorityTier, SeverityBand } from '@/lib/api';

export interface PriorityMeta {
  label: string;
  // What a reader should do about a document in this tier. Short, because it
  // sits in a badge's hover beside the matrix cell that produced it.
  gloss: string;
  tone: StatusTone;
}

const META: Record<string, PriorityMeta> = {
  critical: {
    label: 'Act now',
    gloss: 'Severe, and the warehouse rows back it up.',
    tone: 'red',
  },
  // Above the `high` tier, deliberately. A severe finding nothing could check
  // is the one a reader most needs to see: severity sets the ceiling and
  // confidence only refines inside it, so this is "worth a look", not
  // "probably nothing".
  investigate: {
    label: 'Investigate',
    gloss: 'Severe if true, and nothing could check it yet.',
    tone: 'amber',
  },
  high: {
    label: 'High',
    gloss: 'Material and checked, but not severe.',
    tone: 'blue',
  },
  low: {
    label: 'Low',
    gloss: 'Small, or too weakly evidenced to act on.',
    tone: 'grey',
  },
};

const FALLBACK: PriorityMeta = {
  label: 'Unknown',
  gloss: 'Tier not recognised — this document was scored by a newer version.',
  tone: 'grey',
};

export function priorityMeta(tier: string | undefined): PriorityMeta {
  if (!tier) return FALLBACK;
  return META[tier] ?? FALLBACK;
}

// tierOrder ranks the tiers for a client-side sort, highest first.
export const TIER_ORDER: PriorityTier[] = ['critical', 'investigate', 'high', 'low'];

const ORDER: Record<string, number> = {
  critical: 0,
  investigate: 1,
  high: 2,
  low: 3,
};

export function priorityTierOrder(tier: string | undefined): number {
  if (!tier) return 9;
  return ORDER[tier] ?? 9;
}

// trustedConfidenceBands is the confidence half of the matrix, collapsed.
//
// Two groups rather than five columns, because the matrix answers one question
// — can a reader act on this without checking it first — and three of the five
// bands give different reasons for the same "no". They are NOT equivalent
// elsewhere: `low` means checked and contradicted, `unverifiable` means
// nothing could check it, `not_confirmed` means nothing tried, and the
// confidence badge says which. They agree only here.
//
// Mirrors the engine's own partition, and the API serves it
// (`priority.trusted_confidence_bands`) so a breakdown can name the column
// without this list being the authority.
const TRUSTED = new Set<string>(['high', 'medium']);

export function isTrustedConfidence(band: ConfidenceBand | string | undefined): boolean {
  if (!band) return false;
  return TRUSTED.has(band);
}

// matrixCellWords is the sentence a priority breakdown opens with: the two
// bands that selected the tier, in the words the matrix uses for them.
export function matrixCellWords(
  severityBand: SeverityBand | string | undefined,
  confidenceBand: ConfidenceBand | string | undefined,
): string {
  if (!severityBand) return '';
  const trust = isTrustedConfidence(confidenceBand)
    ? 'evidence that held up'
    : 'evidence that did not hold up, or was never checked';
  return `${severityBand} severity × ${trust}`;
}
