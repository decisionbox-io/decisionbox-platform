'use client';

import { Badge, HoverCard, Text } from '@mantine/core';
import { toneToMantineColor } from '@/components/validation/statusMeta';
import type { Scoring } from '@/lib/api';
import { confidenceMeta } from './confidenceMeta';
import { noShrink } from './badgeStyle';
import { ScoringBreakdown } from './ScoringBreakdown';

// The confidence band, its reason, and the breakdown behind both.
//
// A band rather than a number, because the number was never worth five
// significant figures: it is a verdict folded with a handful of evidence
// flags, and reporting "0.65" invites a reader to compare it with "0.70" as
// though the difference meant something. The continuous score is kept — it
// still sorts, and it is in the breakdown — but the label is what the reader
// is given.
//
// The reason is rendered verbatim from the document. The engine wrote it, so
// the engine phrases it; a second wording here would drift from the first the
// moment either changed.

export function ConfidenceBadge({
  scoring,
  size = 'sm',
  labelForm = 'full',
}: {
  scoring: Scoring;
  size?: 'xs' | 'sm' | 'md';
  /** `short` drops the axis word for a surface that already names it. */
  labelForm?: 'full' | 'short';
}) {
  const meta = confidenceMeta(scoring.confidence_band);

  return (
    <HoverCard width="auto" shadow="md" withinPortal position="bottom-start" openDelay={120}>
      <HoverCard.Target>
        <span style={{ display: 'inline-flex', alignItems: 'center', cursor: 'help' }}>
          <Badge
            size={size}
            variant="light"
            color={toneToMantineColor(meta.tone)}
            style={noShrink}
            data-testid="confidence-band"
            data-band={scoring.confidence_band ?? ''}
          >
            {labelForm === 'short' ? meta.short : meta.label}
          </Badge>
        </span>
      </HoverCard.Target>
      <HoverCard.Dropdown>
        <ScoringBreakdown scoring={scoring} />
      </HoverCard.Dropdown>
    </HoverCard>
  );
}
