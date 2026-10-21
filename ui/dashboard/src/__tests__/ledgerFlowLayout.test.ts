import { buildMap, indexGraph } from '@/components/ledger/graph';
import {
  FlowCard, FlowGeometry, NODE_HEIGHT, NODE_WIDTH, RUN_COLUMN, laneLeft, placeFlow,
  roundedPath, workedCardID,
} from '@/components/ledger/flowLayout';
import type { GraphEdge, LedgerGraph } from '@/lib/api';

// The layout's promise is that a reader can follow every line. A line drawn
// through a card renders without complaint and reads as nonsense, so these
// tests check the geometry itself rather than what a component outputs.

const noTruncation = { runs: false, learnings: false, recommendations: false, finding_trails: false, task_pursuits: false };

const run = (n: number) => ({
  node_id: `run:r${n}`, run_id: `r${n}`, discovery_id: `d${n}`,
  date: `2026-05-0${n}T09:00:00Z`, new_findings: 1, total_findings: n, marginal_ratio: 1,
});

/** A finding found by run `n`, with its own insight so a recommendation can
 *  hang off it. */
function finding(id: string, n: number, severity = 'high') {
  return {
    node_id: `finding:${id}`, id, name: `Finding ${id}`, area: 'ops', severity,
    status: 'confirmed', seen_count: 1,
    first_seen: `2026-05-0${n}T09:00:00Z`, last_seen: `2026-05-0${n}T09:00:00Z`,
    source_insight_ids: [`ins-${id}`],
    sightings: [{ run_id: `r${n}`, discovery_id: `d${n}`, date: `2026-05-0${n}T09:00:00Z`, insight_id: `ins-${id}` }],
  };
}

/** Three runs that between them do every thing the layout has to place:
 *
 *  run 1 finds A, which drives two recommendations and opens thread T1;
 *  run 2 picks T1 up and answers it with B, which drives one recommendation
 *  and opens T2, a follow-up continuing T1;
 *  run 3 pursues T2 without answering it, and recommends something on A —
 *  a finding from two runs before.
 *
 *  Plus, in run 1, a finding nothing links to and a thread nothing links to:
 *  the shape of every project whose history predates the links. */
