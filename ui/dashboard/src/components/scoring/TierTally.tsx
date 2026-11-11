'use client';

import { Badge, Group, Text, Tooltip } from '@mantine/core';
import { toneToMantineColor } from '@/components/validation/statusMeta';
import type { Insight } from '@/lib/api';
import { priorityMeta, TIER_ORDER } from './priorityMeta';
import { confidenceMeta } from './confidenceMeta';

// The run header's tally: how many findings are in each priority tier, plus
// how many nothing could check.
//
// Computed where the list renders rather than persisted. It is a count over
// fields every run already writes, so storing it would be a second copy of a
// derivable number — one that goes stale the moment a document is re-validated
// from the dashboard, which is exactly when a reader is looking at it.
//
// The `unverifiable` count sits beside the tiers rather than inside them
// because it answers a different question. A tier says what to do; this says
// how much of the run nothing could check — and a run with a high one is
// telling the reader about its own evidence rather than about its findings.

export interface TierCounts {
  tiers: { tier: string; count: number }[];
  unverifiable: number;
  ungraded: number;
}

// countTiers counts by tier, and counts separately the documents that have no
// tier at all.
//
// A legacy insight is NOT folded into a tier here. The run header is a
// statement about this run, and mapping an old severity word onto a tier the
// document was never triaged by would overstate what is known about it. They
// are reported as ungraded instead, which is what they are.
export function countTiers(insights: Pick<Insight, 'evidence_scoring'>[]): TierCounts {
  const byTier = new Map<string, number>();
  let unverifiable = 0;
  let ungraded = 0;

  for (const insight of insights) {
    const scoring = insight.evidence_scoring;
    if (scoring?.confidence_band === 'unverifiable') unverifiable++;
    const tier = scoring?.priority_tier;
    if (!tier) {
      ungraded++;
      continue;
    }
    byTier.set(tier, (byTier.get(tier) ?? 0) + 1);
  }

  return {
    tiers: TIER_ORDER.map((tier) => ({ tier, count: byTier.get(tier) ?? 0 })),
    unverifiable,
    ungraded,
  };
}

export function TierTally({ insights }: { insights: Pick<Insight, 'evidence_scoring'>[] }) {
  const counts = countTiers(insights);

  // Nothing graded at all — a run from before the scorer, or one that
  // produced no insights. A row of zeroes would say less than nothing.
  if (counts.tiers.every((t) => t.count === 0)) return null;

  return (
    <Group gap={6} wrap="wrap" data-testid="tier-tally">
      {counts.tiers.map(({ tier, count }) => {
        const meta = priorityMeta(tier);
        return (
          <Tooltip key={tier} label={meta.gloss} withinPortal>
            <Badge
              size="sm"
              variant="light"
              color={toneToMantineColor(meta.tone)}
              data-testid={`tier-tally-${tier}`}
              // Dimmed rather than hidden at zero: "no findings to act on now"
              // is information, and a tally whose columns come and go cannot
              // be compared between two runs at a glance.
              style={count === 0 ? { opacity: 0.45 } : undefined}
            >
              {count} {meta.label}
            </Badge>
          </Tooltip>
        );
      })}
      {counts.unverifiable > 0 && (
        <Tooltip label={confidenceMeta('unverifiable').gloss} withinPortal>
          <Badge size="sm" variant="light" color="gray" data-testid="tier-tally-unverifiable">
            {counts.unverifiable} unverifiable
          </Badge>
        </Tooltip>
      )}
      {counts.ungraded > 0 && (
        <Text size="xs" c="dimmed" data-testid="tier-tally-ungraded">
          {counts.ungraded} not graded
        </Text>
      )}
    </Group>
  );
}
