import {
  DEFAULT_OPEN_DEPTH, MAX_TREE_DEPTH, TreeNode, areasOf, buildStories, buildTree,
  countNodes, findingChangedInRun, findingEvidenceLink, indexGraph, insightLink,
  MAX_TREE_NODES, matchesFilters, nodeKind, nodeRawId, recommendationLink, runLabel,
  runRefForDiscovery, severitiesOf, statusTrail, statusesOf,
} from '@/components/ledger/graph';
import type { LedgerGraph } from '@/lib/api';

// The pure half of the Threads view. These are the parts that are actually
// hard — building the tree from a flat payload, not walking a cycle forever,
// tolerating a reference to something that is gone — so they are tested without
// rendering anything.

const noTruncation = { runs: false, learnings: false, recommendations: false, finding_trails: false, task_pursuits: false };

/** The acceptance-criterion chain: a finding opens a hypothesis, a later run
 *  closes it, the close points at the finding that answered it, and a
 *  recommendation hangs off that finding's insight. */
function chainGraph(): LedgerGraph {
  return {
    runs: [
      { node_id: 'run:r1', run_id: 'r1', discovery_id: 'd1', date: '2026-05-01T09:00:00Z', new_findings: 2, total_findings: 2, marginal_ratio: 1 },
      { node_id: 'run:r2', run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', new_findings: 1, total_findings: 3, marginal_ratio: 0.33 },
    ],
    findings: [
      {
        node_id: 'finding:f1', id: 'f1', name: 'Dead stock in a few sellers', area: 'inventory',
        severity: 'high', status: 'changed', seen_count: 2,
        first_seen: '2026-05-01T09:00:00Z', last_seen: '2026-05-03T09:00:00Z',
        source_insight_ids: ['ins1', 'ins3'],
        sightings: [
          { run_id: 'r1', discovery_id: 'd1', date: '2026-05-01T09:00:00Z', affected_count: 12, insight_id: 'ins1' },
          { run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', affected_count: 31, insight_id: 'ins3' },
        ],
        status_history: [
          { status: 'confirmed', run_id: 'r1', at: '2026-05-01T09:00:00Z', source: 'reflection', reason: 'first seen' },
          { status: 'changed', run_id: 'r2', at: '2026-05-03T09:00:00Z', source: 'trend', reason: 'affected count moved from 12 to 31' },
        ],
      },
      {
        node_id: 'finding:f2', id: 'f2', name: 'Centre backlog drives late orders', area: 'logistics',
        severity: 'critical', status: 'confirmed', seen_count: 1,
        first_seen: '2026-05-03T09:00:00Z', last_seen: '2026-05-03T09:00:00Z',
        source_insight_ids: ['ins2'], answered_task_ids: ['t1'],
        sightings: [{ run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', insight_id: 'ins2' }],
      },
      {
        node_id: 'finding:f3', id: 'f3', name: 'An archived old thing', area: 'misc',
        severity: 'low', status: 'resolved', seen_count: 1, archived: true,
        first_seen: '2026-05-01T09:00:00Z', last_seen: '2026-05-01T09:00:00Z',
      },
    ],
    tasks: [
      {
        node_id: 'task:t1', id: 't1', title: 'Do those sellers share a centre?',
        text: 'join sellers to centres', kind: 'hypothesis', status: 'done',
        origin_finding_ids: ['f1'], resolved_by_finding_ids: ['f2'],
        pursued_in_run_ids: ['r1', 'r2'], created_run_id: 'r1',
        closed_run_id: 'r2', closed_discovery_id: 'd2',
        closed_at: '2026-05-03T09:00:00Z', close_reason: '9 of 12 share the same centre',
      },
      {
        node_id: 'task:t2', id: 't2', title: 'Quantify the backlog cost', text: 'cost it',
        kind: 'next_task', status: 'open', supersedes: 't1',
        origin_finding_ids: ['f2'], created_run_id: 'r2',
      },
    ],
    insights: [
      { node_id: 'insight:ins1', id: 'ins1', discovery_id: 'd1', name: 'Unsold stock', area: 'inventory', severity: 'high' },
      { node_id: 'insight:ins2', id: 'ins2', discovery_id: 'd2', name: 'Late orders', area: 'logistics', severity: 'critical' },
      { node_id: 'insight:ins3', id: 'ins3', discovery_id: 'd2', name: 'Unsold stock again', area: 'inventory', severity: 'high' },
    ],
    recommendations: [
      { node_id: 'rec:rec1', id: 'rec1', discovery_id: 'd2', title: 'Re-route the overflow', priority: 1, related_insight_ids: ['ins2'] },
    ],
    learnings: [
      { node_id: 'learning:l1', id: 'l1', category: 'schema', note: 'centres are keyed by code', relevance: 0.8, run_id: 'r2', discovery_id: 'd2' },
    ],
    proposals: [
      { node_id: 'proposal:p1', id: 'p1', action: 'add_area', area_id: 'logistics', rationale: 'recurs', status: 'proposed', created_run_id: 'r2' },
    ],
    edges: [
      { from: 'run:r1', to: 'finding:f1', kind: 'found' },
      { from: 'run:r2', to: 'finding:f1', kind: 'seen_in' },
      { from: 'run:r2', to: 'finding:f2', kind: 'found' },
      { from: 'run:r1', to: 'finding:f3', kind: 'found' },
      { from: 'finding:f1', to: 'insight:ins1', kind: 'evidenced_by' },
      { from: 'finding:f1', to: 'insight:ins3', kind: 'evidenced_by' },
      { from: 'finding:f2', to: 'insight:ins2', kind: 'evidenced_by' },
      { from: 'task:t1', to: 'finding:f1', kind: 'grew_from' },
      { from: 'task:t1', to: 'finding:f2', kind: 'answered_by' },
      { from: 'task:t2', to: 'task:t1', kind: 'supersedes' },
      { from: 'task:t2', to: 'finding:f2', kind: 'grew_from' },
      { from: 'run:r1', to: 'task:t1', kind: 'opened' },
      { from: 'run:r2', to: 'task:t1', kind: 'closed' },
      { from: 'run:r2', to: 'task:t2', kind: 'opened' },
      { from: 'rec:rec1', to: 'insight:ins2', kind: 'recommends_on' },
      { from: 'insight:ins2', to: 'run:r2', kind: 'in_run' },
      { from: 'run:r2', to: 'learning:l1', kind: 'learned' },
      { from: 'run:r2', to: 'proposal:p1', kind: 'proposed' },
    ],
    truncated: noTruncation,
  };
}

/** Flattens a tree to "depth kind:id" lines, for readable assertions. */
function flatten(nodes: TreeNode[], out: string[] = []): string[] {
  nodes.forEach((n) => {
    out.push(`${n.depth} ${n.nodeId}${n.cyclic ? ' (cyclic)' : ''}`);
    flatten(n.children, out);
  });
  return out;
}

describe('indexGraph', () => {
  it('indexes nodes and both edge directions', () => {
    const idx = indexGraph(chainGraph());
    expect(idx.findings.size).toBe(3);
    expect(idx.tasks.size).toBe(2);
    expect(idx.out.get('task:t1')).toHaveLength(2);
    expect(idx.in.get('finding:f2')?.map((e) => e.kind).sort()).toEqual(['answered_by', 'found', 'grew_from']);
  });

  it('numbers runs oldest-first so "Run 2" means the second run', () => {
    const idx = indexGraph(chainGraph());
    expect(idx.runOrdinal.get('run:r1')).toBe(1);
    expect(idx.runOrdinal.get('run:r2')).toBe(2);
    expect(idx.latestRun?.run_id).toBe('r2');
    expect(runLabel(idx, 'run:r2')).toBe('Run 2');
  });

  it('survives a null graph and a payload with missing arrays', () => {
    expect(indexGraph(null).findings.size).toBe(0);
    const partial = { runs: [], findings: [], edges: [] } as unknown as LedgerGraph;
    const idx = indexGraph(partial);
    expect(idx.tasks.size).toBe(0);
    expect(idx.latestRun).toBeNull();
  });

  it('labels a run stub that has no date and no ordinal', () => {
    const graph = chainGraph();
    graph.runs.push({ node_id: 'run:gone', run_id: 'gone-run-id', new_findings: 0, total_findings: 0, marginal_ratio: 0, missing: true });
    const idx = indexGraph(graph);
    expect(idx.runOrdinal.get('run:gone')).toBeUndefined();
    expect(runLabel(idx, 'run:gone')).toBe('Run gone-run');
    expect(runLabel(idx, 'run:never-seen')).toBe('An earlier run');
  });

  it('resolves a discovery id to the run node the server drew', () => {
    // Insights and recommendations carry only a discovery id, and a run is
    // named by its run id where one survives and by its discovery id
    // otherwise. Only the server knows which, so the client reads the pairing
    // off the run nodes instead of guessing a prefix.
    const graph = chainGraph();
    // A run whose document has aged out: the server drew it as a disc: node.
    graph.runs.push({
      node_id: 'disc:d9', discovery_id: 'd9', date: '2026-05-09T09:00:00Z',
      new_findings: 0, total_findings: 0, marginal_ratio: 0,
    });
    const idx = indexGraph(graph);
    expect(runRefForDiscovery(idx, 'd2')).toBe('run:r2');
    expect(runLabel(idx, runRefForDiscovery(idx, 'd2'))).toBe('Run 2');
    expect(runRefForDiscovery(idx, 'd9')).toBe('disc:d9');
    // It is still a run of this investigation, so it is numbered with the rest.
    expect(runLabel(idx, runRefForDiscovery(idx, 'd9'))).toBe('Run 3');
    expect(runRefForDiscovery(idx, 'nope')).toBe('');
    expect(runRefForDiscovery(idx, undefined)).toBe('');
  });
});

describe('node references', () => {
  it('splits a prefixed reference, and tolerates an id containing a colon', () => {
    expect(nodeKind('finding:abc')).toBe('finding');
    expect(nodeRawId('finding:abc')).toBe('abc');
    expect(nodeRawId('run:a:b')).toBe('a:b');
    expect(nodeKind('nocolon')).toBe('');
    expect(nodeRawId('nocolon')).toBe('nocolon');
  });
});

describe('buildTree', () => {
  it('builds the full finding → hypothesis → answer → recommendation chain', () => {
    const idx = indexGraph(chainGraph());
    const roots = buildTree(idx);
    const flat = flatten(roots);

    // f2 answered t1 and t2 grew from it, so it is not a root; f1 and f3 are.
    expect(flat).toContain('0 finding:f1');
    expect(flat).toContain('0 finding:f3');
    expect(flat).not.toContain('0 finding:f2');

    // The chain below f1: the hypothesis, the finding that answered it, the
    // recommendation that finding drove, and the follow-up thread.
    const f1 = roots.find((r) => r.nodeId === 'finding:f1')!;
    const byId = (node: TreeNode, id: string): TreeNode | undefined => {
      if (node.nodeId === id) return node;
      for (const child of node.children) {
        const hit = byId(child, id);
        if (hit) return hit;
      }
      return undefined;
    };
    const t1 = byId(f1, 'task:t1');
    expect(t1).toBeDefined();
    expect(t1!.via).toBe('grew_from');
    const f2 = byId(t1!, 'finding:f2');
    expect(f2).toBeDefined();
    expect(f2!.via).toBe('answered_by');
    expect(byId(f2!, 'rec:rec1')?.via).toBe('recommends_on');
    // The follow-up continues the hypothesis.
    expect(byId(t1!, 'task:t2')?.via).toBe('supersedes');
  });

  it('puts the most severe findings first', () => {
    const idx = indexGraph(chainGraph());
    const roots = buildTree(idx);
    expect(roots[0].nodeId).toBe('finding:f1'); // high beats the low-severity f3
    expect(roots.map((r) => r.kind)).not.toContain(undefined);
  });

  it('keeps archived findings as nodes rather than hiding them', () => {
    const idx = indexGraph(chainGraph());
    expect(flatten(buildTree(idx))).toContain('0 finding:f3');
  });

  it('does not hang on a cyclic supersedes chain', () => {
    const graph = chainGraph();
    // t1 continues t2 AND t2 continues t1 — a cycle the writer should never
    // produce, and a payload the page must survive.
    graph.edges.push({ from: 'task:t1', to: 'task:t2', kind: 'supersedes' });
    const idx = indexGraph(graph);

    const roots = buildTree(idx);
    const flat = flatten(roots);
    expect(flat.some((line) => line.includes('(cyclic)'))).toBe(true);
    // Finite, and bounded well under the hard depth cap.
    const deepest = Math.max(...flat.map((line) => Number(line.split(' ')[0])));
    expect(deepest).toBeLessThanOrEqual(MAX_TREE_DEPTH);
    expect(flat.length).toBeLessThan(500);
  });

  it('stops at the hard depth cap on a long chain', () => {
    // A chain of 30 threads, each continuing the last.
    const graph: LedgerGraph = {
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [], truncated: noTruncation,
    };
    for (let i = 0; i < 30; i += 1) {
      graph.tasks.push({ node_id: `task:t${i}`, id: `t${i}`, text: `thread ${i}`, kind: 'next_task', status: 'open' });
      if (i > 0) graph.edges.push({ from: `task:t${i}`, to: `task:t${i - 1}`, kind: 'supersedes' });
    }
    const flat = flatten(buildTree(indexGraph(graph)));
    const deepest = Math.max(...flat.map((line) => Number(line.split(' ')[0])));
    expect(deepest).toBeLessThanOrEqual(MAX_TREE_DEPTH);
  });

  it('shows a thread whose origin finding is outside the window', () => {
    const graph = chainGraph();
    // The finding f1 is gone from the payload; t1 still says it grew from it.
    graph.findings = graph.findings.filter((f) => f.id !== 'f1');
    const roots = buildTree(indexGraph(graph));
    // t1's parent is absent, so t1 itself becomes a root rather than vanishing.
    expect(flatten(roots)).toContain('0 task:t1');
  });

  it('counts a subtree for the collapsed badge', () => {
    const idx = indexGraph(chainGraph());
    const f1 = buildTree(idx).find((r) => r.nodeId === 'finding:f1')!;
    expect(countNodes(f1)).toBeGreaterThan(3);
  });

  it('collapses below the default depth', () => {
    expect(DEFAULT_OPEN_DEPTH).toBe(2);
  });
});

describe('filters', () => {
  it('filters roots by area, severity, status and date', () => {
    const idx = indexGraph(chainGraph());
    expect(buildTree(idx, { area: 'inventory' }).map((r) => r.nodeId)).toEqual(
      expect.arrayContaining(['finding:f1']),
    );
    expect(buildTree(idx, { area: 'inventory' }).map((r) => r.nodeId)).not.toContain('finding:f3');
    expect(buildTree(idx, { severity: 'low' }).map((r) => r.nodeId)).toContain('finding:f3');
    expect(buildTree(idx, { status: 'resolved' }).map((r) => r.nodeId)).toContain('finding:f3');
    expect(buildTree(idx, { from: '2026-05-03' }).map((r) => r.nodeId)).not.toContain('finding:f3');
    expect(buildTree(idx, { to: '2026-05-01' }).map((r) => r.nodeId)).not.toContain('finding:f1');
  });

  it('"changed in the last run" keeps only what the latest run touched', () => {
    const idx = indexGraph(chainGraph());
    const roots = buildTree(idx, { changedSinceLastRun: true }).map((r) => r.nodeId);
    // f1 was seen again in r2; f3 was not touched after r1.
    expect(roots).toContain('finding:f1');
    expect(roots).not.toContain('finding:f3');
  });

  it('counts a status change as having been touched, not only a sighting', () => {
    const graph = chainGraph();
    const f3 = graph.findings.find((f) => f.id === 'f3')!;
    f3.status_history = [{ status: 'refuted', run_id: 'r2', at: '2026-05-03T09:00:00Z', source: 'reflection', reason: 'disproved' }];
    const idx = indexGraph(graph);
    expect(findingChangedInRun(f3, 'r2')).toBe(true);
    expect(buildTree(idx, { changedSinceLastRun: true }).map((r) => r.nodeId)).toContain('finding:f3');
  });

  it('a finding with no runs is unchanged by definition', () => {
    const bare = { node_id: 'finding:x', id: 'x', seen_count: 0 };
    expect(findingChangedInRun(bare, 'r2')).toBe(false);
    expect(findingChangedInRun(bare, undefined)).toBe(false);
  });

  it('matchesFilters is permissive with no filters set', () => {
    const idx = indexGraph(chainGraph());
    const f1 = idx.findings.get('finding:f1')!;
    expect(matchesFilters(f1, {}, 'r2')).toBe(true);
  });

  it('offers only the values the project actually has', () => {
    const idx = indexGraph(chainGraph());
    expect(areasOf(idx)).toEqual(['inventory', 'logistics', 'misc']);
    expect(severitiesOf(idx)).toEqual(['critical', 'high', 'low']);
    expect(statusesOf(idx)).toEqual(['changed', 'confirmed', 'resolved']);
  });
});

describe('links', () => {
  it('a finding links to the newest sighting that has a discovery and an insight', () => {
    const idx = indexGraph(chainGraph());
    const f1 = idx.findings.get('finding:f1')!;
    expect(findingEvidenceLink('p1', f1)).toBe('/projects/p1/discoveries/d2/insights/ins3');
  });

  it('a finding with no usable sighting has no link rather than a broken one', () => {
    expect(findingEvidenceLink('p1', { node_id: 'finding:x', id: 'x', seen_count: 0 })).toBeNull();
    expect(findingEvidenceLink('p1', {
      node_id: 'finding:x', id: 'x', seen_count: 1,
      sightings: [{ run_id: 'r1', discovery_id: '', date: '2026-05-01T09:00:00Z' }],
    })).toBeNull();
  });

  it('an insight or recommendation that is gone has no link', () => {
    expect(insightLink('p1', { node_id: 'insight:a', id: 'a', discovery_id: 'd1' }))
      .toBe('/projects/p1/discoveries/d1/insights/a');
    expect(insightLink('p1', { node_id: 'insight:a', id: 'a', missing: true })).toBeNull();
    expect(insightLink('p1', { node_id: 'insight:a', id: 'a' })).toBeNull();
    expect(recommendationLink('p1', { node_id: 'rec:r', id: 'r', discovery_id: 'd2' }))
      .toBe('/projects/p1/discoveries/d2/recommendations/r');
    expect(recommendationLink('p1', { node_id: 'rec:r', id: 'r' })).toBeNull();
  });
});

describe('statusTrail', () => {
  it('reads out the history with its reasons and runs', () => {
    const idx = indexGraph(chainGraph());
    const trail = statusTrail(idx, idx.findings.get('finding:f1')!);
    expect(trail).toEqual([
      'confirmed (Run 1, “first seen”)',
      'changed (Run 2, “affected count moved from 12 to 31”)',
    ]);
  });

  it('is empty for a finding with no history, and names an unknown run honestly', () => {
    const idx = indexGraph(chainGraph());
    expect(statusTrail(idx, { node_id: 'finding:x', id: 'x', seen_count: 0 })).toEqual([]);
    expect(statusTrail(idx, {
      node_id: 'finding:x', id: 'x', seen_count: 1,
      status_history: [{ status: 'confirmed', at: '2026-05-01T09:00:00Z' }],
    })).toEqual(['confirmed (an earlier run)']);
  });
});

describe('buildStories', () => {
  it('reads each run as a story, newest first', () => {
    const stories = buildStories(indexGraph(chainGraph()));
    expect(stories.map((s) => s.run.run_id)).toEqual(['r2', 'r1']);

    const r2 = stories[0];
    expect(r2.newFindings.map((f) => f.id)).toEqual(['f2']);
    expect(r2.seenAgain.map((f) => f.id)).toEqual(['f1']);
    expect(r2.closedTasks.map((t) => t.id)).toEqual(['t1']);
    expect(r2.openedTasks.map((t) => t.id)).toEqual(['t2']);
    expect(r2.learnings.map((l) => l.id)).toEqual(['l1']);
    expect(r2.proposals.map((p) => p.id)).toEqual(['p1']);
    expect(r2.highCount).toBe(1); // the critical finding

    const r1 = stories[1];
    expect(r1.newFindings.map((f) => f.id).sort()).toEqual(['f1', 'f3']);
    expect(r1.openedTasks.map((t) => t.id)).toEqual(['t1']);
  });

  it('leaves out run stubs for discoveries that no longer exist', () => {
    const graph = chainGraph();
    graph.runs.push({ node_id: 'run:gone', run_id: 'gone', new_findings: 0, total_findings: 0, marginal_ratio: 0, missing: true });
    const stories = buildStories(indexGraph(graph));
    expect(stories.map((s) => s.run.run_id)).not.toContain('gone');
  });

  it('is empty on a project that has never reflected', () => {
    expect(buildStories(indexGraph(null))).toEqual([]);
  });
});

// --- Scale and reachability (first platform review round) ---

describe('tree expansion is bounded', () => {
  /** A graph where every finding leads into every thread of the next stage —
   *  67 nodes, and the shape whose paths multiply. */
  function fanOutGraph(stages: number, perStage: number): LedgerGraph {
    const g: LedgerGraph = {
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [], truncated: noTruncation,
    };
    for (let i = 0; i < 7; i += 1) {
      g.findings.push({
        node_id: `finding:f${i}`, id: `f${i}`, name: `finding ${i}`,
        severity: 'high', status: 'confirmed', seen_count: 1,
      });
    }
    let previous = g.findings.map((f) => f.node_id);
    for (let stage = 0; stage < stages; stage += 1) {
      const current: string[] = [];
      for (let i = 0; i < perStage; i += 1) {
        const id = `t${stage}_${i}`;
        g.tasks.push({ node_id: `task:${id}`, id, text: id, kind: 'next_task', status: 'open' });
        current.push(`task:${id}`);
        previous.forEach((parent) => {
          g.edges.push({ from: `task:${id}`, to: parent, kind: 'grew_from' });
        });
      }
      previous = current;
    }
    return g;
  }

  it('does not multiply a subtree once per path', () => {
    const graph = fanOutGraph(6, 10);
    const payloadNodes = graph.findings.length + graph.tasks.length;
    const started = Date.now();
    const roots = buildTree(indexGraph(graph));
    const built = flatten(roots).length;

    // Without repeat-suppression this shape materialises millions of nodes.
    expect(built).toBeLessThanOrEqual(MAX_TREE_NODES);
    expect(built).toBeLessThan(payloadNodes * 20);
    expect(Date.now() - started).toBeLessThan(5000);
  });

  it('flags a node whose subtree was already drawn', () => {
    // One finding answering two threads: genuinely reachable by two paths.
    const graph = chainGraph();
    graph.tasks.push({
      node_id: 'task:t3', id: 't3', title: 'Another thread f2 answered',
      text: 'another', kind: 'next_task', status: 'done',
      resolved_by_finding_ids: ['f2'],
    });
    graph.edges.push({ from: 'task:t3', to: 'finding:f2', kind: 'answered_by' });
    const roots = buildTree(indexGraph(graph));
    const repeated: TreeNode[] = [];
    const visit = (nodes: TreeNode[]) => nodes.forEach((n) => {
      if (n.repeated) repeated.push(n);
      visit(n.children);
    });
    visit(roots);
    expect(repeated.length).toBeGreaterThan(0);
    expect(repeated.every((n) => n.children.length === 0)).toBe(true);
  });

  it('stops scheduling siblings once the budget is spent', () => {
    // One node with thousands of children: a budget that only stopped
    // descending would still materialise every one of them.
    const graph: LedgerGraph = {
      runs: [], findings: [
        { node_id: 'finding:f1', id: 'f1', name: 'One root', severity: 'high', status: 'confirmed', seen_count: 1 },
      ],
      tasks: [], insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [], truncated: noTruncation,
    };
    for (let i = 0; i < 6000; i += 1) {
      graph.tasks.push({ node_id: `task:t${i}`, id: `t${i}`, text: `t${i}`, kind: 'next_task', status: 'open' });
      graph.edges.push({ from: `task:t${i}`, to: 'finding:f1', kind: 'grew_from' });
    }
    const built = flatten(buildTree(indexGraph(graph))).length;
    expect(built).toBeLessThanOrEqual(MAX_TREE_NODES);
  });

  it('keeps a component whose nodes all claim each other', () => {
    // A thread that re-checks a finding and is answered by that same finding
    // is a valid two-node cycle: both are somebody's child, so neither is a
    // root, and the whole history would otherwise disappear.
    const graph: LedgerGraph = {
      runs: [], findings: [
        { node_id: 'finding:f1', id: 'f1', name: 'Recurring', severity: 'high', status: 'confirmed', seen_count: 2 },
      ],
      tasks: [
        { node_id: 'task:t1', id: 't1', title: 'Re-check it', text: 'recheck', kind: 'next_task', status: 'done',
          origin_finding_ids: ['f1'], resolved_by_finding_ids: ['f1'] },
      ],
      insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [
        { from: 'task:t1', to: 'finding:f1', kind: 'grew_from' },
        { from: 'task:t1', to: 'finding:f1', kind: 'answered_by' },
      ],
      truncated: noTruncation,
    };
    const flat = flatten(buildTree(indexGraph(graph)));
    expect(flat.length).toBeGreaterThan(0);
    expect(flat.some((line) => line.includes('finding:f1') || line.includes('task:t1'))).toBe(true);
  });

  it('does not promote a filtered-out finding to a root when rescuing', () => {
    // The documented rule: a finding the filters exclude is not shown as a
    // ROOT, but may still appear beneath a thread that points at it — hiding
    // it there would break the chain the view exists to show. The rescue must
    // not quietly turn it back into a root.
    const graph: LedgerGraph = {
      runs: [], findings: [
        { node_id: 'finding:f1', id: 'f1', name: 'Low', area: 'misc', severity: 'low', status: 'confirmed', seen_count: 1 },
      ],
      tasks: [
        { node_id: 'task:t1', id: 't1', text: 'recheck', kind: 'next_task', status: 'done',
          origin_finding_ids: ['f1'], resolved_by_finding_ids: ['f1'] },
      ],
      insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [
        { from: 'task:t1', to: 'finding:f1', kind: 'grew_from' },
        { from: 'task:t1', to: 'finding:f1', kind: 'answered_by' },
      ],
      truncated: noTruncation,
    };
    const roots = buildTree(indexGraph(graph), { severity: 'critical' });
    expect(roots.map((r) => r.nodeId)).not.toContain('finding:f1');
    // The thread is still rescued, and the finding is reachable through it.
    expect(roots.map((r) => r.nodeId)).toContain('task:t1');
    expect(flatten(roots).join(' ')).toContain('finding:f1');

    // And with no thread to rescue, a filtered-out finding is simply absent.
    const lone: LedgerGraph = { ...graph, tasks: [], edges: [] };
    expect(flatten(buildTree(indexGraph(lone), { severity: 'critical' }))).toEqual([]);
  });
});
