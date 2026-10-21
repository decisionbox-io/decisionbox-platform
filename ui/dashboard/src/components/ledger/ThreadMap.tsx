'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import Link from 'next/link';
import {
  Background, BackgroundVariant, BaseEdge, Controls, Handle, MarkerType, MiniMap,
  Position, ReactFlow, ReactFlowProvider, useEdgesState, useNodesState,
  useReactFlow, useViewport, type Edge, type EdgeProps, type Node, type NodeProps,
} from '@xyflow/react';
import {
  ActionIcon, Badge, Checkbox, CloseButton, Group, NativeSelect, Paper, ScrollArea,
  Select, Stack, Text, TextInput, Tooltip,
} from '@mantine/core';
import {
  IconArrowRight, IconArrowUp, IconBulb, IconChevronDown, IconClockHour4,
  IconCornerDownRight, IconExternalLink, IconFlag3, IconLayoutGrid,
  IconRoute, IconTargetArrow,
} from '@tabler/icons-react';
import { LedgerGraph } from '@/lib/api';
import {
  GraphIndex, InvestigationMap, KIND_LABEL, MapNode,
  MapNodeKind, ThreadOutcome, TreeFilters,
  areasOf, buildMap, findingEvidenceLink, humanise, insightLink,
  recommendationLink, runLabel, runRefForDiscovery, seenInRuns, severitiesOf,
  statusTrail, statusesOf,
} from './graph';
import {
  FLOW_WIDTH, FlowGeometry, FlowRow, LANE_TITLES, NODE_HEIGHT,
  NODE_WIDTH, Point, laneLeft, placeFlow, roundedPath,
} from './flowLayout';

import '@xyflow/react/dist/style.css';

// Level 2 — the investigation map.
//
// The flat page could say what a project knows. The map says how it was
// worked out: a run picks up a thread an earlier run left, finds something
// that answers it, that drives a recommendation, and it leaves new threads for
// the run after. Reading that as indented text meant holding the shape in your
// head; drawn, it is the shape.
//
// @xyflow/react renders; the layout is flowLayout.ts, because the thing that
// makes an investigation readable is the order it happened in and no graph
// library knows that. One row per run, oldest at the top, each read left to
// right. The library is MIT and bundled at build time, so an air-gapped image
// needs nothing at run time.

const SEVERITY_COLOR: Record<string, string> = {
  critical: 'var(--mantine-color-red-6)',
  high: 'var(--mantine-color-orange-6)',
  medium: 'var(--mantine-color-yellow-6)',
  low: 'var(--mantine-color-gray-5)',
  info: 'var(--mantine-color-gray-5)',
};

const KIND_COLOR: Record<MapNodeKind, string> = {
  finding: 'var(--mantine-color-blue-6)',
  task: 'var(--mantine-color-grape-6)',
  recommendation: 'var(--mantine-color-indigo-6)',
};

const STATUS_COLOR: Record<string, string> = {
  confirmed: 'teal', monitoring: 'blue', changed: 'orange', resolved: 'gray', refuted: 'red',
  open: 'blue', in_progress: 'blue', done: 'teal', dropped: 'gray',
};

function KindIcon({ kind, size = 15 }: { kind: MapNodeKind; size?: number }) {
  if (kind === 'finding') return <IconFlag3 size={size} />;
  if (kind === 'task') return <IconRoute size={size} />;
  return <IconTargetArrow size={size} />;
}

/** accentFor is the stripe down a node's left edge: severity where a node has
 *  one, otherwise the colour of its kind. It is the first thing read, so it
 *  carries the most useful distinction each kind has. */
function accentFor(node: MapNode): string {
  if (node.kind === 'finding' && node.severity) {
    return SEVERITY_COLOR[node.severity] ?? KIND_COLOR.finding;
  }
  return KIND_COLOR[node.kind];
}

// xyflow requires a node's data to be an index-signature record, so the map
// node is widened rather than reshaped — the fields stay named and typed.
type MapNodeData = MapNode & {
  selected?: boolean;
  dimmed?: boolean;
  /** Set on the card that stands in for a thread in the run that took it up. */
  threadOf?: string;
  /** Set on the card that stands in for a finding in a later run that saw it
   *  again and built on it. */
  seenOf?: string;
} & Record<string, unknown>;

/** The thing a card is: itself, or the thread or finding a later card stands
 *  in for. Selecting either card selects the thing, and lights both. */
const entityOf = (card: { id: string; threadOf?: string; seenOf?: string }) =>
  card.threadOf ?? card.seenOf ?? card.id;

const OUTCOME_COLOR: Record<ThreadOutcome, string> = {
  answered: 'teal', closed: 'teal', dropped: 'gray', pursued: 'blue',
};

