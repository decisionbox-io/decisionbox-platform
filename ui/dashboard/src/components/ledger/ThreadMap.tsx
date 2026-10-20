'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import {
  Background, BackgroundVariant, Controls, Handle, MarkerType, MiniMap,
  Position, ReactFlow, ReactFlowProvider, useEdgesState, useNodesState,
  useReactFlow, type Edge, type Node, type NodeProps,
} from '@xyflow/react';
import {
  ActionIcon, Badge, Button, Checkbox, CloseButton, Group, Paper, ScrollArea,
  Select, Stack, Text, TextInput, Tooltip,
} from '@mantine/core';
import {
  IconArrowRight, IconBulb, IconChevronDown, IconClockHour4, IconExternalLink,
  IconFlag3, IconLayoutGrid, IconMaximize, IconRoute, IconTargetArrow,
} from '@tabler/icons-react';
import { LedgerGraph } from '@/lib/api';
import {
  EDGE_DRAWN_REVERSED, GraphIndex, InvestigationMap, KIND_LABEL, MapNode,
  MapNodeKind, TreeFilters,
  areasOf, buildMap, findingEvidenceLink, humanise, insightLink,
  recommendationLink, runLabel, runRefForDiscovery, seenInRuns, severitiesOf,
  statusTrail, statusesOf,
} from './graph';

import '@xyflow/react/dist/style.css';

// Level 2 — the investigation map.
//
// The flat page could say what a project knows. The map says how it was
// worked out: a finding opens a thread, a later run answers it with another
// finding, and that drives a recommendation. Reading that as indented text
// meant holding the shape in your head; drawn, it is the shape.
//
// @xyflow/react renders; the layout is this file's own, because the thing
// that makes an investigation readable is the order it happened in and no
// graph library knows that. Left to right is run 1, run 2, run 3. The library
// is MIT and bundled at build time, so an air-gapped image needs nothing at
// run time.

const NODE_WIDTH = 268;
const NODE_HEIGHT = 92;

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
} & Record<string, unknown>;

/** One card on the canvas. Deliberately quiet: a title, one line of context
 *  and a status. Everything else is a click away in the panel, because a
 *  canvas of forty cards each shouting five facts is the wall of text this
 *  view replaced. */
function InvestigationNode({ data }: NodeProps<Node<MapNodeData>>) {
  const node = data;
  return (
    <div
      style={{
        width: NODE_WIDTH,
        minHeight: NODE_HEIGHT,
        display: 'flex',
        borderRadius: 10,
        overflow: 'hidden',
        background: 'var(--mantine-color-body)',
        border: `1px solid ${node.selected ? accentFor(node) : 'var(--db-border-default)'}`,
        boxShadow: node.selected
          ? `0 0 0 2px ${accentFor(node)}33, 0 6px 16px rgba(0,0,0,.10)`
          : '0 1px 3px rgba(0,0,0,.07)',
        opacity: node.dimmed ? 0.35 : (node.archived ? 0.6 : 1),
        transition: 'box-shadow 150ms ease, opacity 150ms ease, border-color 150ms ease',
        cursor: 'pointer',
      }}
    >
      <Handle type="target" position={Position.Left} style={{ opacity: 0 }} />
      <div style={{ width: 4, background: accentFor(node), flexShrink: 0 }} />
      <div style={{ padding: '9px 11px', minWidth: 0, flex: 1 }}>
        <Group gap={6} wrap="nowrap" mb={3}>
          <span style={{ color: accentFor(node), display: 'flex' }}>
            <KindIcon kind={node.kind} size={13} />
          </span>
          <Text size="10px" fw={700} c="dimmed" tt="uppercase" style={{ letterSpacing: '.04em' }}>
            {KIND_LABEL[node.kind]}
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
          {node.status && (
            <Badge size="xs" variant="light" tt="none" color={STATUS_COLOR[node.status] ?? 'gray'}>
              {humanise(node.status)}
            </Badge>
          )}
          {typeof node.priority === 'number' && node.priority > 0 && (
            <Badge size="xs" variant="outline" color="gray" tt="none">P{node.priority}</Badge>
          )}
          {node.meta && (
            <Text size="10px" c="dimmed" lineClamp={1}>{node.meta}</Text>
          )}
        </Group>
      </div>
      <Handle type="source" position={Position.Right} style={{ opacity: 0 }} />
    </div>
  );
}

