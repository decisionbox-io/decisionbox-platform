'use client';

import { useMemo, useState } from 'react';
import Link from 'next/link';
import {
  Badge, Button, Checkbox, Group, Select, Stack, Text, TextInput, Tooltip, UnstyledButton,
} from '@mantine/core';
import { IconChevronRight } from '@tabler/icons-react';
import { LedgerGraph } from '@/lib/api';
import {
  DEFAULT_OPEN_DEPTH, GraphIndex, ROOTS_PAGE_SIZE, TreeFilters, TreeNode,
  areasOf, buildTree, countNodes, findingEvidenceLink, insightLink,
  recommendationLink, runLabel, runRefForDiscovery, severitiesOf, statusTrail, statusesOf,
} from './graph';

// Level 2 — Threads: the tree.
//
// Roots are findings and root hypotheses; branches are the follow-up threads;
// hanging off each node are the findings it produced and the recommendations
// those drove. Hand-rolled indentation and a CSS spine — the dashboard has no
// graph library and this view does not justify adding one for air-gapped
// builds. A node-link canvas is a separate decision.
//
// Two things keep it usable on a mature project: everything below depth 2 is
// collapsed, and the roots are paged. Virtualising a variable-height
// collapsible tree needs either a dependency or a measurement layer, and
// neither is warranted for a list that is already bounded by the run window.

const SEVERITY_COLOR: Record<string, string> = {
  critical: 'red', high: 'orange', medium: 'yellow', low: 'gray', info: 'gray',
};

const FINDING_STATUS_COLOR: Record<string, string> = {
  confirmed: 'teal', monitoring: 'blue', changed: 'orange', resolved: 'gray', refuted: 'red',
};

const TASK_STATUS_COLOR: Record<string, string> = {
  open: 'blue', in_progress: 'blue', done: 'teal', dropped: 'gray',
};

/** Node glyphs, so the kind of a node is readable without expanding it. */
const GLYPH: Record<string, string> = {
  finding: '●',
  task: '○',
  recommendation: '▸',
  learning: '✎',
};

const VIA_LABEL: Record<string, string> = {
  grew_from: 'grew out of this',
  supersedes: 'continues this thread',
  answered_by: 'answered by',
  recommends_on: 'recommendation',
};

