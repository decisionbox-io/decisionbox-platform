'use client';

import type { Scoring } from '@/lib/api';
import { SeverityBadge } from './SeverityBadge';
import { LegacySeverityBadge } from './LegacySeverityBadge';

// The one entry point every surface that used to render a severity word should
// use.
//
// The branch is a single fact: a document with a stored `severity_band` has a
// derived band, and one without it carries the analysis model's own word. Note
// that this is NOT "is there a scoring block" — a document an earlier engine
// version scored on the confidence axis alone has one and no severity half, so
// testing the block would label its model-written word as derived.
//
// A component per surface deciding that for itself is how one page ends up
// rendering a legacy `critical` as though the current engine had chosen it.

export function SeverityCell({
  scoring,
  severity,
  size = 'sm',
  showDriver = true,
}: {
  scoring: Scoring | undefined;
  severity: string;
  size?: 'xs' | 'sm' | 'md';
  showDriver?: boolean;
}) {
  if (scoring?.severity_band) {
    return <SeverityBadge scoring={scoring} size={size} showDriver={showDriver} />;
  }
  return <LegacySeverityBadge severity={severity} />;
}
