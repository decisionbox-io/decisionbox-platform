/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import LedgerPage from '@/app/projects/[id]/ledger/page';
import { LedgerGraph, LedgerView } from '@/lib/api';

jest.mock('next/navigation', () => ({ useParams: () => ({ id: 'p1' }) }));
jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true, default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

// @xyflow/react measures a real container and jsdom has none, so the canvas is
// stood in for by something that renders the nodes it is given. The layout is
// the library's problem; the projection behind it is tested in
// ledgerGraph.test.ts, and the node markup still renders through this.
jest.mock('@xyflow/react', () => {
  const React = jest.requireActual('react');
  return {
    __esModule: true,
    ReactFlow: ({ nodes, nodeTypes, onNodeClick }: {
      nodes: { id: string; data: unknown }[];
      nodeTypes: Record<string, React.ComponentType<{ data: unknown }>>;
      onNodeClick: (e: unknown, node: { id: string }) => void;
    }) => {
      const Node = nodeTypes.investigation;
      return (
        <div data-testid="map-canvas">
          {nodes.map((n) => (
            <div key={n.id} onClick={(e) => onNodeClick(e, n)}>
              <Node data={n.data} />
            </div>
          ))}
        </div>
      );
    },
    ReactFlowProvider: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
    Background: () => null,
    Controls: () => null,
    MiniMap: () => null,
    Handle: () => null,
    Position: { Left: 'left', Right: 'right' },
    BackgroundVariant: { Dots: 'dots' },
    useNodesState: (initial: unknown[]) => {
      const [v, setV] = React.useState(initial);
      React.useEffect(() => setV(initial), [initial]);
      return [v, setV, jest.fn()];
    },
    useEdgesState: (initial: unknown[]) => {
      const [v, setV] = React.useState(initial);
      React.useEffect(() => setV(initial), [initial]);
      return [v, setV, jest.fn()];
    },
    useReactFlow: () => ({ fitView: jest.fn() }),
  };
});

const getLedger = jest.fn();
const getEvolutionSettings = jest.fn();
const listPackProposals = jest.fn();
const decidePackProposal = jest.fn();
const getLedgerGraph = jest.fn();

jest.mock('@/lib/api', () => ({
  ApiError: class extends Error { status = 0; constructor(m: string, s: number) { super(m); this.status = s; } },
  api: {
    getProject: jest.fn().mockResolvedValue({ id: 'p1', name: 'Acme' }),
    getLedger: (...a: unknown[]) => getLedger(...a),
    getEvolutionSettings: (...a: unknown[]) => getEvolutionSettings(...a),
    listPackProposals: (...a: unknown[]) => listPackProposals(...a),
    decidePackProposal: (...a: unknown[]) => decidePackProposal(...a),
    getLedgerGraph: (...a: unknown[]) => getLedgerGraph(...a),
  },
}));

const ledger: LedgerView = {
  coverage: { explored_tables: ['ds.orders', 'ds.customers'], total_tables: 10, summary: 'orders covered; events untouched' },
  convergence: [{ run_id: 'r1', new_findings: 3, total_findings: 5, marginal_ratio: 0.6, date: '2026-01-01T00:00:00Z' }],
  findings: [
    { id: 'f1', area: 'churn', name: 'High EU churn', severity: 'high', status: 'changed', key_metric: 'affected=300', seen_count: 3, first_seen: '', last_seen: '' },
  ],
  tasks: [{ id: 't1', text: 'explore the events tables', kind: 'next_task', status: 'open' }],
};

function wrap() {
  return render(<MantineProvider><LedgerPage /></MantineProvider>);
}

/** Scopes a query to the Story / Threads region. The same finding appears both
 *  there and in the findings table at the bottom of the page, so an unscoped
 *  query cannot say which one it found. */
function view() {
  return within(screen.getByRole('region', { name: 'Investigation view' }));
}

