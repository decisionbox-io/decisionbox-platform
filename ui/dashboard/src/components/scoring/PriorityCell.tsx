'use client';

import { Text } from '@mantine/core';
import type { Scoring } from '@/lib/api';
import { PriorityBadge } from './PriorityBadge';

// The entry point for a priority column.
//
// A document with no tier gets an em dash rather than a badge, and that is the
// honest rendering: priority is a rollup of two bands, so a document missing
// the severity half has no cell to be in. Inventing a tier for it — "low",
// say — would claim it had been triaged and found unimportant, when nothing
// has looked at it. It sorts last for the same reason.

export function PriorityCell({
  scoring,
  size = 'sm',
}: {
  scoring: Scoring | undefined;
  size?: 'xs' | 'sm' | 'md';
}) {
  if (!scoring?.priority_tier) {
    return (
      <Text size="xs" c="dimmed" data-testid="priority-ungraded" title="Not graded by the scoring engine">
        —
      </Text>
    );
  }
  return <PriorityBadge scoring={scoring} size={size} />;
}
