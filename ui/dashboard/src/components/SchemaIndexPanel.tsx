'use client';

/**
 * SchemaIndexPanel renders the per-project schema-indexing lifecycle.
 * Always shows:
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
import { useTranslations } from 'next-intl';
import { Alert, Button, Group, Modal, Progress, ScrollArea, Stack, Text } from '@mantine/core';
import { IconAlertCircle, IconCheck, IconPlayerStop, IconRefresh, IconRotateClockwise } from '@tabler/icons-react';
import { api, SchemaIndexLogLine, SchemaIndexStatus } from '@/lib/api';
import { useFormat } from '@/lib/format';

interface Props {
  projectId: string;
  onStatusChange?: (status: SchemaIndexStatus) => void;
  /**
   * Optional override for the heading. Defaults to "Schema index".
   * Plugin overlays may wrap this component with a custom title.
   */
  title?: string;
  /**
   * When true, the panel renders nothing while `status === 'ready'`.
   * Polling continues internally so the panel re-appears the moment
   * the status flips to anything actionable (`needs_reindex`,
   * `indexing`, `failed`, `cancelled`). Used by surfaces where the
   * "Ready" steady state is visual noise — the badge / helper copy
   * already convey the "ready" signal.
   */
  hideWhenReady?: boolean;
}

const POLL_MS = 2000;
const LOG_LIMIT = 300; // recent lines on first open; then since-cursor

// Maps a worker phase enum to its i18n key under providerSetup. Kept as a
// data map (enum key → message key) so the phase values stay stable while
// the human labels are translated at render time.
const PHASE_LABEL_KEYS: Record<string, string> = {
  listing_tables: 'phaseListingTables',
  schema_discovery: 'phaseSchemaDiscovery',
  describing_tables: 'phaseDescribingTables',
  embedding: 'phaseEmbedding',
};