// The investigation graph: one finding, the thread it opened, and the run that
// closed that thread pointing at the finding which answered it.
const graph: LedgerGraph = {
  runs: [
    { node_id: 'run:r1', run_id: 'r1', discovery_id: 'd1', date: '2026-01-01T00:00:00Z', new_findings: 1, total_findings: 1, marginal_ratio: 1 },
    { node_id: 'run:r2', run_id: 'r2', discovery_id: 'd2', date: '2026-01-05T00:00:00Z', new_findings: 1, total_findings: 2, marginal_ratio: 0.5 },
  ],
  findings: [
    {
      node_id: 'finding:f1', id: 'f1', name: 'High EU churn', area: 'churn', severity: 'high',
      status: 'changed', seen_count: 2, first_seen: '2026-01-01T00:00:00Z', last_seen: '2026-01-05T00:00:00Z',
      source_insight_ids: ['ins1'],
      sightings: [
        { run_id: 'r1', discovery_id: 'd1', date: '2026-01-01T00:00:00Z', insight_id: 'ins1' },
        { run_id: 'r2', discovery_id: 'd2', date: '2026-01-05T00:00:00Z', insight_id: 'ins1' },
      ],
      status_history: [
        { status: 'confirmed', run_id: 'r1', at: '2026-01-01T00:00:00Z', source: 'reflection', reason: 'first seen' },
        { status: 'changed', run_id: 'r2', at: '2026-01-05T00:00:00Z', source: 'trend', reason: 'magnitude up 2.1x' },
      ],
    },
    {
      node_id: 'finding:f2', id: 'f2', name: 'Late deliveries cluster in one hub', area: 'logistics',
      severity: 'critical', status: 'confirmed', seen_count: 1,
      first_seen: '2026-01-05T00:00:00Z', last_seen: '2026-01-05T00:00:00Z',
      source_insight_ids: ['ins2'], answered_task_ids: ['t1'],
      sightings: [{ run_id: 'r2', discovery_id: 'd2', date: '2026-01-05T00:00:00Z', insight_id: 'ins2' }],
    },
  ],
  tasks: [
    {
      node_id: 'task:t1', id: 't1', title: 'Do those orders share a hub?', text: 'join orders to hubs',
      kind: 'hypothesis', status: 'done', origin_finding_ids: ['f1'], resolved_by_finding_ids: ['f2'],
      created_run_id: 'r1', closed_run_id: 'r2', closed_discovery_id: 'd2',
      closed_at: '2026-01-05T00:00:00Z', close_reason: 'yes, nine of twelve do',
    },
  ],
  insights: [
    { node_id: 'insight:ins1', id: 'ins1', discovery_id: 'd1', name: 'Churn signal', area: 'churn', severity: 'high' },
    { node_id: 'insight:ins2', id: 'ins2', discovery_id: 'd2', name: 'Hub signal', area: 'logistics', severity: 'critical' },
  ],
  recommendations: [
    { node_id: 'rec:rec1', id: 'rec1', discovery_id: 'd2', title: 'Re-route the hub overflow', priority: 1, related_insight_ids: ['ins2'] },
  ],
  learnings: [
    { node_id: 'learning:l1', id: 'l1', category: 'schema', note: 'hubs are keyed by code', relevance: 0.8, run_id: 'r2', discovery_id: 'd2' },
  ],
  proposals: [],
  edges: [
    { from: 'run:r1', to: 'finding:f1', kind: 'found' },
    { from: 'run:r2', to: 'finding:f1', kind: 'seen_in' },
    { from: 'run:r2', to: 'finding:f2', kind: 'found' },
    { from: 'finding:f1', to: 'insight:ins1', kind: 'evidenced_by' },
    { from: 'finding:f2', to: 'insight:ins2', kind: 'evidenced_by' },
    { from: 'task:t1', to: 'finding:f1', kind: 'grew_from' },
    { from: 'task:t1', to: 'finding:f2', kind: 'answered_by' },
    { from: 'run:r1', to: 'task:t1', kind: 'opened' },
    { from: 'run:r2', to: 'task:t1', kind: 'closed' },
    { from: 'rec:rec1', to: 'insight:ins2', kind: 'recommends_on' },
    { from: 'run:r2', to: 'learning:l1', kind: 'learned' },
  ],
  truncated: { runs: false, learnings: false, recommendations: false, finding_trails: false, task_pursuits: false },
};

/** The happy path: the ledger, the settings, no proposals, and the graph. */
function mockAll() {
  getLedger.mockResolvedValue(ledger);
  getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'auto', frontier_policy: 'balanced' });
  listPackProposals.mockResolvedValue([]);
  getLedgerGraph.mockResolvedValue(graph);
}

const emptyGraph: LedgerGraph = {
  runs: [], findings: [], tasks: [], insights: [], recommendations: [],
  learnings: [], proposals: [], edges: [],
  truncated: { runs: false, learnings: false, recommendations: false, finding_trails: false, task_pursuits: false },
};

beforeEach(() => {
  jest.clearAllMocks();
  // The tests that predate the investigation graph are about the sections the
  // untouched investigation-view endpoint feeds; an empty graph keeps them
  // about exactly that.
  getLedgerGraph.mockResolvedValue(emptyGraph);
});

