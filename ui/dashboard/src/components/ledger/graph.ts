// Pure helpers over the investigation graph's flat node + edge payload.
//
// Kept free of React so the parts that are actually hard — building the tree,
// not walking a cycle forever, deciding what "changed since the last run"
// means — are unit-testable without rendering anything.

import type {
  FindingSighting, GraphEdge, GraphEdgeKind, GraphFinding, GraphInsight,
  GraphLearning, GraphProposal, GraphRecommendation, GraphRun, GraphTask,
  LedgerGraph,
} from '@/lib/api';

/** How many nodes the map draws before it stops and says so. A canvas handles
 *  a few hundred comfortably; a 500-finding project with every thread and
 *  recommendation hanging off it is past what a reader can take in, and the
 *  filters are the way through. */
export const MAX_MAP_NODES = 400;

export type MapNodeKind = 'finding' | 'task' | 'recommendation';

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
  /** Discovery id → the node_id of the run that produced it. */
  runRefByDiscovery: Map<string, string>;
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
    out: new Map(), in: new Map(), runOrdinal: new Map(),
    runRefByDiscovery: new Map(), latestRun: null,
  };
  if (!graph) return idx;

  (graph.runs ?? []).forEach((r) => {
    idx.runs.set(r.node_id, r);
    if (r.discovery_id) idx.runRefByDiscovery.set(r.discovery_id, r.node_id);
  });
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

  // "Run 7" has to mean the seventh run of the investigation. The server
  // counts over the whole history for exactly that reason, because the
  // response carries a window: numbering the rows here would call the twelfth
  // run "run 1" and renumber it the next time the window moved. The row order
  // is the fallback for an API that predates the field, where it is at least
  // the best available answer. A run with no date gets no ordinal either way.
  const dated = (graph.runs ?? [])
    .filter((r) => !r.missing && r.date)
    .sort((a, b) => new Date(a.date!).getTime() - new Date(b.date!).getTime());
  dated.forEach((r, i) => idx.runOrdinal.set(r.node_id, r.ordinal ?? i + 1));
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

/** runRefForDiscovery resolves a discovery id to the run node that covers it.
 *
 *  A run is named by its run id where one survives and by its discovery id
 *  otherwise, and only the server knows which — it holds the pairing. Insights
 *  and recommendations carry a discovery id alone, so this map is how the
 *  client gets from one to the run node the tree actually drew. Guessing the
 *  prefix instead produces a key that matches nothing, which is how every
 *  evidence link ended up labelled "an earlier run". */
