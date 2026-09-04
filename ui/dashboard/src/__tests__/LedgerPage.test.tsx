/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import LedgerPage from '@/app/projects/[id]/ledger/page';
import { LedgerView } from '@/lib/api';

jest.mock('next/navigation', () => ({ useParams: () => ({ id: 'p1' }) }));
jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true, default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

const getLedger = jest.fn();
const getEvolutionSettings = jest.fn();
const listPackProposals = jest.fn();
const decidePackProposal = jest.fn();

jest.mock('@/lib/api', () => ({
  ApiError: class extends Error { status = 0; constructor(m: string, s: number) { super(m); this.status = s; } },
  api: {
    getProject: jest.fn().mockResolvedValue({ id: 'p1', name: 'Acme' }),
    getLedger: (...a: unknown[]) => getLedger(...a),
    getEvolutionSettings: (...a: unknown[]) => getEvolutionSettings(...a),
    listPackProposals: (...a: unknown[]) => listPackProposals(...a),
    decidePackProposal: (...a: unknown[]) => decidePackProposal(...a),
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

beforeEach(() => { jest.clearAllMocks(); });

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
});
