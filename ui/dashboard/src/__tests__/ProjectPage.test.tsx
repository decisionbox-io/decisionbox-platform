/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor, act, within } from '@testing-library/react';
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
  default: ({ children, actions }: { children: React.ReactNode; actions?: React.ReactNode }) => <div>{actions}{children}</div>,
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
const discardRun = jest.fn();
const getRun = jest.fn();
const triggerDiscovery = jest.fn();
const cancelRun = jest.fn();

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
    triggerDiscovery: (...a: unknown[]) => triggerDiscovery(...a),
    getRun: (...a: unknown[]) => getRun(...a),
    cancelRun: (...a: unknown[]) => cancelRun(...a),
    resumeRun: (...a: unknown[]) => resumeRun(...a),
    discardRun: (...a: unknown[]) => discardRun(...a),
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

  it('rebuilds the live step feed from the head of the new attempt rather than appending to the old one', async () => {
    // The live feed is attempt-scoped server-side, and a resume re-emits the
    // replayed prefix under the new attempt. The panel used to be keyed on
    // run.id alone — and a resume re-enters the SAME run id, so it stayed
    // mounted across the resume with its rendered rows and its `since` cursor
    // intact. The re-emitted prefix then arrived as an append to rows already
    // on screen from the previous attempt, showing the operator every
    // replayed step twice. Remounting on the attempt is what makes it once.
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', progress: 35, last_checkpoint_step: 42, attempt: 1 })),
    );
    listRunSteps.mockResolvedValue([
      { id: 'a1-row-1', step_num: 1, type: 'query', message: 'attempt 1 step 1' },
    ]);
    resumeRun.mockResolvedValue({ status: 'resumed', run_id: 'r1', attempt: 2 });

    renderPage();
    const btn = await waitFor(() => screen.getByText(/Resume from step 42/));
    // The first attempt's poll has already returned, so the panel is holding
    // 'a1-row-1' as its cursor. It does not poll again on its own — the run is
    // terminal, so the panel stops scheduling — but the cursor is live in the
    // ref and is what the next request would carry.
    await waitFor(() => expect(listRunSteps).toHaveBeenCalledWith('r1', undefined));

    listRunSteps.mockClear();
    await act(async () => { btn.click(); });
    await flush();

    // Remounted: the very first request of the new attempt must start from
    // the head of the stream, not from the previous attempt's cursor.
    expect(listRunSteps).toHaveBeenCalled();
    expect(listRunSteps.mock.calls[0]).toEqual(['r1', undefined]);
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

  it('does not let a refused resume install its old run over a newer one', async () => {
    const { notifications } = jest.requireMock('@mantine/notifications');
    jest.useFakeTimers();
    try {
      getProjectStatus.mockResolvedValue(status(makeRun({
        id: 'run-a', status: 'failed', last_checkpoint_step: 42,
      })));
      // The refusal round-trip is held open: that is the window in which the
      // run on screen can change under the handler.
      let refuse: (e: Error) => void = () => {};
      resumeRun.mockImplementation(() => new Promise((_res, rej) => { refuse = rej; }));
      // What the handler re-reads when it is refused: run A, still failed.
      getRun.mockResolvedValue(makeRun({ id: 'run-a', status: 'failed', last_checkpoint_step: 42 }));

      renderPage();
      await act(async () => { await jest.advanceTimersByTimeAsync(0); });
      const btn = await waitFor(() => screen.getByText(/Resume from step 42/));
      await act(async () => { btn.click(); });

      // Dismissing re-arms the poll for a terminal run — that watch exists
      // precisely to notice a new one — so run B can arrive while the resume
      // for A is still in flight.
      const dismiss = screen.getByText('Dismiss');
      await act(async () => { dismiss.click(); });
      getProjectStatus.mockResolvedValue(status(makeRun({ id: 'run-b', status: 'running' })));
      await act(async () => { await jest.advanceTimersByTimeAsync(15000); });
      await waitFor(() => expect(screen.getByText('Discovery running')).toBeInTheDocument());

      // Only now is A's resume refused, and its handler re-reads A.
      await act(async () => {
        refuse(new Error('another run is already in progress'));
        await jest.advanceTimersByTimeAsync(0);
      });

      // The refusal is still surfaced...
      expect(notifications.show).toHaveBeenCalledWith(
        expect.objectContaining({ title: 'Cannot resume' }),
      );
      // ...and B stays on screen. Installing failed A here would stop the
      // polling B depends on, so its progress and its Cancel button would be
      // gone until somebody reloaded.
      expect(screen.getByText('Discovery running')).toBeInTheDocument();
      expect(screen.queryByText(/Resume from step/)).not.toBeInTheDocument();
    } finally {
      jest.useRealTimers();
    }
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

describe('Dismissing a finished run that is later resumed (#438)', () => {
  // Dismiss hides a finished run's panel, and `failed` is exactly the state a
  // run is resumable from. The dismissal used to call setRun(null), which also
  // stopped the status poll — so if anyone resumed that run, from another tab,
  // another user, or the API, this page showed nothing live until a reload.
  //
  // The run is now kept in state and only the panel is hidden, so the poll can
  // keep watching it and bring the panel back when it goes live again.

  it('hides the panel on dismiss but keeps watching the run', async () => {
    jest.useFakeTimers();
    try {
      getProjectStatus.mockResolvedValue(
        status(makeRun({ status: 'failed', progress: 35, last_checkpoint_step: 42, attempt: 1 })),
      );

      renderPage();
      await act(async () => { await jest.advanceTimersByTimeAsync(0); });

      const dismiss = await waitFor(() => screen.getByText('Dismiss'));
      await act(async () => { dismiss.click(); });

      expect(screen.queryByText('Dismiss')).not.toBeInTheDocument();
      expect(screen.queryByText(/Resume from step 42/)).not.toBeInTheDocument();

      // Someone resumes it elsewhere: same run id, now running.
      getProjectStatus.mockResolvedValue(
        status(makeRun({ status: 'running', progress: 40, attempt: 2, updated_at: 'later' })),
      );

      // The slow watch poll picks it up without a reload. Nothing happens on
      // the live cadence — this run is terminal, so it is only watched at the
      // 15s one.
      await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
      expect(screen.queryByText(/attempt 2/)).not.toBeInTheDocument();

      await act(async () => { await jest.advanceTimersByTimeAsync(15000); });
      await waitFor(() => expect(screen.getByText(/attempt 2/)).toBeInTheDocument());
    } finally {
      jest.useRealTimers();
    }
  });

  it('keeps a dismissed run hidden while it is still finished', async () => {
    // The flip side: the status endpoint keeps answering with the same
    // finished run, and none of those answers may un-hide it — otherwise
    // Dismiss would do nothing at all.
    jest.useFakeTimers();
    try {
      getProjectStatus.mockResolvedValue(
        status(makeRun({ status: 'failed', progress: 35, last_checkpoint_step: 42, attempt: 1 })),
      );

      renderPage();
      await act(async () => { await jest.advanceTimersByTimeAsync(0); });

      const dismiss = await waitFor(() => screen.getByText('Dismiss'));
      await act(async () => { dismiss.click(); });
      await act(async () => { await jest.advanceTimersByTimeAsync(60000); });

      expect(screen.queryByText('Dismiss')).not.toBeInTheDocument();
      expect(screen.queryByText(/Resume from step 42/)).not.toBeInTheDocument();
    } finally {
      jest.useRealTimers();
    }
  });
});

// Discard is the control an operator had no way to exercise: a failed run is
// resumable AND is what the project page shows, so one they were done with
// stayed pinned there. Cancel refuses anything inactive and Dismiss is local
// to one tab, so neither served it.
describe('Discarding a terminal run (#438)', () => {
  it.each([
    ['a failed run', { status: 'failed', last_checkpoint_step: 4 }],
    ['a failed run that never checkpointed', { status: 'failed', last_checkpoint_step: 0 }],
    ['a cancelled run', { status: 'cancelled' }],
  ])('offers Discard for %s', async (_name, partial) => {
    getProjectStatus.mockResolvedValue(status(makeRun(partial as Partial<DiscoveryRunStatus>)));

    renderPage();

    await waitFor(() => expect(screen.getByText('Discard')).toBeInTheDocument());
  });

  it.each([
    ['a completed run — its discovery is the project history, and the server refuses it', { status: 'completed' }],
    ['a run still in flight — cancel is the verb for that', { status: 'running' }],
  ])('withholds Discard for %s', async (_name, partial) => {
    getProjectStatus.mockResolvedValue(status(makeRun(partial as Partial<DiscoveryRunStatus>)));

    renderPage();
    await waitFor(() => expect(getProjectStatus).toHaveBeenCalled());
    await flush();

    expect(screen.queryByText('Discard')).not.toBeInTheDocument();
  });

  // Unlike Dismiss, Discard cannot be undone — it throws away the checkpoints
  // the run could have been resumed from. The dashboard's destructive tier is
  // red plus a confirm modal (Delete project, Delete list, Remove column), so
  // one stray click must not reach the API.
  it('opens a confirm modal rather than calling the API, and fires on confirm', async () => {
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', last_checkpoint_step: 4 })),
    );
    discardRun.mockResolvedValue({ status: 'discarded', run_id: 'r1' });

    renderPage();
    const btn = await waitFor(() => screen.getByText('Discard'));

    await act(async () => { btn.click(); });
    await flush();
    expect(discardRun).not.toHaveBeenCalled();
    // The modal says what is lost and what survives — the distinction people
    // hesitate over, because Discard reads as Delete.
    await waitFor(() => expect(screen.getByText('Discard this run?')).toBeInTheDocument());
    expect(screen.getByText(/can no longer be resumed from step 4/)).toBeInTheDocument();
    expect(screen.getByText(/step history stay readable/)).toBeInTheDocument();

    // Two "Discard" nodes exist now (trigger + modal action); the modal's is
    // the one inside the dialog.
    const dialog = await waitFor(() => screen.getByRole('dialog'));
    const confirm = within(dialog).getByRole('button', { name: 'Discard' });
    await act(async () => { confirm.click(); });
    await flush();
    expect(discardRun).toHaveBeenCalledWith('r1');
  });

  // Backing out of the modal is the whole point of having one.
  it('does not call the API when the confirm modal is cancelled', async () => {
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', last_checkpoint_step: 4 })),
    );

    renderPage();
    const btn = await waitFor(() => screen.getByText('Discard'));
    await act(async () => { btn.click(); });
    await flush();

    const dialog = await waitFor(() => screen.getByRole('dialog'));
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' });
    await act(async () => { cancel.click(); });
    await flush();

    expect(discardRun).not.toHaveBeenCalled();
  });

  // A refusal is a real answer — the run went active again, or another tab
  // got there first — so the panel must not be torn down on one.
  it('keeps the run on screen when the server refuses', async () => {
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', last_checkpoint_step: 4 })),
    );
    discardRun.mockRejectedValue(new Error('run is still active (status: running) — cancel it first, then discard it'));

    renderPage();
    const btn = await waitFor(() => screen.getByText('Discard'));
    await act(async () => { btn.click(); });
    await flush();
    const dialog = await waitFor(() => screen.getByRole('dialog'));
    const confirm = within(dialog).getByRole('button', { name: 'Discard' });
    await act(async () => { confirm.click(); });
    await flush();

    expect(discardRun).toHaveBeenCalled();
    await waitFor(() => expect(screen.getByText('Discard')).toBeInTheDocument());
  });
});

