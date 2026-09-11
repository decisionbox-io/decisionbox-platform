/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import SchemaIndexHistory from '@/components/projects/SchemaIndexHistory';
import { api, SchemaIndexRun } from '@/lib/api';

jest.mock('@/lib/api', () => ({
  api: { listSchemaIndexRuns: jest.fn(), getSchemaIndexStatus: jest.fn() },
}));

const mockedApi = api as jest.Mocked<typeof api>;

function mount(datasourceId?: string, datasourceName?: string) {
  return render(
    <MantineProvider>
      <SchemaIndexHistory projectId="p1" datasourceId={datasourceId} datasourceName={datasourceName} />
    </MantineProvider>
  );
}

const readyRun: SchemaIndexRun = {
  datasource_id: 'wh_a', datasource_name: 'Redshift', run_id: 'r2', kind: 'tables',
  objects_indexed: 42, blurbs_generated: 40, status: 'ready',
  started_at: '2026-09-01T14:19:40Z', finished_at: '2026-09-01T14:20:00Z',
};
const olderRun: SchemaIndexRun = {
  datasource_id: 'wh_a', datasource_name: 'Redshift', run_id: 'r1', kind: 'tables',
  objects_indexed: 30, blurbs_generated: 30, status: 'failed', error: 'connect: timeout',
  started_at: '2026-08-01T10:00:00Z', finished_at: '2026-08-01T10:00:12Z',
};

beforeEach(() => {
  jest.clearAllMocks();
  // Default: project is ready. Individual tests override for other states.
  (mockedApi.getSchemaIndexStatus as jest.Mock).mockResolvedValue({ status: 'ready' });
});

describe('SchemaIndexHistory', () => {
  it('shows the latest run as the current-status line when ready', async () => {
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockResolvedValue({ runs: [readyRun, olderRun] });
    mount();
    await waitFor(() => expect(screen.getByText(/42 objects indexed/)).toBeInTheDocument());
    // View-history toggle reflects the total count.
    expect(screen.getByRole('button', { name: /View history \(2\)/i })).toBeInTheDocument();
  });

  it('renders the empty state when never indexed', async () => {
    (mockedApi.getSchemaIndexStatus as jest.Mock).mockResolvedValue({ status: '' });
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockResolvedValue({ runs: [] });
    mount(undefined, 'bigquery');
    await waitFor(() => expect(screen.getByText(/Not indexed yet for bigquery/)).toBeInTheDocument());
    // No toggle when there's nothing to expand.
    expect(screen.queryByRole('button', { name: /View history/i })).not.toBeInTheDocument();
  });

  it('shows "re-index required" (not stale Ready) after the cache is cleared', async () => {
    // needs_reindex: the index was dropped but run rows are retained. The
    // current-status line must reflect the live state, not the last run.
    (mockedApi.getSchemaIndexStatus as jest.Mock).mockResolvedValue({ status: 'needs_reindex' });
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockResolvedValue({ runs: [readyRun] });
    mount();
    await waitFor(() => expect(screen.getByText(/Re-index required/i)).toBeInTheDocument());
    // The stale success count must NOT be presented as current status.
    expect(screen.queryByText(/42 objects indexed/)).not.toBeInTheDocument();
    // …but the durable record is still available in the history table.
    expect(screen.getByRole('button', { name: /View history/i })).toBeInTheDocument();
  });

  it('reflects a live failed status in the current-status line', async () => {
    (mockedApi.getSchemaIndexStatus as jest.Mock).mockResolvedValue({ status: 'failed', error: 'qdrant unreachable' });
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockResolvedValue({ runs: [readyRun] });
    mount();
    await waitFor(() => expect(screen.getByText(/qdrant unreachable/)).toBeInTheDocument());
  });

  it('expands to a history table with a row per run, surfacing the error', async () => {
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockResolvedValue({ runs: [readyRun, olderRun] });
    mount();
    const toggle = await screen.findByRole('button', { name: /View history/i });
    fireEvent.click(toggle);
    await waitFor(() => expect(screen.getByRole('table')).toBeInTheDocument());
    // Column headers present.
    expect(screen.getByText('Objects')).toBeInTheDocument();
    expect(screen.getByText('Blurbs')).toBeInTheDocument();
    expect(screen.getByText('Duration')).toBeInTheDocument();
    // The failed run's error is shown in its row.
    expect(screen.getByText(/connect: timeout/)).toBeInTheDocument();
  });

  it('passes the datasourceId filter through to the API', async () => {
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockResolvedValue({ runs: [] });
    mount('wh_b', 'Snowflake');
    await waitFor(() => expect(mockedApi.listSchemaIndexRuns).toHaveBeenCalledWith('p1', 'wh_b'));
  });

  it('shows an error message if the history fails to load', async () => {
    (mockedApi.listSchemaIndexRuns as jest.Mock).mockRejectedValue(new Error('mongo down'));
    mount();
    await waitFor(() => expect(screen.getByText(/Couldn't load indexing status: mongo down/)).toBeInTheDocument());
  });
});
