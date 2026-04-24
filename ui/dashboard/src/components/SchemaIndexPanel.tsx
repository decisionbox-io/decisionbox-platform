'use client';

/**
 * SchemaIndexPanel renders the per-project schema-indexing lifecycle
 * (PLAN-SCHEMA-RETRIEVAL.md §8.5). Always shows:
 *   - a progress bar (fills during schema_discovery, resets for blurb
 *     generation, fills again for embedding)
 *   - a phase label
 *   - the Retry / Re-index actions appropriate to the current status
 *
 * Poll cadence: 2s while the worker is active (pending_indexing /
 * indexing), stops once status settles. On ready / failed / empty
 * states the bar still renders (full for ready, empty otherwise) so
 * the UI never collapses — users asked for "progress bar always, for
 * better ux".
 *
 * When `debugLogsEnabled` is true (the same localStorage toggle used
 * for the discovery debug tail), the panel also renders a tail of
 * recent agent stderr lines, polled from /schema-index/logs every 2 s.
 */

import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Group, Progress, ScrollArea, Stack, Text } from '@mantine/core';
import { IconAlertCircle, IconCheck, IconRefresh, IconRotateClockwise } from '@tabler/icons-react';
import { api, SchemaIndexLogLine, SchemaIndexStatus } from '@/lib/api';

interface Props {
  projectId: string;
  onStatusChange?: (status: SchemaIndexStatus) => void;
}

const POLL_MS = 2000;
const LOG_LIMIT = 300; // recent lines on first open; then since-cursor

const PHASE_LABELS: Record<string, string> = {
  listing_tables: 'Listing tables',
  schema_discovery: 'Discovering table schemas',
  describing_tables: 'Generating blurbs',
  embedding: 'Building vector index',
};