function threeRunGraph(): LedgerGraph {
  const edges: GraphEdge[] = [
    { from: 'run:r1', to: 'finding:A', kind: 'found' },
    { from: 'run:r1', to: 'finding:L', kind: 'found' },
    { from: 'run:r2', to: 'finding:B', kind: 'found' },
    { from: 'finding:A', to: 'insight:ins-A', kind: 'evidenced_by' },
    { from: 'finding:B', to: 'insight:ins-B', kind: 'evidenced_by' },
    { from: 'finding:L', to: 'insight:ins-L', kind: 'evidenced_by' },
    { from: 'rec:R1', to: 'insight:ins-A', kind: 'recommends_on' },
    { from: 'rec:R2', to: 'insight:ins-A', kind: 'recommends_on' },
    { from: 'rec:R3', to: 'insight:ins-B', kind: 'recommends_on' },
    { from: 'rec:R4', to: 'insight:ins-A', kind: 'recommends_on' },
    { from: 'task:T1', to: 'finding:A', kind: 'grew_from' },
    { from: 'task:T1', to: 'finding:B', kind: 'answered_by' },
    { from: 'task:T2', to: 'finding:B', kind: 'grew_from' },
    { from: 'task:T2', to: 'task:T1', kind: 'supersedes' },
    { from: 'run:r1', to: 'task:T1', kind: 'opened' },
    { from: 'run:r1', to: 'task:loose', kind: 'opened' },
    { from: 'run:r2', to: 'task:T1', kind: 'closed' },
    { from: 'run:r2', to: 'task:T1', kind: 'pursued' },
    { from: 'run:r2', to: 'task:T2', kind: 'opened' },
    { from: 'run:r3', to: 'task:T2', kind: 'pursued' },
  ];
  return {
    runs: [run(1), run(2), run(3)],
    findings: [finding('A', 1, 'critical'), finding('B', 2), finding('L', 1, 'low')],
    tasks: [
      {
        node_id: 'task:T1', id: 'T1', title: 'Is it one centre?', kind: 'hypothesis', status: 'done',
        origin_finding_ids: ['A'], resolved_by_finding_ids: ['B'], created_run_id: 'r1',
        closed_run_id: 'r2', closed_discovery_id: 'd2',
      },
      {
        node_id: 'task:T2', id: 'T2', title: 'Cost the backlog', kind: 'next_task', status: 'open',
        supersedes: 'T1', origin_finding_ids: ['B'], created_run_id: 'r2', pursued_in_run_ids: ['r3'],
      },
      { node_id: 'task:loose', id: 'loose', title: 'Look at returns', kind: 'next_task', status: 'open', created_run_id: 'r1' },
    ],
    insights: ['A', 'B', 'L'].map((id) => ({
      node_id: `insight:ins-${id}`, id: `ins-${id}`, discovery_id: `d${id === 'B' ? 2 : 1}`, name: id,
    })),
    recommendations: [
      { node_id: 'rec:R1', id: 'R1', discovery_id: 'd1', title: 'First', priority: 1, related_insight_ids: ['ins-A'] },
      { node_id: 'rec:R2', id: 'R2', discovery_id: 'd1', title: 'Second', priority: 2, related_insight_ids: ['ins-A'] },
      { node_id: 'rec:R3', id: 'R3', discovery_id: 'd2', title: 'Third', priority: 1, related_insight_ids: ['ins-B'] },
      { node_id: 'rec:R4', id: 'R4', discovery_id: 'd3', title: 'Fourth', priority: 1, related_insight_ids: ['ins-A'] },
    ],
    learnings: [], proposals: [], edges, truncated: noTruncation,
  };
}

/** A bigger history, generated: every run finds a handful of things, most
 *  drive a recommendation or two, some open threads, and later runs pick
 *  threads up and answer them — with deliberate cross-run links, the kind that
 *  have to travel between rows. Deterministic, so a failure reproduces. */
function generatedGraph(runs: number, perRun: number): LedgerGraph {
  const g: LedgerGraph = {
    runs: [], findings: [], tasks: [], insights: [], recommendations: [],
    learnings: [], proposals: [], edges: [], truncated: noTruncation,
  };
  const severities = ['critical', 'high', 'medium', 'low'];
  const openThreads: string[] = [];
  for (let r = 1; r <= runs; r += 1) {
    g.runs.push(run(r));
    for (let i = 0; i < perRun; i += 1) {
      const id = `F${r}_${i}`;
      g.findings.push(finding(id, r, severities[(r + i) % 4]));
      g.insights.push({ node_id: `insight:ins-${id}`, id: `ins-${id}`, discovery_id: `d${r}`, name: id });
      g.edges.push({ from: `run:r${r}`, to: `finding:${id}`, kind: 'found' });
      g.edges.push({ from: `finding:${id}`, to: `insight:ins-${id}`, kind: 'evidenced_by' });
      // Recommendations: none, one or two per finding.
      for (let k = 0; k < (i % 3); k += 1) {
        const rec = `R${r}_${i}_${k}`;
        g.recommendations.push({ node_id: `rec:${rec}`, id: rec, discovery_id: `d${r}`, title: rec, priority: 1 });
        g.edges.push({ from: `rec:${rec}`, to: `insight:ins-${id}`, kind: 'recommends_on' });
      }
      // A later run's answer to a thread an earlier one opened.
      if (i === 0 && openThreads.length > 0) {
        const t = openThreads.shift() as string;
        g.edges.push({ from: `task:${t}`, to: `finding:${id}`, kind: 'answered_by' });
        g.edges.push({ from: `run:r${r}`, to: `task:${t}`, kind: 'closed' });
        const task = g.tasks.find((x) => x.id === t)!;
        task.status = 'done';
        task.closed_run_id = `r${r}`;
      }
      // Threads, some of them continuing the one before.
      if (i % 2 === 1) {
        const t = `T${r}_${i}`;
        g.tasks.push({
          node_id: `task:${t}`, id: t, title: t, kind: 'next_task', status: 'open', created_run_id: `r${r}`,
        });
        g.edges.push({ from: `task:${t}`, to: `finding:${id}`, kind: 'grew_from' });
        g.edges.push({ from: `run:r${r}`, to: `task:${t}`, kind: 'opened' });
        if (openThreads.length > 0 && i === 3) {
          g.edges.push({ from: `task:${t}`, to: `task:${openThreads[0]}`, kind: 'supersedes' });
        }
        openThreads.push(t);
      }
      // A recommendation built on a finding from an earlier run.
      if (r > 1 && i === 2) {
        const rec = `X${r}`;
        g.recommendations.push({ node_id: `rec:${rec}`, id: rec, discovery_id: `d${r}`, title: rec, priority: 2 });
        g.edges.push({ from: `rec:${rec}`, to: `insight:ins-F${r - 1}_1`, kind: 'recommends_on' });
      }
      // A thread pursued and not answered.
      if (r > 2 && i === 4 && openThreads.length > 1) {
        g.edges.push({ from: `run:r${r}`, to: `task:${openThreads[1]}`, kind: 'pursued' });
      }
    }
  }
  return g;
}