export function SchemaIndexPanel({ projectId, onStatusChange, title, hideWhenReady = false }: Props) {
  const t = useTranslations('providerSetup');
  const fmt = useFormat();
  const [status, setStatus] = useState<SchemaIndexStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showLogs, setShowLogs] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false;
    return window.localStorage.getItem(`db:showDebugLogs:${projectId}`) === '1';
  });
  const [logs, setLogs] = useState<SchemaIndexLogLine[]>([]);
  const [cancelModalOpen, setCancelModalOpen] = useState(false);
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

  const handleCancel = async () => {
    setCancelModalOpen(false);
    setBusy(true);
    setError(null);
    try {
      await api.cancelSchemaIndex(projectId);
      // Don't bother re-polling immediately — the worker writes
      // the "cancelled" status transition asynchronously; the
      // 2-second status poll will pick it up.
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const handleReindex = async () => {
    if (!confirm(t('reindexConfirm'))) {
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
    if (status?.status === 'ready') return t('statusReady');
    if (status?.status === 'failed') return t('statusFailed');
    if (status?.status === 'cancelled') return t('statusCancelled');
    if (status?.status === 'needs_reindex') return t('statusNeedsReindex');
    if (status?.status === 'pending_indexing') return t('statusQueued');
    if (progress?.phase && PHASE_LABEL_KEYS[progress.phase]) return t(PHASE_LABEL_KEYS[progress.phase]);
    return t('statusNotIndexed');
  })();
  const bannerColor =
    status?.status === 'ready' ? 'green'
      : status?.status === 'failed' ? 'red'
        : status?.status === 'cancelled' ? 'orange'
          : status?.status === 'needs_reindex' ? 'orange'
            : status?.status === 'indexing' || status?.status === 'pending_indexing' ? 'blue'
              : 'yellow';
  const bannerIcon =
    status?.status === 'ready' ? <IconCheck size={16} />
      : status?.status === 'failed' ? <IconAlertCircle size={16} />
        : status?.status === 'cancelled' ? <IconPlayerStop size={16} />
          : status?.status === 'needs_reindex' ? <IconAlertCircle size={16} />
            : <IconRotateClockwise size={16} />;

  if (!status) {
    return hideWhenReady
      // When the caller opts in to hide-when-ready, also suppress
      // the brief "Loading schema index status..." flash on the
      // initial mount — for the project-home steady state the
      // panel is supposed to be invisible whenever there's
      // nothing actionable to surface, and the pre-poll moment
      // counts. Polling still kicks off the moment the component
      // mounts, so the panel re-appears the instant the first
      // poll returns a non-ready status.
      ? null
      : <Text size="sm" c="dimmed">{t('loadingStatus')}</Text>;
  }

  // hideWhenReady is the opt-in for surfaces (project home, plugin
  // overlays) that convey "ready" through their own badge / helper
  // copy, so the verbose "Schema index: Ready · last built X · Re-index"
  // banner is redundant visual noise in steady state. Polling
  // continues (the useEffect above still runs), so the panel
  // re-appears the moment status changes to anything else
  // (`needs_reindex` after Settings → Clear cache, `indexing`
  // after a re-index kick-off, `failed` / `cancelled` recovery).
  if (hideWhenReady && status.status === 'ready') {
    return null;
  }

  const updatedDate = status.updated_at ? fmt.dateTime(status.updated_at) : null;

  const actions = (() => {
    if (status.status === 'failed') {
      return (
        <Group gap="xs">
          <Button size="xs" leftSection={<IconRotateClockwise size={14} />} onClick={handleRetry} loading={busy}>
            {t('retryIndexing')}
          </Button>
          <Button size="xs" variant="subtle" onClick={handleReindex} loading={busy}>
            {t('resetRebuild')}
          </Button>
        </Group>
      );
    }
    if (status.status === '') {
      return (
        <Button size="xs" leftSection={<IconRotateClockwise size={14} />} onClick={handleReindex} loading={busy}>
          {t('buildSchemaIndex')}
        </Button>
      );
    }
    if (status.status === 'ready') {
      return (
        <Button size="xs" variant="subtle" leftSection={<IconRefresh size={14} />} onClick={handleReindex} loading={busy}>
          {t('reindex')}
        </Button>
      );
    }
    if (status.status === 'indexing' || status.status === 'pending_indexing') {
      // Cancel is only meaningful once the worker has actually picked
      // up the project; the backend returns 409 for pending_indexing
      // (there's no subprocess yet to kill). Keeping the button enabled
      // only during `indexing` matches that contract.
      return (
        <Button
          size="xs"
          color="red"
          variant="light"
          leftSection={<IconPlayerStop size={14} />}
          onClick={() => setCancelModalOpen(true)}
          disabled={status.status !== 'indexing' || busy}
        >
          {t('cancelIndexing')}
        </Button>
      );
    }
    if (status.status === 'cancelled') {
      return (
        <Group gap="xs">
          <Button size="xs" leftSection={<IconRefresh size={14} />} onClick={handleReindex} loading={busy}>
            {t('reindex')}
          </Button>
        </Group>
      );
    }
    if (status.status === 'needs_reindex') {
      return (
        <Group gap="xs">
          <Button size="xs" leftSection={<IconRefresh size={14} />} onClick={handleReindex} loading={busy}>
            {t('reindexNow')}
          </Button>
        </Group>
      );
    }
    return null;
  })();

  return (
    <Stack gap="xs">
      <Alert color={bannerColor} icon={bannerIcon} variant="light">
        <Stack gap={6}>
          <Group justify="space-between" wrap="nowrap">
            <Text size="sm" fw={500}>
              {t('headingWithPhase', { title: title || t('schemaIndex'), phase: phaseLabel })}
              {status.status === 'ready' && updatedDate && (
                <Text component="span" size="xs" c="dimmed" ml="sm">{t('lastBuilt', { date: updatedDate })}</Text>
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
              {total > 0
                ? t('progressCounts', { done, total, pct })
                : t('progressStartingUp')}
            </Text>
          )}
          {(status.status === 'ready' || status.status === 'failed' || status.status === 'cancelled') &&
            ((progress?.input_tokens ?? 0) > 0 || (progress?.output_tokens ?? 0) > 0) && (
              <Text size="xs" c="dimmed">
                {t('blurbTokens', {
                  in: fmt.number(progress?.input_tokens ?? 0),
                  out: fmt.number(progress?.output_tokens ?? 0),
                })}
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
              {status.status === 'indexing'
                ? t('logTailStreaming', { count: logs.length })
                : t('logTail', { count: logs.length })}
            </Text>
            <Text size="xs" c="dimmed">{t('logToggleHint')}</Text>
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
                ? t('logEmpty')
                : logs.map((l) => `${fmt.dateTime(l.created_at, { hour: '2-digit', minute: '2-digit', second: '2-digit' })}  ${l.line}`).join('\n')}
            </pre>
          </ScrollArea>
        </div>
      )}

      <Modal
        opened={cancelModalOpen}
        onClose={() => setCancelModalOpen(false)}
        title={t('cancelModalTitle')}
        centered
      >
        <Stack gap="md">
          <Text size="sm">
            {t.rich('cancelModalBody', { b: (chunks) => <b>{chunks}</b> })}
          </Text>
          <Text size="xs" c="dimmed">
            {t('cancelModalHint')}
          </Text>
          <Group justify="flex-end" gap="xs">
            <Button variant="subtle" onClick={() => setCancelModalOpen(false)}>
              {t('keepRunning')}
            </Button>
            <Button color="red" leftSection={<IconPlayerStop size={14} />} onClick={handleCancel} loading={busy}>
              {t('yesCancel')}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </Stack>
  );
}
