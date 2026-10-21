/**
 * @jest-environment jsdom
 */
import '@testing-library/jest-dom';
import { render, screen, waitFor } from '@testing-library/react';
import { MantineProvider } from '@mantine/core';
import { UpcomingInvestigation } from '@/components/projects/UpcomingInvestigation';
import type { LedgerView } from '@/lib/api';

// The poll is a quarter of a minute in the product; a few milliseconds here,
// so a test can watch an update finish without fake timers fighting Mantine.
jest.mock('@/components/ledger/updating', () => ({
  ...jest.requireActual('@/components/ledger/updating'),
  LEDGER_UPDATE_POLL_MS: 10,
}));

const getLedger = jest.fn();
const listPackProposals = jest.fn();
jest.mock('@/lib/api', () => ({
  api: {
    getLedger: (...a: unknown[]) => getLedger(...a),
    listPackProposals: (...a: unknown[]) => listPackProposals(...a),
  },
}));

const view = (over: Partial<LedgerView>): LedgerView => ({
  coverage: { explored_tables: [], summary: '' },
  convergence: [],
  findings: [],
  tasks: [],
  ...over,
});
const thread = (id: string, title: string) => ({ id, text: title, title, kind: 'next_task', status: 'open' });

function wrap(refreshKey?: string) {
  return render(
    <MantineProvider><UpcomingInvestigation projectId="p1" refreshKey={refreshKey} /></MantineProvider>,
  );
}

beforeEach(() => {
  jest.clearAllMocks();
  window.localStorage.clear();
  listPackProposals.mockResolvedValue([]);
});

