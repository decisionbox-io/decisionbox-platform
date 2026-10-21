// Shared by the two places that show the ledger: the project home page's
// "What happens next" card and the Ledger page itself.

/** How often a view showing an update in progress checks back. Reflection
 *  usually takes a minute or three; a quarter of a minute is quick enough to
 *  notice it finishing without hammering the API for the length of a long one. */
export const LEDGER_UPDATE_POLL_MS = 15_000;

/** How often a page watching a run in progress checks whether it has finished.
 *  A run takes many minutes; a quarter of a minute is plenty. */
export const RUN_WATCH_POLL_MS = 15_000;

/** Whether a run is still going — pending or running — and so has a ledger
 *  update still ahead of it. */
export function runInProgress(status: string | undefined): boolean {
  return status === 'running' || status === 'pending';
}

/** "2:04 PM" — when the update started, for the line that says so. Empty when
 *  the time cannot be read, so the line drops it rather than show nonsense. */
export function updateStartedAt(since: string | undefined): string {
  if (!since) return '';
  const at = new Date(since);
  if (Number.isNaN(at.getTime())) return '';
  return at.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}
