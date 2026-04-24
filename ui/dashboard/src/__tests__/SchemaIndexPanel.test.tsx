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
});

describe('SchemaIndexPanel', () => {
  it('renders ready state with "Re-index" button', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'ready',
      updated_at: '2026-04-01T12:00:00Z',
    });

    mount();
    await waitFor(() => expect(screen.getByText(/Schema index ready/)).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Re-index/i })).toBeInTheDocument();
  });

  it('renders failed state with Retry + Reset buttons', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'failed',
      error: 'qdrant unreachable',
    });

    mount();
    await waitFor(() => expect(screen.getByText(/Schema indexing failed/)).toBeInTheDocument());
    expect(screen.getByText(/qdrant unreachable/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Retry indexing/i })).toBeInTheDocument();
  });

  it('renders empty-status pre-migration banner', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: '' });

    mount();
    await waitFor(() => expect(screen.getByText(/Schema not indexed/)).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Build schema index/i })).toBeInTheDocument();
  });

  it('renders progress bar with phase + counters', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'indexing',
      progress: {
        phase: 'describing_tables',
        tables_total: 100,
        tables_done: 42,
      },
    });

    mount();
    await waitFor(() => expect(screen.getByText(/Generating blurbs/)).toBeInTheDocument());
    expect(screen.getByText(/42 of 100 tables/)).toBeInTheDocument();
  });

  it('renders indexing state even when progress doc is missing yet', async () => {
    // Worker just started — project flipped to pending_indexing but the
    // progress doc hasn't been written yet.
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'pending_indexing' });

    mount();
    await waitFor(() => expect(screen.getByText(/Waiting for worker/)).toBeInTheDocument());
  });

  it('fires onStatusChange after each successful fetch', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'ready' });
    const onChange = jest.fn();
    mount(onChange);
    await waitFor(() => expect(onChange).toHaveBeenCalledTimes(1));
    expect(onChange).toHaveBeenCalledWith({ status: 'ready' });
  });
});