function NodeRow({
  projectId, index, node, open, onToggle,
}: {
  projectId: string;
  index: GraphIndex;
  node: TreeNode;
  open: boolean;
  onToggle: () => void;
}) {
  const finding = index.findings.get(node.nodeId);
  const task = index.tasks.get(node.nodeId);
  const rec = index.recommendations.get(node.nodeId);

  const hasChildren = node.children.length > 0;
  const below = hasChildren ? countNodes(node) - 1 : 0;

  let label = node.nodeId;
  let badges: React.ReactNode = null;
  let href: string | null = null;
  let detail: string | undefined;
  let dimmed = false;

  if (finding) {
    label = finding.name || finding.id;
    dimmed = Boolean(finding.archived) || Boolean(finding.missing);
    href = findingEvidenceLink(projectId, finding);
    const runs = (finding.sightings ?? [])
      .map((s) => index.runOrdinal.get(`run:${s.run_id}`))
      .filter((n): n is number => Boolean(n));
    badges = finding.missing ? (
      <Badge size="xs" variant="outline" color="gray">not in this view</Badge>
    ) : (
      <>
        {finding.severity && (
          <Badge size="xs" variant="light" color={SEVERITY_COLOR[finding.severity] || 'gray'}>
            {finding.severity}
          </Badge>
        )}
        {finding.status && (
          <Badge size="xs" variant="light" color={FINDING_STATUS_COLOR[finding.status] || 'gray'}>
            {finding.status}
          </Badge>
        )}
        {finding.seen_count > 1 && (
          <Text size="xs" c="dimmed">
            seen {finding.seen_count}×{runs.length > 0 ? ` · runs ${runs.join(', ')}` : ''}
          </Text>
        )}
        {finding.archived && (
          <Tooltip label="Archived by the retention cap. Still part of the history." openDelay={200}>
            <Badge size="xs" variant="outline" color="gray">archived</Badge>
          </Tooltip>
        )}
      </>
    );
  } else if (task) {
    label = task.title || task.text || task.id;
    detail = task.title && task.text !== task.title ? task.text : undefined;
    dimmed = Boolean(task.missing);
    const openedIn = index.runOrdinal.get(`run:${task.created_run_id}`);
    const closedIn = index.runOrdinal.get(`run:${task.closed_run_id}`);
    badges = task.missing ? (
      <Badge size="xs" variant="outline" color="gray">not in this view</Badge>
    ) : (
      <>
        <Badge size="xs" variant="light" color={task.kind === 'hypothesis' ? 'grape' : 'blue'}>
          {(task.kind || 'task').replace('_', ' ')}
        </Badge>
        {task.status && (
          <Badge size="xs" variant="light" color={TASK_STATUS_COLOR[task.status] || 'gray'}>
            {task.status}
          </Badge>
        )}
        {openedIn && <Text size="xs" c="dimmed">opened run {openedIn}</Text>}
        {closedIn && <Text size="xs" c="dimmed">closed run {closedIn}</Text>}
      </>
    );
  } else if (rec) {
    label = rec.title || rec.id;
    href = recommendationLink(projectId, rec);
    badges = (
      <>
        <Badge size="xs" variant="light" color="indigo">recommendation</Badge>
        {typeof rec.priority === 'number' && rec.priority > 0 && (
          <Badge size="xs" variant="outline" color="gray">P{rec.priority}</Badge>
        )}
        {rec.validation_verdict && (
          <Badge size="xs" variant="light" color="teal">{rec.validation_verdict}</Badge>
        )}
      </>
    );
  }

  const trail = finding ? statusTrail(index, finding) : [];
  const insights = finding
    ? (finding.source_insight_ids ?? [])
      .map((id) => index.insights.get(`insight:${id}`))
      .filter((i): i is NonNullable<typeof i> => Boolean(i))
    : [];

  return (
    <div style={{ opacity: dimmed ? 0.55 : 1 }}>
      <Group gap={6} wrap="nowrap" align="flex-start">
        {hasChildren ? (
          <UnstyledButton
            onClick={onToggle}
            aria-expanded={open}
            aria-label={open ? `Collapse ${label}` : `Expand ${label}`}
            style={{ flexShrink: 0, marginTop: 2 }}
          >
            <IconChevronRight
              size={14}
              style={{ transform: open ? 'rotate(90deg)' : 'none', transition: 'transform 150ms ease' }}
            />
          </UnstyledButton>
        ) : (
          <div style={{ width: 14, flexShrink: 0 }} />
        )}
        <Text size="sm" c="dimmed" style={{ flexShrink: 0, marginTop: 1 }}>{GLYPH[node.kind] || '·'}</Text>
        <div style={{ minWidth: 0, flex: 1 }}>
          <Group gap={6} wrap="wrap" align="center">
            {href ? (
              <Link href={href} style={{ textDecoration: 'none' }}>
                <Text size="sm" fw={600} td="underline">{label}</Text>
              </Link>
            ) : (
              <Text size="sm" fw={600}>{label}</Text>
            )}
            {badges}
            {!open && below > 0 && (
              <Badge size="xs" variant="outline" color="gray">+{below} below</Badge>
            )}
            {node.cyclic && (
              <Tooltip label="This thread links back to one of its own parents; the chain stops here." openDelay={200}>
                <Badge size="xs" variant="outline" color="orange">loops back</Badge>
              </Tooltip>
            )}
            {node.repeated && (
              <Tooltip label="This node is reachable by more than one path; its branch is drawn where it first appears." openDelay={200}>
                <Badge size="xs" variant="outline" color="gray">shown above</Badge>
              </Tooltip>
            )}
            {node.budgetReached && (
              <Tooltip label="This investigation is larger than one tree can show; narrow the view with the filters." openDelay={200}>
                <Badge size="xs" variant="outline" color="gray">more below</Badge>
              </Tooltip>
            )}
          </Group>
          {detail && <Text size="xs" c="dimmed" mt={2}>{detail}</Text>}
          {task?.close_reason && (
            <Text size="xs" c="dimmed" fs="italic" mt={2}>“{task.close_reason}”</Text>
          )}
          {open && trail.length > 0 && (
            <Text size="xs" c="dimmed" mt={4}>{trail.join(' → ')}</Text>
          )}
          {open && insights.length > 0 && (
            <Group gap={8} wrap="wrap" mt={4}>
              <Text size="xs" c="dimmed">evidence:</Text>
              {insights.map((insight) => {
                const link = insightLink(projectId, insight);
                // Labelled by the run it came from, not by the insight's name:
                // a finding and the insight behind it usually carry the same
                // sentence, so repeating it says nothing, while "Run 2" says
                // which run's SQL the link opens.
                const runRef = runRefForDiscovery(index, insight.discovery_id);
                const label = insight.missing
                  ? 'not in this view'
                  : (runRef ? runLabel(index, runRef) : 'an earlier run');
                if (!link) {
                  return <Text key={insight.node_id} size="xs" c="dimmed">{label}</Text>;
                }
                return (
                  <Link key={insight.node_id} href={link} style={{ textDecoration: 'none' }}>
                    <Text size="xs" td="underline" c="dimmed">{label}</Text>
                  </Link>
                );
              })}
            </Group>
          )}
          {open && finding && (finding.answered_task_ids ?? []).length > 0 && (
            <Text size="xs" c="dimmed" mt={4}>
              answered {(finding.answered_task_ids ?? []).length} thread
              {(finding.answered_task_ids ?? []).length === 1 ? '' : 's'}
            </Text>
          )}
          {open && task && (task.pursued_in_run_ids ?? []).length > 0 && (
            <Text size="xs" c="dimmed" mt={4}>
              pursued in {(task.pursued_in_run_ids ?? [])
                .map((runID) => runLabel(index, `run:${runID}`))
                .join(', ')}
            </Text>
          )}
        </div>
      </Group>
    </div>
  );
}

