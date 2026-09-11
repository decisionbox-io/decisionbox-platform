'use client';

/**
 * SchemaIndexHistory renders a datasource's "Indexing" section:
 *   - a current-status line (from the latest durable run), and
 *   - an expandable history table of index runs
 *     (finished_at · objects · blurbs · duration · status · error).
 *
 * It reads the durable per-datasource run records (GET /schema-index/runs),
 * which survive the next run — so a successful re-index leaves a visible record
 * here, not just a toast. Co-located with the datasource's edit + re-index
 * controls on WarehouseConfigPanel.
 *
 * `datasourceId` filters to one data source; omit it (single-warehouse
 * projects) to show that project's runs. The runs are fetched once on mount —
 * the status line and the table share that single response; the Collapse just
 * shows/hides the already-loaded table.
 */

import { useEffect, useState } from 'react';
import { Badge, Button, Collapse, Group, Stack, Table, Text } from '@mantine/core';
import { IconChevronDown, IconChevronRight } from '@tabler/icons-react';
import { api, objectNoun, SchemaIndexRun, SchemaIndexStatus } from '@/lib/api';

interface Props {
  projectId: string;
  /** Filter to one data source; omit for single-warehouse projects. */
  datasourceId?: string;
  /** Human label for the empty-state line; falls back to "this data source". */
  datasourceName?: string;
}

const POLL_MS = 2000; // while a run is active (pending/indexing)
// Slow cadence while settled: keeps the live status line fresh against
// out-of-band changes this component can't observe directly — notably a "Clear
// schema cache" from the Advanced settings tab, which is a sibling Mantine tab
// that stays mounted (no remount, no window focus) when the user switches back.
const SETTLED_POLL_MS = 15000;

// Formats a millisecond duration compactly: "820ms", "14s", "3m 5s", "1h 2m".
function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const totalSec = Math.round(ms / 1000);
  if (totalSec < 60) return `${totalSec}s`;
  const min = Math.floor(totalSec / 60);
  const sec = totalSec % 60;
  if (min < 60) return sec ? `${min}m ${sec}s` : `${min}m`;
  const hr = Math.floor(min / 60);
  const remMin = min % 60;
  return remMin ? `${hr}h ${remMin}m` : `${hr}h`;
}

function runDurationMs(run: SchemaIndexRun): number | null {
  if (!run.started_at || !run.finished_at) return null;
  const ms = new Date(run.finished_at).getTime() - new Date(run.started_at).getTime();
  return Number.isFinite(ms) && ms >= 0 ? ms : null;
}

function StatusBadge({ status }: { status: string }) {
  if (status === 'ready') return <Badge color="green" variant="light" size="sm">Ready</Badge>;
  if (status === 'failed') return <Badge color="red" variant="light" size="sm">Failed</Badge>;
  return <Badge color="gray" variant="light" size="sm">{status || 'Unknown'}</Badge>;
}

