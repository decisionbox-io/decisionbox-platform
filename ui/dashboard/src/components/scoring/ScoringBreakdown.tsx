'use client';

import { Group, Stack, Text, Divider } from '@mantine/core';
import type { Scoring } from '@/lib/api';
import { confidenceMeta, formatDock, formatScore } from './confidenceMeta';
import { engineFor, useScoringEngines } from './useScoringEngines';

// "How this was scored": base → each deduction with its amount → final → the
// cutoff the final crossed → the engine version that did it.
//
// The arithmetic on screen adds up exactly, and that is a property of the
// stored data rather than of this component. The engine trims a deduction the
// cap or the floor reduced, and drops one trimmed to nothing, so that
// base − Σamounts == final always holds. The alternative — storing the
// untrimmed amounts and having this render "a cap applied" — puts two places
// in charge of one piece of arithmetic and leaves the column not adding up for
// the reader, which is the one thing a breakdown must never do.
//
// Everything here except the cutoff line comes off the document. The cutoff
// comes from the engine the document says scored it, so an API older than the
// agent costs this one line and nothing else.

function cutoffLine(band: string, high: number, medium: number): string {
  switch (band) {
    case 'high':
      return `at or above ${formatScore(high)} → High`;
    case 'medium':
      return `at or above ${formatScore(medium)} → Medium`;
    case 'low':
      return `below ${formatScore(medium)} → Low`;
    default:
      return '';
  }
}

export function ScoringBreakdown({ scoring }: { scoring: Scoring }) {
  const engines = useScoringEngines();
  const engine = engineFor(engines, scoring.engine_version);
  const meta = confidenceMeta(scoring.confidence_band);

  const base = scoring.confidence_base ?? 0;
  const final = scoring.confidence_score ?? 0;
  const docks = scoring.confidence_docks ?? [];

  const cutoff = engine
    ? cutoffLine(scoring.confidence_band ?? '', engine.confidence.cutoffs.high, engine.confidence.cutoffs.medium)
    : '';

  return (
    <Stack gap={6} style={{ minWidth: 260, maxWidth: 420 }}>
      <Text size="sm" fw={600}>How this was scored</Text>

      {scoring.confidence_reason && (
        <Text size="xs" c="dimmed">{scoring.confidence_reason}</Text>
      )}

      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text size="xs">Validation verdict</Text>
        <Text size="xs" fw={600} data-testid="scoring-base">{formatScore(base)}</Text>
      </Group>

      {docks.map((dock, i) => (
        <Group key={`${dock.code}-${i}`} justify="space-between" gap="xs" wrap="nowrap" align="flex-start">
          <Text size="xs" c="dimmed" style={{ flex: 1 }}>{dock.label}</Text>
          <Text size="xs" fw={600} c="red" data-testid={`scoring-dock-${dock.code}`}>
            {formatDock(dock.amount)}
          </Text>
        </Group>
      ))}

      <Divider />

      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text size="xs" fw={600}>{meta.label}</Text>
        <Text size="xs" fw={700} data-testid="scoring-final">{formatScore(final)}</Text>
      </Group>

      {/* Omitted, not faked, when the engine that scored this document is not
          one this API knows — which is what a dashboard newer than its API, or
          older than its agent, will see. */}
      {cutoff !== '' && (
        <Text size="xs" c="dimmed" data-testid="scoring-cutoff">{cutoff}</Text>
      )}

      <Text size="xs" c="dimmed" data-testid="scoring-engine">
        Scoring engine v{scoring.engine_version}
      </Text>
    </Stack>
  );
}