export function runRefForDiscovery(idx: GraphIndex, discoveryID: string | undefined): string {
  if (!discoveryID) return '';
  return idx.runRefByDiscovery.get(discoveryID) ?? '';
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

/** Whether an insight id leads anywhere: present in the response, and not a
 *  stub standing in for something that has been deleted. A link to a deleted
 *  insight opens a not-found page, which is worse than no link. */
function evidenceResolves(idx: GraphIndex, insightId: string | undefined): boolean {
  if (!insightId) return false;
  const stub = idx.insights.get(`insight:${insightId}`);
  return Boolean(stub) && !stub!.missing;
}

/** The discovery id a finding's evidence page lives under: the newest sighting
 *  that names evidence the response carries, which is the run whose SQL a
 *  reader most likely wants. */
export function findingEvidenceLink(
  projectId: string, finding: GraphFinding, idx: GraphIndex,
): string | null {
  const sightings = finding.sightings ?? [];
  for (let i = sightings.length - 1; i >= 0; i -= 1) {
    const s = sightings[i];
    if (s.discovery_id && evidenceResolves(idx, s.insight_id)) {
      return `/projects/${projectId}/discoveries/${s.discovery_id}/insights/${s.insight_id}`;
    }
  }
  return null;
}

/** The insight one sighting is evidence for.
 *
 *  A sighting recorded by a run names its own insight. A sighting recovered by
 *  the backfill names only the run it came from — so the insight it meant is
 *  the finding's evidence FROM THAT SAME RUN, and only when exactly one piece
 *  of evidence came from it. Two from one run cannot be told apart, and
 *  evidence from any other run is a different run's SQL. */
function sightingInsight(idx: GraphIndex, seen: RunSighting): GraphInsight | null {
  const discoveryId = seen.sighting?.discovery_id || seen.runDiscoveryId;
  if (!discoveryId) return null;
  const namedId = seen.sighting?.insight_id;
  if (namedId) {
    const named = idx.insights.get(`insight:${namedId}`);
    return named && !named.missing ? named : null;
  }
  const sameRun = (seen.finding.source_insight_ids ?? [])
    .map((id) => idx.insights.get(`insight:${id}`))
    .filter((stub): stub is GraphInsight =>
      stub !== undefined && !stub.missing && stub.discovery_id === discoveryId);
  return sameRun.length === 1 ? sameRun[0] : null;
}

/** The evidence page for a finding AS ONE RUN SAW IT: the link opens the SQL
 *  that run wrote rather than whatever the latest run happened to produce. */
export function sightingEvidenceLink(
  projectId: string, seen: RunSighting, idx: GraphIndex,
): string | null {
  // Never a fallback to the finding's newest evidence, in any branch — not
  // even for a run whose sighting is missing entirely, which is what a
  // clipped trail and a legacy backfill both leave behind. A historical card
  // that borrowed it would present SQL that run never wrote as the thing it
  // found. A card with no link is honest; a card with the wrong link is not.
  const insight = sightingInsight(idx, seen);
  if (!insight) return null;
  return `/projects/${projectId}/discoveries/${insight.discovery_id}/insights/${insight.id}`;
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
export function findingChangedInRun(
  finding: GraphFinding, runId: string | undefined, discoveryId?: string,
): boolean {
  // Either id. A run whose document has aged out is drawn as a `disc:` node
  // and has no run id at all, and matching only on that would reject every
  // finding while the filter still looked available.
  const matches = (run?: string, discovery?: string) =>
    (Boolean(runId) && run === runId) || (Boolean(discoveryId) && discovery === discoveryId);
  if (!runId && !discoveryId) return false;
  if ((finding.sightings ?? []).some((s) => matches(s.run_id, s.discovery_id))) return true;
  return (finding.status_history ?? []).some((h) => matches(h.run_id, h.discovery_id));
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
  finding: GraphFinding, filters: TreeFilters,
  latestRunId: string | undefined, latestDiscoveryId?: string,
): boolean {
  if (filters.area && finding.area !== filters.area) return false;
  if (filters.severity && finding.severity !== filters.severity) return false;
  if (filters.status && finding.status !== filters.status) return false;
  if (!withinDates(finding.last_seen, filters)) return false;
  if (filters.changedSinceLastRun
    && !findingChangedInRun(finding, latestRunId, latestDiscoveryId)) return false;
  return true;
}

/** One node of the investigation map. */
export interface MapNode {
  id: string; // the payload node_id
  kind: MapNodeKind;
  title: string;
  severity?: string; // findings only
  status?: string;
  /** The one-line subtitle: area, how often it was seen, which runs. */
  meta?: string;
  archived?: boolean;
  missing?: boolean;
  priority?: number; // recommendations only
  /** The run whose work this was — the row it sits in on the map. Empty
   *  when nothing attributes it to a run the response can date. */
  runRef?: string;
  runOrdinal?: number;
  /** Threads only: the later run that took this thread up — the one that
   *  closed it, or else the latest one that had it in front of it — and what
   *  came of it there. Unset when no later run did. */
  workedIn?: string;
  workedInOrdinal?: number;
  outcome?: ThreadOutcome;
  /** One line saying what happened to the thread after it was opened. */
  workedMeta?: string;
}

/** What became of a thread in the run that took it up. */
export type ThreadOutcome = 'answered' | 'closed' | 'dropped' | 'pursued';

/** One link of the investigation map, with the words a reader needs. */
export interface MapEdge {
  id: string;
  source: string;
  target: string;
  kind: GraphEdgeKind;
  label: string;
}

export interface InvestigationMap {
  nodes: MapNode[];
  edges: MapEdge[];
  /** True when the node cap cut the map short. */
  truncated: boolean;
  /** How many nodes the filters admitted, before the cap. */
  totalNodes: number;
}

/** The relationship words, in the direction the map draws them. */
/** The label each link carries, worded to read ALONG the arrow as it is
 *  drawn: source, label, target. */
const EDGE_LABEL: Partial<Record<GraphEdgeKind, string>> = {
  grew_from: 'Opened',
  supersedes: 'Continued by',
  answered_by: 'Answered by',
  recommends_on: 'Drove',
};

/** The links the payload stores pointing BACKWARDS in time: a thread names
 *  the finding it grew out of, a recommendation names the finding it is built
 *  on, a thread names the older thread it continues. That is the right way to
 *  store them — the later thing is the one that knows — but drawing them that
 *  way puts the arrowhead on the earlier card, so a map laid out oldest-left
 *  has arrows running back against its own timeline. They are turned around
 *  to be drawn, and their labels read in that direction.
 *
 *  `answered_by` is not here: the thread comes first and the finding that
 *  answered it comes later, so stored order is already reading order. */
export const EDGE_DRAWN_REVERSED: Partial<Record<GraphEdgeKind, boolean>> = {
  grew_from: true,
  supersedes: true,
  recommends_on: true,
};

/** Human labels for a node kind, for the panel and the legend. */
export const KIND_LABEL: Record<MapNodeKind, string> = {
  finding: 'Finding',
  task: 'Thread',
  recommendation: 'Recommendation',
};

/** Sentence case for a stored lower-case token: "next_task" → "Next task". */
export function humanise(token: string | undefined): string {
  if (!token) return '';
  const spaced = token.replace(/_/g, ' ');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/** The runs a finding was seen in, as "2, 4, 7". Empty when none resolve. */
export function seenInRuns(idx: GraphIndex, finding: GraphFinding): string {
  const ordinals = (finding.sightings ?? [])
    .map((s) => idx.runOrdinal.get(`run:${s.run_id}`)
      ?? idx.runOrdinal.get(runRefForDiscovery(idx, s.discovery_id)))
    .filter((n): n is number => Boolean(n));
  return [...new Set(ordinals)].sort((a, b) => a - b).join(', ');
}

function findingMeta(idx: GraphIndex, f: GraphFinding): string {
  const parts: string[] = [];
  if (f.area) parts.push(humanise(f.area));
  if (f.seen_count > 1) {
    const runs = seenInRuns(idx, f);
    parts.push(runs ? `Seen ${f.seen_count}× in runs ${runs}` : `Seen ${f.seen_count}×`);
  }
  return parts.join(' · ');
}

function taskMeta(idx: GraphIndex, t: GraphTask): string {
  const parts: string[] = [humanise(t.kind)];
  const opened = idx.runOrdinal.get(`run:${t.created_run_id}`);
  const closed = idx.runOrdinal.get(`run:${t.closed_run_id}`);
  if (opened) parts.push(`Opened in run ${opened}`);
  if (closed) parts.push(`Closed in run ${closed}`);
  return parts.filter(Boolean).join(' · ');
}

/** mapNeighbours are the nodes one link away that the map draws a link to. */
function mapNeighbours(idx: GraphIndex, nodeId: string): string[] {
  const out: string[] = [];
  (idx.out.get(nodeId) ?? []).forEach((e) => {
    if (EDGE_LABEL[e.kind]) out.push(e.to);
  });
  (idx.in.get(nodeId) ?? []).forEach((e) => {
    if (EDGE_LABEL[e.kind]) out.push(e.from);
  });
  // A recommendation and a finding reach each other through the insight they
  // share. Both directions, because this is what decides whether two nodes are
  // on the same chain: one-way, a recommendation looks like the end of a chain
  // that in fact continues through it to another finding.
  if (nodeKind(nodeId) === 'finding') {
    (idx.out.get(nodeId) ?? []).forEach((e) => {
      if (e.kind !== 'evidenced_by') return;
      (idx.in.get(e.to) ?? []).forEach((r) => {
        if (r.kind === 'recommends_on') out.push(r.from);
      });
    });
  }
  if (nodeKind(nodeId) === 'rec') {
    (idx.out.get(nodeId) ?? []).forEach((e) => {
      if (e.kind !== 'recommends_on') return;
      (idx.in.get(e.to) ?? []).forEach((f) => {
        if (f.kind === 'evidenced_by') out.push(f.from);
      });
    });
  }
  return out;
}

/** runRefFor resolves whichever of a run id and a discovery id the response
 *  can actually place in time. A run whose document aged out is a `disc:`
 *  node, so the two ids have to be tried rather than assumed. */
function runRefFor(idx: GraphIndex, runID?: string, discoveryID?: string): string {
  if (runID && idx.runOrdinal.has(`run:${runID}`)) return `run:${runID}`;
  const byDiscovery = runRefForDiscovery(idx, discoveryID);
  if (byDiscovery && idx.runOrdinal.has(byDiscovery)) return byDiscovery;
  return byDiscovery || (runID ? `run:${runID}` : '');
}

/** producedIn is the run whose work a node was — where it belongs on a map
 *  laid out in time.
 *
 *  For a finding that is the run that FOUND it, not the latest run to see it
 *  again: the map records when work happened, and a finding carried forward
 *  for six runs was still a thing run 1 did. Seeing it again is an edge, not
 *  a relocation. */
export function producedIn(idx: GraphIndex, nodeId: string): string {
  const finding = idx.findings.get(nodeId);
  if (finding) {
    // Only when the trail reaches the beginning. A trail shorter than the
    // counter has lost its oldest entries — the backfill writes a single
    // sighting for a finding seen ten times, and the writer caps a trail at
    // fifty — so its earliest surviving sighting is NOT the run that found
    // it, and filing the card under that run would credit it with work it did
    // not do. Unattributed is the honest answer, and there is a column that
    // says so.
    const trail = finding.sightings ?? [];
    if (trail.length < finding.seen_count) return '';
    // Sightings are append-only and oldest-first, so the first one that can
    // be placed is the run that found it.
    for (const s of trail) {
      const ref = runRefFor(idx, s.run_id, s.discovery_id);
      if (ref) return ref;
    }
    return '';
  }
  const task = idx.tasks.get(nodeId);
  if (task) return runRefFor(idx, task.created_run_id, task.created_discovery_id);
  const rec = idx.recommendations.get(nodeId);
  if (rec) return runRefForDiscovery(idx, rec.discovery_id);
  return '';
}

/** threadWorkedIn is the run that took a thread up after the one that opened
 *  it, and what came of it there.
 *
 *  The run that closed it if one did, because that is where its answer is;
 *  otherwise the LATEST run that had it in front of it, because that is the
 *  last thing anyone did with it. Only a run after the opening one counts — a
 *  thread is opened at the end of a run, so the run that opened it cannot also
 *  have pursued it, and a record saying so places the thread nowhere useful.
 *
 *  Both the run edges and the thread's own fields are read: the edges are what
 *  the server resolved, and the fields are what a payload without those edges
 *  still carries. */
export function threadWorkedIn(
  idx: GraphIndex, nodeId: string,
): { ref: string; outcome: ThreadOutcome; pursuedIn: number[] } | null {
  const task = idx.tasks.get(nodeId);
  if (!task) return null;
  const openedAt = idx.runOrdinal.get(producedIn(idx, nodeId)) ?? 0;
  const later = (ref: string) => {
    const at = idx.runOrdinal.get(ref);
    return at !== undefined && at > openedAt ? at : 0;
  };
  const inbound = idx.in.get(nodeId) ?? [];

  const pursued = new Map<string, number>();
  [
    ...inbound.filter((e) => e.kind === 'pursued').map((e) => e.from),
    ...(task.pursued_in_run_ids ?? []).map((id) => runRefFor(idx, id)),
  ].forEach((ref) => {
    const at = later(ref);
    if (at) pursued.set(ref, at);
  });
  const pursuedIn = [...new Set(pursued.values())].sort((a, b) => a - b);

  const closedBy = [
    ...inbound.filter((e) => e.kind === 'closed').map((e) => e.from),
    ...(task.closed_run_id || task.closed_discovery_id
      ? [runRefFor(idx, task.closed_run_id, task.closed_discovery_id)]
      : []),
  ].find((ref) => later(ref));
  if (closedBy) {
    const answered = (task.resolved_by_finding_ids ?? []).length > 0
      || (idx.out.get(nodeId) ?? []).some((e) => e.kind === 'answered_by');
    const outcome: ThreadOutcome = task.status === 'dropped'
      ? 'dropped'
      : (answered ? 'answered' : 'closed');
    return { ref: closedBy, outcome, pursuedIn: pursuedIn.filter((at) => at < later(closedBy)) };
  }

  let latest = '';
  pursued.forEach((at, ref) => {
    if (!latest || at > (pursued.get(latest) as number)) latest = ref;
  });
  return latest ? { ref: latest, outcome: 'pursued', pursuedIn } : null;
}

const OUTCOME_WORDS: Record<ThreadOutcome, string> = {
  answered: 'Answered', closed: 'Closed', dropped: 'Dropped', pursued: 'Pursued',
};

/** workedMeta says in one line what became of a thread after it was opened:
 *  "Answered in run 5 · pursued in runs 3, 4", or "Pursued in runs 3, 4 ·
 *  still open". */
function workedMeta(
  idx: GraphIndex, worked: { ref: string; outcome: ThreadOutcome; pursuedIn: number[] },
): string {
  const at = idx.runOrdinal.get(worked.ref);
  const runs = (list: number[]) => `run${list.length === 1 ? '' : 's'} ${list.join(', ')}`;
  if (worked.outcome === 'pursued') return `Pursued in ${runs(worked.pursuedIn)} · still open`;
  const parts = [`${OUTCOME_WORDS[worked.outcome]} in run ${at}`];
  if (worked.pursuedIn.length > 0) parts.push(`pursued in ${runs(worked.pursuedIn)}`);
  return parts.join(' · ');
}

/** buildMap projects the payload onto the nodes and links the canvas draws.
 *
 *  No recursion and no tree. A graph is what the data is: a cycle is just a
 *  cycle and needs no guard, and a node reachable two ways is one node with
 *  two links rather than a subtree drawn twice.
 *
 *  The filters decide which FINDINGS are in; anything a drawable link reaches
 *  from them comes with them, so a chain is never shown with its middle
 *  missing. */
/** Whether any filter narrows the map. An all-defaults filter set is the
 *  whole investigation, which is a different question from a filtered one. */
function narrowed(filters: TreeFilters): boolean {
  return Boolean(
    filters.area || filters.severity || filters.status
    || filters.from || filters.to || filters.changedSinceLastRun,
  );
}

/** withinBudget keeps as much of the map as MAX_MAP_NODES allows, a chain at a
 *  time, so what is drawn is drawn whole. Chains are taken in the order of
 *  their best node, and one too big to fit alongside what is already taken is
 *  passed over for the next that fits — except when nothing has been taken at
 *  all, where a chain larger than the whole budget is clipped rather than
 *  leaving an empty map. */
function withinBudget(
  idx: GraphIndex, candidates: MapNode[], rank: (n: MapNode) => number,
  seeds: Set<string>,
): MapNode[] {
  if (candidates.length <= MAX_MAP_NODES) return candidates;

  const order = new Map(candidates.map((n, i) => [n.id, i]));
  const chain = new Map<string, string[]>();
  const chainOf = new Map<string, string>();

  candidates.forEach((n) => {
    if (chainOf.has(n.id)) return;
    const members: string[] = [];
    const queue = [n.id];
    chainOf.set(n.id, n.id);
    while (queue.length > 0) {
      const id = queue.shift() as string;
      members.push(id);
      mapNeighbours(idx, id).forEach((next) => {
        if (!order.has(next) || chainOf.has(next)) return;
        chainOf.set(next, n.id);
        queue.push(next);
      });
    }
    members.sort((a, b) => (order.get(a) as number) - (order.get(b) as number));
    chain.set(n.id, members);
  });

  const byId = new Map(candidates.map((n) => [n.id, n]));
  const roots = [...chain.keys()].sort((a, b) => {
    const ma = chain.get(a) as string[];
    const mb = chain.get(b) as string[];
    const ra = Math.min(...ma.map((id) => rank(byId.get(id) as MapNode)));
    const rb = Math.min(...mb.map((id) => rank(byId.get(id) as MapNode)));
    if (ra !== rb) return ra - rb;
    if (ma.length !== mb.length) return mb.length - ma.length;
    return (order.get(a) as number) - (order.get(b) as number);
  });

  const kept: MapNode[] = [];
  roots.forEach((root) => {
    const members = chain.get(root) as string[];
    if (kept.length === 0 && members.length > MAX_MAP_NODES) {
      // One chain too big for the whole budget. Taking its best-ranked
      // members would take findings only — they outrank everything — and
      // leave a map with no links in it. Walking out keeps what is kept
      // connected.
      //
      // From a card that MATCHED, not from the chain's best-ranked one:
      // under a filter the match can sit at the far end of a chain, and
      // starting at the other end would clip away the only thing the reader
      // asked for and leave narrowing the filter changing nothing.
      const from = members.find((id) => seeds.has(id)) ?? members[0];
      reachableFrom(idx, from, new Set(members), MAX_MAP_NODES)
        .forEach((id) => kept.push(byId.get(id) as MapNode));
      return;
    }
    if (kept.length + members.length > MAX_MAP_NODES) return;
    members.forEach((id) => kept.push(byId.get(id) as MapNode));
  });
  return kept;
}

/** reachableFrom walks out from one node, nearest first, and returns at most
 *  `budget` ids — a connected piece of a chain rather than a ranked slice of
 *  one, so whatever is drawn still has its links. */
function reachableFrom(
  idx: GraphIndex, start: string, within: Set<string>, budget: number,
): string[] {
  const taken: string[] = [];
  const seen = new Set<string>([start]);
  const queue = [start];
  while (queue.length > 0 && taken.length < budget) {
    const id = queue.shift() as string;
    taken.push(id);
    mapNeighbours(idx, id).forEach((next) => {
      if (!within.has(next) || seen.has(next)) return;
      seen.add(next);
      queue.push(next);
    });
  }
  return taken;
}

export function buildMap(idx: GraphIndex, filters: TreeFilters = {}): InvestigationMap {
  const latestRunId = idx.latestRun?.run_id;
  const latestDiscoveryId = idx.latestRun?.discovery_id;

  const wanted = new Set<string>();
  idx.findings.forEach((f, nodeId) => {
    if (matchesFilters(f, filters, latestRunId, latestDiscoveryId)) wanted.add(nodeId);
  });
  // A thread and a recommendation are part of the investigation whether or
  // not a finding links to them, and on any project that predates this
  // provenance none of them do — so seeding only from findings draws a map
  // with no threads on it at all, and loses a recommendation whose insight
  // the backfill could not match to a ledger finding. Under a filter the map
  // answers a question about findings, and what nothing matching reaches is
  // not part of that answer.
  if (!narrowed(filters)) {
    idx.tasks.forEach((_, nodeId) => wanted.add(nodeId));
    idx.recommendations.forEach((_, nodeId) => wanted.add(nodeId));
  }

  // What the filters actually admitted, before the walk below pulls in
  // everything they reach. The cap needs to know which cards the reader asked
  // for, as opposed to which came along with them.
  const seeds = new Set(wanted);

  const queue = [...wanted];
  while (queue.length > 0) {
    const nodeId = queue.shift()!;
    mapNeighbours(idx, nodeId).forEach((next) => {
      const kind = nodeKind(next);
      if (kind !== 'finding' && kind !== 'task' && kind !== 'rec') return;
      if (wanted.has(next)) return;
      wanted.add(next);
      queue.push(next);
    });
  }

  // Most important first, so the cap keeps those.
  const severityRank: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3, info: 4 };
  const candidates: MapNode[] = [];
  const placeInTime = (node: MapNode): MapNode => {
    const ref = producedIn(idx, node.id);
    return { ...node, runRef: ref, runOrdinal: idx.runOrdinal.get(ref) };
  };
  wanted.forEach((nodeId) => {
    const finding = idx.findings.get(nodeId);
    if (finding) {
      candidates.push(placeInTime({
        id: nodeId,
        kind: 'finding',
        title: finding.name || finding.id,
        severity: finding.severity,
        status: finding.status,
        meta: findingMeta(idx, finding),
        archived: finding.archived,
        missing: finding.missing,
      }));
      return;
    }
    const task = idx.tasks.get(nodeId);
    if (task) {
      const worked = threadWorkedIn(idx, nodeId);
      candidates.push(placeInTime({
        id: nodeId,
        kind: 'task',
        title: task.title || task.text || task.id,
        status: task.status,
        meta: taskMeta(idx, task),
        missing: task.missing,
        ...(worked ? {
          workedIn: worked.ref,
          workedInOrdinal: idx.runOrdinal.get(worked.ref),
          outcome: worked.outcome,
          workedMeta: workedMeta(idx, worked),
        } : {}),
      }));
      return;
    }
    const rec = idx.recommendations.get(nodeId);
    if (rec) {
      candidates.push(placeInTime({
        id: nodeId,
        kind: 'recommendation',
        title: rec.title || rec.id,
        meta: rec.validation_verdict ? humanise(rec.validation_verdict) : undefined,
        priority: rec.priority,
      }));
    }
  });

  const kindOrder: Record<MapNodeKind, number> = { finding: 0, task: 1, recommendation: 2 };
  const rank = (n: MapNode) => severityRank[n.severity ?? ''] ?? 5;
  candidates.sort((a, b) => {
    if (a.kind !== b.kind) return kindOrder[a.kind] - kindOrder[b.kind];
    const sa = rank(a);
    const sb = rank(b);
    if (sa !== sb) return sa - sb;
    return a.title.localeCompare(b.title);
  });

  // The budget is spent a chain at a time, not a node at a time. Ranking every
  // finding ahead of every thread and then taking the first N fills the map
  // with findings on a project that has enough of them and leaves not one link
  // standing — and the links are the whole point of drawing it.
  const nodes = withinBudget(idx, candidates, rank, seeds);
  const truncated = nodes.length < candidates.length;
  const drawn = new Set(nodes.map((n) => n.id));

  // Deduped: the same relationship can be asserted from either end.
  const edges: MapEdge[] = [];
  const seenEdge = new Set<string>();
  const addEdge = (source: string, target: string, kind: GraphEdgeKind) => {
    if (!drawn.has(source) || !drawn.has(target)) return;
    const id = `${source}->${target}:${kind}`;
    if (seenEdge.has(id)) return;
    seenEdge.add(id);
    edges.push({ id, source, target, kind, label: EDGE_LABEL[kind] ?? humanise(kind) });
  };

  nodes.forEach((n) => {
    (idx.out.get(n.id) ?? []).forEach((e) => {
      if (EDGE_LABEL[e.kind]) addEdge(n.id, e.to, e.kind);
    });
    if (n.kind !== 'finding') return;
    (idx.out.get(n.id) ?? []).forEach((e) => {
      if (e.kind !== 'evidenced_by') return;
      (idx.in.get(e.to) ?? []).forEach((r) => {
        if (r.kind === 'recommends_on') addEdge(r.from, n.id, 'recommends_on');
      });
    });
  });

  return { nodes, edges, truncated, totalNodes: candidates.length };
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

/** A finding as one particular run saw it.
 *
 *  A finding carries its CURRENT severity, metric and evidence — consolidation
 *  overwrites them on every merge — so reading a run's story off the finding
 *  itself makes an old run report today's state: a finding first seen as low
 *  and later raised to high would be reported as high in the run that found
 *  it, linking to SQL that had not been written yet. The sighting is what that
 *  run actually recorded. */
export interface RunSighting {
  finding: GraphFinding;
  /** The sighting from this run, when the finding carries one. */
  sighting?: FindingSighting;
  /** The discovery the run produced. The sighting usually names it, but a
   *  trail that was clipped or never recorded leaves none — and the evidence
   *  still has to be scoped to THIS run rather than borrowed from another. */
  runDiscoveryId?: string;
  /** Severity as that run recorded it. Undefined when the run recorded none:
   *  the finding's current severity is a later run's answer, not this one's. */
  severity?: string;
}

/** One run's story, as the Story timeline reads it out. */
export interface RunStory {
  run: GraphRun;
  ordinal: number;
  newFindings: RunSighting[];
  seenAgain: RunSighting[];
  openedTasks: GraphTask[];
  closedTasks: GraphTask[];
  pursuedTasks: GraphTask[];
  learnings: GraphLearning[];
  proposals: GraphProposal[];
  /** What this run's findings drove. Reached through `in_run`, which every
   *  recommendation carries, so a run's card can offer them directly rather
   *  than making a reader go looking. */
  recommendations: GraphRecommendation[];
  highCount: number;
}

/** buildStories turns the graph into one card per run, newest first. This is
 *  the default view: plain language, no graph, and every noun a link. */
export function buildStories(idx: GraphIndex): RunStory[] {
  const stories: RunStory[] = [];
  idx.runs.forEach((run, nodeId) => {
    if (run.missing) return;
    const asSeenBy = (finding: GraphFinding): RunSighting => {
      // The LAST sighting this run recorded, not the first. One run writes
      // two when two of its insights consolidate into one finding, and the
      // later one is that run's settled answer — taking the first reported a
      // run that ended up calling something critical as having called it low,
      // and linked the evidence it had already superseded.
      const mine = (finding.sightings ?? []).filter(
        (s) => (s.run_id && `run:${s.run_id}` === nodeId)
          || (s.discovery_id && `disc:${s.discovery_id}` === nodeId)
          || (s.discovery_id && s.discovery_id === run.discovery_id),
      );
      const sighting = mine.length > 0 ? mine[mine.length - 1] : undefined;
      return {
        finding,
        sighting,
        runDiscoveryId: run.discovery_id,
        // Only what THIS run recorded. A finding carries its current
        // severity, so borrowing it would report a run as having found
        // something high that a later run raised to high — and would count it
        // in that run's headline. A severity nobody recorded stays unknown.
        severity: sighting?.severity,
      };
    };
    const story: RunStory = {
      run,
      ordinal: idx.runOrdinal.get(nodeId) ?? 0,
      newFindings: [], seenAgain: [], openedTasks: [], closedTasks: [],
      pursuedTasks: [], learnings: [], proposals: [], recommendations: [], highCount: 0,
    };
    (idx.out.get(nodeId) ?? []).forEach((e) => {
      switch (e.kind) {
        case 'found': {
          const f = idx.findings.get(e.to);
          if (f) story.newFindings.push(asSeenBy(f));
          break;
        }
        case 'seen_in': {
          const f = idx.findings.get(e.to);
          if (f) story.seenAgain.push(asSeenBy(f));
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
    // Recommendations point AT the run, so they are read from the inbound side.
    (idx.in.get(nodeId) ?? []).forEach((e) => {
      if (e.kind !== 'in_run') return;
      const rec = idx.recommendations.get(e.from);
      if (rec) story.recommendations.push(rec);
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
    // Either id. A run whose document has aged out is drawn as a `disc:`
    // node, so an entry carrying only the discovery still names a run the
    // reader can see on the map — calling it "an earlier run" would be
    // throwing away attribution that is right there.
    const ref = (h.run_id && idx.runOrdinal.has(`run:${h.run_id}`))
      ? `run:${h.run_id}`
      : (runRefForDiscovery(idx, h.discovery_id) || (h.run_id ? `run:${h.run_id}` : ''));
    const where = ref ? runLabel(idx, ref) : 'an earlier run';
    const why = h.reason ? `, “${h.reason}”` : '';
    return `${humanise(h.status)} in ${where.toLowerCase()}${why}`;
  });
}