export function SchemaIndexPanel({ projectId, onStatusChange }: Props) {
  const [status, setStatus] = useState<SchemaIndexStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showLogs, setShowLogs] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false;
    return window.localStorage.getItem(`db:showDebugLogs:${projectId}`) === '1';
  });
  const [logs, setLogs] = useState<SchemaIndexLogLine[]>([]);
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const logTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const alive = useRef(true);
  const sinceRef = useRef<string>(''); // RFC3339 cursor for incremental log tail

  // Poll schema-index status.
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
      } catch (e: unknown) {
        if (!alive.current) return;
        setError(e instanceof Error ? e.message : String(e));
        pollTimer.current = setTimeout(poll, POLL_MS * 2);
      }
    };
    poll();
    return () => {
      alive.current = false;
      if (pollTimer.current) clearTimeout(pollTimer.current);
    };
  }, [projectId, onStatusChange]);

  // Sync showLogs when the settings page flips the localStorage key.
  // Uses a storage event + a focus refetch so both same-tab and
  // cross-tab updates show up without a hard reload.
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const refresh = () => {
      setShowLogs(window.localStorage.getItem(`db:showDebugLogs:${projectId}`) === '1');
    };
    window.addEventListener('storage', refresh);
    window.addEventListener('focus', refresh);
    return () => {
      window.removeEventListener('storage', refresh);
      window.removeEventListener('focus', refresh);
    };
  }, [projectId]);

  // Poll the log tail when showLogs is on AND a run is active or just
  // finished. We keep tailing for ~30s after "ready" / "failed" so the
  // final lines remain visible without needing another click.
  useEffect(() => {
    if (!showLogs) {
      if (logTimer.current) clearTimeout(logTimer.current);
      return;
    }
    let cancelled = false;
    const pullLogs = async () => {
      try {
        const since = sinceRef.current;
        const rows = await api.listSchemaIndexLogs(projectId, since || undefined, since ? 500 : LOG_LIMIT);
        if (cancelled) return;
        if (rows.length > 0) {
          setLogs((prev) => {
            const next = [...prev, ...rows];
            // Hard cap client-side memory.
            if (next.length > 2000) return next.slice(next.length - 2000);
            return next;
          });
          sinceRef.current = rows[rows.length - 1].created_at;
        }
      } catch {
        // Transient — keep polling. Don't surface to UI; the status
        // banner will scream first if the API's actually down.
      } finally {
        if (!cancelled) logTimer.current = setTimeout(pullLogs, POLL_MS);
      }
    };
    // Reset cursor on first open so we load the most-recent tail.
    sinceRef.current = '';
    setLogs([]);
    pullLogs();
    return () => {
      cancelled = true;
      if (logTimer.current) clearTimeout(logTimer.current);
    };
  }, [showLogs, projectId]);

  const handleRetry = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.retrySchemaIndex(projectId);
      const s = await api.getSchemaIndexStatus(projectId);
      setStatus(s);
      onStatusChange?.(s);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
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
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  // Progress math — always computed, regardless of state, so the bar
  // is always visible (empty, filling, or full).
  const progress = status?.progress;
  const total = progress?.tables_total ?? 0;
  const done = progress?.tables_done ?? 0;
  const pct = (() => {
    if (status?.status === 'ready') return 100;
    if (total > 0) return Math.min(100, Math.round((done / total) * 100));
    return 0;
  })();
  const phaseLabel = (() => {
    if (status?.status === 'ready') return 'Ready';
    if (status?.status === 'failed') return 'Failed';
    if (status?.status === 'pending_indexing') return 'Queued';
    if (progress?.phase && PHASE_LABELS[progress.phase]) return PHASE_LABELS[progress.phase];
    return 'Not indexed';
  })();
  const bannerColor =
    status?.status === 'ready' ? 'green'
      : status?.status === 'failed' ? 'red'
        : status?.status === 'indexing' || status?.status === 'pending_indexing' ? 'blue'
          : 'yellow';
  const bannerIcon =
    status?.status === 'ready' ? <IconCheck size={16} />
      : status?.status === 'failed' ? <IconAlertCircle size={16} />
        : <IconRotateClockwise size={16} />;

  if (!status) {
    return <Text size="sm" c="dimmed">Loading schema index status...</Text>;
  }

  const updatedDate = status.updated_at ? new Date(status.updated_at).toLocaleString() : null;

  const actions = (() => {
    if (status.status === 'failed') {
      return (
        <Group gap="xs">
          <Button size="xs" leftSection={<IconRotateClockwise size={14} />} onClick={handleRetry} loading={busy}>
            Retry indexing
          </Button>
          <Button size="xs" variant="subtle" onClick={handleReindex} loading={busy}>
            Reset + rebuild
          </Button>
        </Group>
      );
    }
    if (status.status === '') {
      return (
        <Button size="xs" leftSection={<IconRotateClockwise size={14} />} onClick={handleReindex} loading={busy}>
          Build schema index
        </Button>
      );
    }
    if (status.status === 'ready') {
      return (
        <Button size="xs" variant="subtle" leftSection={<IconRefresh size={14} />} onClick={handleReindex} loading={busy}>
          Re-index
        </Button>
      );
    }
    // indexing / pending_indexing — no button; let the progress speak.
    return null;
  })();

  return (
    <Stack gap="xs">
      <Alert color={bannerColor} icon={bannerIcon} variant="light">
        <Stack gap={6}>
          <Group justify="space-between" wrap="nowrap">
            <Text size="sm" fw={500}>
              Schema index: {phaseLabel}
              {status.status === 'ready' && updatedDate && (
                <Text component="span" size="xs" c="dimmed" ml="sm">last built {updatedDate}</Text>
              )}
            </Text>
            {actions}
          </Group>
          {/*
            Always-visible progress bar. During schema_discovery and
            embedding the underlying counters climb; during ready it
            locks at 100%; during empty/failed it shows 0% with the
            banner color signalling what state we're in.
          */}
          <Progress
            value={pct}
            animated={status.status === 'indexing' || status.status === 'pending_indexing'}
            color={bannerColor}
          />
          {(status.status === 'indexing' || status.status === 'pending_indexing') && (
            <Text size="xs" c="dimmed">
              {total > 0 ? `${done} of ${total} tables (${pct}%)` : 'Starting up…'}
              {' '}— you can close this tab, indexing continues in the background.
            </Text>
          )}
          {status.status === 'failed' && status.error && (
            <Text size="xs" c="red">{status.error}</Text>
          )}
          {error && <Text size="xs" c="red">{error}</Text>}
        </Stack>
      </Alert>

      {showLogs && (
        <div
          style={{
            border: '1px solid var(--mantine-color-gray-3)',
            borderRadius: 4,
            background: 'var(--mantine-color-dark-9, #111)',
            color: '#d0d0d0',
          }}
        >
          <Group justify="space-between" p="xs" style={{ borderBottom: '1px solid var(--mantine-color-gray-3)' }}>
            <Text size="xs" c="dimmed">
              Agent log tail — latest {logs.length} lines {status.status === 'indexing' ? '· streaming' : ''}
            </Text>
            <Text size="xs" c="dimmed">Toggle via Project Settings → Advanced</Text>
          </Group>
          <ScrollArea h={280} offsetScrollbars type="always">
            <pre style={{
              margin: 0,
              padding: 10,
              fontSize: 11,
              fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
            }}>
              {logs.length === 0
                ? '(no log lines yet — the agent will start emitting when indexing begins)'
                : logs.map((l) => `${new Date(l.created_at).toLocaleTimeString()}  ${l.line}`).join('\n')}
            </pre>
          </ScrollArea>
        </div>
      )}
    </Stack>
  );
}