/** One card on the canvas. Deliberately quiet: a title, one line of context
 *  and a status. Everything else is a click away in the panel, because a
 *  canvas of forty cards each shouting five facts is the wall of text this
 *  view replaced.
 *
 *  The same thread is drawn twice when a later run took it up: once where it
 *  was opened, once where it was picked up. The second card is dashed and
 *  says so, and it carries what came of the thread rather than how it began.
 *  A finding a later run saw again and built on gets the same treatment. */
function InvestigationNode({ data }: NodeProps<Node<MapNodeData>>) {
  const node = data;
  const pickedUp = Boolean(node.threadOf);
  const seenAgain = Boolean(node.seenOf);
  const dashed = pickedUp || seenAgain;
  const accent = accentFor(node);
  const meta = pickedUp
    ? node.workedMeta
    : (seenAgain ? (node.runOrdinal ? `First found in run ${node.runOrdinal}` : 'Found earlier') : node.meta);
  return (
    <div
      style={{
        width: NODE_WIDTH,
        minHeight: NODE_HEIGHT,
        display: 'flex',
        borderRadius: 10,
        overflow: 'hidden',
        background: 'var(--mantine-color-body)',
        border: `1px ${dashed ? 'dashed' : 'solid'} ${node.selected || dashed ? accent : 'var(--db-border-default)'}`,
        boxShadow: node.selected
          ? `0 0 0 2px ${accent}33, 0 6px 16px rgba(0,0,0,.10)`
          : '0 1px 3px rgba(0,0,0,.07)',
        opacity: node.dimmed ? 0.35 : (node.archived ? 0.6 : 1),
        transition: 'box-shadow 150ms ease, opacity 150ms ease, border-color 150ms ease',
        cursor: 'pointer',
      }}
    >
      <Handle type="target" position={Position.Left} style={{ opacity: 0 }} />
      <div style={{ width: 4, background: accent, flexShrink: 0 }} />
      <div style={{ padding: '9px 11px', minWidth: 0, flex: 1 }}>
        <Group gap={6} wrap="nowrap" mb={3}>
          <span style={{ color: accent, display: 'flex' }}>
            {pickedUp ? <IconCornerDownRight size={13} /> : <KindIcon kind={node.kind} size={13} />}
          </span>
          <Text size="10px" fw={700} c="dimmed" tt="uppercase" style={{ letterSpacing: '.04em' }}>
            {pickedUp ? 'Thread picked up' : (seenAgain ? 'Finding seen again' : KIND_LABEL[node.kind])}
          </Text>
          {node.archived && (
            <Badge size="xs" variant="outline" color="gray" tt="none">Archived</Badge>
          )}
          {node.missing && (
            <Badge size="xs" variant="outline" color="gray" tt="none">Not in this view</Badge>
          )}
        </Group>
        <Text size="xs" fw={600} lineClamp={2} style={{ lineHeight: 1.35 }}>{node.title}</Text>
        <Group gap={6} wrap="nowrap" mt={5}>
          {pickedUp && node.outcome && (
            <Badge size="xs" variant="light" tt="none" color={OUTCOME_COLOR[node.outcome]}>
              {humanise(node.outcome)}
            </Badge>
          )}
          {!pickedUp && node.status && (
            <Badge size="xs" variant="light" tt="none" color={STATUS_COLOR[node.status] ?? 'gray'}>
              {humanise(node.status)}
            </Badge>
          )}
          {typeof node.priority === 'number' && node.priority > 0 && (
            <Badge size="xs" variant="outline" color="gray" tt="none">P{node.priority}</Badge>
          )}
          {meta && <Text size="10px" c="dimmed" lineClamp={1}>{meta}</Text>}
        </Group>
      </div>
      <Handle type="source" position={Position.Right} style={{ opacity: 0 }} />
    </div>
  );
}

/** The zoom the map opens at, at most: close enough to read a card. It opens
 *  wider when that is what it takes to get all four lanes of a run across the
 *  screen, because a row read only halfway is the sentence cut off. */
const MAX_OPENING_ZOOM = 0.85;
const MIN_OPENING_ZOOM = 0.4;

const moreNodeID = (ref: string) => `more:${ref || 'undated'}`;
const rowRefOfMore = (id: string) => {
  const ref = id.slice('more:'.length);
  return ref === 'undated' ? '' : ref;
};

