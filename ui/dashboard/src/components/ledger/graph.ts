// Pure helpers over the investigation graph's flat node + edge payload.
//
// Kept free of React so the parts that are actually hard — building the tree,
// not walking a cycle forever, deciding what "changed since the last run"
// means — are unit-testable without rendering anything.

import type {
  GraphEdge, GraphEdgeKind, GraphFinding, GraphInsight, GraphLearning,
  GraphProposal, GraphRecommendation, GraphRun, GraphTask, LedgerGraph,
} from '@/lib/api';

/** How deep the tree expands by default. Below this, nodes are collapsed. */
export const DEFAULT_OPEN_DEPTH = 2;
/** How many roots are rendered before the "show more" control. */
export const ROOTS_PAGE_SIZE = 25;
/** Hard ceiling on the walk, so a malformed payload cannot build forever. */
export const MAX_TREE_DEPTH = 12;

export type TreeNodeKind = 'finding' | 'task' | 'recommendation' | 'learning';

/** One node in the rendered tree. `path` makes a repeated node addressable. */
export interface TreeNode {
  /** The payload node_id. The same node can legitimately appear under two
   *  parents (a finding that answered two threads), so this is not unique in
   *  the tree — `path` is. */
  nodeId: string;
  path: string;
  kind: TreeNodeKind;
  depth: number;
  /** Why this node hangs off its parent, for the connector label. */
  via?: GraphEdgeKind;
  children: TreeNode[];
  /** True when the node is already an ancestor of itself on this path: the
   *  payload contains a cycle, and the walk stops here rather than looping. */
  cyclic?: boolean;
}

/** Everything the tree view needs, indexed. */
export interface GraphIndex {
  runs: Map<string, GraphRun>;
  findings: Map<string, GraphFinding>;
  tasks: Map<string, GraphTask>;
  insights: Map<string, GraphInsight>;
  recommendations: Map<string, GraphRecommendation>;
  learnings: Map<string, GraphLearning>;
  proposals: Map<string, GraphProposal>;
  /** Edges grouped by their `from` node. */
  out: Map<string, GraphEdge[]>;
  /** Edges grouped by their `to` node. */
  in: Map<string, GraphEdge[]>;
  /** Run node_id → 1-based ordinal, oldest run = 1, for "Run 7" labels. */
  runOrdinal: Map<string, number>;
  /** The most recent run, or null on a project with no runs. */
  latestRun: GraphRun | null;
}

function pushTo<K, V>(map: Map<K, V[]>, key: K, value: V) {
  const existing = map.get(key);
  if (existing) existing.push(value);
  else map.set(key, [value]);
}

/** indexGraph builds the lookup maps. Tolerant of a partial payload: a missing
 *  list is treated as empty, because a graph from an older API, or one whose
 *  optional arrays came back absent, must still render. */
export function indexGraph(graph: LedgerGraph | null | undefined): GraphIndex {
  const idx: GraphIndex = {
    runs: new Map(), findings: new Map(), tasks: new Map(), insights: new Map(),
    recommendations: new Map(), learnings: new Map(), proposals: new Map(),
    out: new Map(), in: new Map(), runOrdinal: new Map(), latestRun: null,
  };
  if (!graph) return idx;

  (graph.runs ?? []).forEach((r) => idx.runs.set(r.node_id, r));
  (graph.findings ?? []).forEach((f) => idx.findings.set(f.node_id, f));
  (graph.tasks ?? []).forEach((t) => idx.tasks.set(t.node_id, t));
  (graph.insights ?? []).forEach((i) => idx.insights.set(i.node_id, i));
  (graph.recommendations ?? []).forEach((r) => idx.recommendations.set(r.node_id, r));
  (graph.learnings ?? []).forEach((l) => idx.learnings.set(l.node_id, l));
  (graph.proposals ?? []).forEach((p) => idx.proposals.set(p.node_id, p));

  (graph.edges ?? []).forEach((e) => {
    pushTo(idx.out, e.from, e);
    pushTo(idx.in, e.to, e);
  });

  // Ordinals come from date order, oldest first, so "Run 7" means the seventh
  // run of the investigation rather than the seventh row of the response. A
  // run with no date (a stub for a deleted discovery) gets no ordinal.
  const dated = (graph.runs ?? [])
    .filter((r) => !r.missing && r.date)
    .sort((a, b) => new Date(a.date!).getTime() - new Date(b.date!).getTime());
  dated.forEach((r, i) => idx.runOrdinal.set(r.node_id, i + 1));
  idx.latestRun = dated.length > 0 ? dated[dated.length - 1] : null;

  return idx;
}

