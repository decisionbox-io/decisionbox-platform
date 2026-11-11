'use client';

import { Group, Stack, Text, Divider } from '@mantine/core';
import type { Scoring, ScoringEngine } from '@/lib/api';
import { confidenceMeta, formatDock, formatScore } from './confidenceMeta';
import { figureWords, notQuantified, severityMeta } from './severityMeta';
import { matrixCellWords, priorityMeta } from './priorityMeta';
import { engineFor, useScoringEngines } from './useScoringEngines';

// "How this was scored", for whichever axis was opened — or all three on a
// detail page.
//
// The arithmetic on screen adds up exactly, and that is a property of the
// stored data rather than of this component. The engine trims a deduction the
// cap or the floor reduced, and drops one trimmed to nothing, so that
// base − Σamounts == final always holds. The alternative — storing the
// untrimmed amounts and having this render "a cap applied" — puts two places
// in charge of one piece of arithmetic and leaves the column not adding up for
// the reader, which is the one thing a breakdown must never do.
//
// Everything here except the cutoff lines comes off the document. The cutoffs
// come from the engine the document says scored it, so an API older than the
// agent costs those lines and nothing else.

export type ScoringAxis = 'confidence' | 'severity' | 'priority' | 'all';

// cutoffLine says which band boundary a score crossed.
//
// Only the three scored confidence bands have one. `not_confirmed` and
// `unverifiable` come from the verdict and are terminal, so there is no
// boundary to name — and saying so is better than leaving the line blank,
// which would read as the cutoff having failed to load.
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

// terminalBands are the two decided by the verdict rather than by a cutoff.
const terminalBands = new Set(['not_confirmed', 'unverifiable']);

function Row({ label, value, testId, dimmed, strong }: {
  label: string;
  value: string;
  testId?: string;
  dimmed?: boolean;
  strong?: boolean;
}) {
  return (
    <Group justify="space-between" gap="xs" wrap="nowrap" align="flex-start">
      <Text size="xs" c={dimmed ? 'dimmed' : undefined} fw={strong ? 600 : undefined} style={{ flex: 1 }}>
        {label}
      </Text>
      <Text size="xs" fw={strong ? 700 : 600} data-testid={testId} style={{ whiteSpace: 'nowrap' }}>
        {value}
      </Text>
    </Group>
  );
}

function ConfidenceSection({ scoring, engine }: { scoring: Scoring; engine?: ScoringEngine }) {
  const meta = confidenceMeta(scoring.confidence_band);
  const base = scoring.confidence_base ?? 0;
  const final = scoring.confidence_score ?? 0;
  const docks = scoring.confidence_docks ?? [];
  const band = scoring.confidence_band ?? '';

  const cutoff = terminalBands.has(band)
    ? 'set by the validation verdict, not by a score cutoff'
    : engine
      ? cutoffLine(band, engine.confidence.cutoffs.high, engine.confidence.cutoffs.medium)
      : '';

  return (
    <>
      {scoring.confidence_reason && (
        <Text size="xs" c="dimmed">{scoring.confidence_reason}</Text>
      )}

      <Row label="Validation verdict" value={formatScore(base)} testId="scoring-base" />

      {docks.map((dock, i) => (
        <Group key={`${dock.code}-${i}`} justify="space-between" gap="xs" wrap="nowrap" align="flex-start">
          <Text size="xs" c="dimmed" style={{ flex: 1 }}>{dock.label}</Text>
          <Text size="xs" fw={600} c="red" data-testid={`scoring-dock-${dock.code}`}>
            {formatDock(dock.amount)}
          </Text>
        </Group>
      ))}

      <Divider />
      <Row label={meta.label} value={formatScore(final)} testId="scoring-final" strong />

      {/* Omitted, not faked, when the engine that scored this document is not
          one this API knows — which is what a dashboard newer than its API, or
          older than its agent, will see. */}
      {cutoff !== '' && (
        <Text size="xs" c="dimmed" data-testid="scoring-cutoff">{cutoff}</Text>
      )}
    </>
  );
}