export default function SchemaIndexHistory({ projectId, datasourceId, datasourceName }: Props) {
  const [open, setOpen] = useState(false);
  const [runs, setRuns] = useState<SchemaIndexRun[] | null>(null);
  const [status, setStatus] = useState<SchemaIndexStatus | null>(null);
  // loading starts true and is cleared in finally — mirrors WarehouseConfigPanel
  // and avoids a synchronous setState in the effect body.
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | null = null;

    // Fetch the live status AND the durable history. The current-status line is
    // driven by the LIVE status (not the newest run record), because run rows
    // are append-only and retained across a cache clear / cancel — reading
    // runs[0] there would show a stale green "Ready" after the index was
    // dropped. The history table below is legitimately the run records.
    const fetchOnce = async (): Promise<SchemaIndexStatus | null> => {
      const [s, res] = await Promise.all([
        api.getSchemaIndexStatus(projectId).catch(() => null),
        api.listSchemaIndexRuns(projectId, datasourceId),
      ]);
      if (alive) {
        setStatus(s);
        setRuns(res.runs || []);
        setError(null);
      }
      return s;
    };

    // loop owns the single polling chain: fast while a run is active, slow once
    // settled. This panel stays mounted on the Settings page (Mantine keeps tab
    // panels mounted), and the "Clear schema cache" action lives in a sibling
    // tab — so neither a remount nor a window focus fires when the user returns
    // here. The slow settled-state poll is what catches that out-of-band change.
    const loop = async () => {
      try {
        const s = await fetchOnce();
        if (!alive) return;
        const active = s != null && (s.status === 'pending_indexing' || s.status === 'indexing');
        timer = setTimeout(loop, active ? POLL_MS : SETTLED_POLL_MS);
      } catch (e: unknown) {
        if (!alive) return;
        setError(e instanceof Error ? e.message : String(e));
        timer = setTimeout(loop, SETTLED_POLL_MS);
      } finally {
        if (alive) setLoading(false);
      }
    };

    // onFocus is an immediate one-off refresh (no scheduling of its own, so it
    // can't start a second polling chain) for the common return-to-window case.
    const onFocus = () => { fetchOnce().catch(() => { /* loop keeps its cadence */ }); };

    void loop();
    window.addEventListener('focus', onFocus);
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
      window.removeEventListener('focus', onFocus);
    };
  }, [projectId, datasourceId]);

  // Latest run supplies the object count for the status line + feeds the table.
  const latest = runs && runs.length > 0 ? runs[0] : null;

  const statusLine = (() => {
    if (loading) return <Text size="xs" c="dimmed">Loading indexing status…</Text>;
    if (error) return <Text size="xs" c="red">Couldn&apos;t load indexing status: {error}</Text>;
    const live = status?.status ?? '';
    // needs_reindex / cancelled: the index was cleared or aborted, so the prior
    // run's green "Ready" no longer describes the live index — say so.
    if (live === 'needs_reindex') {
      return <Text size="xs" c="orange">Re-index required — the schema cache was cleared.</Text>;
    }
    if (live === 'cancelled') {
      return <Text size="xs" c="orange">Last indexing run was cancelled — re-index to rebuild.</Text>;
    }
    if (live === 'indexing' || live === 'pending_indexing') {
      return <Text size="xs" c="blue">Indexing in progress…</Text>;
    }
    if (live === 'failed') {
      return (
        <Group gap="xs" wrap="nowrap">
          <StatusBadge status="failed" />
          <Text size="xs" c="red" lineClamp={1} style={{ maxWidth: 360 }}>
            {status?.error || latest?.error || 'failed'}
          </Text>
        </Group>
      );
    }
    if (live === 'ready') {
      // A project indexed before this feature existed (or whose best-effort run
      // write failed) is ready with no run record — the index is still usable,
      // so report Ready; just note the per-run history isn't available.
      if (!latest) {
        return (
          <Group gap="xs" wrap="nowrap">
            <StatusBadge status="ready" />
            <Text size="xs" c="dimmed">index ready — no run history recorded yet (re-index to start tracking)</Text>
          </Group>
        );
      }
      const when = latest.finished_at ? new Date(latest.finished_at).toLocaleString() : null;
      return (
        <Group gap="xs" wrap="nowrap">
          <StatusBadge status="ready" />
          <Text size="xs" c="dimmed">{latest.objects_indexed} {objectNoun(latest.kind)} indexed{when ? ` · ${when}` : ''}</Text>
        </Group>
      );
    }
    // No live status (never indexed).
    return <Text size="xs" c="dimmed">Not indexed yet for {datasourceName || 'this data source'}.</Text>;
  })();

  const hasRuns = !!runs && runs.length > 0;

  // Header noun follows the runs' object kind (homogeneous per datasource in
  // practice), so the count column matches the roll-up's wording — "Tables"
  // today rather than a generic "Objects". Falls back to "Objects" when the set
  // is empty or mixed.
  const kindList = [...new Set((runs ?? []).map((r) => r.kind).filter(Boolean))];
  const objectsHeader = kindList.length === 1
    ? kindList[0].charAt(0).toUpperCase() + kindList[0].slice(1)
    : 'Objects';

  return (
    <Stack gap={6}>
      <Group justify="space-between" wrap="nowrap">
        <Text size="sm" fw={500}>Indexing</Text>
        {hasRuns && (
          <Button
            variant="subtle"
            size="compact-xs"
            leftSection={open ? <IconChevronDown size={14} /> : <IconChevronRight size={14} />}
            onClick={() => setOpen((v) => !v)}
          >
            {open ? 'Hide history' : `View history (${runs!.length})`}
          </Button>
        )}
      </Group>

      {statusLine}

      {hasRuns && (
        <Collapse in={open}>
          <Table striped withTableBorder fz="xs" verticalSpacing={4} horizontalSpacing="sm">
            <Table.Thead>
              <Table.Tr>
                <Table.Th>Finished</Table.Th>
                <Table.Th>{objectsHeader}</Table.Th>
                <Table.Th>Blurbs</Table.Th>
                <Table.Th>Duration</Table.Th>
                <Table.Th>Status</Table.Th>
                <Table.Th>Error</Table.Th>
              </Table.Tr>
            </Table.Thead>
            <Table.Tbody>
              {runs!.map((run) => {
                const ms = runDurationMs(run);
                return (
                  <Table.Tr key={`${run.datasource_id}:${run.run_id}`}>
                    <Table.Td>{run.finished_at ? new Date(run.finished_at).toLocaleString() : '—'}</Table.Td>
                    <Table.Td>{run.objects_indexed}</Table.Td>
                    <Table.Td>{run.blurbs_generated}</Table.Td>
                    <Table.Td>{ms != null ? formatDuration(ms) : '—'}</Table.Td>
                    <Table.Td><StatusBadge status={run.status} /></Table.Td>
                    <Table.Td>
                      {run.error
                        ? <Text size="xs" c="red" lineClamp={2} title={run.error} style={{ maxWidth: 280 }}>{run.error}</Text>
                        : <Text size="xs" c="dimmed">—</Text>}
                    </Table.Td>
                  </Table.Tr>
                );
              })}
            </Table.Tbody>
          </Table>
        </Collapse>
      )}
    </Stack>
  );
}
