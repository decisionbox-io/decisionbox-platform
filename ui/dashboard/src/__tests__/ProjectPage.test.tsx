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
const resumeRun = jest.fn();
const getRun = jest.fn();

jest.mock('@/lib/api', () => ({
  __esModule: true,
  ApiError: class ApiError extends Error {
    status = 0;
  },
  PROJECT_STATE_READY: 'ready',
  DEFAULT_WAREHOUSE_ID: 'default',
  // Pure helper the project page calls during render to scope the schema-index
  // roll-up; this suite doesn't exercise datasource resolution (api.test.ts
  // does), so a constant keeps the page rendering.
  resolvePrimaryDatasourceId: () => 'default',
  api: {
    getProject: (...a: unknown[]) => getProject(...a),
    getAnalysisAreas: (...a: unknown[]) => getAnalysisAreas(...a),
    listDiscoveries: (...a: unknown[]) => listDiscoveries(...a),
    getProjectStatus: (...a: unknown[]) => getProjectStatus(...a),
    listProjectQuestions: (...a: unknown[]) => listProjectQuestions(...a),
    listRunSteps: (...a: unknown[]) => listRunSteps(...a),
    getDebugLogs: jest.fn().mockResolvedValue([]),
    triggerDiscovery: jest.fn(),
    getRun: (...a: unknown[]) => getRun(...a),
    estimateCost: jest.fn(),
    cancelRun: jest.fn(),
    resumeRun: (...a: unknown[]) => resumeRun(...a),
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

  it('keeps polling a still-running run but skips the state update when nothing changed', async () => {
    jest.useFakeTimers();
    try {
      // Every poll returns the identical run doc (same id/status/updated_at).
      // This is the branch the conditional setRun exists for: the interval is
      // correctly gated on `running`, so it keeps polling, but each response
      // is a no-op — replacing `run` here is what used to spin the loop.
      const frozen = makeRun({ status: 'running', progress: 25, updated_at: 'frozen' });
      getProjectStatus.mockResolvedValue(status(frozen));

      renderPage();
      await act(async () => { await jest.advanceTimersByTimeAsync(0); });
      expect(getProjectStatus).toHaveBeenCalledTimes(1);
      await waitFor(() => expect(screen.getByText('25%')).toBeInTheDocument());

      // Two more interval ticks: still polled (gated on the live run), progress
      // unchanged, and — crucially — no state churn from the identical docs.
      await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
      await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
      expect(getProjectStatus).toHaveBeenCalledTimes(3);
      expect(screen.getByText('25%')).toBeInTheDocument();
    } finally {
      jest.useRealTimers();
    }
  });
});

describe('Resume affordance on a failed run (#438)', () => {
  // Exploration is where a discovery run's cost sits, and a crashed run used
  // to lose all of it. The button is the whole user-facing surface of the
  // recovery path, so what it is offered FOR — and what it is withheld for —
  // is the thing worth pinning.

  it('offers Resume, with the step it would pick up from, for a failed run that has a checkpoint', async () => {
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', progress: 35, last_checkpoint_step: 42, attempt: 1 })),
    );

    renderPage();

    await waitFor(() => expect(screen.getByText(/Resume from step 42/)).toBeInTheDocument());
  });

  it.each([
    ['a failed run that never checkpointed', { status: 'failed', last_checkpoint_step: 0 }],
    ['a failed run from before checkpointing existed', { status: 'failed' }],
    ['a cancelled run — cancel is a deliberate hard kill and stays terminal', { status: 'cancelled', last_checkpoint_step: 42 }],
    ['a completed run — there is nothing left to resume', { status: 'completed', last_checkpoint_step: 42 }],
    ['a run still in flight', { status: 'running', last_checkpoint_step: 42 }],
  ])('withholds Resume for %s', async (_name, partial) => {
    getProjectStatus.mockResolvedValue(status(makeRun(partial as Partial<DiscoveryRunStatus>)));

    renderPage();
    await waitFor(() => expect(getProjectStatus).toHaveBeenCalled());
    await flush();

    expect(screen.queryByText(/Resume from step/)).not.toBeInTheDocument();
  });

  it('calls resumeRun and flips the panel to running so polling re-arms', async () => {
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', progress: 35, last_checkpoint_step: 42, attempt: 1 })),
    );
    resumeRun.mockResolvedValue({ status: 'resumed', run_id: 'r1', attempt: 2 });

    renderPage();
    const btn = await waitFor(() => screen.getByText(/Resume from step 42/));

    await act(async () => { btn.click(); });

    expect(resumeRun).toHaveBeenCalledWith('r1');
    // The 2s poll is gated on the run being live, so the optimistic flip is
    // what stops the panel looking dead for a beat after the click.
    await waitFor(() => expect(screen.getByText('Discovery running')).toBeInTheDocument());
  });

  it('surfaces the server message when a resume is refused', async () => {
    const { notifications } = jest.requireMock('@mantine/notifications');
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', last_checkpoint_step: 42 })),
    );
    // A 409 here is a real answer — the checkpoint expired, or another
    // request got there first — so the user must see what the server said.
    resumeRun.mockRejectedValue(new Error('no checkpoint to resume from — it expired or was never written'));

    renderPage();
    const btn = await waitFor(() => screen.getByText(/Resume from step 42/));

    await act(async () => { btn.click(); });

    await waitFor(() => expect(notifications.show).toHaveBeenCalledWith(
      expect.objectContaining({
        title: 'Cannot resume',
        message: 'no checkpoint to resume from — it expired or was never written',
      }),
    ));
    // And the run stays failed, so the operator can try again.
    expect(screen.getByText('Discovery failed')).toBeInTheDocument();
  });

  it('keeps the elapsed label advancing on a resumed run that is still going', async () => {
    // active_ms holds only the attempts that have FINISHED — each books its
    // time at its terminal write — so using it alone would freeze the label
    // at the previous attempts' total while the run visibly progresses.
    // 120s booked + 65s since this attempt started = 3m 5s.
    getProjectStatus.mockResolvedValue(status(makeRun({
      status: 'running',
      attempt: 2,
      active_ms: 120_000,
      started_at: '2026-01-01T00:00:00Z',      // hours of downtime before the resume
      last_resumed_at: '2026-01-02T00:00:00Z',
      updated_at: '2026-01-02T00:01:05Z',
    })));

    renderPage();

    await waitFor(() => expect(screen.getByText('3m 5s elapsed')).toBeInTheDocument());
  });

  it('does not count the downtime before a resume, even when the prior attempt booked nothing', async () => {
    // A prior attempt hard-killed before its terminal write books 0. Falling
    // back to started_at here would report a full day of "work".
    getProjectStatus.mockResolvedValue(status(makeRun({
      status: 'running',
      attempt: 2,
      started_at: '2026-01-01T00:00:00Z',
      last_resumed_at: '2026-01-02T00:00:00Z',
      updated_at: '2026-01-02T00:00:45Z',
    })));

    renderPage();

    await waitFor(() => expect(screen.getByText('45s elapsed')).toBeInTheDocument());
  });

  it('does not report pre-resume downtime for an attempt that died before booking its time', async () => {
    // An OOM, a pod eviction, or a startup failure right after the resume
    // leaves active_ms at 0 on a terminal run. Falling back to started_at
    // would report the whole day the failed run sat idle as work.
    getProjectStatus.mockResolvedValue(status(makeRun({
      status: 'failed',
      attempt: 2,
      last_checkpoint_step: 42,
      started_at: '2026-01-01T00:00:00Z',
      last_resumed_at: '2026-01-02T00:00:00Z',
      updated_at: '2026-01-02T00:00:12Z',
    })));

    renderPage();

    await waitFor(() => expect(screen.getByText('12s elapsed')).toBeInTheDocument());
  });

  it('re-reads the run when a resume is refused, so a lost race does not leave a stale panel', async () => {
    const { notifications } = jest.requireMock('@mantine/notifications');
    getProjectStatus.mockResolvedValue(status(makeRun({
      status: 'failed', last_checkpoint_step: 42,
    })));
    // Another tab won the race: the POST is refused, and by then the run is
    // already running.
    resumeRun.mockRejectedValue(new Error('run is no longer resumable — another request got there first'));
    getRun.mockResolvedValue(makeRun({ status: 'running', attempt: 2, last_checkpoint_step: 42 }));

    renderPage();
    const btn = await waitFor(() => screen.getByText(/Resume from step 42/));

    await act(async () => { btn.click(); });

    // The refusal is still surfaced verbatim...
    await waitFor(() => expect(notifications.show).toHaveBeenCalledWith(
      expect.objectContaining({ title: 'Cannot resume' }),
    ));
    // ...and the panel converges on what the server actually has. A terminal
    // run does not poll, so without the re-read this tab would offer Resume
    // for an active run until someone reloaded.
    expect(getRun).toHaveBeenCalledWith('r1');
    await waitFor(() => expect(screen.getByText('Discovery running')).toBeInTheDocument());
    expect(screen.queryByText(/Resume from step/)).not.toBeInTheDocument();
  });

  it('keeps the panel usable when the refusal refresh itself fails', async () => {
    const { notifications } = jest.requireMock('@mantine/notifications');
    getProjectStatus.mockResolvedValue(status(makeRun({
      status: 'failed', last_checkpoint_step: 42,
    })));
    resumeRun.mockRejectedValue(new Error('no checkpoint to resume from — it expired'));
    getRun.mockRejectedValue(new Error('network down'));

    renderPage();
    const btn = await waitFor(() => screen.getByText(/Resume from step 42/));

    await act(async () => { btn.click(); });

    // The real error is what the user sees; a failed refresh must not
    // replace it or throw.
    await waitFor(() => expect(notifications.show).toHaveBeenCalledWith(
      expect.objectContaining({
        title: 'Cannot resume',
        message: 'no checkpoint to resume from — it expired',
      }),
    ));
    expect(screen.getByText('Discovery failed')).toBeInTheDocument();
  });

  it('reports cumulative active time rather than wall-clock across attempts', async () => {
    // Wall-clock from started_at would count the hours a failed run sat
    // waiting to be noticed as work. 185s = 3m 5s.
    getProjectStatus.mockResolvedValue(status(makeRun({
      status: 'failed',
      last_checkpoint_step: 42,
      attempt: 2,
      active_ms: 185_000,
      started_at: '2026-01-01T00:00:00Z',
      updated_at: '2026-01-02T00:00:00Z',
    })));

    renderPage();

    await waitFor(() => expect(screen.getByText('3m 5s elapsed')).toBeInTheDocument());
    expect(screen.getByText('attempt 2')).toBeInTheDocument();
  });
});
