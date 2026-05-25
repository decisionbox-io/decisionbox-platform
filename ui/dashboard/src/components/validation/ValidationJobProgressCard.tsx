'use client';

import { Button, Card, Group, Loader, Stack, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';
import { useEffect, useState } from 'react';
import type { ValidationJob, ValidationJobStep } from '@/lib/api';

// In-flight validation card. Shows the current step ("Queued" /
// "Verifier running" / "Refuter running" / "Combining"), elapsed
// time, and a cancel button. On terminal status (failed / cancelled)
// the card swaps to an error + retry surface — the router takes over
// once `validation` is repopulated on the underlying doc.
//
// Step copy is decision-maker friendly: no jargon-shaped agent names
// in the user-visible string; the badge that says which agent is
// running is enough.

const STEP_COPY: Record<ValidationJobStep | 'queued', string> = {
  queued: 'Queued — waiting for a worker',
  verifier: 'Verifier running — checking claims against cited evidence',
  refuter: 'Refuter running — searching for contradicting evidence',
  combining: 'Combining verdicts — finalising',
};

function elapsedLabel(startISO: string | undefined): string {
  if (!startISO) return '';
  const start = new Date(startISO).getTime();
  if (Number.isNaN(start)) return '';
  const sec = Math.max(0, Math.floor((Date.now() - start) / 1000));
  if (sec < 60) return `${sec}s elapsed`;
  return `${Math.floor(sec / 60)}m ${sec % 60}s elapsed`;
}

export function ValidationJobProgressCard({
  job,
  onCancel,
  onRetry,
}: {
  job: ValidationJob;
  onCancel?: (jobId: string) => void;
  onRetry?: () => void;
}) {
  // Re-render every second so elapsed-time stays live without
  // depending on the polling cadence of the router.
  const [, force] = useState(0);
  useEffect(() => {
    if (job.status !== 'pending' && job.status !== 'running') return;
    const t = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [job.status]);

  const isTerminal = job.status === 'completed' || job.status === 'failed' || job.status === 'cancelled';
  const stepKey: ValidationJobStep | 'queued' = job.status === 'pending'
    ? 'queued'
    : (job.step ?? 'verifier');
  const stepText = STEP_COPY[stepKey];

  // Failed / cancelled — render the error with a Retry button (the
  // router refetches the discovery on terminal status; if the verdict
  // *did* land before the failure, the router swaps to NewValidationPanel
  // automatically and this card unmounts).
  if (job.status === 'failed' || job.status === 'cancelled') {
    return (
      <Card withBorder p="md">
        <Group justify="space-between" mb={6} align="center">
          <Group gap={6}>
            <IconShieldCheck size={14} color="var(--db-text-secondary)" />
            <Text
              size="xs"
              fw={600}
              tt="uppercase"
              c="dimmed"
              style={{ letterSpacing: '0.5px' }}
            >
              Validation
            </Text>
          </Group>
        </Group>
        <Text size="xs" c="red" mb={4}>
          {job.status === 'cancelled' ? 'Validation cancelled.' : 'Validation failed.'}
        </Text>
        {job.error && (
          <Text size="xs" c="dimmed" mb={8} lineClamp={3}>
            {job.error}
          </Text>
        )}
        {onRetry && (
          <Button size="xs" variant="filled" onClick={onRetry}>
            Try again
          </Button>
        )}
      </Card>
    );
  }

  // pending or running.
  return (
    <Card withBorder p="md">
      <Group justify="space-between" mb={6} align="center">
        <Group gap={6}>
          <IconShieldCheck size={14} color="var(--db-text-secondary)" />
          <Text
            size="xs"
            fw={600}
            tt="uppercase"
            c="dimmed"
            style={{ letterSpacing: '0.5px' }}
          >
            Validation
          </Text>
        </Group>
        <Loader size="xs" />
      </Group>
      <Stack gap={4}>
        <Text size="xs">{stepText}</Text>
        <Text size="xs" c="dimmed">{elapsedLabel(job.started_at ?? job.enqueued_at)}</Text>
        {!isTerminal && onCancel && (
          <Button
            size="xs"
            variant="subtle"
            color="red"
            onClick={() => onCancel(job.id)}
            px={0}
            style={{ alignSelf: 'flex-start' }}
          >
            Cancel
          </Button>
        )}
      </Stack>
    </Card>
  );
}
