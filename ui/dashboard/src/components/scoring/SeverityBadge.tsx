'use client';

import { Badge, HoverCard, Text } from '@mantine/core';
import { toneToMantineColor } from '@/components/validation/statusMeta';
import type { Scoring } from '@/lib/api';
import { driverSummary, notQuantified, severityMeta } from './severityMeta';
import { noShrink } from './badgeStyle';
import { ScoringBreakdown } from './ScoringBreakdown';

// The severity band, the figure that drove it, and the arithmetic behind both.
//
// A band with a number beside it, because the band alone is the thing a reader
// cannot check. "High severity" invites "says who?"; "High severity · $2.4M"
// answers it, and the hover shows the figure the platform re-checked to get
// there, with its verdict.
//
// A missing magnitude renders as *not quantified* rather than as a low band.
// "We could not measure this" is not evidence that the thing is small, and
// collapsing the two would hide exactly the finding whose numbers a model
// failed to declare.

export function SeverityBadge({
  scoring,
  size = 'sm',
  showDriver = true,
  labelForm = 'full',
}: {
  scoring: Scoring;
  size?: 'xs' | 'sm' | 'md';
  // Dense surfaces can drop the driver and keep it in the breakdown; every
  // surface that has room shows it, because the number is the point.
  showDriver?: boolean;
  /** `short` drops the axis word for a surface that already names it. */
  labelForm?: 'full' | 'short';
}) {
  const meta = severityMeta(scoring.severity_band);
  const driver = driverSummary(scoring.severity_driver);
  const unquantified = notQuantified(scoring.severity_driver);

  return (
    <HoverCard width="auto" shadow="md" withinPortal position="bottom-start" openDelay={120}>
      <HoverCard.Target>
        {/* The figure sits UNDER the band, not beside it. Beside, the two
            compete for one column's width in a list whose rows are already
            two or three lines tall for the headline — so the column had to be
            wide enough for a band plus `$2,033` on one line, and bought
            nothing with the vertical space it was already using. Stacked, the
            column is as narrow as its widest single element and the figure
            reads as what it is: the measurement behind the band above it. */}
        <span style={{ display: 'inline-flex', flexDirection: 'column', alignItems: 'flex-start', gap: 2, cursor: 'help' }}>
          <Badge
            size={size}
            variant="light"
            color={toneToMantineColor(meta.tone)}
            style={noShrink}
            data-testid="severity-band"
            data-band={scoring.severity_band ?? ''}
          >
            {labelForm === 'short' ? meta.short : meta.label}
          </Badge>
          {showDriver && driver !== '' && (
            <Text
              size="xs"
              c={unquantified ? 'dimmed' : undefined}
              fs={unquantified ? 'italic' : undefined}
              data-testid="severity-driver"
            >
              {driver}
            </Text>
          )}
        </span>
      </HoverCard.Target>
      <HoverCard.Dropdown>
        <ScoringBreakdown scoring={scoring} axis="severity" />
      </HoverCard.Dropdown>
    </HoverCard>
  );
}
