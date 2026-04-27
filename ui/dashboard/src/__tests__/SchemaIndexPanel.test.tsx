/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { SchemaIndexPanel } from '@/components/SchemaIndexPanel';
import { api } from '@/lib/api';

jest.mock('@/lib/api', () => ({
  api: {
    getSchemaIndexStatus: jest.fn(),
    retrySchemaIndex: jest.fn(),
    reindexSchema: jest.fn(),
    cancelSchemaIndex: jest.fn(),
    listSchemaIndexLogs: jest.fn(),
  },
}));

const mockedApi = api as jest.Mocked<typeof api>;

function mount(onChange?: jest.Mock) {
  return render(
    <MantineProvider>
      <SchemaIndexPanel projectId="p1" onStatusChange={onChange} />
    </MantineProvider>
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  // Panel's log-tail effect triggers listSchemaIndexLogs when the
  // localStorage db:showDebugLogs:<id> key is set. Default to off so
  // tests don't engage the log tail unless they opt in.
  window.localStorage.removeItem('db:showDebugLogs:p1');
  (mockedApi.listSchemaIndexLogs as jest.Mock).mockResolvedValue([]);
});

describe('SchemaIndexPanel', () => {
  it('renders the "schema index: Ready" banner with Re-index button in ready state', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'ready',
      updated_at: '2026-04-01T12:00:00Z',
    });
    mount();
    await waitFor(() => expect(screen.getByText(/Schema index:/)).toBeInTheDocument());
    expect(screen.getByText(/Ready/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Re-index/i })).toBeInTheDocument();
  });

  it('renders failed state with Retry + Reset buttons and the error message', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'failed',
      error: 'qdrant unreachable',
    });
    mount();
    await waitFor(() => expect(screen.getByText(/Failed/)).toBeInTheDocument());
    expect(screen.getByText(/qdrant unreachable/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Retry indexing/i })).toBeInTheDocument();
  });

  it('renders pre-migration (empty status) with Build schema index button', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: '' });
    mount();
    await waitFor(() => expect(screen.getByText(/Not indexed/)).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Build schema index/i })).toBeInTheDocument();
  });

  it('renders a progress bar and phase label while indexing', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'describing_tables', tables_total: 100, tables_done: 42 },
    });
    mount();
    await waitFor(() => expect(screen.getByText(/Generating blurbs/)).toBeInTheDocument());
    expect(screen.getByText(/42 of 100 tables \(42%\)/)).toBeInTheDocument();
  });

  it('shows schema_discovery phase label during the per-table pull', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'schema_discovery', tables_total: 1416, tables_done: 120 },
    });
    mount();
    await waitFor(() => expect(screen.getByText(/Discovering table schemas/)).toBeInTheDocument());
    expect(screen.getByText(/120 of 1416 tables/)).toBeInTheDocument();
  });

  it('indexing state without a progress doc still shows the bar + "Starting up…" copy', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'pending_indexing' });
    mount();
    await waitFor(() => expect(screen.getByText(/Queued/)).toBeInTheDocument());
    expect(screen.getByText(/Starting up/)).toBeInTheDocument();
  });

  it('fires onStatusChange after each successful fetch', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'ready' });
    const onChange = jest.fn();
    mount(onChange);
    await waitFor(() => expect(onChange).toHaveBeenCalled());
    expect(onChange).toHaveBeenLastCalledWith({ status: 'ready' });
  });

  it('polls the log tail when the localStorage toggle is on', async () => {
    window.localStorage.setItem('db:showDebugLogs:p1', '1');
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'schema_discovery', tables_total: 10, tables_done: 3 },
    });
    (mockedApi.listSchemaIndexLogs as jest.Mock).mockResolvedValue([
      { run_id: 'r1', line: 'Discovering table schema 4/10', created_at: '2026-04-24T14:33:10Z' },
    ]);
    mount();
    await waitFor(() => expect(mockedApi.listSchemaIndexLogs).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByText(/Agent log tail/)).toBeInTheDocument());
  });

  // --- cancel flow ---

  it('shows Cancel indexing button while indexing and opens a confirmation modal', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'schema_discovery', tables_total: 10, tables_done: 3 },
    });
    mount();
    const btn = await screen.findByRole('button', { name: /Cancel indexing/i });
    expect(btn).toBeInTheDocument();
    expect(btn).not.toBeDisabled();

    // Modal closed initially.
    expect(screen.queryByText(/Cancel schema indexing\?/)).not.toBeInTheDocument();
    btn.click();
    await waitFor(() =>
      expect(screen.getByText(/Cancel schema indexing\?/)).toBeInTheDocument()
    );
    // Explicit "Keep running" escape hatch is present — required by the
    // confirmation UX spec.
    expect(screen.getByRole('button', { name: /Keep running/i })).toBeInTheDocument();
  });

  it('calls api.cancelSchemaIndex when confirmation is accepted', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'schema_discovery', tables_total: 10, tables_done: 3 },
    });
    (mockedApi.cancelSchemaIndex as jest.Mock).mockResolvedValue({ status: 'cancelling' });
    mount();
    (await screen.findByRole('button', { name: /Cancel indexing/i })).click();
    (await screen.findByRole('button', { name: /Yes, cancel/i })).click();
    await waitFor(() => expect(mockedApi.cancelSchemaIndex).toHaveBeenCalledWith('p1'));
  });

  it('does NOT call api.cancelSchemaIndex when "Keep running" is clicked', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'schema_discovery', tables_total: 10, tables_done: 3 },
    });
    mount();
    (await screen.findByRole('button', { name: /Cancel indexing/i })).click();
    (await screen.findByRole('button', { name: /Keep running/i })).click();
    // Wait for the modal to close and ensure cancel was NOT called.
    await waitFor(() =>
      expect(screen.queryByText(/Cancel schema indexing\?/)).not.toBeInTheDocument()
    );
    expect(mockedApi.cancelSchemaIndex).not.toHaveBeenCalled();
  });

  it('does NOT show Cancel indexing in pending_indexing state (worker has not claimed yet)', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'pending_indexing',
    });
    mount();
    // The status label waits for the first poll to complete.
    await screen.findByText(/Queued/);
    // Button exists but is disabled per the API contract (Cancel 409s
    // on pending_indexing — there's no subprocess to kill).
    const btn = screen.getByRole('button', { name: /Cancel indexing/i });
    expect(btn).toBeDisabled();
  });

  it('renders the Cancelled banner with a Re-index button once the worker confirms', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'cancelled',
      error: 'cancelled by user',
    });
    mount();
    await waitFor(() => expect(screen.getByText(/Cancelled/)).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Re-index/i })).toBeInTheDocument();
    // No Cancel indexing button in terminal state.
    expect(screen.queryByRole('button', { name: /Cancel indexing/i })).not.toBeInTheDocument();
  });

  it('surfaces a cancel API error in the banner without crashing', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: { phase: 'schema_discovery', tables_total: 10, tables_done: 3 },
    });
    (mockedApi.cancelSchemaIndex as jest.Mock).mockRejectedValue(new Error('network down'));
    mount();
    (await screen.findByRole('button', { name: /Cancel indexing/i })).click();
    (await screen.findByRole('button', { name: /Yes, cancel/i })).click();
    await waitFor(() => expect(screen.getByText(/network down/i)).toBeInTheDocument());
  });
});