// Two cases Codex found on the discard work, both about state the panel can
// be left holding.
describe('Discard: runs with a result, and a project with none (#438)', () => {
  // "Not completed" is not the same as "produced nothing": the agent stamps
  // a discovery on a run that saved a partial result before failing, and
  // discarding that would orphan it. The server refuses either way; the
  // button must not offer a guaranteed 409.
  it('withholds Discard from a failed run that saved a discovery', async () => {
    getProjectStatus.mockResolvedValue(
      status(makeRun({ status: 'failed', last_checkpoint_step: 4, discovery_id: 'disc-partial-1' })),
    );

    renderPage();
    await waitFor(() => expect(getProjectStatus).toHaveBeenCalled());
    await flush();

    expect(screen.queryByText('Discard')).not.toBeInTheDocument();
    // Resume is still right: a partial result does not make the run
    // unresumable, it makes it undiscardable.
    expect(screen.getByText(/Resume from step 4/)).toBeInTheDocument();
  });

  // A terminal run is not polled, so if the project stops having a run to
  // show — every run discarded, perhaps from another tab — nothing would ever
  // take the stale panel off screen.
  it('clears the panel when a status read returns no run at all', async () => {
    getProjectStatus.mockResolvedValueOnce(
      status(makeRun({ status: 'failed', last_checkpoint_step: 4 })),
    );
    renderPage();
    await waitFor(() => expect(screen.getByText(/Resume from step 4/)).toBeInTheDocument());

    // Someone else discarded it; the project now has nothing to represent it.
    getProjectStatus.mockResolvedValue({ project_id: 'p1', run: null, last_discovery: null });
    discardRun.mockRejectedValue(new Error('run was already discarded'));

    const btn = screen.getByText('Discard');
    await act(async () => { btn.click(); });
    await flush();
    const dialog = await waitFor(() => screen.getByRole('dialog'));
    const confirm = within(dialog).getByRole('button', { name: 'Discard' });
    await act(async () => { confirm.click(); });
    await flush();

    await waitFor(() => expect(screen.queryByText(/Resume from step 4/)).not.toBeInTheDocument());
    expect(screen.queryByText('Discard')).not.toBeInTheDocument();
  });

// The race the clear-on-empty rule had to be fenced against: an empty status
// response arriving while a LIVE run is on screen. It happens because the
// first status request can still be in flight when the operator starts their
// first run, so its PRE-trigger "no run" answer lands after the live run is
// installed. Clearing on that hid an active run — and the poll is gated on a
// live run, so nothing brought it back short of a reload.
//
// Asserted as the invariant rather than by driving the trigger menu: an empty
// response must never clear a run installed after it went out, whatever
// produced the ordering.
it('does not clear a live run when an empty status response arrives', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 25 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('25%')).toBeInTheDocument());

    // The project reports no run. The run itself is still live, which is
    // what the reconcile asks about — so the panel stays.
    getProjectStatus.mockResolvedValue({ project_id: 'p1', run: null, last_discovery: null });
    getRun.mockResolvedValue(makeRun({ status: 'running', progress: 25 }));
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // Still there, still cancellable, and still polling.
    expect(screen.getByText('25%')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
    const polls = getProjectStatus.mock.calls.length;
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
    expect(getProjectStatus.mock.calls.length).toBeGreaterThan(polls);
  } finally {
    jest.useRealTimers();
  }
});

