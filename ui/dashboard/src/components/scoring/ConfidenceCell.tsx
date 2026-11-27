'use client';

import type { Scoring } from '@/lib/api';
import { ConfidenceBadge } from './ConfidenceBadge';
import { LegacyConfidenceBadge } from './LegacyConfidenceBadge';

// The one entry point every surface that used to render a confidence number
// should use.
//
// The branch is a single fact: a document with a stored `evidence_scoring` has
// a derived band, and one without it was written before the scorer existed. A
// component per surface deciding that for itself is how one page ends up
// labelling a legacy number as High.

export function ConfidenceCell({
  scoring,
  confidence,
  size = 'sm',
  labelForm = 'full',
}: {
  scoring: Scoring | undefined;
  confidence: number;
  size?: 'xs' | 'sm' | 'md';
  /** Passed to the badge: `short` drops the axis word. */
  labelForm?: 'full' | 'short';
}) {
  if (scoring) {
    return <ConfidenceBadge scoring={scoring} size={size} labelForm={labelForm} />;
  }
  return <LegacyConfidenceBadge confidence={confidence} />;
}
