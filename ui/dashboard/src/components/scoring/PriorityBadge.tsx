'use client';

import { Badge, HoverCard } from '@mantine/core';
import { toneToMantineColor } from '@/components/validation/statusMeta';
import type { Scoring } from '@/lib/api';
import { priorityMeta } from './priorityMeta';
import { ScoringBreakdown } from './ScoringBreakdown';

// The priority tier, with the matrix cell that selected it one hover away.
//
// A tier rather than a number, because the number is a sort key: it folds a
// tier weight together with two small refinements, and showing "1.1225" would
// invite a reader to compare it with "1.1150" as though the difference were a
// judgement. The tier is the judgement; the score only orders findings inside
// it.

export function PriorityBadge({
  scoring,
  size = 'sm',
}: {
  scoring: Scoring;
  size?: 'xs' | 'sm' | 'md';
}) {
  const meta = priorityMeta(scoring.priority_tier, scoring.confidence_band);

  return (
    <HoverCard width="auto" shadow="md" withinPortal position="bottom-start" openDelay={120}>
      <HoverCard.Target>
        <span style={{ display: 'inline-flex', alignItems: 'center', cursor: 'help' }}>
          <Badge
            size={size}
            variant="light"
            color={toneToMantineColor(meta.tone)}
            data-testid="priority-tier"
            data-tier={scoring.priority_tier ?? ''}
          >
            {meta.label}
          </Badge>
        </span>
      </HoverCard.Target>
      <HoverCard.Dropdown>
        <ScoringBreakdown scoring={scoring} axis="priority" />
      </HoverCard.Dropdown>
    </HoverCard>
  );
}
