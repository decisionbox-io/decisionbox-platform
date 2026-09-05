'use client';

import { Button, Card, Group, Loader, Stack, Text } from '@mantine/core';
import { IconShieldCheck } from '@tabler/icons-react';
import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';
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

// Elapsed seconds since the given ISO timestamp, or null when the
// timestamp is missing/unparseable (the caller renders nothing then).
function elapsedSeconds(startISO: string | undefined): number | null {
  if (!startISO) return null;
  const start = new Date(startISO).getTime();
  if (Number.isNaN(start)) return null;
  return Math.max(0, Math.floor((Date.now() - start) / 1000));
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
  const t = useTranslations('validation');
  // Re-render every second so elapsed-time stays live without
  // depending on the polling cadence of the router.
  const [, force] = useState(0);
  useEffect(() => {
    if (job.status !== 'pending' && job.status !== 'running') return;
    const timer = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(timer);
  }, [job.status]);

  const isTerminal = job.status === 'completed' || job.status === 'failed' || job.status === 'cancelled';
  const stepKey: ValidationJobStep | 'queued' = job.status === 'pending'
    ? 'queued'
    : (job.step ?? 'verifier');
  const stepText = t(`step_${stepKey}`);

  const sec = elapsedSeconds(job.started_at ?? job.enqueued_at);
  const elapsedLabel = sec == null
    ? ''
    : sec < 60
      ? t('elapsedSeconds', { seconds: sec })
      : t('elapsedMinutes', { minutes: Math.floor(sec / 60), seconds: sec % 60 });

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
              {t('title')}
            </Text>
          </Group>
        </Group>
        <Text size="xs" c="red" mb={4}>
          {job.status === 'cancelled' ? t('cancelled') : t('failed')}
        </Text>
        {job.error && (
          <Text size="xs" c="dimmed" mb={8} lineClamp={3}>
            {job.error}
          </Text>
        )}
        {onRetry && (
          <Button size="xs" variant="filled" onClick={onRetry}>
            {t('tryAgain')}
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
            {t('title')}
          </Text>
        </Group>
        <Loader size="xs" />
      </Group>
      <Stack gap={4}>
        <Text size="xs">{stepText}</Text>
        <Text size="xs" c="dimmed">{elapsedLabel}</Text>
        {!isTerminal && onCancel && (
          <Button
            size="xs"
            variant="subtle"
            color="red"
            onClick={() => onCancel(job.id)}
            px={0}
            style={{ alignSelf: 'flex-start' }}
          >
            {t('cancel')}
          </Button>
        )}
      </Stack>
    </Card>
  );
}