function layOut(graph: LedgerGraph, expanded: Set<string> = new Set()): FlowGeometry {
  return placeFlow(buildMap(indexGraph(graph)), expanded);
}

interface Box { x0: number; y0: number; x1: number; y1: number }
const boxOf = (c: FlowCard): Box => ({ x0: c.x, y0: c.y, x1: c.x + NODE_WIDTH, y1: c.y + NODE_HEIGHT });

/** Whether an axis-aligned segment runs through a box's interior. Touching an
 *  edge is how a line arrives at a card, so the box is shrunk by a pixel. */
function crosses(a: { x: number; y: number }, b: { x: number; y: number }, box: Box): boolean {
  const x0 = box.x0 + 1; const x1 = box.x1 - 1; const y0 = box.y0 + 1; const y1 = box.y1 - 1;
  if (a.y === b.y) {
    const lo = Math.min(a.x, b.x); const hi = Math.max(a.x, b.x);
    return a.y > y0 && a.y < y1 && hi > x0 && lo < x1;
  }
  const lo = Math.min(a.y, b.y); const hi = Math.max(a.y, b.y);
  return a.x > x0 && a.x < x1 && hi > y0 && lo < y1;
}

function expectNoLineThroughACard(geometry: FlowGeometry) {
  const cards = [...geometry.cards.values()];
  const runLabels: Box[] = geometry.rows.map((r) => ({ x0: 0, y0: r.top, x1: RUN_COLUMN, y1: r.top + r.height }));
  geometry.edges.forEach((e) => {
    for (let i = 1; i < e.points.length; i += 1) {
      const a = e.points[i - 1]; const b = e.points[i];
      // Orthogonal: every segment is horizontal or vertical.
      expect(a.x === b.x || a.y === b.y).toBe(true);
      cards.forEach((card) => {
        if (card.id === e.source || card.id === e.target) return;
        if (crosses(a, b, boxOf(card))) {
          throw new Error(`line ${e.id} runs through ${card.id}`);
        }
      });
      runLabels.forEach((box) => expect(crosses(a, b, box)).toBe(false));
    }
  });
}