describe('LedgerPage', () => {
  it('shows the not-available state when the ledger route 404s', async () => {
    const { ApiError } = jest.requireMock('@/lib/api');
    getLedger.mockRejectedValue(new ApiError('nope', 404));
    getEvolutionSettings.mockResolvedValue(null);
    wrap();
    expect(await screen.findByText(/Discovery Ledger not available/i)).toBeInTheDocument();
  });

  it('renders coverage, findings, tasks and a pending proposal', async () => {
    getLedger.mockResolvedValue(ledger);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'admin_approval', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([
      { id: 'pr1', project_id: 'p1', action: 'add_area', area_id: 'fraud', area_name: 'Fraud', rationale: 'fraud signals recur', status: 'proposed', created_at: '2026-01-01T00:00:00Z' },
    ]);
    wrap();
    expect(await screen.findByText('High EU churn')).toBeInTheDocument();
    expect(screen.getByText(/events untouched/)).toBeInTheDocument();
    expect(screen.getByText('explore the events tables')).toBeInTheDocument();
    expect(screen.getByText('fraud signals recur')).toBeInTheDocument();
  });

  it('renders (no crash) when the ledger exists but is empty — null slices from the API', async () => {
    // A project that has never reflected: the API returns 200 with null
    // coverage/convergence/findings/tasks (Go nil → JSON null). Regression for
    // "Cannot read properties of null (reading 'length')".
    getLedger.mockResolvedValue({
      coverage: { explored_tables: null, area_depth: null, total_tables: 0, summary: '' },
      convergence: null,
      findings: null,
      tasks: null,
    } as unknown as LedgerView);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    // The findings empty-state renders instead of a crash.
    expect(await screen.findByText(/No findings yet/i)).toBeInTheDocument();
  });

  it('says the table counts are the warehouse side only when the project has a cube', async () => {
    // A cube-shaped datasource has no tables, so it contributes nothing to
    // "Tables explored" or "Frontier". Every table covered then renders as
    // "Frontier 0", which reads as "nothing left to look at" on a project
    // where most of what can be queried has never been touched.
    getLedger.mockResolvedValue({
      ...ledger,
      coverage: {
        explored_tables: ['ds.orders', 'ds.customers'],
        total_tables: 2,
        explored_catalog_items: ['sessions', 'activeUsers'],
        total_catalog_items: 470,
        summary: 'the warehouse is tiled',
      },
    } as unknown as LedgerView);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    expect(await screen.findByText(/the counts above are the\s+warehouse side only/i)).toBeInTheDocument();
    expect(screen.getByText(/2 of its 470 metrics and dimensions/i)).toBeInTheDocument();
    expect(screen.getByText(/never fully explored/i)).toBeInTheDocument();
  });

  it('keeps recorded cube slices visible when the catalog size is unavailable', async () => {
    // A run whose cube catalog could not be read records a total of zero while
    // the slices earlier runs recorded are still carried. Testing only the
    // total would hide them — and print "of its 0 metrics" if it did not.
    getLedger.mockResolvedValue({
      ...ledger,
      coverage: {
        explored_tables: ['ds.orders'],
        total_tables: 2,
        explored_catalog_items: ['sessions', 'activeUsers'],
        total_catalog_items: 0,
        summary: '',
      },
    } as unknown as LedgerView);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    expect(await screen.findByText(/2 of its metrics and dimensions have been queried/i)).toBeInTheDocument();
    expect(screen.queryByText(/of its 0 metrics/i)).not.toBeInTheDocument();
  });

  it('does not mention a cube on an ordinary warehouse-only project', async () => {
    getLedger.mockResolvedValue(ledger);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    expect(await screen.findByText(/events untouched/)).toBeInTheDocument();
    expect(screen.queryByText(/cube-shaped datasource/i)).not.toBeInTheDocument();
  });

  it('keeps the findings detail collapsed until toggled', async () => {
    // Findings are advanced detail — the table is collapsed behind a "Show
    // detail" toggle so the top of the page stays focused on convergence + the
    // open threads. Clicking flips the control to "Hide detail".
    getLedger.mockResolvedValue(ledger);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    const toggle = await screen.findByText(/Show detail/i);
    fireEvent.click(toggle);
    expect(await screen.findByText(/Hide detail/i)).toBeInTheDocument();
  });

  it('expands a follow-up task to its closed parent (ancestors survive normalize)', async () => {
    // Regression: normalizeLedger dropped `ancestors`, so a follow-up showed
    // "Parent thread is no longer available." instead of the closed parent.
    getLedger.mockResolvedValue({
      ...ledger,
      tasks: [{ id: 't2', title: 'Refine churn drivers', text: 'dig into the churn drivers', kind: 'next_task', status: 'open', supersedes: 'p1' }],
      ancestors: [{ id: 'p1', title: 'Look at churn', text: 'look at churn broadly', kind: 'next_task', status: 'done' }],
    } as unknown as LedgerView);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    fireEvent.click(await screen.findByText(/follow-up/i));
    expect(await screen.findByText('Look at churn')).toBeInTheDocument();
    expect(screen.queryByText(/no longer available/i)).not.toBeInTheDocument();
  });

  it('expands a follow-up whose parent is still open and listed among the tasks', async () => {
    // A thread may supersede one that has not been closed — on real projects
    // that is the common case. The parent is then in `tasks`, not `ancestors`,
    // and looking only at `ancestors` reported every follow-up as orphaned.
    getLedger.mockResolvedValue({
      ...ledger,
      tasks: [
        { id: 't2', title: 'Refine churn drivers', text: 'dig into the churn drivers', kind: 'next_task', status: 'open', supersedes: 'p1' },
        { id: 'p1', title: 'Look at churn', text: 'look at churn broadly', kind: 'next_task', status: 'open' },
      ],
      ancestors: [],
    } as unknown as LedgerView);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'suggest_only', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    wrap();
    fireEvent.click(await screen.findByText(/follow-up/i));
    // The sentence must not call an open parent resolved.
    const intro = await screen.findByText(/Grew out of this earlier thread/i);
    const chain = intro.parentElement as HTMLElement;
    // The parent is named inside the expansion, not just in its own row.
    expect(within(chain).getByText('Look at churn')).toBeInTheDocument();
    expect(screen.queryByText(/no longer available/i)).not.toBeInTheDocument();
  });

  it('approves a pending proposal', async () => {
    getLedger.mockResolvedValue(ledger);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'admin_approval', frontier_policy: 'balanced' });
    listPackProposals
      .mockResolvedValueOnce([{ id: 'pr1', project_id: 'p1', action: 'add_area', area_id: 'fraud', rationale: 'r', status: 'proposed', created_at: '2026-01-01T00:00:00Z' }])
      .mockResolvedValueOnce([]);
    decidePackProposal.mockResolvedValue({});
    wrap();
    const approve = await screen.findByRole('button', { name: /Approve/i });
    fireEvent.click(approve);
    await waitFor(() => expect(decidePackProposal).toHaveBeenCalledWith('p1', 'pr1', 'approve'));
  });

  // --- The three levels (enterprise#389) ---

  it('opens on the Story level and reads each run back in plain language', async () => {
    mockAll();
    wrap();
    // One card per run, newest first, with what it did.
    expect(await screen.findByText('Run 2')).toBeInTheDocument();
    expect(screen.getByText('Run 1')).toBeInTheDocument();
    expect(screen.getByText(/found 1 new finding \(1 high or critical\), saw 1 again, closed 1 thread/i))
      .toBeInTheDocument();
    // The reason a thread closed — the line the flat page could not show.
    expect(screen.getByText(/yes, nine of twelve do/i)).toBeInTheDocument();
    // And the run's learning.
    expect(screen.getByText(/hubs are keyed by code/i)).toBeInTheDocument();
  });

  it('switches to the Map and draws the whole chain as cards', async () => {
    mockAll();
    wrap();
    fireEvent.click(await screen.findByText('Map'));
    await screen.findByText('Do those orders share a hub?');

    // The finding, the thread that grew out of it, the finding that answered
    // that thread, and the recommendation it drove — all on the canvas at
    // once, which is the point of a map over a collapsed tree.
    expect(view().getByText('High EU churn')).toBeInTheDocument();
    expect(view().getByText('Do those orders share a hub?')).toBeInTheDocument();
    expect(view().getByText('Late deliveries cluster in one hub')).toBeInTheDocument();
    expect(view().getByText('Re-route the hub overflow')).toBeInTheDocument();

    // Each card says what kind of thing it is, in words.
    expect(view().getAllByText('Finding').length).toBeGreaterThan(0);
    expect(view().getAllByText('Thread').length).toBeGreaterThan(0);
    expect(view().getAllByText('Recommendation').length).toBeGreaterThan(0);
  });

  it('opens a card and shows its evidence, with a way out to a new tab', async () => {
    mockAll();
    wrap();
    fireEvent.click(await screen.findByText('Map'));
    // The collapsed Findings section at the foot of the page carries the same
    // names, so the card has to be picked out of the canvas itself.
    const canvas = await screen.findByTestId('map-canvas');
    fireEvent.click(within(canvas).getByText('High EU churn'));

    // The panel carries the status trail the card deliberately leaves out...
    expect(await screen.findByText(/Changed in run 2, “magnitude up 2.1x”/)).toBeInTheDocument();
    // ...and a link to the evidence, openable in place or in a new tab.
    const link = screen.getByText(/Open the finding's evidence/i).closest('a');
    expect(link).toHaveAttribute('href', '/projects/p1/discoveries/d2/insights/ins1');
    expect(screen.getByLabelText(/Open the finding's evidence in a new tab/i))
      .toHaveAttribute('target', '_blank');
  });

  it('fetches the graph once, not per level switch', async () => {
    mockAll();
    wrap();
    await screen.findByText('Run 2');
    fireEvent.click(screen.getByText('Map'));
    await screen.findByText('Do those orders share a hub?');
    fireEvent.click(screen.getByText('Story'));
    await screen.findByText('Run 2');
    expect(getLedgerGraph).toHaveBeenCalledTimes(1);
  });

  it('stays usable when the graph route is unavailable', async () => {
    // An API that predates the graph route 404s. The rest of the page — which
    // is served by the untouched investigation-view endpoint — must be intact.
    getLedger.mockResolvedValue(ledger);
    getEvolutionSettings.mockResolvedValue({ project_id: 'p1', evolution_mode: 'auto', frontier_policy: 'balanced' });
    listPackProposals.mockResolvedValue([]);
    const { ApiError } = jest.requireMock('@/lib/api');
    getLedgerGraph.mockRejectedValue(new ApiError('nope', 404));
    wrap();
    expect(await screen.findByText(/does not serve the investigation graph yet/i)).toBeInTheDocument();
    // The stat cards and the open-threads queue still render.
    expect(screen.getByText('Tables explored')).toBeInTheDocument();
    expect(screen.getByText('explore the events tables')).toBeInTheDocument();
  });

  it('filters the map to what the last run changed', async () => {
    mockAll();
    wrap();
    fireEvent.click(await screen.findByText('Map'));
    await screen.findByText('Do those orders share a hub?');
    expect(view().getByText('High EU churn')).toBeInTheDocument();

    fireEvent.click(screen.getByLabelText(/Only what changed in the last run/i));
    // f1 was seen again in run 2, so it survives the filter.
    expect(await view().findByText('High EU churn')).toBeInTheDocument();
  });

  it('renders the Threads level for a project with an empty graph', async () => {
    getLedger.mockResolvedValue(ledger);
    getEvolutionSettings.mockResolvedValue(null);
    listPackProposals.mockResolvedValue([]);
    getLedgerGraph.mockResolvedValue({
      runs: [], findings: [], tasks: [], insights: [], recommendations: [],
      learnings: [], proposals: [], edges: [],
      truncated: { runs: false, learnings: false, recommendations: false, finding_trails: false, task_pursuits: false },
    });
    wrap();
    expect(await screen.findByText(/No runs have reflected yet/i)).toBeInTheDocument();
    fireEvent.click(screen.getByText('Map'));
    expect(await screen.findByText(/Nothing matches these filters/i)).toBeInTheDocument();
  });

  it('draws a cyclic thread chain without hanging', async () => {
    mockAll();
    getLedgerGraph.mockResolvedValue({
      ...graph,
      edges: [
        ...graph.edges,
        // t1 continues itself through a second thread, and that one back again.
        { from: 'task:t1', to: 'task:t9', kind: 'supersedes' },
        { from: 'task:t9', to: 'task:t1', kind: 'supersedes' },
      ],
      tasks: [
        ...graph.tasks,
        { node_id: 'task:t9', id: 't9', title: 'A loop', text: 'loops', kind: 'next_task', status: 'open' },
      ],
    });
    wrap();
    fireEvent.click(await screen.findByText('Map'));
    // A cycle was the shape that could hang a tree walk. A map has no paths to
    // walk: both threads are simply drawn, each once.
    expect(await screen.findByText('A loop')).toBeInTheDocument();
    expect(view().getByText('Do those orders share a hub?')).toBeInTheDocument();
  });
});
