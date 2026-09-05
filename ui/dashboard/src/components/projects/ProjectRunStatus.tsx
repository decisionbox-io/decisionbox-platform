'use client';

import { Badge, Group, Text } from '@mantine/core';
import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';

// ProjectRunStatus renders the latest discovery-run state for a project
// card on the homepage list:
//   - running / pending → "Running" badge + live elapsed time that
//     counts up every second while the page is open.
//   - completed         → "Completed" badge + completion timestamp
//     (date + time).
//   - failed            → "Failed" badge.
//   - cancelled         → "Cancelled" badge.
//   - "" (never run)    → nothing (the card simply omits the line).
//
// The status + timestamps are derived server-side and arrive on the
// Project payload as last_run_status / last_run_at / last_run_completed_at
// (see lib/api.ts).

export interface ProjectRunStatusProps {
  /** last_run_status from the project payload. */
  status: string;
  /** last_run_at — when the most recent run started (ISO 8601 or null). */
  startedAt: string | null;
  /** last_run_completed_at — when it finished (ISO 8601, null while running). */
  completedAt?: string | null;
}

// Statuses for which a run is still in flight and the elapsed timer ticks.
const IN_FLIGHT = new Set(['pending', 'running']);

function pad2(n: number): string {
  return n < 10 ? `0${n}` : `${n}`;
}

// formatElapsed turns a second count into "45s", "4m 12s", or
// "1h 04m 12s". Negative inputs (clock skew) clamp to 0.
export function formatElapsed(totalSeconds: number): string {
  const sec = Math.max(0, Math.floor(totalSeconds));
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `${h}h ${pad2(m)}m ${pad2(s)}s`;
  if (m > 0) return `${m}m ${pad2(s)}s`;
  return `${s}s`;
}

// formatRunTimestamp renders an ISO timestamp as local "YYYY-MM-DD HH:MM".
// Returns "" for an unparseable timestamp so callers can omit the label.
export function formatRunTimestamp(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return (
    `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ` +
    `${pad2(d.getHours())}:${pad2(d.getMinutes())}`
  );
}

// elapsedSince returns the live "now − start" label for a run that
// began at startISO, or "" when the timestamp is missing/invalid. It
// reads the wall clock, so it is only used for in-flight runs — those
// re-render every second via the ticker below, keeping the label
// current. Mirrors the ValidationJobProgressCard elapsed-time pattern.
function elapsedSince(startISO: string | null): string {
  if (!startISO) return '';
  const start = new Date(startISO).getTime();
  if (Number.isNaN(start)) return '';
  return formatElapsed((Date.now() - start) / 1000);
}

export function ProjectRunStatus({ status, startedAt, completedAt }: ProjectRunStatusProps) {
  const t = useTranslations('projectsMisc');
  const isInFlight = IN_FLIGHT.has(status);

  // Re-render once a second while a run is in flight so the elapsed
  // time stays live. Only in-flight cards mount an interval; the
  // setState lives in the timer callback (not the effect body) to keep
  // the elapsed label ticking without violating the hook rules.
  const [, force] = useState(0);
  useEffect(() => {
    if (!isInFlight) return;
    const t = setInterval(() => force((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, [isInFlight]);

  // Never run — nothing to show (preserves the old "hide when no run"
  // behaviour rather than rendering an empty placeholder).
  if (!status) return null;

  if (isInFlight) {
    const elapsed = elapsedSince(startedAt);
    return (
      <Group gap="xs" mt="sm" wrap="nowrap">
        <Badge color="blue" variant="light" size="sm">{t('statusRunning')}</Badge>
        {elapsed && <Text size="xs" c="dimmed">{elapsed}</Text>}
      </Group>
    );
  }

  if (status === 'completed') {
    const ts = completedAt ? formatRunTimestamp(completedAt) : '';
    return (
      <Group gap="xs" mt="sm" wrap="nowrap">
        <Badge color="green" variant="light" size="sm">{t('statusCompleted')}</Badge>
        {ts && <Text size="xs" c="dimmed">{ts}</Text>}
      </Group>
    );
  }

  if (status === 'failed') {
    return (
      <Group gap="xs" mt="sm">
        <Badge color="red" variant="light" size="sm">{t('statusFailed')}</Badge>
      </Group>
    );
  }

  if (status === 'cancelled') {
    return (
      <Group gap="xs" mt="sm">
        <Badge color="gray" variant="light" size="sm">{t('statusCancelled')}</Badge>
      </Group>
    );
  }

  // Forward-compatible: an unknown status is shown verbatim rather than
  // swallowed, so a new backend state is at least visible.
  return (
    <Group gap="xs" mt="sm">
      <Badge color="gray" variant="light" size="sm">{status}</Badge>
    </Group>
  );
}
