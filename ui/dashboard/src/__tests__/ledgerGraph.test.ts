import {
  InvestigationMap, MAX_MAP_NODES, areasOf, buildMap, buildStories,
  findingChangedInRun, findingEvidenceLink, indexGraph, insightLink,
  matchesFilters, nodeKind, nodeRawId, recommendationLink, runLabel,
  producedIn, runRefForDiscovery, seenInRuns, severitiesOf, sightingEvidenceLink,
  statusTrail, statusesOf, threadWorkedIn,
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
        sightings: [{ run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', insight_id: 'ins2', severity: 'critical' }],
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

/** The node ids a map holds, sorted, for readable assertions. */
function mapIds(map: InvestigationMap): string[] {
  return map.nodes.map((n) => n.id).sort();
}

function hasEdge(map: InvestigationMap, source: string, target: string, kind: string): boolean {
  return map.edges.some((e) => e.source === source && e.target === target && e.kind === kind);
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

describe('buildMap', () => {
  it('draws the full finding → thread → answer → recommendation chain', () => {
    const map = buildMap(indexGraph(chainGraph()));
    expect(mapIds(map)).toEqual(expect.arrayContaining([
      'finding:f1', 'finding:f2', 'task:t1', 'task:t2', 'rec:rec1',
    ]));
    expect(hasEdge(map, 'task:t1', 'finding:f1', 'grew_from')).toBe(true);
    expect(hasEdge(map, 'task:t1', 'finding:f2', 'answered_by')).toBe(true);
    expect(hasEdge(map, 'task:t2', 'task:t1', 'supersedes')).toBe(true);
    expect(hasEdge(map, 'rec:rec1', 'finding:f2', 'recommends_on')).toBe(true);
  });

  it('labels every link in words, reading along the arrow', () => {
    // The label is read source-label-target as the canvas draws it, and the
    // canvas draws every link oldest-first — so a finding OPENED a thread and
    // an older thread was CONTINUED BY a newer one, not the other way round.
    const map = buildMap(indexGraph(chainGraph()));
    const labels = new Set(map.edges.map((e) => e.label));
    expect(labels).toContain('Opened');
    expect(labels).toContain('Answered by');
    expect(labels).toContain('Continued by');
    expect(labels).toContain('Drove');
    map.edges.forEach((e) => {
      expect(e.label).toMatch(/^[A-Z]/);
      expect(e.label).not.toContain('_');
    });
  });

  it('keeps archived findings on the map rather than hiding them', () => {
    const map = buildMap(indexGraph(chainGraph()));
    const archived = map.nodes.find((n) => n.id === 'finding:f3');
    expect(archived).toBeDefined();
    expect(archived!.archived).toBe(true);
  });

  it('is unbothered by a cycle — a graph is what the data is', () => {
    const graph = chainGraph();
    graph.edges.push({ from: 'task:t1', to: 'task:t2', kind: 'supersedes' });
    const map = buildMap(indexGraph(graph));
    expect(hasEdge(map, 'task:t1', 'task:t2', 'supersedes')).toBe(true);
    expect(hasEdge(map, 'task:t2', 'task:t1', 'supersedes')).toBe(true);
    expect(map.nodes.length).toBeLessThan(20);
  });

  it('pulls in what a link reaches, so a chain is never shown with a hole', () => {
    // Filtered to one area: the thread that finding opened, the finding that
    // answered the thread and the recommendation must come with it.
    const map = buildMap(indexGraph(chainGraph()), { area: 'inventory' });
    expect(mapIds(map)).toEqual(expect.arrayContaining([
      'finding:f1', 'task:t1', 'finding:f2', 'rec:rec1',
    ]));
  });

  it('never draws a link to a node it did not draw', () => {
    const map = buildMap(indexGraph(chainGraph()), { severity: 'high' });
    const drawn = new Set(map.nodes.map((n) => n.id));
    map.edges.forEach((e) => {
      expect(drawn.has(e.source)).toBe(true);
      expect(drawn.has(e.target)).toBe(true);
    });
  });

  it('caps a large investigation, keeps the worst, and says what it left out', () => {
    const graph: LedgerGraph = {
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [], truncated: noTruncation,
    };
    for (let i = 0; i < MAX_MAP_NODES + 50; i += 1) {
      graph.findings.push({
        node_id: `finding:f${i}`, id: `f${i}`, name: `finding ${i}`,
        severity: i < 10 ? 'critical' : 'low', status: 'confirmed', seen_count: 1,
      });
    }
    const map = buildMap(indexGraph(graph));
    expect(map.nodes).toHaveLength(MAX_MAP_NODES);
    expect(map.truncated).toBe(true);
    expect(map.totalNodes).toBe(MAX_MAP_NODES + 50);
    expect(map.nodes.slice(0, 10).every((n) => n.severity === 'critical')).toBe(true);
  });

  it('gives each node a readable title, kind and subtitle', () => {
    const map = buildMap(indexGraph(chainGraph()));
    const finding = map.nodes.find((n) => n.id === 'finding:f1')!;
    expect(finding.kind).toBe('finding');
    expect(finding.title).toBe('Dead stock in a few sellers');
    expect(finding.meta).toContain('Inventory');
    expect(finding.meta).toContain('runs 1, 2');

    const thread = map.nodes.find((n) => n.id === 'task:t1')!;
    expect(thread.kind).toBe('task');
    expect(thread.meta).toContain('Hypothesis');
    expect(thread.meta).toContain('Opened in run 1');
    expect(thread.meta).toContain('Closed in run 2');
  });
});

describe('filters', () => {
  it('filters roots by area, severity, status and date', () => {
    const idx = indexGraph(chainGraph());
    expect(mapIds(buildMap(idx, { area: 'inventory' }))).toEqual(
      expect.arrayContaining(['finding:f1']),
    );
    expect(mapIds(buildMap(idx, { area: 'inventory' }))).not.toContain('finding:f3');
    expect(mapIds(buildMap(idx, { severity: 'low' }))).toContain('finding:f3');
    expect(mapIds(buildMap(idx, { status: 'resolved' }))).toContain('finding:f3');
    expect(mapIds(buildMap(idx, { from: '2026-05-03' }))).not.toContain('finding:f3');
    expect(mapIds(buildMap(idx, { to: '2026-05-01' }))).not.toContain('finding:f1');
  });

  it('"changed in the last run" keeps only what the latest run touched', () => {
    const idx = indexGraph(chainGraph());
    const roots = mapIds(buildMap(idx, { changedSinceLastRun: true }));
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
    expect(mapIds(buildMap(idx, { changedSinceLastRun: true }))).toContain('finding:f3');
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
  it('a finding links to the newest sighting whose evidence the response carries', () => {
    const idx = indexGraph(chainGraph());
    const f1 = idx.findings.get('finding:f1')!;
    expect(findingEvidenceLink('p1', f1, idx)).toBe('/projects/p1/discoveries/d2/insights/ins3');
  });

  it('a finding with no usable sighting has no link rather than a broken one', () => {
    const idx = indexGraph(chainGraph());
    expect(findingEvidenceLink('p1', { node_id: 'finding:x', id: 'x', seen_count: 0 }, idx)).toBeNull();
    expect(findingEvidenceLink('p1', {
      node_id: 'finding:x', id: 'x', seen_count: 1,
      sightings: [{ run_id: 'r1', discovery_id: '', date: '2026-05-01T09:00:00Z' }],
    }, idx)).toBeNull();
  });

  it('a finding whose evidence has been deleted has no link', () => {
    // The graph keeps the sighting and marks the insight stub missing. A URL
    // built from the retained ids opens a not-found page.
    const graph = chainGraph();
    graph.insights = graph.insights.map((i) => (
      i.id === 'ins3' ? { ...i, missing: true, discovery_id: undefined } : i));
    const idx = indexGraph(graph);
    const f1 = idx.findings.get('finding:f1')!;
    // It falls back to the older sighting, whose evidence is still there.
    expect(findingEvidenceLink('p1', f1, idx)).toBe('/projects/p1/discoveries/d1/insights/ins1');
    // And with none of it left, no link at all.
    const none = indexGraph({ ...graph, insights: [] });
    expect(findingEvidenceLink('p1', none.findings.get('finding:f1')!, none)).toBeNull();
  });

  it('"changed in the last run" matches a run identified only by its discovery', () => {
    // A run whose document has aged out is drawn as a disc: node and has no
    // run id, so matching only on that would reject every finding while the
    // filter still looked available.
    const graph: LedgerGraph = {
      runs: [{ node_id: 'disc:d9', discovery_id: 'd9', date: '2026-05-09T09:00:00Z', new_findings: 1, total_findings: 1, marginal_ratio: 1 }],
      findings: [{
        node_id: 'finding:f9', id: 'f9', name: 'Seen by that run', severity: 'high',
        status: 'confirmed', seen_count: 1,
        sightings: [{ run_id: '', discovery_id: 'd9', date: '2026-05-09T09:00:00Z' }],
      }],
      tasks: [], insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [], truncated: noTruncation,
    };
    const idx = indexGraph(graph);
    expect(findingChangedInRun(idx.findings.get('finding:f9')!, undefined, 'd9')).toBe(true);
    expect(mapIds(buildMap(idx, { changedSinceLastRun: true }))).toContain('finding:f9');
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
      // Sentence case, and the run named in words: the trail reads as a
      // sentence rather than as the tokens it is stored as.
      'Confirmed in run 1, “first seen”',
      'Changed in run 2, “affected count moved from 12 to 31”',
    ]);
  });

  it('names a status move by its discovery when the run id does not resolve', () => {
    // A run whose document has aged out is drawn as a `disc:` node. An entry
    // carrying only the discovery still names a run the reader can see on the
    // map, so calling it "an earlier run" throws away attribution that is
    // right there.
    const graph = chainGraph();
    graph.runs = [
      { node_id: 'disc:d1', discovery_id: 'd1', date: '2026-05-01T09:00:00Z', new_findings: 1, total_findings: 1, marginal_ratio: 1 },
    ];
    const f1 = graph.findings.find((f) => f.id === 'f1')!;
    f1.status_history = [
      { status: 'confirmed', run_id: 'r-aged-out', discovery_id: 'd1', at: '2026-05-01T09:00:00Z', source: 'reflection', reason: 'first seen' },
    ];
    expect(statusTrail(indexGraph(graph), f1)).toEqual(['Confirmed in run 1, “first seen”']);

    // With neither resolvable, it stays honest.
    f1.status_history = [
      { status: 'confirmed', run_id: 'gone', at: '2026-05-01T09:00:00Z', source: 'reflection' },
    ];
    expect(statusTrail(indexGraph(graph), f1)[0]).toMatch(/^Confirmed in /);
  });

  it('is empty for a finding with no history, and names an unknown run honestly', () => {
    const idx = indexGraph(chainGraph());
    expect(statusTrail(idx, { node_id: 'finding:x', id: 'x', seen_count: 0 })).toEqual([]);
    expect(statusTrail(idx, {
      node_id: 'finding:x', id: 'x', seen_count: 1,
      status_history: [{ status: 'confirmed', at: '2026-05-01T09:00:00Z' }],
    })).toEqual(['Confirmed in an earlier run']);
  });
});

describe('buildStories', () => {
  it('reads each run as a story, newest first', () => {
    const stories = buildStories(indexGraph(chainGraph()));
    expect(stories.map((s) => s.run.run_id)).toEqual(['r2', 'r1']);

    const r2 = stories[0];
    expect(r2.newFindings.map((s) => s.finding.id)).toEqual(['f2']);
    expect(r2.seenAgain.map((s) => s.finding.id)).toEqual(['f1']);
    expect(r2.closedTasks.map((t) => t.id)).toEqual(['t1']);
    expect(r2.openedTasks.map((t) => t.id)).toEqual(['t2']);
    expect(r2.learnings.map((l) => l.id)).toEqual(['l1']);
    expect(r2.proposals.map((p) => p.id)).toEqual(['p1']);
    expect(r2.highCount).toBe(1); // the critical finding

    const r1 = stories[1];
    expect(r1.newFindings.map((s) => s.finding.id).sort()).toEqual(['f1', 'f3']);
    expect(r1.openedTasks.map((t) => t.id)).toEqual(['t1']);
  });

  it("reads each run by its own sighting, not the finding's current state", () => {
    // A finding first seen as low and later raised to high must not be
    // reported as high in the run that found it, nor link to SQL that run
    // never wrote.
    const graph = chainGraph();
    const f1 = graph.findings.find((f) => f.id === 'f1')!;
    f1.severity = 'high'; // today's state
    f1.sightings = [
      { run_id: 'r1', discovery_id: 'd1', date: '2026-05-01T09:00:00Z', severity: 'low', insight_id: 'ins1' },
      { run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', severity: 'high', insight_id: 'ins3' },
    ];
    const stories = buildStories(indexGraph(graph));
    const r1 = stories.find((s) => s.run.run_id === 'r1')!;
    const idx = indexGraph(graph);
    const asFound = r1.newFindings.find((s) => s.finding.id === 'f1')!;
    expect(asFound.severity).toBe('low');
    expect(sightingEvidenceLink('p1', asFound, idx)).toBe('/projects/p1/discoveries/d1/insights/ins1');
    // And the later run reports what IT saw.
    const r2 = stories.find((s) => s.run.run_id === 'r2')!;
    const asSeen = r2.seenAgain.find((s) => s.finding.id === 'f1')!;
    expect(asSeen.severity).toBe('high');
    expect(sightingEvidenceLink('p1', asSeen, idx)).toBe('/projects/p1/discoveries/d2/insights/ins3');

    // A sighting the backfill recovered names its run but not its insight.
    // The finding's evidence from that same run is what it meant...
    const recovered = { ...asFound, sighting: { ...asFound.sighting!, insight_id: undefined } };
    expect(sightingEvidenceLink('p1', recovered, idx)).toBe('/projects/p1/discoveries/d1/insights/ins1');

    // ...but a run with no evidence of its own borrows none: a card that
    // linked to a later run's SQL would present it as what this run found.
    const elsewhere = {
      ...recovered,
      finding: { ...recovered.finding, source_insight_ids: ['ins3'] },
    };
    expect(sightingEvidenceLink('p1', elsewhere, idx)).toBeNull();

    // Two pieces of evidence from the one run cannot be told apart either.
    const ambiguous = {
      ...recovered,
      sighting: { ...recovered.sighting!, discovery_id: 'd2' },
      finding: { ...recovered.finding, source_insight_ids: ['ins2', 'ins3'] },
    };
    expect(sightingEvidenceLink('p1', ambiguous, idx)).toBeNull();

    // A run with no sighting at all — a clipped trail, or a project the
    // backfill never reached — is scoped to its own discovery just the same,
    // and never falls back to whatever the finding last produced.
    const noSighting = { finding: asFound.finding, runDiscoveryId: 'd1' };
    expect(sightingEvidenceLink('p1', noSighting, idx))
      .toBe('/projects/p1/discoveries/d1/insights/ins1');
    const noSightingElsewhere = {
      finding: { ...asFound.finding, source_insight_ids: ['ins3'] },
      runDiscoveryId: 'd1',
    };
    expect(sightingEvidenceLink('p1', noSightingElsewhere, idx)).toBeNull();
    // And with nothing at all to scope it to, there is no link.
    expect(sightingEvidenceLink('p1', { finding: asFound.finding }, idx)).toBeNull();
  });

  it('numbers a run by the whole history, not by its row in a windowed response', () => {
    // The response carries a window. Counting its rows would call the twelfth
    // run of an investigation "Run 1" and renumber it whenever the window
    // moved, so the server sends the absolute number and it wins.
    const graph = chainGraph();
    graph.runs[0].ordinal = 11;
    graph.runs[1].ordinal = 12;
    const idx = indexGraph(graph);
    expect(runLabel(idx, 'run:r1')).toBe('Run 11');
    expect(runLabel(idx, 'run:r2')).toBe('Run 12');

    // An API that predates the field still gets date order out of the rows.
    const older = chainGraph();
    expect(runLabel(indexGraph(older), 'run:r1')).toBe('Run 1');
  });

  it('reads a run that recorded two sightings by its last word', () => {
    // One run writes two sightings when two of its insights consolidate into
    // one finding. The later one is what that run settled on.
    const graph = chainGraph();
    const f2 = graph.findings.find((f) => f.id === 'f2')!;
    f2.source_insight_ids = ['ins2', 'ins3'];
    f2.sightings = [
      { run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', severity: 'low', insight_id: 'ins2' },
      { run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:30:00Z', severity: 'critical', insight_id: 'ins3' },
    ];

    const idx = indexGraph(graph);
    const r2 = buildStories(idx).find((st) => st.run.run_id === 'r2')!;
    const seen = r2.newFindings.find((x) => x.finding.id === 'f2')!;
    expect(seen.severity).toBe('critical');
    expect(r2.highCount).toBe(1);
    // And the link is to the evidence it settled on, not the one it revised.
    expect(sightingEvidenceLink('p1', seen, idx))
      .toBe('/projects/p1/discoveries/d2/insights/ins3');
  });

  it("reports no severity for a run that recorded none, rather than today's", () => {
    // A finding carries its CURRENT severity. Borrowing it for a run with no
    // sighting of its own — a trimmed trail, a legacy backfill — would report
    // that run as having found something high that a later run raised to
    // high, and would count it in that run's headline.
    const graph = chainGraph();
    const f2 = graph.findings.find((f) => f.id === 'f2')!;
    f2.severity = 'critical';
    f2.sightings = [{ run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z' }];

    const stories = buildStories(indexGraph(graph));
    const r2 = stories.find((st) => st.run.run_id === 'r2')!;
    const seen = r2.newFindings.find((x) => x.finding.id === 'f2')!;
    expect(seen.severity).toBeUndefined();
    expect(r2.highCount).toBe(0);

    // With the sighting's own severity, it counts.
    f2.sightings = [{ run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', severity: 'critical' }];
    const withIt = buildStories(indexGraph(graph)).find((st) => st.run.run_id === 'r2')!;
    expect(withIt.highCount).toBe(1);
  });

  it('names the runs a finding was seen in, however the sighting identifies them', () => {
    const graph = chainGraph();
    const f1 = graph.findings.find((f) => f.id === 'f1')!;
    expect(seenInRuns(indexGraph(graph), f1)).toBe('1, 2');

    // A sighting the backfill recovered carries the discovery, not the run id.
    // The run whose document survived is a `run:` node, so looking for a
    // `disc:` node by that discovery finds nothing and the run goes unnamed.
    f1.sightings = (f1.sightings ?? []).map((s) => ({ ...s, run_id: '' }));
    expect(seenInRuns(indexGraph(graph), f1)).toBe('1, 2');

    // A sighting that names neither is not attributed to a run at all.
    f1.sightings = [{ date: '2026-05-01T09:00:00Z' }];
    expect(seenInRuns(indexGraph(graph), f1)).toBe('');
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

describe('the map stays finite', () => {
  /** A graph where every finding leads into every thread of the next stage —
   *  the shape whose PATHS multiply even though its node count does not. */
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

  it('draws one node per node, however many paths reach it', () => {
    // The tree this replaced expanded a node once per PATH, so this 67-node
    // graph became millions. A map has no paths to multiply.
    const graph = fanOutGraph(6, 10);
    const payloadNodes = graph.findings.length + graph.tasks.length;
    const started = Date.now();
    const map = buildMap(indexGraph(graph));
    expect(map.nodes).toHaveLength(payloadNodes);
    expect(new Set(map.nodes.map((n) => n.id)).size).toBe(payloadNodes);
    expect(Date.now() - started).toBeLessThan(2000);
  });

  it('keeps a component whose nodes all claim each other', () => {
    // A thread that re-checks a finding and is answered by that same finding
    // is a valid two-node cycle. A tree had no root for it and dropped the
    // whole component; a map just draws both links.
    const graph: LedgerGraph = {
      runs: [],
      findings: [{ node_id: 'finding:f1', id: 'f1', name: 'Recurring', severity: 'high', status: 'confirmed', seen_count: 2 }],
      tasks: [{
        node_id: 'task:t1', id: 't1', title: 'Re-check it', text: 'recheck',
        kind: 'next_task', status: 'done',
        origin_finding_ids: ['f1'], resolved_by_finding_ids: ['f1'],
      }],
      insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [
        { from: 'task:t1', to: 'finding:f1', kind: 'grew_from' },
        { from: 'task:t1', to: 'finding:f1', kind: 'answered_by' },
      ],
      truncated: noTruncation,
    };
    const map = buildMap(indexGraph(graph));
    expect(mapIds(map)).toEqual(['finding:f1', 'task:t1']);
    expect(map.edges).toHaveLength(2);
  });

  it('draws the threads on a project where no finding links to any of them', () => {
    // Every project that predates this provenance is this shape: real threads,
    // no finding-to-thread edges. Seeding the walk from findings alone drew a
    // map with not one thread on it.
    const graph: LedgerGraph = {
      runs: [],
      findings: [{ node_id: 'finding:f1', id: 'f1', name: 'Dead stock', area: 'misc', severity: 'high', status: 'confirmed', seen_count: 1 }],
      tasks: [
        { node_id: 'task:t1', id: 't1', title: 'Check the sellers', text: 'check', kind: 'next_task', status: 'done' },
        { node_id: 'task:t2', id: 't2', title: 'Check them again', text: 'again', kind: 'next_task', status: 'open', supersedes: 't1' },
      ],
      insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [{ from: 'task:t2', to: 'task:t1', kind: 'supersedes' }],
      truncated: noTruncation,
    };
    const map = buildMap(indexGraph(graph));
    expect(mapIds(map)).toEqual(['finding:f1', 'task:t1', 'task:t2']);
    expect(hasEdge(map, 'task:t2', 'task:t1', 'supersedes')).toBe(true);

    // Under a filter the map answers a question about findings, so a thread
    // nothing matching reaches is not part of that answer.
    expect(mapIds(buildMap(indexGraph(graph), { severity: 'high' }))).toEqual(['finding:f1']);
  });

  it('spends the node budget on whole chains, not on findings alone', () => {
    // Findings outrank threads, so taking the highest-ranked MAX_MAP_NODES
    // nodes on a project with that many findings kept every finding and not
    // one link — a map of a few hundred cards joined by nothing.
    const graph: LedgerGraph = {
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [], truncated: noTruncation,
    };
    for (let i = 0; i < MAX_MAP_NODES + 20; i += 1) {
      graph.findings.push({
        node_id: `finding:f${i}`, id: `f${i}`, name: `finding ${i}`,
        severity: 'high', status: 'confirmed', seen_count: 1,
      });
    }
    // One critical finding, with a thread hanging off it.
    graph.findings[0].severity = 'critical';
    graph.tasks.push({ node_id: 'task:t1', id: 't1', title: 'Dig in', text: 'dig', kind: 'next_task', status: 'open' });
    graph.edges.push({ from: 'task:t1', to: 'finding:f0', kind: 'grew_from' });

    const map = buildMap(indexGraph(graph));
    expect(map.nodes.length).toBeLessThanOrEqual(MAX_MAP_NODES);
    expect(map.truncated).toBe(true);
    // The chain survives whole: the thread is drawn WITH the finding it grew
    // from, and the link between them is still there.
    expect(mapIds(map)).toContain('finding:f0');
    expect(mapIds(map)).toContain('task:t1');
    expect(hasEdge(map, 'task:t1', 'finding:f0', 'grew_from')).toBe(true);
  });

  it('keeps a clipped chain connected rather than keeping its best nodes', () => {
    // One chain bigger than the whole budget. Findings outrank threads, so a
    // ranked slice of it is every finding and not one thread — a map with no
    // links at all, which is the one thing it must never be.
    const graph: LedgerGraph = {
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [], truncated: noTruncation,
    };
    // finding 0 → task 0 → finding 1 → task 1 → ... one long alternating chain.
    const links = MAX_MAP_NODES;
    for (let i = 0; i <= links; i += 1) {
      graph.findings.push({
        node_id: `finding:f${i}`, id: `f${i}`, name: `finding ${i}`,
        severity: i === 0 ? 'critical' : 'high', status: 'confirmed', seen_count: 1,
      });
      if (i < links) {
        graph.tasks.push({
          node_id: `task:t${i}`, id: `t${i}`, title: `thread ${i}`, text: `thread ${i}`,
          kind: 'next_task', status: 'open',
        });
        graph.edges.push({ from: `task:t${i}`, to: `finding:f${i}`, kind: 'grew_from' });
        graph.edges.push({ from: `finding:f${i + 1}`, to: `task:t${i}`, kind: 'answered_by' });
      }
    }

    const map = buildMap(indexGraph(graph));
    expect(map.nodes.length).toBeLessThanOrEqual(MAX_MAP_NODES);
    expect(map.truncated).toBe(true);
    // Threads survive, and so do the links that make it a chain.
    expect(map.nodes.some((n) => n.kind === 'task')).toBe(true);
    expect(map.edges.length).toBeGreaterThan(0);
    // Every drawn node is reachable from the critical finding it started at.
    const drawn = new Set(mapIds(map));
    expect(drawn.has('finding:f0')).toBe(true);
    expect(hasEdge(map, 'task:t0', 'finding:f0', 'grew_from')).toBe(true);
  });

  it('attributes each card to the run whose work it was', () => {
    // The map is laid out in time, so every card has to know which run it
    // belongs to. A finding belongs to the run that FOUND it, not the latest
    // one to see it again: seeing it again is an edge, not a relocation.
    const graph = chainGraph();
    const idx = indexGraph(graph);
    expect(producedIn(idx, 'finding:f1')).toBe('run:r1'); // first seen in run 1
    expect(producedIn(idx, 'finding:f2')).toBe('run:r2');
    expect(producedIn(idx, 'task:t1')).toBe('run:r1');    // created_run_id
    expect(producedIn(idx, 'rec:rec1')).toBe('run:r2');   // through its discovery

    // The map carries it through, so a band can be built from it.
    const map = buildMap(idx);
    const byId = new Map(map.nodes.map((n) => [n.id, n]));
    expect(byId.get('finding:f1')?.runOrdinal).toBe(1);
    expect(byId.get('finding:f2')?.runOrdinal).toBe(2);

    // A finding nothing can date is attributed to no run rather than guessed
    // into one — it belongs in the band that says so.
    const orphan = { ...graph };
    orphan.findings = graph.findings.map((f) => (f.id === 'f1' ? { ...f, sightings: [] } : f));
    const orphaned = buildMap(indexGraph(orphan));
    const f1 = orphaned.nodes.find((n) => n.id === 'finding:f1');
    expect(f1?.runOrdinal).toBeUndefined();
  });

  it('will not file a finding under a run that only saw it again', () => {
    // A trail shorter than the counter has lost its oldest entries: the
    // backfill writes one sighting for a finding seen ten times, and the
    // writer caps a trail at fifty. Its earliest surviving sighting is then
    // NOT the run that found it, and putting the card in that run's column
    // would credit it with work it did not do.
    const graph = chainGraph();
    const f1 = graph.findings.find((f) => f.id === 'f1')!;
    f1.seen_count = 6;
    f1.sightings = [
      { run_id: 'r2', discovery_id: 'd2', date: '2026-05-03T09:00:00Z', severity: 'high' },
    ];
    expect(producedIn(indexGraph(graph), 'finding:f1')).toBe('');

    // A complete trail is attributed to its first run, as before.
    f1.seen_count = 1;
    expect(producedIn(indexGraph(graph), 'finding:f1')).toBe('run:r2');
  });

  it('clips a huge chain from the card the filter matched', () => {
    // A filter whose only match sits at the far end of a chain bigger than
    // the whole budget. Clipping from the chain's best-ranked card instead
    // would drop the one thing the reader asked for — and narrowing the
    // filter further would keep producing the same clipped chain.
    const graph: LedgerGraph = {
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [], truncated: noTruncation,
    };
    const links = MAX_MAP_NODES;
    for (let i = 0; i <= links; i += 1) {
      graph.findings.push({
        node_id: `finding:f${i}`, id: `f${i}`, name: `finding ${i}`,
        // The needle is last and lowest-ranked; everything else outranks it.
        area: i === links ? 'needle' : 'haystack',
        severity: i === links ? 'low' : 'critical',
        status: 'confirmed', seen_count: 1,
      });
      if (i < links) {
        graph.tasks.push({
          node_id: `task:t${i}`, id: `t${i}`, title: `thread ${i}`, text: `thread ${i}`,
          kind: 'next_task', status: 'open',
        });
        graph.edges.push({ from: `task:t${i}`, to: `finding:f${i}`, kind: 'grew_from' });
        graph.edges.push({ from: `finding:f${i + 1}`, to: `task:t${i}`, kind: 'answered_by' });
      }
    }

    const map = buildMap(indexGraph(graph), { area: 'needle' });
    expect(map.nodes.length).toBeLessThanOrEqual(MAX_MAP_NODES);
    expect(mapIds(map)).toContain(`finding:f${links}`);
  });

  it('excludes a filtered-out finding that nothing else reaches', () => {
    const graph: LedgerGraph = {
      runs: [],
      findings: [{ node_id: 'finding:f1', id: 'f1', name: 'Low', area: 'misc', severity: 'low', status: 'confirmed', seen_count: 1 }],
      tasks: [], insights: [], recommendations: [], learnings: [], proposals: [],
      edges: [], truncated: noTruncation,
    };
    expect(buildMap(indexGraph(graph), { severity: 'critical' }).nodes).toHaveLength(0);
  });
});

describe('threadWorkedIn', () => {
  /** chainGraph plus a third run, so "the latest run that pursued it" has
   *  something to choose between. */
  function threeRuns(): LedgerGraph {
    const graph = chainGraph();
    graph.runs.push({
      node_id: 'run:r3', run_id: 'r3', discovery_id: 'd3', date: '2026-05-05T09:00:00Z',
      new_findings: 0, total_findings: 3, marginal_ratio: 0,
    });
    return graph;
  }

  it('is the run that closed the thread, where its answer is', () => {
    const worked = threadWorkedIn(indexGraph(chainGraph()), 'task:t1');
    expect(worked).toEqual({ ref: 'run:r2', outcome: 'answered', pursuedIn: [] });
  });

  it('is the latest run that pursued a thread still open', () => {
    const graph = threeRuns();
    graph.edges.push({ from: 'run:r3', to: 'task:t2', kind: 'pursued' });
    expect(threadWorkedIn(indexGraph(graph), 'task:t2'))
      .toEqual({ ref: 'run:r3', outcome: 'pursued', pursuedIn: [3] });
  });

  it('reads the pursuit off the thread when the payload carries no edge for it', () => {
    const graph = threeRuns();
    graph.tasks[1] = { ...graph.tasks[1], pursued_in_run_ids: ['r3'] };
    expect(threadWorkedIn(indexGraph(graph), 'task:t2')?.ref).toBe('run:r3');
  });

  it('remembers the runs that pursued a thread before the one that closed it', () => {
    const graph = threeRuns();
    const t1 = graph.tasks[0];
    graph.tasks[0] = { ...t1, closed_run_id: 'r3', closed_discovery_id: 'd3' };
    graph.edges = graph.edges.map((e) => (e.kind === 'closed' ? { ...e, from: 'run:r3' } : e));
    graph.edges.push({ from: 'run:r2', to: 'task:t1', kind: 'pursued' });
    expect(threadWorkedIn(indexGraph(graph), 'task:t1'))
      .toEqual({ ref: 'run:r3', outcome: 'answered', pursuedIn: [2] });
  });

  it('tells a dropped thread and one closed with no answer apart from an answered one', () => {
    const dropped = chainGraph();
    dropped.tasks[0] = { ...dropped.tasks[0], status: 'dropped' };
    expect(threadWorkedIn(indexGraph(dropped), 'task:t1')?.outcome).toBe('dropped');

    const unanswered = chainGraph();
    unanswered.tasks[0] = { ...unanswered.tasks[0], resolved_by_finding_ids: [] };
    unanswered.edges = unanswered.edges.filter((e) => e.kind !== 'answered_by');
    expect(threadWorkedIn(indexGraph(unanswered), 'task:t1')?.outcome).toBe('closed');
  });

  it('never names the run that opened the thread, or one it cannot place', () => {
    // A thread is opened at the end of a run, so the run that opened it cannot
    // have taken it up — a record saying so places the thread nowhere useful.
    const own = chainGraph();
    own.tasks[1] = { ...own.tasks[1], pursued_in_run_ids: ['r2'] };
    expect(threadWorkedIn(indexGraph(own), 'task:t2')).toBeNull();

    const ghost = chainGraph();
    ghost.tasks[1] = { ...ghost.tasks[1], pursued_in_run_ids: ['gone'] };
    expect(threadWorkedIn(indexGraph(ghost), 'task:t2')).toBeNull();

    expect(threadWorkedIn(indexGraph(chainGraph()), 'finding:f1')).toBeNull();
  });

  it('carries onto the map, with a line saying what came of the thread', () => {
    const map = buildMap(indexGraph(chainGraph()));
    const t1 = map.nodes.find((n) => n.id === 'task:t1')!;
    expect(t1.workedIn).toBe('run:r2');
    expect(t1.workedInOrdinal).toBe(2);
    expect(t1.outcome).toBe('answered');
    expect(t1.workedMeta).toBe('Answered in run 2');
    // A thread nothing took up yet has none of it.
    const t2 = map.nodes.find((n) => n.id === 'task:t2')!;
    expect(t2.workedIn).toBeUndefined();
  });
});