/** runLabel is the human name for a run node: "Run 7", or its id when the run
 *  is a stub for something that no longer exists. */
export function runLabel(idx: GraphIndex, nodeId: string): string {
  const ordinal = idx.runOrdinal.get(nodeId);
  if (ordinal) return `Run ${ordinal}`;
  const run = idx.runs.get(nodeId);
  if (run?.run_id) return `Run ${run.run_id.slice(0, 8)}`;
  return 'An earlier run';
}

/** nodeKind reads the kind out of a node_id prefix. */
export function nodeKind(nodeId: string): string {
  const i = nodeId.indexOf(':');
  return i > 0 ? nodeId.slice(0, i) : '';
}

/** nodeRawId is the id part of a node_id — what a drill-through URL needs. */
export function nodeRawId(nodeId: string): string {
  const i = nodeId.indexOf(':');
  return i > 0 ? nodeId.slice(i + 1) : nodeId;
}

/** The discovery id a finding's evidence page lives under: the newest sighting
 *  that has one, which is the run whose SQL a reader most likely wants. */
export function findingEvidenceLink(projectId: string, finding: GraphFinding): string | null {
  const sightings = finding.sightings ?? [];
  for (let i = sightings.length - 1; i >= 0; i -= 1) {
    const s = sightings[i];
    if (s.discovery_id && s.insight_id) {
      return `/projects/${projectId}/discoveries/${s.discovery_id}/insights/${s.insight_id}`;
    }
  }
  return null;
}

/** The evidence page for one insight stub. */
export function insightLink(projectId: string, insight: GraphInsight): string | null {
  if (!insight.discovery_id || insight.missing) return null;
  return `/projects/${projectId}/discoveries/${insight.discovery_id}/insights/${insight.id}`;
}

/** The detail page for one recommendation stub. */
export function recommendationLink(projectId: string, rec: GraphRecommendation): string | null {
  if (!rec.discovery_id) return null;
  return `/projects/${projectId}/discoveries/${rec.discovery_id}/recommendations/${rec.id}`;
}

/** Filters the Threads view applies. */
export interface TreeFilters {
  area?: string;
  severity?: string;
  status?: string;
  from?: string; // ISO date, inclusive
  to?: string;   // ISO date, inclusive
  /** Only what moved in the most recent run — the most useful filter once a
   *  project has a long history. */
  changedSinceLastRun?: boolean;
}

/** findingChangedInRun reports whether the run moved this finding: it was seen
 *  in it, or its status changed in it. */
export function findingChangedInRun(finding: GraphFinding, runId: string | undefined): boolean {
  if (!runId) return false;
  if ((finding.sightings ?? []).some((s) => s.run_id === runId)) return true;
  return (finding.status_history ?? []).some((h) => h.run_id === runId);
}

function withinDates(iso: string | undefined, filters: TreeFilters): boolean {
  if (!iso) return !filters.from && !filters.to;
  const at = new Date(iso).getTime();
  if (filters.from && at < new Date(filters.from).getTime()) return false;
  // `to` is inclusive of the whole day the user picked.
  if (filters.to && at > new Date(filters.to).getTime() + 24 * 60 * 60 * 1000 - 1) return false;
  return true;
}

/** matchesFilters tests a finding against the filter bar. A node that fails is
 *  not shown as a root, but can still appear as somebody's child — hiding a
 *  finding that a visible thread points at would break the chain the view
 *  exists to show. */
export function matchesFilters(
  finding: GraphFinding, filters: TreeFilters, latestRunId: string | undefined,
): boolean {
  if (filters.area && finding.area !== filters.area) return false;
  if (filters.severity && finding.severity !== filters.severity) return false;
  if (filters.status && finding.status !== filters.status) return false;
  if (!withinDates(finding.last_seen, filters)) return false;
  if (filters.changedSinceLastRun && !findingChangedInRun(finding, latestRunId)) return false;
  return true;
}

/** childrenOf returns the nodes that hang under a node, with the edge that put
 *  them there.
 *
 *  Only four edge kinds make one node a child of another: a thread hangs under
 *  the finding it grew out of, a follow-up under the thread it continues, a
 *  finding that answered a thread under that thread, and a recommendation under
 *  the finding whose insight it was made from. Everything else — a run's
 *  `found`, `opened`, `closed`, `pursued`, an `in_run` — is an attribute of a
 *  node rather than a level of the tree: a tree rooted in runs is the Story
 *  view, and this one is rooted in what was found.
 *
 *  `grew_from` and `supersedes` point child → parent, so they are read
 *  backwards. */