// SeveritySection renders the severity derivation: magnitude → × weight →
// final → the cutoff crossed, with the checked figure behind the number shown.
//
// Two figures can appear, and the second line is the one that explains the
// case a reader would otherwise read as a bug: a finding with a grounded $2.4M
// banded Medium off a 4% share. Amount bands are a per-business threshold and
// ship unconfigured, so the amount is the money at stake and the share is what
// ranked it. Saying both is the only honest rendering of that.
function SeveritySection({ scoring, engine }: { scoring: Scoring; engine?: ScoringEngine }) {
  const meta = severityMeta(scoring.severity_band);
  const magnitude = scoring.magnitude_score ?? 0;
  const weight = scoring.area_weight ?? 1;
  const final = scoring.severity_score ?? 0;
  const driver = scoring.severity_driver;
  const unquantified = notQuantified(driver);

  // engine?.severity rather than engine.severity, and the difference is a
  // crash. An API from before this axis shipped serves engine version 1 with
  // a confidence object and nothing else -- and version 1 is what this
  // build's own documents carry, so the lookup by version matches it and
  // returns an engine whose severity calibration is simply absent. Missing
  // calibration costs the lines that depend on it and nothing else, which is
  // the same contract the confidence cutoff line already honoured.
  const severityCfg = engine?.severity;
  const cutoff = severityCfg
    ? cutoffLine(scoring.severity_band ?? '', severityCfg.cutoffs.high, severityCfg.cutoffs.medium)
    : '';

  const weightSource = scoring.weight_source;
  const weightWords = weightSource?.kind === 'category' && weightSource.category
    ? `× ${weight} weight (${weightSource.category})`
    : weightSource?.kind === 'area_override'
      ? `× ${weight} weight (set for this area)`
      : `× ${weight} weight (default)`;

  return (
    <>
      {scoring.severity_reason && (
        <Text size="xs" c="dimmed" data-testid="severity-reason">{scoring.severity_reason}</Text>
      )}

      <Row
        label={unquantified ? 'Area baseline — not quantified' : 'Magnitude'}
        value={magnitude.toFixed(2)}
        testId="severity-magnitude"
        dimmed={unquantified}
      />

      {driver?.figure && (
        <Row
          label={driver.kind === 'amount' ? 'From the checked amount' : 'From the checked share'}
          value={figureWords(driver.figure)}
          testId="severity-banded-by"
          dimmed
        />
      )}
      {/* Shown whenever the document states one, and labelled as what it is
          rather than as what banded the magnitude. With amount bands
          unconfigured these are usually two different figures. */}
      {driver?.amount && driver.amount.figure_id !== driver.figure?.figure_id && (
        <Row
          label="Amount at stake"
          value={figureWords(driver.amount)}
          testId="severity-amount"
          dimmed
        />
      )}
      {/* Why a grounded amount did not set the band. Two different
          sentences, because there are two different outcomes and one wording
          for both contradicted the line above it: a share-banded document was
          ranked on its share, and a baseline one was ranked on nothing. */}
      {severityCfg && severityCfg.amount_bands.length === 0 && driver?.amount
        && driver.kind === 'share' && (
        <Text size="xs" c="dimmed" data-testid="severity-no-amount-bands">
          No amount thresholds are configured, so the band comes from the share.
        </Text>
      )}
      {severityCfg && severityCfg.amount_bands.length === 0 && driver?.amount
        && driver.kind === 'baseline' && (
        <Text size="xs" c="dimmed" data-testid="severity-no-amount-bands">
          No amount thresholds are configured and this finding states no share, so
          nothing could rank the amount and the area baseline applies.
        </Text>
      )}

      <Text size="xs" c="dimmed" data-testid="severity-weight">{weightWords}</Text>

      <Divider />
      <Row label={meta.label} value={final.toFixed(2)} testId="severity-final" strong />

      {cutoff !== '' && (
        <Text size="xs" c="dimmed" data-testid="severity-cutoff">{cutoff}</Text>
      )}
    </>
  );
}

// PrioritySection renders the matrix cell and the two bands that selected it.
//
// No arithmetic, because priority stores none: the cell is recomputable from
// the two bands, which is exactly what a reader is being shown. The sort score
// is deliberately absent — it is a key, not a judgement.
function PrioritySection({ scoring }: { scoring: Scoring }) {
  const meta = priorityMeta(scoring.priority_tier, scoring.confidence_band);
  const cell = matrixCellWords(scoring.severity_band, scoring.confidence_band);

  return (
    <>
      <Text size="xs" fw={700} data-testid="priority-label">{meta.label}</Text>
      <Text size="xs" c="dimmed">{meta.gloss}</Text>
      {cell !== '' && (
        <Text size="xs" data-testid="priority-cell">{cell}</Text>
      )}
    </>
  );
}

export function ScoringBreakdown({
  scoring,
  axis = 'confidence',
}: {
  scoring: Scoring;
  axis?: ScoringAxis;
}) {
  const engines = useScoringEngines();
  const engine = engineFor(engines, scoring.engine_version);

  // A document an earlier engine version scored on the confidence axis alone
  // has no severity half, so the sections that would describe it are skipped
  // rather than rendered against zeroes.
  const hasSeverity = scoring.severity_band != null;
  const hasPriority = scoring.priority_tier != null;

  const showConfidence = axis === 'confidence' || axis === 'all';
  const showSeverity = (axis === 'severity' || axis === 'all') && hasSeverity;
  const showPriority = (axis === 'priority' || axis === 'all') && hasPriority;

  return (
    <Stack gap={6} style={{ minWidth: 260, maxWidth: 420 }}>
      <Text size="sm" fw={600}>How this was scored</Text>

      {showPriority && <PrioritySection scoring={scoring} />}

      {showPriority && (showSeverity || showConfidence) && <Divider />}

      {showSeverity && <SeveritySection scoring={scoring} engine={engine} />}

      {showSeverity && showConfidence && <Divider />}

      {showConfidence && <ConfidenceSection scoring={scoring} engine={engine} />}

      <Text size="xs" c="dimmed" data-testid="scoring-engine">
        Scoring engine v{scoring.engine_version}
      </Text>
    </Stack>
  );
}