describe('UpcomingInvestigation', () => {
  it('shows the open threads, and says nothing about an update when there is none', async () => {
    getLedger.mockResolvedValue(view({ tasks: [thread('t1', 'Check the hubs')] }));
    wrap();
    expect(await screen.findByText('Check the hubs')).toBeInTheDocument();
    expect(screen.getByText(/Carried into your next discovery run/)).toBeInTheDocument();
    expect(screen.queryByText('Updating')).not.toBeInTheDocument();
  });

  it('says the threads are from before the run that just finished, then swaps in the new ones', async () => {
    // The run is marked completed before its findings reach the ledger, so
    // the first answer is the old queue with the update still in flight.
    // The SAME answer twice: whether the card keeps checking must not depend
    // on getting a new object back each time.
    const inFlight = view({
      tasks: [thread('t1', 'An old thread')],
      updating: { run_id: 'r9', since: '2026-10-21T14:02:00Z' },
    });
    getLedger
      .mockResolvedValueOnce(inFlight)
      .mockResolvedValueOnce(inFlight)
      .mockResolvedValueOnce(inFlight)
      .mockResolvedValue(view({ tasks: [thread('t2', 'A new thread')] }));
    wrap();

    expect(await screen.findByText('An old thread')).toBeInTheDocument();
    expect(screen.getByText('Updating')).toBeInTheDocument();
    expect(screen.getByTestId('ledger-updating')).toHaveTextContent(/threads from before it/);

    // It checks back on its own until the ledger says it is done...
    expect(await screen.findByText('A new thread')).toBeInTheDocument();
    expect(getLedger.mock.calls.length).toBeGreaterThanOrEqual(4);
    // ...and then stops saying it is updating, and stops asking.
    expect(screen.queryByText('Updating')).not.toBeInTheDocument();
    expect(screen.queryByTestId('ledger-updating')).not.toBeInTheDocument();
    const calls = getLedger.mock.calls.length;
    await new Promise((r) => setTimeout(r, 60));
    expect(getLedger.mock.calls.length).toBe(calls);
  });

  it('keeps what it shows and keeps checking when a check fails mid-update', async () => {
    // A failed request is not an answer. Read as one, it emptied the card and
    // stopped checking while the update was still going.
    const inFlight = view({
      tasks: [thread('t1', 'An old thread')],
      updating: { run_id: 'r9', since: '2026-10-21T14:02:00Z' },
    });
    getLedger
      .mockResolvedValueOnce(inFlight)
      .mockRejectedValue(new Error('network blip'));
    wrap();
    expect(await screen.findByText('An old thread')).toBeInTheDocument();
    // Through several failed checks, the card still shows the old threads and
    // still says why...
    await waitFor(() => expect(getLedger.mock.calls.length).toBeGreaterThanOrEqual(4));
    expect(screen.getByText('An old thread')).toBeInTheDocument();
    expect(screen.getByText('Updating')).toBeInTheDocument();
    // ...and it is still checking: when the ledger answers again, done, the
    // new threads arrive and the badge goes.
    getLedger.mockResolvedValue(view({ tasks: [thread('t2', 'A new thread')] }));
    expect(await screen.findByText('A new thread')).toBeInTheDocument();
    expect(screen.queryByText('Updating')).not.toBeInTheDocument();
  });

  it('does not let a slow earlier load overwrite a newer one', async () => {
    // The first load is still out when the page sees the run finish and
    // loads again. The newer answer is the one that stands.
    let releaseFirst: (v: LedgerView) => void = () => {};
    getLedger
      .mockImplementationOnce(() => new Promise<LedgerView>((res) => { releaseFirst = res; }))
      .mockResolvedValue(view({
        tasks: [thread('t2', 'After the run')],
        updating: { run_id: 'r2', since: '2026-10-21T14:02:00Z' },
      }));
    const { rerender } = wrap('');
    rerender(<MantineProvider><UpcomingInvestigation projectId="p1" refreshKey="r2" /></MantineProvider>);
    expect(await screen.findByText('After the run')).toBeInTheDocument();

    // The stale first answer finally arrives — from before the run, idle.
    releaseFirst(view({ tasks: [thread('t1', 'Before the run')] }));
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByText('Before the run')).not.toBeInTheDocument();
    expect(screen.getByText('Updating')).toBeInTheDocument();
  });

  it('asks again when the reload a run\'s finish triggers fails', async () => {
    // The card loaded while the run was going, so nothing was polling. Fail
    // that one reload and, without a retry, the old threads stay up for good.
    getLedger.mockResolvedValueOnce(view({ tasks: [thread('t1', 'Before the run')] }));
    const { rerender } = wrap('');
    await screen.findByText('Before the run');

    getLedger
      .mockRejectedValueOnce(new Error('network blip'))
      .mockResolvedValue(view({
        tasks: [thread('t1', 'Before the run')],
        updating: { run_id: 'r2', since: '2026-10-21T14:02:00Z' },
      }));
    rerender(<MantineProvider><UpcomingInvestigation projectId="p1" refreshKey="r2" /></MantineProvider>);
    expect(await screen.findByText('Updating')).toBeInTheDocument();
  });

  it('does not keep asking a deployment that has no ledger', async () => {
    // Community builds 404 the route: nothing to show, nothing to wait for.
    getLedger.mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }));
    const { container } = wrap('r1');
    await waitFor(() => expect(getLedger).toHaveBeenCalled());
    await new Promise((r) => setTimeout(r, 60));
    expect(getLedger).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.mantine-Card-root')).toBeNull();
  });

  it('clears itself when a later load finds no ledger at all', async () => {
    // A 404 after a good load: what the card showed is gone, including an
    // Updating badge that nothing would ever clear.
    getLedger
      .mockResolvedValueOnce(view({
        tasks: [thread('t1', 'An old thread')],
        updating: { run_id: 'r9', since: '2026-10-21T14:02:00Z' },
      }))
      .mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }));
    const { container } = wrap();
    expect(await screen.findByText('An old thread')).toBeInTheDocument();
    await waitFor(() => expect(container.querySelector('.mantine-Card-root')).toBeNull());
  });

  it('shows up for a first run whose threads are still being built, instead of staying hidden', async () => {
    getLedger.mockResolvedValue(view({ updating: { run_id: 'r1', since: '2026-10-21T14:02:00Z' } }));
    wrap();
    expect(await screen.findByTestId('ledger-updating')).toHaveTextContent(/Its threads will appear here/);
  });

  it('renders nothing when there is nothing upcoming and nothing being built', async () => {
    getLedger.mockResolvedValue(view({}));
    const { container } = wrap();
    await waitFor(() => expect(getLedger).toHaveBeenCalled());
    expect(container.querySelector('.mantine-Card-root')).toBeNull();
  });

  it('looks again when the page sees a run finish', async () => {
    // The card may have loaded while the run was still going, when the ledger
    // had nothing new to say yet.
    getLedger.mockResolvedValue(view({ tasks: [thread('t1', 'Check the hubs')] }));
    const { rerender } = wrap('');
    await screen.findByText('Check the hubs');
    const before = getLedger.mock.calls.length;

    getLedger.mockResolvedValue(view({
      tasks: [thread('t1', 'Check the hubs')],
      updating: { run_id: 'r2', since: '2026-10-21T14:02:00Z' },
    }));
    rerender(<MantineProvider><UpcomingInvestigation projectId="p1" refreshKey="r2" /></MantineProvider>);
    expect(await screen.findByText('Updating')).toBeInTheDocument();
    expect(getLedger.mock.calls.length).toBeGreaterThan(before);
  });
});