/** countsOf reads a set of cards back as "28 findings · 3 threads". */
function countsOf(nodes: MapNode[]): string {
  const counts = { finding: 0, task: 0, recommendation: 0 } as Record<MapNodeKind, number>;
  nodes.forEach((n) => { counts[n.kind] += 1; });
  const label: Record<MapNodeKind, [string, string]> = {
    finding: ['finding', 'findings'],
    task: ['thread', 'threads'],
    recommendation: ['recommendation', 'recommendations'],
  };
  return (Object.keys(counts) as MapNodeKind[])
    .filter((k) => counts[k] > 0)
    .map((k) => `${counts[k]} ${label[k][counts[k] === 1 ? 0 : 1]}`)
    .join(' · ');
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** rowSummary is what a run did, in the order its row reads. The counts are of
 *  what the map holds for that run, so they agree with the cards beside it
 *  even when a filter is on. */
function rowSummary(row: FlowRow): string[] {
  const counts = { finding: 0, task: 0, recommendation: 0 } as Record<MapNodeKind, number>;
  row.nodes.forEach((n) => { counts[n.kind] += 1; });
  const lines: string[] = [];
  if (row.pickedUp) lines.push(`${plural(row.pickedUp, 'thread', 'threads')} picked up`);
  if (counts.finding) lines.push(plural(counts.finding, 'finding', 'findings'));
  if (row.seenAgain) lines.push(`${plural(row.seenAgain, 'earlier finding', 'earlier findings')} seen again`);
  if (counts.recommendation) lines.push(plural(counts.recommendation, 'recommendation', 'recommendations'));
  if (counts.task) lines.push(`${plural(counts.task, 'thread', 'threads')} opened`);
  return lines;
}

const LANE_HINTS: [string, string, string, string] = [
  'Threads earlier runs left it',
  'What it found',
  'What that drove',
  'Threads it left for the next run',
];

// The frozen panes. The lane names across the top and the runs down the left
// are not on the canvas: they are held in place over it while the map scrolls
// underneath, the way a spreadsheet freezes its header row and first column.
// On a map this size, scrolled into the middle, the cards alone cannot say
// which run they are in or which step they are — and that is being lost.
const FROZEN_TOP = 54;
const FROZEN_LEFT = 158;
/** Roughly how tall a run's label is, so it can ride down a tall row without
 *  running off the bottom of it. */
const RUN_LABEL_HEIGHT = 112;

/** What the frozen column says about one row. */
interface RowLabel {
  ref: string;
  label: string;
  date: string;
  lines: string[];
  top: number;
  height: number;
  dated: boolean;
}

function rowLabels(idx: GraphIndex, rows: FlowRow[]): RowLabel[] {
  return rows.map((row) => {
    const run = row.ref ? idx.runs.get(row.ref) : undefined;
    return {
      ref: row.ref,
      label: row.ref ? runLabel(idx, row.ref) : 'Not attributed to a run',
      date: run?.date ? new Date(run.date).toLocaleDateString() : '',
      lines: rowSummary(row),
      top: row.top,
      height: row.height,
      dated: Boolean(row.ordinal),
    };
  });
}

const PANE_STYLE: React.CSSProperties = {
  position: 'absolute',
  background: 'var(--mantine-color-body)',
  zIndex: 4,
};

/** FrozenPanes tracks the viewport and keeps each name lined up with what it
 *  names: a lane's title over that lane however far across the map has
 *  scrolled, and a run's name beside its row however far down. A run's name
 *  rides down with the view while its row fills the screen, so a reader in the
 *  middle of a long run can still see which run it is. */
function FrozenPanes({
  rows, earlierRuns, onJump,
}: {
  rows: RowLabel[]; earlierRuns: boolean; onJump: (ref: string) => void;
}) {
  const { x, y, zoom } = useViewport();
  return (
    <>
      <div
        data-testid="frozen-lanes"
        style={{
          ...PANE_STYLE, top: 0, left: 0, right: 0, height: FROZEN_TOP,
          borderBottom: '1px solid var(--db-border-default)', overflow: 'hidden',
        }}
      >
        {LANE_TITLES.map((title, lane) => (
          <div
            key={title}
            style={{
              position: 'absolute', top: 0, height: FROZEN_TOP, overflow: 'hidden',
              left: laneLeft(lane) * zoom + x, width: NODE_WIDTH * zoom,
              padding: '10px 2px 0',
            }}
          >
            <Group gap={6} wrap="nowrap">
              <Text size="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '.05em' }} truncate>
                {title}
              </Text>
              {lane < LANE_TITLES.length - 1 && (
                <IconArrowRight size={12} style={{ opacity: 0.45, flexShrink: 0 }} />
              )}
            </Group>
            <Text size="10px" c="dimmed" truncate>{LANE_HINTS[lane]}</Text>
          </div>
        ))}
      </div>

      <div
        data-testid="frozen-runs"
        style={{
          ...PANE_STYLE, top: FROZEN_TOP, left: 0, bottom: 0, width: FROZEN_LEFT,
          borderRight: '1px solid var(--db-border-default)', overflow: 'hidden',
        }}
      >
        {rows.map((row) => {
          const top = row.top * zoom + y - FROZEN_TOP;
          const height = row.height * zoom;
          const ride = Math.min(Math.max(0, -top), Math.max(0, height - RUN_LABEL_HEIGHT));
          return (
            <div
              key={row.ref || 'undated'}
              data-testid="frozen-run"
              style={{
                position: 'absolute', top, height, left: 6, right: 6,
                borderRadius: 8,
                background: row.dated ? 'var(--mantine-color-blue-light)' : 'var(--mantine-color-gray-1)',
                border: '1px solid var(--db-border-default)',
              }}
            >
              <div style={{ position: 'absolute', top: ride, left: 0, right: 0, padding: '9px 10px' }}>
                <Group gap={6} wrap="nowrap">
                  <IconClockHour4 size={13} style={{ flexShrink: 0, opacity: 0.7 }} />
                  <Text size="sm" fw={700} lineClamp={2}>{row.label}</Text>
                </Group>
                {row.date && <Text size="10px" c="dimmed" mt={1}>{row.date}</Text>}
                <Stack gap={0} mt={6}>
                  {row.lines.map((line) => (
                    <Text key={line} size="10px" c="dimmed" lineClamp={1}>{line}</Text>
                  ))}
                </Stack>
              </div>
            </div>
          );
        })}
      </div>

      <div
        style={{
          ...PANE_STYLE, zIndex: 5, top: 0, left: 0, width: FROZEN_LEFT, height: FROZEN_TOP,
          borderRight: '1px solid var(--db-border-default)',
          borderBottom: '1px solid var(--db-border-default)',
          padding: earlierRuns ? '4px 8px 0' : '11px 8px 0',
        }}
      >
        <NativeSelect
          size="xs"
          aria-label="Jump to a run"
          // Always back on the prompt: it is a way to move, not a setting.
          value=""
          data={[
            { value: '', label: 'Jump to a run…' },
            ...rows.map((row) => ({ value: row.ref || 'undated', label: row.label })),
          ]}
          onChange={(e) => {
            const value = e.currentTarget.value;
            if (value) onJump(value === 'undated' ? '' : value);
          }}
        />
        {earlierRuns && (
          <Group gap={4} wrap="nowrap" mt={2}>
            <IconArrowUp size={10} style={{ opacity: 0.5, flexShrink: 0 }} />
            <Text size="9px" c="dimmed">Earlier runs not loaded</Text>
          </Group>
        )}
      </div>
    </>
  );
}

type MoreCardData = {
  label: string;
  detail: string;
  open: boolean;
} & Record<string, unknown>;

/** MoreCard stands in for the part of a run's work nothing links to yet, so a
 *  run that found forty things shows the five that went somewhere. */
function MoreCard({ data }: { data: MoreCardData }) {
  return (
    <div
      style={{
        width: NODE_WIDTH,
        minHeight: NODE_HEIGHT,
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'center',
        padding: '9px 13px',
        borderRadius: 10,
        border: '1px dashed var(--db-border-default)',
        background: 'var(--mantine-color-body)',
        cursor: 'pointer',
      }}
    >
      <Group gap={6} wrap="nowrap">
        <IconChevronDown
          size={13}
          style={{
            flexShrink: 0,
            opacity: 0.6,
            transform: data.open ? 'rotate(180deg)' : 'none',
            transition: 'transform 150ms ease',
          }}
        />
        <Text size="xs" fw={600}>{data.label}</Text>
      </Group>
      {data.detail && <Text size="10px" c="dimmed" mt={3} lineClamp={2}>{data.detail}</Text>}
    </div>
  );
}

type RoutedEdgeData = {
  points: Point[];
  labelAt: Point;
  showLabel: boolean;
} & Record<string, unknown>;

/** RoutedEdge draws the path flowLayout chose, instead of the curve the
 *  library would draw between the two handles. A curve takes the shortest way
 *  across, which on a map this dense is straight through the cards in between;
 *  the routed path only ever runs through the gaps. */
function RoutedEdge({ id, data, style, markerEnd, label }: EdgeProps<Edge<RoutedEdgeData>>) {
  if (!data) return null;
  return (
    <BaseEdge
      id={id}
      path={roundedPath(data.points)}
      style={style}
      markerEnd={markerEnd}
      label={data.showLabel ? label : undefined}
      labelX={data.labelAt.x}
      labelY={data.labelAt.y}
      labelShowBg
      labelBgPadding={[5, 2]}
      labelBgBorderRadius={4}
      labelStyle={{ fontSize: 10, fill: 'var(--db-text-secondary, #666)' }}
      labelBgStyle={{ fill: 'var(--mantine-color-body)', fillOpacity: 0.92 }}
    />
  );
}

const nodeTypes = { investigation: InvestigationNode, more: MoreCard };
const edgeTypes = { routed: RoutedEdge };

/** The lines that carry the story from one run to the next: a run picked this
 *  thread up, this finding answered it, this thread continues that one. They
 *  are drawn stronger, and they are the only ones whose words are drawn too —
 *  a line from Found to Recommended says "drove" by where it goes. */
const NARRATIVE_EDGE: Record<string, boolean> = {
  picked_up: true, answered_by: true, supersedes: true,
};

function layout(
  geometry: FlowGeometry,
  selected: string | null,
  connected: Set<string>,
) {
  const { rows, cards } = geometry;

  const nodes: Node<MapNodeData>[] = [...cards.values()].map((card) => ({
    id: card.id,
    type: 'investigation',
    position: { x: card.x, y: card.y },
    data: {
      ...card.node,
      ...(card.threadOf ? { threadOf: card.threadOf } : {}),
      ...(card.seenOf ? { seenOf: card.seenOf } : {}),
      selected: selected === entityOf(card),
      dimmed: Boolean(selected) && !connected.has(card.id),
    },
  }));

  const more: Node[] = rows
    .filter((row) => row.moreAt)
    .map((row) => ({
      id: moreNodeID(row.ref),
      type: 'more',
      position: row.moreAt as { x: number; y: number },
      draggable: false,
      selectable: false,
      data: {
        label: row.hidden.length > 0
          ? `+${row.hidden.length} more from this run`
          : 'Show less',
        detail: row.hidden.length > 0 ? countsOf(row.hidden) : '',
        open: row.hidden.length === 0,
      } as MoreCardData,
    }));

  const edges: Edge<RoutedEdgeData>[] = geometry.edges.map((e) => {
    const lit = !selected || (connected.has(e.source) && connected.has(e.target));
    const story = NARRATIVE_EDGE[e.kind];
    const stroke = !lit
      ? 'var(--mantine-color-gray-3)'
      : (story ? 'var(--mantine-color-grape-5)' : 'var(--mantine-color-gray-5)');
    return {
      id: e.id,
      source: e.source,
      target: e.target,
      type: 'routed',
      label: e.label,
      animated: lit && Boolean(selected),
      data: { points: e.points, labelAt: e.labelAt, showLabel: Boolean(story) },
      markerEnd: { type: MarkerType.ArrowClosed, color: stroke },
      style: {
        stroke,
        strokeWidth: lit && (selected || story) ? 2 : 1.4,
        opacity: lit ? 1 : 0.3,
      },
    };
  });

  return { nodes: [...nodes, ...more], edges };
}

/** A link out to a detail page, with the affordance to open it in a new tab —
 *  the map is a place you come back to, so following a link should not have to
 *  mean leaving it. */
function DetailLink({ href, label }: { href: string; label: string }) {
  return (
    <Group gap={4} wrap="nowrap">
      <Link href={href} style={{ textDecoration: 'none' }}>
        <Text size="xs" td="underline" c="blue">{label}</Text>
      </Link>
      <Tooltip label="Open in a new tab" openDelay={300}>
        <ActionIcon
          component="a"
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          size="xs"
          variant="subtle"
          color="gray"
          aria-label={`Open ${label} in a new tab`}
        >
          <IconExternalLink size={13} />
        </ActionIcon>
      </Tooltip>
    </Group>
  );
}

/** The panel. Everything the cards deliberately leave out lives here. */
function DetailPanel({
  projectId, index, node, onClose,
}: {
  projectId: string; index: GraphIndex; node: MapNode; onClose: () => void;
}) {
  const finding = index.findings.get(node.id);
  const task = index.tasks.get(node.id);
  const rec = index.recommendations.get(node.id);
  const trail = finding ? statusTrail(index, finding) : [];
  const runsSeenIn = finding ? seenInRuns(index, finding) : '';
  const insights = finding
    ? (finding.source_insight_ids ?? [])
      .map((id) => index.insights.get(`insight:${id}`))
      .filter((i): i is NonNullable<typeof i> => Boolean(i))
    : [];
  const findingHref = finding ? findingEvidenceLink(projectId, finding, index) : null;
  const recHref = rec ? recommendationLink(projectId, rec) : null;

  return (
    <Paper
      withBorder radius="md" shadow="sm"
      style={{
        position: 'absolute', top: FROZEN_TOP + 8, right: 12, bottom: 12, width: 340,
        zIndex: 5, display: 'flex', flexDirection: 'column',
        background: 'var(--mantine-color-body)',
      }}
    >
      <Group justify="space-between" wrap="nowrap" p="sm" pb={8}>
        <Group gap={6} wrap="nowrap">
          <span style={{ color: accentFor(node), display: 'flex' }}><KindIcon kind={node.kind} /></span>
          <Text size="xs" fw={700} tt="uppercase" c="dimmed" style={{ letterSpacing: '.04em' }}>
            {KIND_LABEL[node.kind]}
          </Text>
        </Group>
        <CloseButton onClick={onClose} aria-label="Close details" />
      </Group>

      <ScrollArea style={{ flex: 1 }} px="sm" pb="sm">
        <Text size="sm" fw={600} mb={8}>{node.title}</Text>

        <Group gap={6} wrap="wrap" mb="sm">
          {finding?.severity && (
            <Badge size="sm" variant="light" tt="none"
              color={finding.severity === 'critical' ? 'red' : finding.severity === 'high' ? 'orange' : 'yellow'}>
              {humanise(finding.severity)}
            </Badge>
          )}
          {node.status && (
            <Badge size="sm" variant="light" tt="none" color={STATUS_COLOR[node.status] ?? 'gray'}>
              {humanise(node.status)}
            </Badge>
          )}
          {node.archived && <Badge size="sm" variant="outline" color="gray" tt="none">Archived</Badge>}
          {rec?.validation_verdict && (
            <Badge size="sm" variant="light" color="teal" tt="none">{humanise(rec.validation_verdict)}</Badge>
          )}
        </Group>

        {task?.text && task.text !== node.title && (
          <Text size="xs" c="dimmed" mb="sm">{task.text}</Text>
        )}

        <Stack gap="sm">
          {finding && (
            <Section title="Where it was seen">
              <Text size="xs" c="dimmed">
                {runsSeenIn
                  ? `${runsSeenIn.includes(',') ? 'Runs' : 'Run'} ${runsSeenIn}`
                  : 'No run recorded it by name'}
                {finding.seen_count > 1 ? ` · seen ${finding.seen_count} times` : ''}
              </Text>
            </Section>
          )}

          {trail.length > 0 && (
            <Section title="How its status moved">
              <Stack gap={3}>
                {trail.map((step) => (
                  <Group key={step} gap={5} wrap="nowrap" align="flex-start">
                    <IconArrowRight size={12} style={{ marginTop: 3, flexShrink: 0, opacity: 0.6 }} />
                    <Text size="xs" c="dimmed">{step}</Text>
                  </Group>
                ))}
              </Stack>
            </Section>
          )}

          {task && (task.close_reason || task.closed_run_id) && (
            <Section title="How it was closed">
              {task.closed_run_id && (
                <Text size="xs" c="dimmed">
                  {runLabel(index, `run:${task.closed_run_id}`)}
                  {task.closed_at ? ` · ${new Date(task.closed_at).toLocaleDateString()}` : ''}
                </Text>
              )}
              {task.close_reason && (
                <Text size="xs" fs="italic" c="dimmed" mt={3}>“{task.close_reason}”</Text>
              )}
            </Section>
          )}

          {task && (task.pursued_in_run_ids ?? []).length > 0 && (
            <Section title="Runs that pursued it">
              <Text size="xs" c="dimmed">
                {(task.pursued_in_run_ids ?? [])
                  .map((id) => runLabel(index, `run:${id}`)).join(', ')}
              </Text>
            </Section>
          )}

          {(findingHref || insights.length > 0 || recHref) && (
            <Section title="Evidence">
              <Stack gap={5}>
                {findingHref && <DetailLink href={findingHref} label="Open the finding's evidence" />}
                {insights.map((insight) => {
                  const href = insightLink(projectId, insight);
                  const where = runRefForDiscovery(index, insight.discovery_id);
                  const label = insight.missing
                    ? 'Evidence no longer available'
                    : `Evidence from ${where ? runLabel(index, where).toLowerCase() : 'an earlier run'}`;
                  return href
                    ? <DetailLink key={insight.node_id} href={href} label={label} />
                    : <Text key={insight.node_id} size="xs" c="dimmed">{label}</Text>;
                })}
                {recHref && <DetailLink href={recHref} label="Open the recommendation" />}
              </Stack>
            </Section>
          )}
        </Stack>
      </ScrollArea>
    </Paper>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <Text size="10px" fw={700} tt="uppercase" c="dimmed" mb={4} style={{ letterSpacing: '.05em' }}>
        {title}
      </Text>
      {children}
    </div>
  );
}

function Legend() {
  return (
    <Group gap="md" wrap="wrap">
      <Group gap={5} wrap="nowrap">
        <IconClockHour4 size={13} style={{ opacity: 0.7 }} />
        <Text size="xs" c="dimmed">One row per run, oldest at the top · each row reads left to right · scroll to move, pinch or Ctrl/⌘ + scroll to zoom</Text>
      </Group>
      {(['finding', 'task', 'recommendation'] as MapNodeKind[]).map((kind) => (
        <Group key={kind} gap={5} wrap="nowrap">
          <span style={{ color: KIND_COLOR[kind], display: 'flex' }}><KindIcon kind={kind} size={13} /></span>
          <Text size="xs" c="dimmed">{KIND_LABEL[kind]}</Text>
        </Group>
      ))}
      <Group gap={5} wrap="nowrap">
        <span style={{ color: KIND_COLOR.task, display: 'flex' }}><IconCornerDownRight size={13} /></span>
        <Text size="xs" c="dimmed">Dashed: picked up or seen again by a later run</Text>
      </Group>
    </Group>
  );
}

function Canvas({
  projectId, index, map, earlierRuns,
}: {
  projectId: string; index: GraphIndex; map: InvestigationMap; earlierRuns: boolean;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  // Which runs have had their "+N more" opened. Per row, because opening one
  // run's inventory should not unfold every other run's as well.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const { getViewport, setViewport } = useReactFlow();
  const frame = useRef<HTMLDivElement>(null);

  // A selection a filter has since removed is no selection. Keeping it would
  // leave every card that DID match dimmed in favour of one that is no longer
  // on the canvas, with no panel open to explain why.
  const selectedNode = useMemo(
    () => (selected ? map.nodes.find((n) => n.id === selected) ?? null : null),
    [selected, map.nodes],
  );
  const lit = selectedNode ? selected : null;

  // Where the cards go is decided by the map alone. Selection only changes how
  // they are drawn — so it must not produce new geometry, or the effect below
  // would move the viewport on every click and snap a reader who had panned
  // to another chain straight back to the first one.
  const geometry = useMemo(() => placeFlow(map, expanded), [map, expanded]);

  // What stays lit when a card is selected: the thing itself — both of its
  // cards, for a thread a later run picked up or a finding it saw again — and
  // everything one line away
  // from either. More than that and the highlight stops meaning anything.
  const connected = useMemo(() => {
    const set = new Set<string>();
    if (!lit) return set;
    geometry.cards.forEach((card) => {
      if (entityOf(card) === lit) set.add(card.id);
    });
    const own = new Set(set);
    geometry.edges.forEach((e) => {
      if (own.has(e.source)) set.add(e.target);
      if (own.has(e.target)) set.add(e.source);
    });
    return set;
  }, [lit, geometry]);

  const laid = useMemo(
    () => layout(geometry, lit, connected),
    [geometry, lit, connected],
  );
  const labels = useMemo(() => rowLabels(index, geometry.rows), [index, geometry]);
  const [nodes, setNodes, onNodesChange] = useNodesState(laid.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(laid.edges);

  useEffect(() => {
    setNodes(laid.nodes);
    setEdges(laid.edges);
  }, [laid, setNodes, setEdges]);

  // The map opens on the oldest run — where the investigation started — with
  // its first lane just clear of the frozen run column, at a zoom that gets
  // all four lanes of a row across the screen. Fitting the whole map instead
  // shrinks a long history until no card can be read.
  //
  // Keyed on the MAP, not on what is currently drawn: opening a run's "+N
  // more" changes the second and not the first, and moving the view then
  // would throw away the pan of whoever opened it — and could push the cards
  // they just asked for off the screen. Selecting a card moves nothing either.
  // The frame lets React Flow measure the new nodes first.
  const originKey = useMemo(() => map.nodes.map((n) => n.id).join('|'), [map]);
  useEffect(() => {
    const raf = window.requestAnimationFrame(() => {
      const width = frame.current?.clientWidth || 1100;
      const span = FLOW_WIDTH - laneLeft(0);
      const zoom = Math.min(MAX_OPENING_ZOOM, Math.max(MIN_OPENING_ZOOM, (width - FROZEN_LEFT - 28) / span));
      setViewport({ x: FROZEN_LEFT + 14 - laneLeft(0) * zoom, y: FROZEN_TOP + 12, zoom }, { duration: 300 });
    });
    return () => window.cancelAnimationFrame(raf);
  }, [originKey, setViewport]);

  // Jumping to a run brings its row up under the frozen lane names, from the
  // beginning — its first lane just clear of the run column — at the reader's
  // own zoom. Keeping a sideways scroll instead lands them halfway along the
  // row, which is no help to someone who has lost their place.
  const jumpTo = useCallback((ref: string) => {
    const row = geometry.rows.find((r) => r.ref === ref);
    if (!row) return;
    const { zoom } = getViewport();
    setViewport(
      { x: FROZEN_LEFT + 14 - laneLeft(0) * zoom, y: FROZEN_TOP + 12 - row.top * zoom, zoom },
      { duration: 400 },
    );
  }, [geometry, getViewport, setViewport]);

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    if (node.type === 'more') {
      const ref = rowRefOfMore(node.id);
      setExpanded((prev) => {
        const next = new Set(prev);
        if (!next.delete(ref)) next.add(ref);
        return next;
      });
      return;
    }
    // A picked-up or seen-again card is the thing it stands in for.
    const data = node.data as MapNodeData | undefined;
    const id = entityOf({ id: node.id, threadOf: data?.threadOf, seenOf: data?.seenOf });
    setSelected((prev) => (prev === id ? null : id));
  }, []);

  return (
    <div
      ref={frame}
      style={{
        position: 'relative',
        // As much of the screen as the page can spare: a map this size read
        // through a letterbox is how a reader gets lost in it.
        height: 'max(620px, calc(100vh - 260px))',
        borderRadius: 8, overflow: 'hidden', border: '1px solid var(--db-border-default)',
      }}
    >
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        onPaneClick={() => setSelected(null)}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        // Where a card sits is what it MEANS here — its row is the run whose
        // work it was, its lane is what kind of step it was. Dragging one
        // somewhere else would not rearrange the map, it would make it lie,
        // and the lines are routed around where the cards are.
        nodesDraggable={false}
        // No `fitView` prop: see the opening effect above. The controls and
        // the minimap are there for the whole picture.
        minZoom={0.1}
        maxZoom={1.6}
        // Scrolling moves the map, the way it moves a spreadsheet; zooming is
        // a pinch, or the wheel with Ctrl (Cmd on a Mac) held. Zoom on every
        // scroll is how a reader trying to go down the page ends up somewhere
        // else entirely.
        panOnScroll
        zoomOnScroll={false}
        // A fence round the map, so a fling cannot leave the view on empty
        // canvas with nothing to find the way back by.
        translateExtent={[[-600, -400], [FLOW_WIDTH + 600, geometry.height + 800]]}
        proOptions={{ hideAttribution: false }}
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
        <Controls showInteractive={false} position="top-right" style={{ marginTop: FROZEN_TOP + 10 }} />
        <MiniMap
          pannable zoomable
          nodeColor={(n) => (n.type === 'investigation'
            ? accentFor(n.data as unknown as MapNode)
            : 'var(--mantine-color-gray-3)')}
          style={{ background: 'var(--mantine-color-body)' }}
        />
      </ReactFlow>

      <FrozenPanes rows={labels} earlierRuns={earlierRuns} onJump={jumpTo} />

      {selectedNode && (
        <DetailPanel
          projectId={projectId}
          index={index}
          node={selectedNode}
          onClose={() => setSelected(null)}
        />
      )}
    </div>
  );
}

export default function ThreadMap({
  projectId, graph, index,
}: {
  projectId: string; graph: LedgerGraph; index: GraphIndex;
}) {
  const [filters, setFilters] = useState<TreeFilters>({});
  const areas = useMemo(() => areasOf(index), [index]);
  const severities = useMemo(() => severitiesOf(index), [index]);
  const statuses = useMemo(() => statusesOf(index), [index]);
  const map = useMemo(() => buildMap(index, filters), [index, filters]);
  const hasLatestRun = Boolean(index.latestRun);

  const setFilter = (patch: Partial<TreeFilters>) => setFilters((prev) => ({ ...prev, ...patch }));

  return (
    <Stack gap="sm">
      <Group gap="sm" align="flex-end" wrap="wrap">
        <Select
          label="Area" placeholder="All areas" data={areas.map((a) => ({ value: a, label: humanise(a) }))}
          value={filters.area ?? null} onChange={(v) => setFilter({ area: v ?? undefined })}
          clearable w={170} size="xs"
        />
        <Select
          label="Severity" placeholder="Any" data={severities.map((v) => ({ value: v, label: humanise(v) }))}
          value={filters.severity ?? null} onChange={(v) => setFilter({ severity: v ?? undefined })}
          clearable w={140} size="xs"
        />
        <Select
          label="Status" placeholder="Any" data={statuses.map((v) => ({ value: v, label: humanise(v) }))}
          value={filters.status ?? null} onChange={(v) => setFilter({ status: v ?? undefined })}
          clearable w={150} size="xs"
        />
        <TextInput
          label="Seen from" type="date" value={filters.from ?? ''}
          onChange={(e) => setFilter({ from: e.currentTarget.value || undefined })} w={148} size="xs"
        />
        <TextInput
          label="Seen to" type="date" value={filters.to ?? ''}
          onChange={(e) => setFilter({ to: e.currentTarget.value || undefined })} w={148} size="xs"
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

      {map.nodes.length === 0 ? (
        <Group gap={6}>
          <IconBulb size={15} />
          <Text size="sm" c="dimmed">Nothing matches these filters. Clear one to widen the map.</Text>
        </Group>
      ) : (
        <>
          <Canvas
            projectId={projectId}
            index={index}
            map={map}
            earlierRuns={Boolean(graph?.truncated?.runs)}
          />
          <Group justify="space-between" wrap="wrap" gap="sm">
            <Legend />
            <Group gap="sm">
              <Group gap={5} wrap="nowrap">
                <IconLayoutGrid size={13} />
                <Text size="xs" c="dimmed">
                  {map.nodes.length} of {map.totalNodes} nodes · {map.edges.length} links
                </Text>
              </Group>
              <Text size="xs" c="dimmed">Click a card for its evidence.</Text>
            </Group>
          </Group>
          {map.truncated && (
            <Text size="xs" c="dimmed">
              This investigation is larger than one map can show. Narrow it with the filters above.
            </Text>
          )}
          {(graph.truncated?.runs || graph.truncated?.finding_trails) && (
            <Text size="xs" c="dimmed">
              Some earlier history is not loaded — this project has more than one page of it.
            </Text>
          )}
        </>
      )}
    </Stack>
  );
}

export function ThreadMapWithProvider(props: {
  projectId: string; graph: LedgerGraph; index: GraphIndex;
}) {
  return (
    <ReactFlowProvider>
      <ThreadMap {...props} />
    </ReactFlowProvider>
  );
}