function childrenOf(idx: GraphIndex, nodeId: string): Array<{ nodeId: string; kind: TreeNodeKind; via: GraphEdgeKind }> {
  const out: Array<{ nodeId: string; kind: TreeNodeKind; via: GraphEdgeKind }> = [];

  // Inbound `grew_from` / `supersedes`: the thread names its parent.
  (idx.in.get(nodeId) ?? []).forEach((e) => {
    if (e.kind === 'grew_from' || e.kind === 'supersedes') {
      out.push({ nodeId: e.from, kind: 'task', via: e.kind });
    }
  });
  // Outbound `answered_by`: a thread names the findings that answered it.
  (idx.out.get(nodeId) ?? []).forEach((e) => {
    if (e.kind === 'answered_by') out.push({ nodeId: e.to, kind: 'finding', via: e.kind });
  });
  // A recommendation reaches a finding through the insight they share.
  if (nodeKind(nodeId) === 'finding') {
    const insightIds = (idx.out.get(nodeId) ?? [])
      .filter((e) => e.kind === 'evidenced_by')
      .map((e) => e.to);
    const seen = new Set<string>();
    insightIds.forEach((insightId) => {
      (idx.in.get(insightId) ?? []).forEach((e) => {
        if (e.kind === 'recommends_on' && !seen.has(e.from)) {
          seen.add(e.from);
          out.push({ nodeId: e.from, kind: 'recommendation', via: 'recommends_on' });
        }
      });
    });
  }
  return out;
}

/** buildTree assembles the roots and their descendants.
 *
 *  Roots are the findings and threads nothing else claims as a child: a finding
 *  no thread grew out of upward, and a thread with no origin finding and no
 *  parent task. That is the honest definition — a chain's root is whatever has
 *  no parent — and it means a thread whose origin finding fell outside the
 *  window still shows up rather than vanishing with its parent.
 *
 *  The walk guards against a cycle by tracking the ancestors on the current
 *  path, and stops at MAX_TREE_DEPTH regardless, so a payload that somehow
 *  describes an infinite structure renders finite. */
export function buildTree(
  idx: GraphIndex, filters: TreeFilters = {},
): TreeNode[] {
  const latestRunId = idx.latestRun?.run_id;

  const claimedAsChild = new Set<string>();
  idx.findings.forEach((_f, nodeId) => {
    (idx.in.get(nodeId) ?? []).forEach((e) => {
      if (e.kind === 'answered_by') claimedAsChild.add(nodeId);
    });
  });
  idx.tasks.forEach((_t, nodeId) => {
    (idx.out.get(nodeId) ?? []).forEach((e) => {
      if ((e.kind === 'grew_from' && idx.findings.has(e.to)) ||
          (e.kind === 'supersedes' && idx.tasks.has(e.to))) {
        claimedAsChild.add(nodeId);
      }
    });
  });

  const rootIds: Array<{ nodeId: string; kind: TreeNodeKind }> = [];
  idx.findings.forEach((f, nodeId) => {
    if (claimedAsChild.has(nodeId)) return;
    if (!matchesFilters(f, filters, latestRunId)) return;
    rootIds.push({ nodeId, kind: 'finding' });
  });
  idx.tasks.forEach((_t, nodeId) => {
    if (claimedAsChild.has(nodeId)) return;
    // A thread root is only interesting when no finding root already leads to
    // it; a bare thread with no provenance is still shown, since "we are
    // looking into this and do not know why" is real state.
    rootIds.push({ nodeId, kind: 'task' });
  });

  // Findings first, by severity then recency, so the tree opens on what
  // matters; threads after them.
  const severityOrder: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };
  rootIds.sort((a, b) => {
    if (a.kind !== b.kind) return a.kind === 'finding' ? -1 : 1;
    if (a.kind === 'finding') {
      const fa = idx.findings.get(a.nodeId)!;
      const fb = idx.findings.get(b.nodeId)!;
      const sa = severityOrder[fa.severity ?? ''] ?? 5;
      const sb = severityOrder[fb.severity ?? ''] ?? 5;
      if (sa !== sb) return sa - sb;
      return new Date(fb.last_seen ?? 0).getTime() - new Date(fa.last_seen ?? 0).getTime();
    }
    return a.nodeId.localeCompare(b.nodeId);
  });

  const walk = (
    nodeId: string, kind: TreeNodeKind, depth: number, path: string, ancestors: Set<string>,
  ): TreeNode => {
    const node: TreeNode = { nodeId, path, kind, depth, children: [] };
    if (ancestors.has(nodeId)) {
      node.cyclic = true;
      return node;
    }
    if (depth >= MAX_TREE_DEPTH) return node;
    const nextAncestors = new Set(ancestors);
    nextAncestors.add(nodeId);
    childrenOf(idx, nodeId).forEach((child, i) => {
      const built = walk(
        child.nodeId, child.kind, depth + 1, `${path}/${i}:${child.nodeId}`, nextAncestors,
      );
      built.via = child.via;
      node.children.push(built);
    });
    return node;
  };

  return rootIds.map((root) => walk(root.nodeId, root.kind, 0, root.nodeId, new Set()));
}