/** A run band: one run's work, drawn as its own column under its own header.
 *
 *  The map is laid out in TIME, because that is the only order a reader
 *  already has. Left to right is run 1, then run 2, then run 3 — so "how did
 *  this start" is the leftmost band, and "what followed" is the next one
 *  along. Everything in a band is that run's own work, which makes an edge
 *  leaving a band exactly the thing the ledger exists to record: a later run
 *  building on an earlier one.
 *
 *  Laid out by hand rather than by a graph library. A force or rank layout
 *  arranges a graph by its edges, and most of a real ledger has none — which
 *  is how the first version of this came out as a field of cards with no
 *  beginning. Time is the spine; the edges hang off it. */
interface RunBand {
  ref: string;
  ordinal: number;
  /** Everything this run produced that the filters admitted. */
  nodes: MapNode[];
  /** Everything with a link to anything — always drawn, because an edge
   *  whose other end is not on the canvas is an edge nobody can see. */
  linked: MapNode[];
  /** The run's own work that nothing points at. Behind the "+N more" until
   *  someone asks for it. */
  loose: MapNode[];
  /** What is on the canvas right now. */
  shown: MapNode[];
  hidden: MapNode[];
  x: number;
  width: number;
  /** Where the "+N more" card goes, when the band has one. */
  moreAt?: { x: number; y: number };
}

/** Spacing. Generous on purpose: a line you cannot follow is the same as no
 *  line at all, and these cards are dense enough already. */
const BAND_GAP = 150;
const CARD_GAP_X = 56;
const CARD_GAP_Y = 22;
/** The gap between a run's own tiers and a loose card below them. */
const TIER_GAP = 44;
/** How wide the grid of loose cards gets before it wraps. */
const LOOSE_COLUMNS = 3;
const HEADER_HEIGHT = 58;
const HEADER_GAP = 30;
/** How many runs the opening position is computed from. */
const OPENING_BANDS = 3;
/** The zoom the map opens at: close enough to read a card, wide enough that
 *  the first runs and the lines between them are in frame together. */
const OPENING_ZOOM = 0.72;

const BAND_KIND_ORDER: Record<MapNodeKind, number> = { finding: 0, task: 1, recommendation: 2 };
const BAND_SEVERITY: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };

/** bandsOf groups the nodes by the run whose work they were, oldest first.
 *
 *  Anything no run can be found for goes in one band at the end, labelled as
 *  such. On a project that predates this provenance that is most of the map —
 *  and saying so once is far better than scattering it through the timeline
 *  as though the dates were known. */
function bandsOf(map: InvestigationMap, linked: Set<string>): RunBand[] {
  const byRef = new Map<string, RunBand>();
  map.nodes.forEach((n) => {
    const ref = bandKeyOf(n);
    const band = byRef.get(ref);
    if (band) band.nodes.push(n);
    else byRef.set(ref, {
      ref,
      ordinal: n.runOrdinal ?? 0,
      nodes: [n],
      linked: [],
      loose: [],
      shown: [],
      hidden: [],
      x: 0,
      width: 0,
    });
  });

  const bands = [...byRef.values()].sort((a, b) => {
    if (!a.ordinal !== !b.ordinal) return a.ordinal ? -1 : 1; // undated last
    return a.ordinal - b.ordinal;
  });

  // Within a band, whatever something links to comes first — that is the
  // order the org chart is built in, so the chains that cross bands start at
  // the top of each run where they are easiest to pick up.
  bands.forEach((band) => {
    band.nodes.sort((a, b) => {
      const la = linked.has(a.id) ? 0 : 1;
      const lb = linked.has(b.id) ? 0 : 1;
      if (la !== lb) return la - lb;
      if (a.kind !== b.kind) return BAND_KIND_ORDER[a.kind] - BAND_KIND_ORDER[b.kind];
      const sa = BAND_SEVERITY[a.severity ?? ''] ?? 5;
      const sb = BAND_SEVERITY[b.severity ?? ''] ?? 5;
      if (sa !== sb) return sa - sb;
      return a.title.localeCompare(b.title);
    });
  });
  return bands;
}

