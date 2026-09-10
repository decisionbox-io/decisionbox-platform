/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor, act } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import ProjectPage from '@/app/projects/[id]/page';
import type { DiscoveryRunStatus, ProjectStatus } from '@/lib/api';

// The project page pulls in the whole app chrome and a couple of self-polling
// panels. Stub the ones that aren't under test so the test exercises the
// status-poll wiring (#405), not the nav / schema-index / ledger panels.
jest.mock('next/navigation', () => ({ useParams: () => ({ id: 'p1' }) }));
jest.mock('@mantine/notifications', () => ({ notifications: { show: jest.fn() } }));
jest.mock('@/components/layout/AppShell', () => ({
  __esModule: true,
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));
jest.mock('@/components/SchemaIndexPanel', () => ({
  __esModule: true,
  SchemaIndexPanel: () => null,
}));
jest.mock('@/components/projects/UpcomingInvestigation', () => ({
  __esModule: true,
  UpcomingInvestigation: () => null,
}));

const getProject = jest.fn();
const getAnalysisAreas = jest.fn();
const listDiscoveries = jest.fn();
const getProjectStatus = jest.fn();
const listProjectQuestions = jest.fn();
const listRunSteps = jest.fn();

jest.mock('@/lib/api', () => ({
  __esModule: true,
  ApiError: class ApiError extends Error {
    status = 0;
  },
  PROJECT_STATE_READY: 'ready',
  api: {
    getProject: (...a: unknown[]) => getProject(...a),
    getAnalysisAreas: (...a: unknown[]) => getAnalysisAreas(...a),
    listDiscoveries: (...a: unknown[]) => listDiscoveries(...a),
    getProjectStatus: (...a: unknown[]) => getProjectStatus(...a),
    listProjectQuestions: (...a: unknown[]) => listProjectQuestions(...a),
    listRunSteps: (...a: unknown[]) => listRunSteps(...a),
    getDebugLogs: jest.fn().mockResolvedValue([]),
    triggerDiscovery: jest.fn(),
    getRun: jest.fn(),
    estimateCost: jest.fn(),
    cancelRun: jest.fn(),
  },
}));

function makeRun(partial: Partial<DiscoveryRunStatus> = {}): DiscoveryRunStatus {
  return {
    id: 'r1',
    project_id: 'p1',
    status: 'running',
    phase: 'exploration',
    phase_detail: '',
    progress: 10,
    started_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:01Z',
    completed_at: null,
    error: '',
    total_queries: 0,
    successful_queries: 0,
    failed_queries: 0,
    insights_found: 0,
    ...partial,
  };
}

function status(run: DiscoveryRunStatus): ProjectStatus {
  return { project_id: 'p1', run };
}

function renderPage() {
  return render(
    <MantineProvider>
      <ProjectPage />
    </MantineProvider>,
  );
}

// Flush pending microtasks + macrotasks so any runaway effect loop gets a
// chance to fire additional requests.
const flush = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

beforeEach(() => {
  jest.clearAllMocks();
  // domain/category blank → the page skips getAnalysisAreas (mux redirect guard).
  getProject.mockResolvedValue({ id: 'p1', name: 'Acme', domain: '', category: '' });
  getAnalysisAreas.mockResolvedValue([]);
  listDiscoveries.mockResolvedValue([]);
  listProjectQuestions.mockResolvedValue([]);
  listRunSteps.mockResolvedValue([]);
});

describe('ProjectPage status polling (#405)', () => {
  it('fetches project status once on load rather than looping when a run is present', async () => {
    // A terminal run is the case that used to spin forever: status.run is
    // truthy, so the old unconditional setRun re-created pollStatus on every
    // response and the `[pollStatus]` effect re-fired it without bound.
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'completed', progress: 100, completed_at: '2026-01-01T00:01:00Z' })),
    );

    renderPage();
    await waitFor(() => expect(getProjectStatus).toHaveBeenCalledTimes(1));

    // Give any render→fetch→setRun→render loop several turns to manifest.
    for (let i = 0; i < 5; i++) await flush();

    expect(getProjectStatus).toHaveBeenCalledTimes(1);
  });

  it('does not poll on load when the project has no run', async () => {
    getProjectStatus.mockResolvedValue({ project_id: 'p1' } as ProjectStatus);

    renderPage();
    await waitFor(() => expect(getProjectStatus).toHaveBeenCalledTimes(1));
    for (let i = 0; i < 5; i++) await flush();

    // One initial fetch, then silence — no run to gate the 2s interval on.
    expect(getProjectStatus).toHaveBeenCalledTimes(1);
  });

  it('keeps the 2s interval polling while running, updates the live header, then stops and fires the completion side-effects', async () => {
    jest.useFakeTimers();
    try {
      getProjectStatus
        .mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 10, updated_at: 't1' })))
        .mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 40, updated_at: 't2' })))
        .mockResolvedValue(
          status(makeRun({ status: 'completed', progress: 100, updated_at: 't3', completed_at: 't3' })),
        );

      renderPage();

      // Initial fetch → running at 10%.
      await act(async () => { await jest.advanceTimersByTimeAsync(0); });
      expect(getProjectStatus).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(screen.getByText('10%')).toBeInTheDocument());

      // One interval tick → second poll → the header reflects the new progress
      // (this is why the conditional setRun compares updated_at, not just
      // id/status — otherwise the live header would freeze mid-run).
      await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
      expect(getProjectStatus).toHaveBeenCalledTimes(2);
      await waitFor(() => expect(screen.getByText('40%')).toBeInTheDocument());

      // Next tick → completes. The running→done transition refreshes the
      // discoveries list and polls for clarifying questions.
      await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
      expect(getProjectStatus).toHaveBeenCalledTimes(3);
      await waitFor(() => expect(listProjectQuestions).toHaveBeenCalled());
      // listDiscoveries: once on the initial load, once on the transition.
      expect(listDiscoveries).toHaveBeenCalledTimes(2);

      // Terminal run → the interval effect no longer subscribes, so further
      // time passing produces no more status calls.
      await act(async () => { await jest.advanceTimersByTimeAsync(10000); });
      expect(getProjectStatus).toHaveBeenCalledTimes(3);
    } finally {
      jest.useRealTimers();
    }
  });
});
