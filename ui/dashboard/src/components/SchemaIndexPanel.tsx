'use client';

/**
 * SchemaIndexPanel renders the per-project schema-indexing lifecycle
 * (PLAN-SCHEMA-RETRIEVAL.md §8.5). Shows current status, live progress
 * counters when the worker is running, and the Retry / Re-index actions
 * the user triggers from the project detail page.
 *
 * Polling cadence: 2s while status is pending_indexing or indexing,
 * stops once status settles to ready or failed. The parent page owns
 * discovery-trigger gating; this component only owns the banner shape
 * and the two action buttons.
 */

import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Group, Progress, Stack, Text } from '@mantine/core';
import { IconAlertCircle, IconCheck, IconRefresh, IconRotateClockwise } from '@tabler/icons-react';
import { api, SchemaIndexStatus } from '@/lib/api';

interface Props {
  projectId: string;
  // onStatusChange fires every time a poll tick updates the status.
  // Parent uses it to refresh the `Run Discovery` button's
  // disabled-state without re-fetching the whole project doc.
  onStatusChange?: (status: SchemaIndexStatus) => void;
}

const POLL_MS = 2000;

export function SchemaIndexPanel({ projectId, onStatusChange }: Props) {
  const [status, setStatus] = useState<SchemaIndexStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // pollTimer lives in a ref so the cleanup effect can cancel it when
  // the panel unmounts, preventing the "setState on unmounted" warning.
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const alive = useRef(true);

  useEffect(() => {
    alive.current = true;
    const poll = async () => {
      try {
        const s = await api.getSchemaIndexStatus(projectId);
        if (!alive.current) return;
        setStatus(s);
        onStatusChange?.(s);
        if (s.status === 'pending_indexing' || s.status === 'indexing') {
          pollTimer.current = setTimeout(poll, POLL_MS);
        }
      } catch (e: any) {
        if (!alive.current) return;
        setError(e?.message || String(e));
        // Keep retrying on transient errors — the worker is local and
        // usually recovers within a second or two.
        pollTimer.current = setTimeout(poll, POLL_MS * 2);
      }
    };
    poll();
    return () => {
      alive.current = false;
      if (pollTimer.current) clearTimeout(pollTimer.current);
    };
  }, [projectId, onStatusChange]);

  const handleRetry = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.retrySchemaIndex(projectId);
      const s = await api.getSchemaIndexStatus(projectId);
      setStatus(s);
      onStatusChange?.(s);
    } catch (e: any) {
      setError(e?.message || String(e));
    } finally {
      setBusy(false);
    }
  };

  const handleReindex = async () => {
    if (!confirm('Re-index schema? Drops the current index and rebuilds from scratch. Costs time + LLM tokens.')) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await api.reindexSchema(projectId);
      const s = await api.getSchemaIndexStatus(projectId);
      setStatus(s);
      onStatusChange?.(s);
    } catch (e: any) {
      setError(e?.message || String(e));
    } finally {
      setBusy(false);
    }
  };

  if (!status) {
    return <Text size="sm" c="dimmed">Loading schema index status...</Text>;
  }

  // Empty status (pre-migration) → user must trigger reindex to kick things off.
  if (!status.status) {
    return (
      <Alert color="yellow" icon={<IconAlertCircle size={16} />} title="Schema not indexed">
        <Stack gap="xs">
          <Text size="sm">
            This project pre-dates schema indexing. Discovery is blocked until the index is built.
          </Text>
          <Group>
            <Button size="xs" leftSection={<IconRotateClockwise size={14} />} onClick={handleReindex} loading={busy}>
              Build schema index
            </Button>
          </Group>
        </Stack>
      </Alert>
    );
  }

  if (status.status === 'ready') {
    const updated = status.updated_at ? new Date(status.updated_at).toLocaleString() : 'just now';
    return (
      <Alert color="green" icon={<IconCheck size={16} />} variant="light">
        <Group justify="space-between">
          <Text size="sm">
            Schema index ready — last built {updated}
          </Text>
          <Button size="xs" variant="subtle" leftSection={<IconRefresh size={14} />} onClick={handleReindex} loading={busy}>
            Re-index
          </Button>
        </Group>
      </Alert>
    );
  }

  if (status.status === 'failed') {
    return (
      <Alert color="red" icon={<IconAlertCircle size={16} />} title="Schema indexing failed">
        <Stack gap="xs">
          {status.error && <Text size="sm">{status.error}</Text>}
          <Group>
            <Button size="xs" leftSection={<IconRotateClockwise size={14} />} onClick={handleRetry} loading={busy}>
              Retry indexing
            </Button>
            <Button size="xs" variant="subtle" onClick={handleReindex} loading={busy}>
              Reset + rebuild
            </Button>
          </Group>
        </Stack>
      </Alert>
    );
  }

  // pending_indexing or indexing → show progress.
  const p = status.progress;
  const total = p?.tables_total ?? 0;
  const done = p?.tables_done ?? 0;
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  const phaseLabel =
    p?.phase === 'listing_tables'
      ? 'Listing tables'
      : p?.phase === 'describing_tables'
      ? 'Generating blurbs'
      : p?.phase === 'embedding'
      ? 'Building vector index'
      : 'Waiting for worker';

  return (
    <Alert color="blue" icon={<IconRotateClockwise size={16} />}>
      <Stack gap="xs">
        <Text size="sm" fw={500}>
          Indexing schema — {phaseLabel}
        </Text>
        {total > 0 && (
          <>
            <Progress value={pct} animated />
            <Text size="xs" c="dimmed">
              {done} of {total} tables ({pct}%) — you can close this tab, indexing continues in the background.
            </Text>
          </>
        )}
        {error && <Text size="xs" c="red">{error}</Text>}
      </Stack>
    </Alert>
  );
}