/** bandKeyOf is which band a node belongs to — its run, or the one band for
 *  everything no run can be found for. */
const bandKeyOf = (n: MapNode) => (n.runOrdinal ? (n.runRef ?? '') : '');

/** place lays each run out as a small org chart and puts the runs in a row.
 *
 *  Inside a run, a card that derives from another is drawn in the next tier
 *  along, beside the card it derives from: a recommendation next to the
 *  finding it recommends on, a thread next to the finding that opened it.
 *  Stacking them in one column instead, which is what a grid does, leaves
 *  every one of those links looping around half the run to get where it is
 *  going — the lines are all still there and not one of them is followable.
 *
 *  Everything with a link is drawn, always. The budget comes out of the cards
 *  nothing points at, which fold into one "+N more" per run. An edge with one
 *  end missing is worse than useless: it tells a reader there is something to
 *  follow and then does not let them. */
function place(map: InvestigationMap, expanded: Set<string>): {
  at: Map<string, { x: number; y: number }>;
  bands: RunBand[];
  focus: string[];
} {
  const linked = new Set<string>();
  map.edges.forEach((e) => {
    linked.add(e.source);
    linked.add(e.target);
  });

  const bands = bandsOf(map, linked);
  const bandOf = new Map<string, string>();
  map.nodes.forEach((n) => bandOf.set(n.id, bandKeyOf(n)));

  // Who derives from whom, within one run. A card's parent is the first card
  // in its own run that it points at; a card pointing only at another run has
  // no parent here, and stands as a tier-one card of its own.
  const parentOf = new Map<string, string>();
  map.edges.forEach((e) => {
    if (parentOf.has(e.source)) return;
    if (!linked.has(e.target)) return;
    if (bandOf.get(e.source) !== bandOf.get(e.target)) return;
    if (e.source === e.target) return;
    parentOf.set(e.source, e.target);
  });
  // A two-tier chart, not an arbitrary one: a card whose parent is itself a
  // child stays in tier one, so the tiers keep meaning "found" and "what came
  // off it" rather than drifting into a ladder nobody can scan.
  [...parentOf.keys()].forEach((id) => {
    const parent = parentOf.get(id) as string;
    if (parentOf.has(parent)) parentOf.delete(id);
  });

  const at = new Map<string, { x: number; y: number }>();
  const top = HEADER_HEIGHT + HEADER_GAP;
  const rowStep = NODE_HEIGHT + CARD_GAP_Y;
  const colStep = NODE_WIDTH + CARD_GAP_X;
  let x = 0;

  bands.forEach((band) => {
    const open = expanded.has(band.ref);
    band.linked = band.nodes.filter((n) => linked.has(n.id));
    band.loose = band.nodes.filter((n) => !linked.has(n.id));
    band.shown = open ? [...band.linked, ...band.loose] : band.linked;
    band.hidden = open ? [] : band.loose;

    const children = new Map<string, MapNode[]>();
    const tierOne: MapNode[] = [];
    band.linked.forEach((n) => {
      const parent = parentOf.get(n.id);
      if (parent && bandOf.get(parent) === band.ref) {
        const kids = children.get(parent);
        if (kids) kids.push(n);
        else children.set(parent, [n]);
      } else {
        tierOne.push(n);
      }
    });

    let row = 0;
    let tiers = 1;
    tierOne.forEach((parent) => {
      at.set(parent.id, { x, y: top + row * rowStep });
      const kids = children.get(parent.id) ?? [];
      kids.forEach((kid, i) => {
        at.set(kid.id, { x: x + colStep, y: top + (row + i) * rowStep });
      });
      if (kids.length > 0) tiers = 2;
      row += Math.max(1, kids.length);
    });

    // The loose cards, if they have been asked for, in a block of their own
    // underneath — they have no links to follow, so a grid costs nothing.
    let y = top + row * rowStep + (row > 0 ? TIER_GAP : 0);
    if (open) {
      band.loose.forEach((n, i) => {
        at.set(n.id, {
          x: x + (i % LOOSE_COLUMNS) * colStep,
          y: y + Math.floor(i / LOOSE_COLUMNS) * rowStep,
        });
      });
      if (band.loose.length > 0) {
        tiers = Math.max(tiers, Math.min(LOOSE_COLUMNS, band.loose.length));
        y += Math.ceil(band.loose.length / LOOSE_COLUMNS) * rowStep;
      }
    }

    const hasMore = band.loose.length > 0;
    if (hasMore) band.moreAt = { x, y };

    band.x = x;
    band.width = tiers * NODE_WIDTH + (tiers - 1) * CARD_GAP_X;
    x += band.width + BAND_GAP;
  });

  // The view opens on the first few runs, whole — headers and "+N more"
  // included. The first run alone answers "how did this start" and leaves the
  // rest of the canvas empty; three answers "and what followed", which is the
  // other half of the question and the reason this is a timeline.
  const focus = bands.slice(0, OPENING_BANDS).flatMap((band) => [
    bandNodeID(band.ref),
    ...band.shown.map((n) => n.id),
    ...(band.moreAt ? [moreNodeID(band.ref)] : []),
  ]);
  return { at, bands, focus };
}

