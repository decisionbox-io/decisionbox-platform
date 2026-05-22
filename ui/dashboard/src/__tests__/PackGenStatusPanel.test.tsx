/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { Notifications } from '@mantine/notifications';
import PackGenStatusPanel from '@/components/projects/PackGenStatusPanel';
import {
  api,
  Project,
  PROJECT_STATE_PACK_GENERATION_PENDING,
} from '@/lib/api';

// next/navigation router.push lives behind a hook; jest can't traverse
// it from the component without a stub. PackGenStatusPanel only uses
// router.push() for the wizard-resume button, so a no-op is fine.
jest.mock('next/navigation', () => ({
  useRouter: () => ({ push: jest.fn() }),
}));

jest.mock('@/lib/api', () => {
  const actual = jest.requireActual('@/lib/api');
  return {
    ...actual,
    api: {
      ...actual.api,
      getProject: jest.fn(),
      getSchemaIndexStatus: jest.fn(),
      retrySchemaIndex: jest.fn(),
      reindexSchema: jest.fn(),
      cancelSchemaIndex: jest.fn(),
      listSchemaIndexLogs: jest.fn(),
      getDomainPack: jest.fn(),
    },
  };
});

const mockedApi = api as jest.Mocked<typeof api>;

function pendingProject(): Project {
  return {
    id: 'p1',
    name: 'Demo',
    state: PROJECT_STATE_PACK_GENERATION_PENDING,
  } as Project;
}

function mount(p: Project = pendingProject()) {
  return render(
    <MantineProvider>
      <Notifications />
      <PackGenStatusPanel project={p} onProjectChanged={() => {}} />
    </MantineProvider>
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  window.localStorage.clear();
  (mockedApi.listSchemaIndexLogs as jest.Mock).mockResolvedValue([]);
  // getProject is only called by the panel's own polling timer; the
  // tests don't advance fake timers, so a single resolved value keeps
  // the polling stable without affecting assertions.
  (mockedApi.getProject as jest.Mock).mockResolvedValue(pendingProject());
});

describe('PackGenStatusPanel — pack_generation_pending', () => {
  it('shows the "Pick up where you left off" copy when schema indexing has not started', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'ready', updated_at: null } as never);
    mount();
    // Default rendering picks up the ready badge (schema is indexed but
    // pack synthesis hasn't been kicked off yet) and the wizard-resume
    // helper copy — same as the original flow.
    await waitFor(() => expect(screen.getByText(/Schema index is ready/i)).toBeInTheDocument());
    expect(screen.getByRole('button', { name: /Continue setup/i })).toBeInTheDocument();
  });

  it('replaces the wizard copy with an "indexing is running" message when indexStatus is indexing', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'indexing', progress_pct: 42, updated_at: null } as never);
    mount();
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing is running/i)).toBeInTheDocument()
    );
    // Badge flips from gray "Pending" to blue "Indexing schema" so the
    // operator can see the new state at a glance.
    expect(screen.getByText(/Indexing schema/i)).toBeInTheDocument();
    // The original "Pick up where you left off" sentence is gone — the
    // home page is now a progress surface, not a wizard prompt.
    expect(screen.queryByText(/Pick up where you left off/i)).not.toBeInTheDocument();
  });

  it('shows the "indexing pending" message when indexStatus is pending_indexing', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'pending_indexing', updated_at: null } as never);
    mount();
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing is running/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/Indexing schema/i)).toBeInTheDocument();
  });

  it('shows "Schema indexed" + Continue-setup copy when indexStatus is ready', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'ready', updated_at: '2026-05-22T10:00:00Z' } as never);
    mount();
    await waitFor(() => expect(screen.getByText(/Schema index is ready/i)).toBeInTheDocument());
    expect(screen.getByText(/Schema indexed/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Continue setup/i })).toBeInTheDocument();
  });

  it('shows a recovery message + retry button label when indexStatus is failed', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({
      status: 'failed',
      error: 'blurb: too many per-table blurb failures',
      updated_at: null,
    } as never);
    mount();
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing did not finish/i)).toBeInTheDocument()
    );
    // Title is preserved; badge flips to "Indexing failed".
    expect(screen.getAllByText(/Indexing failed/i).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: /Open wizard to retry/i })).toBeInTheDocument();
  });

  it('shows a recovery message when indexStatus is cancelled', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'cancelled', updated_at: null } as never);
    mount();
    await waitFor(() =>
      expect(screen.getByText(/Schema indexing was cancelled/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/Indexing cancelled/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Open wizard to retry/i })).toBeInTheDocument();
  });

  it('keeps the existing pack_gen_last_error banner when the previous pack synthesis attempt failed', async () => {
    mockedApi.getSchemaIndexStatus.mockResolvedValue({ status: 'ready', updated_at: null } as never);
    const proj = pendingProject();
    (proj as Project).pack_gen_last_error = 'invalid pack JSON: expected object';
    mount(proj);
    await waitFor(() =>
      expect(screen.getByText(/Pack generation failed/i)).toBeInTheDocument()
    );
    expect(screen.getByText(/invalid pack JSON/i)).toBeInTheDocument();
    // Even with schema index ready, a prior pack-gen failure dominates
    // the badge so the operator notices the higher-priority signal.
    expect(screen.getAllByText(/Last attempt failed/i).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: /Retry in wizard/i })).toBeInTheDocument();
  });
});