function expectNoOverlap(geometry: FlowGeometry) {
  const cards = [...geometry.cards.values()];
  cards.forEach((a, i) => cards.slice(i + 1).forEach((b) => {
    const overlap = a.x < b.x + NODE_WIDTH && b.x < a.x + NODE_WIDTH
      && a.y < b.y + NODE_HEIGHT && b.y < a.y + NODE_HEIGHT;
    if (overlap) throw new Error(`${a.id} overlaps ${b.id}`);
  }));
}

describe('placeFlow', () => {
  it('puts each run in its own row, oldest at the top', () => {
    const geometry = layOut(threeRunGraph());
    expect(geometry.rows.map((r) => r.ref)).toEqual(['run:r1', 'run:r2', 'run:r3']);
    geometry.rows.slice(1).forEach((row, i) => {
      const above = geometry.rows[i];
      expect(row.top).toBeGreaterThan(above.top + above.height);
    });
    // Every card sits inside its own row.
    geometry.cards.forEach((card) => {
      const row = geometry.rows.find((r) => r.ref === card.row)!;
      expect(card.y).toBeGreaterThanOrEqual(row.top);
      expect(card.y + NODE_HEIGHT).toBeLessThanOrEqual(row.top + row.height);
    });
  });

  it('reads each row in the order the work went: picked up, found, recommended, opened', () => {
    const { cards } = layOut(threeRunGraph());
    const lane = (id: string) => cards.get(id)?.lane;
    expect(lane(workedCardID('task:T1'))).toBe(0);
    expect(lane('finding:B')).toBe(1);
    expect(lane('rec:R3')).toBe(2);
    expect(lane('task:T2')).toBe(3);
    expect(cards.get('task:T2')?.x).toBe(laneLeft(3));
  });

  it('draws a thread where it was opened and again where a later run took it up', () => {
    const geometry = layOut(threeRunGraph());
    const opened = geometry.cards.get('task:T1')!;
    const picked = geometry.cards.get(workedCardID('task:T1'))!;
    expect(opened.row).toBe('run:r1');
    expect(picked.row).toBe('run:r2');
    expect(picked.threadOf).toBe('task:T1');
    // And T2, pursued by run 3 and still open, is picked up in run 3.
    expect(geometry.cards.get(workedCardID('task:T2'))?.row).toBe('run:r3');
    // A thread nothing took up has no second card.
    expect(geometry.cards.has(workedCardID('task:loose'))).toBe(false);
  });

  it('joins the two with the jump between runs, and answers from where it was picked up', () => {
    const { edges } = layOut(threeRunGraph());
    const edge = (source: string, target: string) =>
      edges.find((e) => e.source === source && e.target === target);
    expect(edge('task:T1', workedCardID('task:T1'))?.label).toBe('Picked up in run 2');
    expect(edge(workedCardID('task:T1'), 'finding:B')?.kind).toBe('answered_by');
    expect(edge('task:T1', 'finding:B')).toBeUndefined();
    // The follow-up keeps its lineage to the thread it continues.
    expect(edge('task:T1', 'task:T2')?.kind).toBe('supersedes');
  });

  it('keeps a picked-up thread beside the finding that answered it', () => {
    const { cards } = layOut(threeRunGraph());
    expect(cards.get(workedCardID('task:T1'))?.y).toBe(cards.get('finding:B')?.y);
  });

  it('starts the threads a finding opened below the recommendations it drove', () => {
    // A finding's line to the thread it opened runs at the thread's height,
    // straight across the recommendation lane. Starting the threads beside a
    // recommendation would draw that line through it.
    const { cards } = layOut(threeRunGraph());
    const recs = ['rec:R1', 'rec:R2'].map((id) => cards.get(id)!.y);
    expect(cards.get('task:T1')!.y).toBeGreaterThan(Math.max(...recs));
  });

  it('never folds a thread away, linked or not, and folds loose findings', () => {
    const geometry = layOut(threeRunGraph());
    expect(geometry.cards.has('task:loose')).toBe(true);
    expect(geometry.cards.has('finding:L')).toBe(false);
    const first = geometry.rows[0];
    expect(first.hidden.map((n) => n.id)).toEqual(['finding:L']);
    expect(first.moreAt).toBeDefined();

    const open = layOut(threeRunGraph(), new Set(['run:r1']));
    expect(open.cards.has('finding:L')).toBe(true);
    expect(open.rows[0].hidden).toEqual([]);
    expect(open.rows[0].moreAt).toBeDefined(); // now "Show less"
  });

  it('never draws a line through a card, on the fixture', () => {
    const geometry = layOut(threeRunGraph());
    expectNoOverlap(geometry);
    expectNoLineThroughACard(geometry);
  });

  it('never draws a line through a card, on a long generated history', () => {
    const graph = generatedGraph(6, 9);
    const geometry = layOut(graph);
    // The history was actually drawn: threads picked up across runs, lines
    // between rows, and plenty of them.
    expect([...geometry.cards.values()].some((c) => c.threadOf)).toBe(true);
    expect(geometry.edges.some((e) => e.kind === 'picked_up')).toBe(true);
    expect(geometry.edges.length).toBeGreaterThan(40);
    expectNoOverlap(geometry);
    expectNoLineThroughACard(geometry);

    // And with every run's loose cards opened up as well.
    const all = new Set(geometry.rows.map((r) => r.ref));
    const opened = layOut(graph, all);
    expectNoOverlap(opened);
    expectNoLineThroughACard(opened);
  });

  it('starts and ends every line on the cards it joins', () => {
    const geometry = layOut(generatedGraph(4, 7));
    geometry.edges.forEach((e) => {
      const from = geometry.cards.get(e.source)!;
      const to = geometry.cards.get(e.target)!;
      const first = e.points[0];
      const last = e.points[e.points.length - 1];
      expect(first).toEqual({ x: from.x + NODE_WIDTH, y: from.y + NODE_HEIGHT / 2 });
      expect([to.x, to.x + NODE_WIDTH]).toContain(last.x);
      expect(last.y).toBe(to.y + NODE_HEIGHT / 2);
    });
  });

  it('files what no run can be placed for in one row at the bottom', () => {
    const graph = threeRunGraph();
    graph.findings.push({
      node_id: 'finding:U', id: 'U', name: 'Undated', severity: 'high', status: 'confirmed', seen_count: 1,
    });
    graph.tasks.push({ node_id: 'task:TU', id: 'TU', title: 'Undated thread', kind: 'next_task', status: 'open' });
    graph.edges.push({ from: 'task:TU', to: 'finding:U', kind: 'grew_from' });
    const geometry = layOut(graph);
    expect(geometry.rows[geometry.rows.length - 1].ref).toBe('');
    expect(geometry.cards.get('finding:U')?.row).toBe('');
    expectNoLineThroughACard(geometry);
  });

  it('is empty, not broken, for an empty map', () => {
    const geometry = placeFlow({ nodes: [], edges: [], truncated: false, totalNodes: 0 }, new Set());
    expect(geometry.rows).toEqual([]);
    expect(geometry.edges).toEqual([]);
  });
});

describe('roundedPath', () => {
  it('draws a straight line as one segment and softens every corner', () => {
    expect(roundedPath([{ x: 0, y: 0 }, { x: 10, y: 0 }])).toBe('M 0 0 L 10 0');
    const path = roundedPath([{ x: 0, y: 0 }, { x: 40, y: 0 }, { x: 40, y: 40 }]);
    expect(path).toContain('Q 40 0');
    expect(path.endsWith('L 40 40')).toBe(true);
  });

  it('does not overshoot a corner on a short segment', () => {
    // A two-pixel jog cannot carry an eight-pixel radius.
    const path = roundedPath([{ x: 0, y: 0 }, { x: 20, y: 0 }, { x: 20, y: 2 }, { x: 40, y: 2 }]);
    expect(path).not.toContain('NaN');
    expect(path).toContain('Q 20 0 20 1');
  });
});