function Branch({
  projectId, index, node, openPaths, toggle,
}: {
  projectId: string;
  index: GraphIndex;
  node: TreeNode;
  openPaths: Set<string>;
  toggle: (path: string) => void;
}) {
  // Depth 0 and 1 are open unless the reader closed them; deeper levels are
  // closed unless they opened them. Keeping one set and inverting the default
  // per depth is what lets a reader close a shallow branch they do not care
  // about and open a deep one they do.
  const defaultOpen = node.depth < DEFAULT_OPEN_DEPTH;
  const toggled = openPaths.has(node.path);
  const open = defaultOpen ? !toggled : toggled;

  return (
    <div style={node.depth === 0 ? undefined : {
      marginLeft: 14,
      paddingLeft: 10,
      borderLeft: '1px solid var(--db-border-default)',
    }}>
      {node.via && node.depth > 0 && (
        <Text size="xs" c="dimmed" mb={2}>{VIA_LABEL[node.via] || node.via}</Text>
      )}
      <NodeRow
        projectId={projectId}
        index={index}
        node={node}
        open={open}
        onToggle={() => toggle(node.path)}
      />
      {open && node.children.map((child) => (
        <Branch
          key={child.path}
          projectId={projectId}
          index={index}
          node={child}
          openPaths={openPaths}
          toggle={toggle}
        />
      ))}
    </div>
  );
}

export default function ThreadTree({
  projectId, graph, index,
}: {
  projectId: string; graph: LedgerGraph; index: GraphIndex;
}) {
  const [filters, setFilters] = useState<TreeFilters>({});
  const [openPaths, setOpenPaths] = useState<Set<string>>(new Set());
  const [visibleRoots, setVisibleRoots] = useState(ROOTS_PAGE_SIZE);

  const areas = useMemo(() => areasOf(index), [index]);
  const severities = useMemo(() => severitiesOf(index), [index]);
  const statuses = useMemo(() => statusesOf(index), [index]);
  const roots = useMemo(() => buildTree(index, filters), [index, filters]);

  const toggle = (path: string) => setOpenPaths((prev) => {
    const next = new Set(prev);
    if (next.has(path)) next.delete(path); else next.add(path);
    return next;
  });

  const setFilter = (patch: Partial<TreeFilters>) => {
    setFilters((prev) => ({ ...prev, ...patch }));
    setVisibleRoots(ROOTS_PAGE_SIZE);
  };

  const shown = roots.slice(0, visibleRoots);
  const hasLatestRun = Boolean(index.latestRun);

  return (
    <Stack gap="md">
      <Group gap="sm" align="flex-end" wrap="wrap">
        <Select
          label="Area" placeholder="All areas" data={areas} value={filters.area ?? null}
          onChange={(v) => setFilter({ area: v ?? undefined })} clearable w={170} size="xs"
        />
        <Select
          label="Severity" placeholder="Any" data={severities} value={filters.severity ?? null}
          onChange={(v) => setFilter({ severity: v ?? undefined })} clearable w={140} size="xs"
        />
        <Select
          label="Status" placeholder="Any" data={statuses} value={filters.status ?? null}
          onChange={(v) => setFilter({ status: v ?? undefined })} clearable w={150} size="xs"
        />
        <TextInput
          label="Seen from" type="date" value={filters.from ?? ''}
          onChange={(e) => setFilter({ from: e.currentTarget.value || undefined })} w={150} size="xs"
        />
        <TextInput
          label="Seen to" type="date" value={filters.to ?? ''}
          onChange={(e) => setFilter({ to: e.currentTarget.value || undefined })} w={150} size="xs"
        />
        <Tooltip
          label={hasLatestRun
            ? 'Only the findings the most recent run saw or re-judged'
            : 'No runs have reflected yet'}
          openDelay={200}
        >
          <Checkbox
            label="Only what changed in the last run"
            checked={Boolean(filters.changedSinceLastRun)}
            disabled={!hasLatestRun}
            onChange={(e) => setFilter({ changedSinceLastRun: e.currentTarget.checked || undefined })}
            size="xs"
          />
        </Tooltip>
      </Group>

      {roots.length === 0 ? (
        <Text size="sm" c="dimmed">
          Nothing matches these filters. Clear one to widen the view.
        </Text>
      ) : (
        <Stack gap="md">
          {shown.map((root) => (
            <Branch
              key={root.path}
              projectId={projectId}
              index={index}
              node={root}
              openPaths={openPaths}
              toggle={toggle}
            />
          ))}
        </Stack>
      )}

      {roots.length > visibleRoots && (
        <Group gap="sm">
          <Button
            size="xs" variant="default"
            onClick={() => setVisibleRoots((n) => n + ROOTS_PAGE_SIZE)}
          >
            Show {Math.min(ROOTS_PAGE_SIZE, roots.length - visibleRoots)} more
          </Button>
          <Text size="xs" c="dimmed">
            {visibleRoots} of {roots.length} threads shown
          </Text>
        </Group>
      )}

      {(graph.truncated?.runs || graph.truncated?.finding_trails) && (
        <Text size="xs" c="dimmed">
          Some earlier history is not loaded — this project has more than one page of it.
        </Text>
      )}
    </Stack>
  );
}