const bandNodeID = (ref: string) => `band:${ref || 'undated'}`;
const moreNodeID = (ref: string) => `more:${ref || 'undated'}`;
const bandRefOfMore = (id: string) => {
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

/** bandSummary is the one line under a run's name: when it ran, and what it
 *  did. The counts are of what the map is showing, so they agree with the
 *  cards underneath even when a filter is on. */
function bandSummary(idx: GraphIndex, band: RunBand): string {
  const counts = { finding: 0, task: 0, recommendation: 0 } as Record<MapNodeKind, number>;
  band.nodes.forEach((n) => { counts[n.kind] += 1; });
  const parts: string[] = [];
  const run = band.ref ? idx.runs.get(band.ref) : undefined;
  if (run?.date) parts.push(new Date(run.date).toLocaleDateString());
  if (counts.finding) parts.push(`${counts.finding} finding${counts.finding === 1 ? '' : 's'}`);
  if (counts.task) parts.push(`${counts.task} thread${counts.task === 1 ? '' : 's'}`);
  if (counts.recommendation) {
    parts.push(`${counts.recommendation} recommendation${counts.recommendation === 1 ? '' : 's'}`);
  }
  return parts.join(' · ');
}

type BandHeaderData = {
  label: string;
  detail: string;
  width: number;
  dated: boolean;
} & Record<string, unknown>;

/** BandHeader names a run and sits above its cards. It is what turns a field
 *  of cards into a sequence someone can read from the left. */
function BandHeader({ data }: { data: BandHeaderData }) {
  return (
    <div
      style={{
        width: data.width,
        height: HEADER_HEIGHT,
        display: 'flex',
        flexDirection: 'column',
        justifyContent: 'center',
        padding: '0 14px',
        borderRadius: 10,
        background: data.dated
          ? 'var(--mantine-color-blue-light)'
          : 'var(--mantine-color-gray-1)',
        border: '1px solid var(--db-border-default)',
      }}
    >
      <Handle type="target" position={Position.Left} style={{ opacity: 0 }} />
      <Group gap={7} wrap="nowrap">
        <IconClockHour4 size={14} style={{ flexShrink: 0, opacity: 0.7 }} />
        <Text size="sm" fw={700} lineClamp={1}>{data.label}</Text>
      </Group>
      {data.detail && (
        <Text size="10px" c="dimmed" lineClamp={1} mt={2}>{data.detail}</Text>
      )}
      <Handle type="source" position={Position.Right} style={{ opacity: 0 }} />
    </div>
  );
}

type MoreCardData = {
  label: string;
  detail: string;
  open: boolean;
} & Record<string, unknown>;

/** MoreCard stands in for the part of a run's work nothing links to yet. It
 *  keeps the band one column wide, which is what lets the whole investigation
 *  be read from left to right. */
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

/** EarlierRuns marks the left edge when the response is a window onto a
 *  longer history. Without it the leftmost band reads as the beginning of the
 *  investigation, which on a mature project it is not. */
function EarlierRuns() {
  return (
    <div
      style={{
        width: NODE_WIDTH - 60,
        height: HEADER_HEIGHT,
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'flex-end',
        paddingRight: 12,
        gap: 6,
      }}
    >
      <Text size="10px" c="dimmed" ta="right" style={{ lineHeight: 1.3 }}>
        Earlier runs
        <br />
        not loaded
      </Text>
      <IconArrowRight size={13} style={{ opacity: 0.45, flexShrink: 0 }} />
      <Handle type="source" position={Position.Right} style={{ opacity: 0 }} />
    </div>
  );
}

const nodeTypes = {
  investigation: InvestigationNode, band: BandHeader, more: MoreCard, earlier: EarlierRuns,
};

/** The edges that carry the story across runs: a finding opened this thread,
 *  this finding answered it, this thread continues that one. They are drawn
 *  stronger than a recommendation's link to the finding behind it, which is
 *  almost always within a single run. */
const NARRATIVE_EDGE: Record<string, boolean> = {
  grew_from: true, answered_by: true, supersedes: true,
};

function layout(
  map: InvestigationMap,
  idx: GraphIndex,
  geometry: ReturnType<typeof place>,
  selected: string | null,
  connected: Set<string>,
  earlierRuns: boolean,
) {
  const { at, bands } = geometry;

  const dated = bands.filter((b) => b.ordinal);
  const marker: Node[] = earlierRuns && dated.length > 0 ? [{
    id: 'earlier-runs',
    type: 'earlier',
    position: { x: dated[0].x - NODE_WIDTH, y: 0 },
    draggable: false,
    selectable: false,
    data: {},
  }] : [];

  const headers: Node[] = bands.map((band) => ({
    id: bandNodeID(band.ref),
    type: 'band',
    position: { x: band.x, y: 0 },
    draggable: false,
    selectable: false,
    data: {
      label: band.ref ? runLabel(idx, band.ref) : 'Not attributed to a run',
      detail: bandSummary(idx, band),
      width: band.width,
      dated: Boolean(band.ordinal),
    } as BandHeaderData,
  }));

  const drawn = new Set<string>();
  bands.forEach((b) => b.shown.forEach((n) => drawn.add(n.id)));
  const nodes: Node<MapNodeData>[] = map.nodes
    .filter((n) => drawn.has(n.id))
    .map((n) => ({
      id: n.id,
      type: 'investigation',
      position: at.get(n.id) ?? { x: 0, y: 0 },
      data: {
        ...n,
        selected: selected === n.id,
        dimmed: Boolean(selected) && !connected.has(n.id),
      },
    }));

  const more: Node[] = bands
    .filter((band) => band.moreAt)
    .map((band) => ({
      id: moreNodeID(band.ref),
      type: 'more',
      position: band.moreAt as { x: number; y: number },
      draggable: false,
      selectable: false,
      data: {
        label: band.hidden.length > 0
          ? `+${band.hidden.length} more from this run`
          : 'Show less',
        detail: band.hidden.length > 0 ? countsOf(band.hidden) : '',
        open: band.hidden.length === 0,
      } as MoreCardData,
    }));

  // The spine: run 1 then run 2 then run 3. Only between runs that are dated,
  // because "then" is a claim about order and the undated band has none.
  const spine: Edge[] = dated.slice(1).map((band, i) => ({
    id: `spine:${dated[i].ref}->${band.ref}`,
    source: bandNodeID(dated[i].ref),
    target: bandNodeID(band.ref),
    label: 'then',
    selectable: false,
    labelShowBg: true,
    labelBgPadding: [5, 2] as [number, number],
    labelBgBorderRadius: 4,
    labelStyle: { fontSize: 10, fill: 'var(--db-text-secondary, #666)' },
    labelBgStyle: { fill: 'var(--mantine-color-body)', fillOpacity: 0.92 },
    markerEnd: { type: MarkerType.ArrowClosed, color: 'var(--mantine-color-blue-4)' },
    style: { stroke: 'var(--mantine-color-blue-4)', strokeWidth: 2 },
  }));
  if (marker.length > 0 && dated.length > 0) {
    spine.unshift({
      id: 'spine:earlier',
      source: 'earlier-runs',
      target: bandNodeID(dated[0].ref),
      selectable: false,
      style: {
        stroke: 'var(--mantine-color-blue-4)',
        strokeWidth: 2,
        strokeDasharray: '4 4',
      },
    });
  }

  const edges: Edge[] = map.edges
    // Only between cards that are actually on screen: a band with its
    // inventory collapsed has no node for a link to land on.
    .filter((e) => drawn.has(e.source) && drawn.has(e.target))
    .map((e) => {
      const lit = !selected || (connected.has(e.source) && connected.has(e.target));
      const story = NARRATIVE_EDGE[e.kind];
      const stroke = !lit
        ? 'var(--mantine-color-gray-3)'
        : (story ? 'var(--mantine-color-grape-5)' : 'var(--mantine-color-gray-5)');
      // Drawn oldest-first, whichever end the payload stores it from, so an
      // arrowhead on this map always means "and then this".
      const back = EDGE_DRAWN_REVERSED[e.kind];
      return {
        id: e.id,
        source: back ? e.target : e.source,
        target: back ? e.source : e.target,
        label: e.label,
        animated: lit && Boolean(selected),
        labelShowBg: true,
        labelBgPadding: [5, 2] as [number, number],
        labelBgBorderRadius: 4,
        labelStyle: { fontSize: 10, fill: 'var(--db-text-secondary, #666)' },
        labelBgStyle: { fill: 'var(--mantine-color-body)', fillOpacity: 0.92 },
        markerEnd: { type: MarkerType.ArrowClosed, color: stroke },
        style: {
          stroke,
          strokeWidth: lit && (selected || story) ? 2 : 1.4,
          opacity: lit ? 1 : 0.3,
        },
      };
    });

  return { nodes: [...marker, ...headers, ...nodes, ...more], edges: [...spine, ...edges] };
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
        position: 'absolute', top: 12, right: 12, bottom: 12, width: 340,
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
        <Text size="xs" c="dimmed">One column per run, oldest first</Text>
      </Group>
      {(['finding', 'task', 'recommendation'] as MapNodeKind[]).map((kind) => (
        <Group key={kind} gap={5} wrap="nowrap">
          <span style={{ color: KIND_COLOR[kind], display: 'flex' }}><KindIcon kind={kind} size={13} /></span>
          <Text size="xs" c="dimmed">{KIND_LABEL[kind]}</Text>
        </Group>
      ))}
    </Group>
  );
}