// The status fence was not enough, and this is the ordering that shows it: a
// status request in flight when `run` changes out of band, whose answer — true
// when it was issued — lands afterwards. A status test cannot save the newer
// run, because a run that has just been cancelled (or that failed fast) is
// terminal by then and indistinguishable from the run the answer was about.
// Only the generation can.
it('ignores a status response that was issued before run state changed', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 30 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    // The next poll hangs; its answer will be "no run".
    let release: () => void = () => {};
    const inFlight = new Promise<void>((r) => { release = r; });
    getProjectStatus.mockImplementation(async () => {
      await inFlight;
      return { project_id: 'p1', run: null, last_discovery: null };
    });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // While it is in flight, the operator cancels — an out-of-band change.
    cancelRun.mockResolvedValue({ status: 'cancelled', message: 'ok' });
    const cancelBtn = screen.getByText('Cancel');
    await act(async () => { cancelBtn.click(); await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Discovery cancelled')).toBeInTheDocument());

    // Now the stale answer lands. It must not take the cancelled run away.
    await act(async () => { release(); await jest.advanceTimersByTimeAsync(0); });

    expect(screen.getByText('Discovery cancelled')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// The cost of never clearing a live run, now paid off. A run the startup
// sweep failed and another tab then discarded is legitimately absent from the
// project-level answer, while this tab's cache still says `running` — so the
// panel used to sit there for ever with progress and Cancel, and isRunning
// kept the trigger disabled. The reconcile asks about that run directly.
it('clears a cached live run once the server says it was discarded', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 30 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    // The project reports none, and the run itself turns out to be discarded.
    getProjectStatus.mockResolvedValue({ project_id: 'p1', run: null, last_discovery: null });
    getRun.mockResolvedValue(makeRun({
      status: 'failed', discarded_at: '2026-10-14T18:00:00Z', last_checkpoint_step: 0,
    }));
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    await waitFor(() => expect(screen.queryByText('Cancel')).not.toBeInTheDocument());
  } finally {
    jest.useRealTimers();
  }
});

// A failed lookup is not evidence. Keep the panel and ask again next poll —
// clearing on a network blip is the regression this whole area keeps circling.
it('keeps a cached live run when the confirming lookup fails', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 30 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    getProjectStatus.mockResolvedValue({ project_id: 'p1', run: null, last_discovery: null });
    getRun.mockRejectedValue(new Error('network'));
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    expect(screen.getByText('Cancel')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// Two POLLS overlapping, which the out-of-band-only epoch did not fence. At a
// 2s interval they can, and an older empty answer arriving after a newer poll
// installed a live run erased it — with polling gated on a live run, until a
// reload. Reachable when another tab discards a dismissed run and starts a
// replacement.
it('drops an older poll answer that lands after a newer poll installed a run', async () => {
  jest.useFakeTimers();
  try {
    // First read: a live run, so the 2s interval is armed.
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 10 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    // Poll A hangs and will answer "no run".
    let releaseA: () => void = () => {};
    const a = new Promise<void>((r) => { releaseA = r; });
    getProjectStatus.mockImplementationOnce(async () => {
      await a;
      return { project_id: 'p1', run: null, last_discovery: null };
    });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // Poll B answers first, with a newer live run.
    getProjectStatus.mockResolvedValue(status(makeRun({ status: 'running', progress: 55, updated_at: 'later' })));
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
    await waitFor(() => expect(screen.getByText('55%')).toBeInTheDocument());

    // Now A's stale empty answer arrives. It must be dropped.
    // Several awaits deep (the deferred promise, then the response, then the
    // reconcile), so give the microtask chain room to drain.
    await act(async () => {
      releaseA();
      for (let i = 0; i < 6; i++) await jest.advanceTimersByTimeAsync(0);
    });

    expect(screen.getByText('55%')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// The mirror of the test above, and the reason one counter could not do both
// jobs: when the OLDER answer arrives first it applies, and a rule of "drop
// anything if `run` changed" then threw away the NEWER answer behind it. With
// the older snapshot `failed` and the newer one `running` — another tab
// resumed — the tab stuck on failed and stopped polling, because polling is
// gated on a live run.
it('applies the newer poll answer even when an older one arrived first', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 10 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    // Poll A (older) hangs; it will answer `failed`.
    let releaseA: () => void = () => {};
    const a = new Promise<void>((r) => { releaseA = r; });
    getProjectStatus.mockImplementationOnce(async () => {
      await a;
      return status(makeRun({ status: 'failed', last_checkpoint_step: 7, updated_at: 'older' }));
    });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // Poll B (newer) also hangs; it will answer `running`.
    let releaseB: () => void = () => {};
    const b = new Promise<void>((r) => { releaseB = r; });
    getProjectStatus.mockImplementationOnce(async () => {
      await b;
      return status(makeRun({ status: 'running', progress: 60, updated_at: 'newer' }));
    });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // A lands first, then B. B is the newer request, so B must win.
    await act(async () => {
      releaseA();
      for (let i = 0; i < 6; i++) await jest.advanceTimersByTimeAsync(0);
      releaseB();
      for (let i = 0; i < 6; i++) await jest.advanceTimersByTimeAsync(0);
    });

    await waitFor(() => expect(screen.getByText('60%')).toBeInTheDocument());
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// Isolates the poll-ORDERING check, which the other two overlap tests do not:
// their stale answer is empty, and the reconcile that follows an empty answer
// rescues the panel either way. A stale answer with CONTENT has no such
// rescue — the newer response applies first, then the older one overwrites it
// with a terminal snapshot and polling stops.
it('does not let an older poll answer overwrite a newer one it arrived behind', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 10 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    // Poll A (older) will answer `failed` — content, not an empty response.
    let releaseA: () => void = () => {};
    const a = new Promise<void>((r) => { releaseA = r; });
    getProjectStatus.mockImplementationOnce(async () => {
      await a;
      return status(makeRun({ status: 'failed', last_checkpoint_step: 7, updated_at: 'older' }));
    });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // Poll B (newer) answers immediately with a live run, and wins the race.
    getProjectStatus.mockResolvedValue(status(makeRun({ status: 'running', progress: 80, updated_at: 'newer' })));
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
    await waitFor(() => expect(screen.getByText('80%')).toBeInTheDocument());

    // Now A lands, behind B. It must be ignored.
    await act(async () => {
      releaseA();
      for (let i = 0; i < 6; i++) await jest.advanceTimersByTimeAsync(0);
    });

    expect(screen.getByText('80%')).toBeInTheDocument();
    expect(screen.queryByText(/Resume from step 7/)).not.toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// The reconcile has to survive a confirming lookup slower than the 2s poll
// interval: polls pile up behind it, and the panel must still resolve once it
// answers.
//
// NOT a regression test for the watermark change that accompanies it — this
// passes with the watermark claimed at the request too, which is how I know
// the starvation I was told about is not reachable that way: whichever poll
// holds the claim is the newest one, and its own reconcile completes when the
// lookup resolves. Kept because the behaviour is worth pinning on its own.
it('still reconciles when the confirming lookup outlives the poll interval', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(status(makeRun({ status: 'running', progress: 30 })));
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await waitFor(() => expect(screen.getByText('Cancel')).toBeInTheDocument());

    // Every subsequent status answer is empty — the run was discarded
    // elsewhere — so each poll must go to the reconcile to resolve it.
    getProjectStatus.mockResolvedValue({ project_id: 'p1', run: null, last_discovery: null });
    // The confirming lookup is slower than the interval, so later polls are
    // issued while the first reconcile is still outstanding.
    let releaseLookup: () => void = () => {};
    const slow = new Promise<void>((r) => { releaseLookup = r; });
    getRun.mockImplementation(async () => {
      await slow;
      return makeRun({ status: 'failed', discarded_at: '2026-10-14T19:00:00Z', last_checkpoint_step: 0 });
    });

    // Three intervals pass with the lookup still hanging.
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // It finally answers. The panel must clear, not stay stuck.
    await act(async () => {
      releaseLookup();
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });

    await waitFor(() => expect(screen.queryByText('Cancel')).not.toBeInTheDocument());
  } finally {
    jest.useRealTimers();
  }
});

// A poll's ticket orders it by when it was ISSUED, and the watermark only
// moved when a poll WROTE. So an answer that confirmed what was already on
// screen left the watermark behind it — and an older poll's still-pending
// answer could then pass the fence and act on a view of `run` that the
// confirmed answer had already superseded.
//
// Reachable from the dismissed-run case, where polling continues with the
// panel hidden: another tab discards the run and starts a replacement, this
// tab installs the replacement from one poll, confirms it with the next, and
// an empty answer issued in between then erases it. Polling is gated on a
// live run, so the panel and Cancel do not come back without a reload.
it('holds the poll watermark for an answer that confirmed the current run', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(
      status(makeRun({ status: 'failed', last_checkpoint_step: 7 })),
    );
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    // Dismissed, so the panel is hidden but the poll keeps running.
    await act(async () => { screen.getByText('Dismiss').click(); });

    // Poll A and then poll B are issued, both hanging. B carries the HIGHER
    // ticket and the older view of `run`.
    let releaseA: (v: ProjectStatus) => void = () => {};
    let releaseB: (v: ProjectStatus) => void = () => {};
    // 15s, not 2s: a dismissed run is watched at the slow cadence.
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { releaseA = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { releaseB = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });

    // A answers with the replacement run, which the panel adopts.
    const live = status(makeRun({ id: 'r2', status: 'running', progress: 55, updated_at: 'newer' }));
    await act(async () => {
      releaseA(live);
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText('55%')).toBeInTheDocument();

    // The next poll returns the same run: nothing to write, but the answer is
    // accepted — and it is NEWER than B.
    getProjectStatus.mockResolvedValue(live);
    await act(async () => { await jest.advanceTimersByTimeAsync(2000); });

    // Now B answers "no run". Its ticket is behind the confirming poll's, so
    // it must be dropped rather than clear a run that was just confirmed.
    await act(async () => {
      releaseB({ project_id: 'p1', run: null, last_discovery: null });
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });

    expect(screen.getByText('55%')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// The same ordering with the confirming poll removed, which the watermark
// cannot fence: B's ticket is genuinely the newest, so its empty answer is
// accepted — and then the reconcile has to decide WHICH run to ask about.
//
// From the closure it names the run that was installed when B was issued,
// which is now two polls out of date. And because that run is TERMINAL, the
// branch does not reach the lookup at all: a non-live cached run is taken as
// confirmation that the empty answer is right, and the panel is cleared with
// no server check — erasing a live run that a later poll had installed, with
// polling gated on a live run, so it does not come back without a reload.
// The lookup exists precisely to avoid deciding that blind; naming the wrong
// run is how it gets skipped.
it('reconciles an empty answer against the run that is installed now', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(
      status(makeRun({ id: 'r1', status: 'failed', last_checkpoint_step: 7 })),
    );
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await act(async () => { screen.getByText('Dismiss').click(); });

    // C is issued first and will install the replacement; B is issued while C
    // is still outstanding, so B carries the HIGHER ticket — the watermark
    // cannot fence it — and the OLDER view of `run`, which is r1.
    let releaseC: (v: ProjectStatus) => void = () => {};
    let releaseB: (v: ProjectStatus) => void = () => {};
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { releaseC = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { releaseB = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });

    await act(async () => {
      releaseC(status(makeRun({ id: 'r2', status: 'running', progress: 55, updated_at: 'newer' })));
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText('55%')).toBeInTheDocument();

    // r2 is alive and well; r1 is the archived run B's closure remembers.
    getRun.mockImplementation(async (runID: string) =>
      runID === 'r2'
        ? makeRun({ id: 'r2', status: 'running', progress: 55, updated_at: 'newer' })
        : makeRun({ id: 'r1', status: 'failed', discarded_at: '2026-10-15T09:00:00Z' }));

    await act(async () => {
      releaseB({ project_id: 'p1', run: null, last_discovery: null });
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });

    // The subject of the lookup is the assertion: ask about r1 and whatever
    // comes back is about the wrong run.
    expect(getRun).toHaveBeenCalledWith('r2');
    expect(getRun).not.toHaveBeenCalledWith('r1');
    expect(screen.getByText('55%')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// runChanged decides whether to WRITE, and comparing it against the closure
// asks the wrong question: "is this different from what I last saw" rather
// than "is this different from what is on screen". The two diverge exactly
// when an overlapping poll has installed something in between.
//
// Two dismissed-run polls both capture A. The first installs a newer failed
// run B. The second answers A — unchanged against its own closure, so it
// writes nothing — while B, which another tab has since discarded, stays on
// screen. B is terminal and is not the dismissed run, so polling stops: its
// Resume and Discard sit there indefinitely, both guaranteed to 409.
it('applies a poll answer that differs from the run on screen, not from its own closure', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(
      status(makeRun({ id: 'r1', status: 'failed', last_checkpoint_step: 7 })),
    );
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await act(async () => { screen.getByText('Dismiss').click(); });
    expect(screen.queryByText(/Resume from step/)).not.toBeInTheDocument();

    // Both polls are issued while A is installed, so both capture A.
    let release1: (v: ProjectStatus) => void = () => {};
    let release2: (v: ProjectStatus) => void = () => {};
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { release1 = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { release2 = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });

    // The first answers with a newer failed run, which is installed and
    // offers Resume because it has checkpoints.
    await act(async () => {
      release1(status(makeRun({ id: 'r2', status: 'failed', last_checkpoint_step: 9, updated_at: 'b' })));
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText(/Resume from step 9/)).toBeInTheDocument();

    // The second answers A: unchanged against its own closure, different
    // from what is actually on screen. B has been discarded elsewhere, so
    // leaving it up offers two buttons that cannot work.
    await act(async () => {
      release2(status(makeRun({ id: 'r1', status: 'failed', last_checkpoint_step: 7 })));
      for (let i = 0; i < 8; i++) await jest.advanceTimersByTimeAsync(0);
    });

    // A is the dismissed run, so adopting it hides the panel.
    expect(screen.queryByText(/Resume from step/)).not.toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// The mirror has to be exact, not eventual. Two poll replies can resolve in
// ONE microtask drain: the first queues its state update, and the second runs
// before any commit — so a mirror synced by an effect on [run] still holds
// the run from before the first reply.
//
// The second reply then decides against that older run. Here it is an empty
// status answer and the older run is terminal, so the branch takes the
// "nothing to reconcile" path and clears the panel outright, erasing the
// live replacement the first reply had just installed. Polling is gated on a
// live run, so progress and Cancel do not come back without a reload.
it('sees the run a reply installed when both replies land in one batch', async () => {
  jest.useFakeTimers();
  try {
    getProjectStatus.mockResolvedValueOnce(
      status(makeRun({ id: 'r1', status: 'failed', last_checkpoint_step: 7 })),
    );
    renderPage();
    await act(async () => { await jest.advanceTimersByTimeAsync(0); });
    await act(async () => { screen.getByText('Dismiss').click(); });

    let release1: (v: ProjectStatus) => void = () => {};
    let release2: (v: ProjectStatus) => void = () => {};
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { release1 = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });
    getProjectStatus.mockImplementationOnce(() => new Promise((r) => { release2 = r; }));
    await act(async () => { await jest.advanceTimersByTimeAsync(15000); });

    // The live replacement, and the run the reconcile should ask about.
    const live = makeRun({ id: 'r2', status: 'running', progress: 55, updated_at: 'newer' });
    getRun.mockResolvedValue(live);

    // Both released without yielding in between, so the second handler runs
    // before React commits the first.
    await act(async () => {
      release1(status(live));
      release2({ project_id: 'p1', run: null, last_discovery: null });
      for (let i = 0; i < 10; i++) await jest.advanceTimersByTimeAsync(0);
    });

    // The empty answer must be reconciled against r2 — which is live, so the
    // panel stays — rather than resolved against the run from before r2.
    expect(getRun).toHaveBeenCalledWith('r2');
    expect(screen.getByText('55%')).toBeInTheDocument();
    expect(screen.getByText('Cancel')).toBeInTheDocument();
  } finally {
    jest.useRealTimers();
  }
});

// After another tab discards the run, this tab's resume-refusal path
// refreshes through getRun and gets the ARCHIVED document back: still
// `failed`, still without a discovery, so the only thing distinguishing it is
// discarded_at. Without checking it, the button reappears offering a
// guaranteed 409.
it('withholds Discard from a run that is already discarded', async () => {
  getProjectStatus.mockResolvedValue(
    status(makeRun({
      status: 'failed',
      last_checkpoint_step: 0,
      discarded_at: '2026-10-14T15:33:45Z',
    })),
  );

  renderPage();
  await waitFor(() => expect(getProjectStatus).toHaveBeenCalled());
  await flush();

  expect(screen.queryByText('Discard')).not.toBeInTheDocument();
  // Nor Resume: discard unsets the checkpoint marker, so there is nothing to
  // resume from either.
  expect(screen.queryByText(/Resume from step/)).not.toBeInTheDocument();
});
});