/** countNodes totals a subtree, for the "N below" badge on a collapsed node. */
export function countNodes(node: TreeNode): number {
  return node.children.reduce((sum, c) => sum + countNodes(c), 1);
}

/** areasOf / statusesOf / severitiesOf populate the filter dropdowns from the
 *  payload, so a project only ever sees values it actually has. */
export function areasOf(idx: GraphIndex): string[] {
  const set = new Set<string>();
  idx.findings.forEach((f) => { if (f.area) set.add(f.area); });
  return [...set].sort();
}

export function statusesOf(idx: GraphIndex): string[] {
  const set = new Set<string>();
  idx.findings.forEach((f) => { if (f.status) set.add(f.status); });
  return [...set].sort();
}

export function severitiesOf(idx: GraphIndex): string[] {
  const order = ['critical', 'high', 'medium', 'low', 'info'];
  const set = new Set<string>();
  idx.findings.forEach((f) => { if (f.severity) set.add(f.severity); });
  return order.filter((s) => set.has(s));
}

/** One run's story, as the Story timeline reads it out. */
export interface RunStory {
  run: GraphRun;
  ordinal: number;
  newFindings: GraphFinding[];
  seenAgain: GraphFinding[];
  openedTasks: GraphTask[];
  closedTasks: GraphTask[];
  pursuedTasks: GraphTask[];
  learnings: GraphLearning[];
  proposals: GraphProposal[];
  highCount: number;
}

/** buildStories turns the graph into one card per run, newest first. This is
 *  the default view: plain language, no graph, and every noun a link. */
export function buildStories(idx: GraphIndex): RunStory[] {
  const stories: RunStory[] = [];
  idx.runs.forEach((run, nodeId) => {
    if (run.missing) return;
    const story: RunStory = {
      run,
      ordinal: idx.runOrdinal.get(nodeId) ?? 0,
      newFindings: [], seenAgain: [], openedTasks: [], closedTasks: [],
      pursuedTasks: [], learnings: [], proposals: [], highCount: 0,
    };
    (idx.out.get(nodeId) ?? []).forEach((e) => {
      switch (e.kind) {
        case 'found': {
          const f = idx.findings.get(e.to);
          if (f) story.newFindings.push(f);
          break;
        }
        case 'seen_in': {
          const f = idx.findings.get(e.to);
          if (f) story.seenAgain.push(f);
          break;
        }
        case 'opened': {
          const t = idx.tasks.get(e.to);
          if (t) story.openedTasks.push(t);
          break;
        }
        case 'closed': {
          const t = idx.tasks.get(e.to);
          if (t) story.closedTasks.push(t);
          break;
        }
        case 'pursued': {
          const t = idx.tasks.get(e.to);
          if (t) story.pursuedTasks.push(t);
          break;
        }
        case 'learned': {
          const l = idx.learnings.get(e.to);
          if (l) story.learnings.push(l);
          break;
        }
        case 'proposed': {
          const p = idx.proposals.get(e.to);
          if (p) story.proposals.push(p);
          break;
        }
        default:
          break;
      }
    });
    story.highCount = story.newFindings.filter(
      (f) => f.severity === 'high' || f.severity === 'critical',
    ).length;
    stories.push(story);
  });
  stories.sort((a, b) => {
    const da = a.run.date ? new Date(a.run.date).getTime() : 0;
    const db = b.run.date ? new Date(b.run.date).getTime() : 0;
    if (da !== db) return db - da;
    return b.ordinal - a.ordinal;
  });
  return stories;
}

/** statusTrail renders a finding's status history as one scannable line:
 *  "confirmed (Run 2) → changed (Run 4, 'magnitude up 2.1x')". This is the
 *  thing the page could not show at all before. */
export function statusTrail(idx: GraphIndex, finding: GraphFinding): string[] {
  return (finding.status_history ?? []).map((h) => {
    const where = h.run_id ? runLabel(idx, `run:${h.run_id}`) : 'an earlier run';
    const why = h.reason ? `, “${h.reason}”` : '';
    return `${h.status} (${where}${why})`;
  });
}