function Canvas({
  projectId, index, map, earlierRuns,
}: {
  projectId: string; index: GraphIndex; map: InvestigationMap; earlierRuns: boolean;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  // Which runs have had their "+N more" opened. Per band, because opening one
  // run's inventory should not unfold every other run's as well.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const { fitView, setViewport } = useReactFlow();

  // What stays lit when a node is selected: itself and everything one link
  // away. More than that and the highlight stops meaning anything.
  // A selection a filter has since removed is no selection. Keeping it would
  // leave every card that DID match dimmed in favour of one that is no longer
  // on the canvas, with no panel open to explain why.
  const selectedNode = useMemo(
    () => (selected ? map.nodes.find((n) => n.id === selected) ?? null : null),
    [selected, map.nodes],
  );
  const lit = selectedNode ? selected : null;

  const connected = useMemo(() => {
    const set = new Set<string>();
    if (!lit) return set;
    set.add(lit);
    map.edges.forEach((e) => {
      if (e.source === lit) set.add(e.target);
      if (e.target === lit) set.add(e.source);
    });
    return set;
  }, [lit, map.edges]);

  // Where the cards go is decided by the map alone. Selection only changes how
  // they are drawn — so it must not produce new geometry, or the effect below
  // would refit the viewport on every click and snap a reader who had panned
  // to another chain straight back to the first one.
  const geometry = useMemo(() => place(map, expanded), [map, expanded]);
  const laid = useMemo(
    () => layout(map, index, geometry, lit, connected, earlierRuns),
    [map, index, geometry, lit, connected, earlierRuns],
  );
  const [nodes, setNodes, onNodesChange] = useNodesState(laid.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(laid.edges);

  useEffect(() => {
    setNodes(laid.nodes);
    setEdges(laid.edges);
  }, [laid, setNodes, setEdges]);

  // A filter rebuilds the layout from nothing, and the viewport it leaves
  // behind can be looking at empty canvas: the one finding that matched is at
  // the origin while the view is still centred where a map several rows deep
  // used to be. Keyed on the map, not on the layout — selecting a card moves
  // nothing, so it must not move the view. The frame lets React Flow measure
  // the new nodes before they are fitted.
  // The map opens at its top left corner, which is the oldest run's header —
  // where the investigation started. Fitting instead would centre on the
  // middle of whatever is tallest and scroll the run headers off the top,
  // taking the one thing that orients a reader with them.
  //
  // Keyed on the MAP, not on what is currently drawn: opening a run's "+N
  // more" changes the second and not the first, and moving the view then
  // would throw away the pan of whoever opened it — and could push the cards
  // they just asked for off the screen.
  const originKey = useMemo(() => map.nodes.map((n) => n.id).join('|'), [map]);
  useEffect(() => {
    const frame = window.requestAnimationFrame(
      () => setViewport({ x: 36, y: 20, zoom: OPENING_ZOOM }, { duration: 300 }),
    );
    return () => window.cancelAnimationFrame(frame);
  }, [originKey, setViewport]);


  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    // A run header is a label, not a card: clicking one would dim the whole
    // map in favour of a node that has no detail to show.
    if (node.type === 'band') return;
    if (node.type === 'more') {
      const ref = bandRefOfMore(node.id);
      setExpanded((prev) => {
        const next = new Set(prev);
        if (!next.delete(ref)) next.add(ref);
        return next;
      });
      return;
    }
    setSelected((prev) => (prev === node.id ? null : node.id));
  }, []);

  return (
    <div style={{ position: 'relative', height: 620, borderRadius: 8, overflow: 'hidden', border: '1px solid var(--db-border-default)' }}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        onNodesChange={onNodesChange}
        onEdgesChange={onEdgesChange}
        onNodeClick={onNodeClick}
        onPaneClick={() => setSelected(null)}
        nodeTypes={nodeTypes}
        // No `fitView` prop: it fits the whole map, which at a zoom floor
        // meant to keep cards readable can centre on empty space between the
        // rows. The effect above opens on the biggest chain instead; Fit and
        // the minimap are there for the whole picture.
        minZoom={0.1}
        maxZoom={1.6}
        proOptions={{ hideAttribution: false }}
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
        <Controls showInteractive={false} />
        <MiniMap
          pannable zoomable
          nodeColor={(n) => (n.type === 'band'
            ? 'var(--mantine-color-blue-3)'
            : accentFor(n.data as unknown as MapNode))}
          style={{ background: 'var(--mantine-color-body)' }}
        />
      </ReactFlow>

      <Group
        gap="xs"
        style={{ position: 'absolute', top: 12, left: 12, zIndex: 4 }}
      >
        <Tooltip label="Fit the whole map in view" openDelay={300}>
          <Button
            size="xs" variant="default" leftSection={<IconMaximize size={13} />}
            onClick={() => fitView({ duration: 300 })}
          >
            Fit
          </Button>
        </Tooltip>
      </Group>

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
