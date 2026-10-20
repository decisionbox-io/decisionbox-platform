'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import {
  Background, BackgroundVariant, Controls, Handle, MiniMap, Position,
  ReactFlow, ReactFlowProvider, useEdgesState, useNodesState, useReactFlow,
  type Edge, type Node, type NodeProps,
} from '@xyflow/react';
import dagre from '@dagrejs/dagre';
import {
  ActionIcon, Badge, Button, Checkbox, CloseButton, Group, Paper, ScrollArea,
  Select, Stack, Text, TextInput, Tooltip,
} from '@mantine/core';
import {
  IconArrowRight, IconBulb, IconExternalLink, IconFlag3, IconLayoutGrid,
  IconMaximize, IconRoute, IconTargetArrow,
} from '@tabler/icons-react';
import { LedgerGraph } from '@/lib/api';
import {
  GraphIndex, InvestigationMap, KIND_LABEL, MapNode, MapNodeKind, TreeFilters,
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
// @xyflow/react renders and @dagrejs/dagre lays out — both MIT, both bundled
// at build time, so an air-gapped image needs nothing at run time. The layout
// is left-to-right because an investigation reads as a chain, not a hierarchy.

const NODE_WIDTH = 268;
const NODE_HEIGHT = 92;
/** How far apart two unrelated chains sit, and how wide a row of them may run
 *  before the next one wraps underneath. */
const CHAIN_GAP = 64;
const MAX_ROW_WIDTH = 2600;

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

const nodeTypes = { investigation: InvestigationNode };

/** chainsOf groups the map into the separate chains it is really made of —
 *  nodes joined by an edge, in either direction. */
function chainsOf(map: InvestigationMap): string[][] {
  const parent = new Map<string, string>();
  map.nodes.forEach((n) => parent.set(n.id, n.id));

  const find = (id: string): string => {
    let root = id;
    while (parent.get(root) !== root) root = parent.get(root) as string;
    let walk = id;
    while (parent.get(walk) !== root) {
      const next = parent.get(walk) as string;
      parent.set(walk, root);
      walk = next;
    }
    return root;
  };

  map.edges.forEach((e) => {
    if (!parent.has(e.source) || !parent.has(e.target)) return;
    const a = find(e.source);
    const b = find(e.target);
    if (a !== b) parent.set(a, b);
  });

  const chains = new Map<string, string[]>();
  map.nodes.forEach((n) => {
    const root = find(n.id);
    const found = chains.get(root);
    if (found) found.push(n.id);
    else chains.set(root, [n.id]);
  });
  // Biggest first, so the chains worth reading start at the top left and the
  // findings nothing has linked yet pack in behind them.
  return [...chains.values()].sort((a, b) => b.length - a.length);
}

/** place lays each chain out on its own, left to right, then packs the chains
 *  into rows. One dagre pass over the whole map would stack every unlinked
 *  finding into a single column — on a real project that is a 5,000px ribbon
 *  one card wide, which is why the chains are laid out separately. */
function place(map: InvestigationMap): Map<string, { x: number; y: number }> {
  const at = new Map<string, { x: number; y: number }>();
  let rowX = 0;
  let rowY = 0;
  let rowHeight = 0;

  chainsOf(map).forEach((ids) => {
    const member = new Set(ids);
    const g = new dagre.graphlib.Graph();
    g.setDefaultEdgeLabel(() => ({}));
    g.setGraph({ rankdir: 'LR', nodesep: 26, ranksep: 110, marginx: 0, marginy: 0 });

    ids.forEach((id) => g.setNode(id, { width: NODE_WIDTH, height: NODE_HEIGHT }));
    map.edges.forEach((e) => {
      if (member.has(e.source) && member.has(e.target)) g.setEdge(e.source, e.target);
    });
    dagre.layout(g);

    const width = g.graph().width ?? NODE_WIDTH;
    const height = g.graph().height ?? NODE_HEIGHT;
    if (rowX > 0 && rowX + width > MAX_ROW_WIDTH) {
      rowY += rowHeight + CHAIN_GAP;
      rowX = 0;
      rowHeight = 0;
    }

    ids.forEach((id) => {
      const pos = g.node(id);
      at.set(id, {
        x: rowX + (pos?.x ?? NODE_WIDTH / 2) - NODE_WIDTH / 2,
        y: rowY + (pos?.y ?? NODE_HEIGHT / 2) - NODE_HEIGHT / 2,
      });
    });

    rowX += width + CHAIN_GAP;
    rowHeight = Math.max(rowHeight, height);
  });

  return at;
}

/** layout positions the map and dresses the nodes and edges for the canvas.
 *  Left to right, because the thing being drawn is a chain of reasoning. */
function layout(map: InvestigationMap, selected: string | null, connected: Set<string>) {
  const at = place(map);

  const nodes: Node<MapNodeData>[] = map.nodes.map((n) => {
    return {
      id: n.id,
      type: 'investigation',
      position: at.get(n.id) ?? { x: 0, y: 0 },
      data: {
        ...n,
        selected: selected === n.id,
        dimmed: Boolean(selected) && !connected.has(n.id),
      },
    };
  });

  const edges: Edge[] = map.edges.map((e) => {
    const lit = !selected || (connected.has(e.source) && connected.has(e.target));
    return {
      id: e.id,
      source: e.source,
      target: e.target,
      label: e.label,
      animated: lit && Boolean(selected),
      labelShowBg: true,
      labelBgPadding: [5, 2] as [number, number],
      labelBgBorderRadius: 4,
      labelStyle: { fontSize: 10, fill: 'var(--db-text-secondary, #666)' },
      labelBgStyle: { fill: 'var(--mantine-color-body)', fillOpacity: 0.92 },
      style: {
        stroke: lit ? 'var(--mantine-color-gray-5)' : 'var(--mantine-color-gray-3)',
        strokeWidth: lit && selected ? 2 : 1.4,
        opacity: lit ? 1 : 0.35,
      },
    };
  });

  return { nodes, edges };
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
  projectId, index, map,
}: {
  projectId: string; index: GraphIndex; map: InvestigationMap;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  const { fitView } = useReactFlow();

  // What stays lit when a node is selected: itself and everything one link
  // away. More than that and the highlight stops meaning anything.
  const connected = useMemo(() => {
    const set = new Set<string>();
    if (!selected) return set;
    set.add(selected);
    map.edges.forEach((e) => {
      if (e.source === selected) set.add(e.target);
      if (e.target === selected) set.add(e.source);
    });
    return set;
  }, [selected, map.edges]);

  const laid = useMemo(() => layout(map, selected, connected), [map, selected, connected]);
  const [nodes, setNodes, onNodesChange] = useNodesState(laid.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(laid.edges);

  useEffect(() => {
    setNodes(laid.nodes);
    setEdges(laid.edges);
  }, [laid, setNodes, setEdges]);

  const selectedNode = selected ? map.nodes.find((n) => n.id === selected) ?? null : null;

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
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
        fitView
        // Opening on the whole map means opening on cards too small to read.
        // The first view lands legible on the biggest chain, which the layout
        // puts top left; Fit and the minimap are there for the whole picture.
        fitViewOptions={{ minZoom: 0.55, maxZoom: 1 }}
        minZoom={0.1}
        maxZoom={1.6}
        proOptions={{ hideAttribution: false }}
      >
        <Background variant={BackgroundVariant.Dots} gap={18} size={1} />
        <Controls showInteractive={false} />
        <MiniMap
          pannable zoomable
          nodeColor={(n) => accentFor(n.data as unknown as MapNode)}
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
          <Canvas projectId={projectId} index={index} map={map} />
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
