/**
 * Restated insights and recommendations — the ones Phase 9 judged a rewording
 * of something already reported.
 *
 * Hidden by default, because a list that shows three wordings of one finding
 * asks the reader to notice they are the same. Never *removed*: the judgement
 * is a similarity score and it can be wrong, so the rows stay one click away
 * with the score that hid them, which is the same contract the API's
 * `include_duplicates=1` offers.
 *
 * Absence of `duplicate_of` means "not a restatement", not "unknown" — the
 * field is omitempty on the wire and every document written before the mark
 * existed has no such key.
 */

/** A document that carries the mark. Both Insight and Recommendation do. */
export interface MaybeRestated {
  duplicate_of?: string;
  similarity_score?: number;
}

export function isRestated(doc: MaybeRestated): boolean {
  return !!doc.duplicate_of;
}

/**
 * Splits a list into what to show and what the toggle reveals, in one pass and
 * preserving order. Returning both is deliberate: the count of hidden rows has
 * to come from the same split that hid them, or a header and its list can
 * disagree.
 */
export function splitRestated<T extends MaybeRestated>(docs: T[]): { shown: T[]; restated: T[] } {
  const shown: T[] = [];
  const restated: T[] = [];
  for (const doc of docs) {
    (isRestated(doc) ? restated : shown).push(doc);
  }
  return { shown, restated };
}

/** The label for the reveal control. Singular for one, so it reads as English. */
export function restatedToggleLabel(count: number, showing: boolean): string {
  const noun = count === 1 ? 'restated item' : 'restated items';
  return showing ? `Hide ${count} ${noun}` : `Show ${count} ${noun}`;
}

/**
 * Collapses rows that share a key, keeping at most one canonical and at most
 * one restatement for each.
 *
 * These lists show one row per subject across runs and used to keep whichever
 * came first. That breaks in both directions once restatements are hidden, and
 * I got it wrong each way in turn:
 *
 *   - keep-first alone: if the first row for a subject was a restatement and
 *     the canonical shared its key, the canonical was dropped here and the
 *     restatement hidden by the split, so a subject reported TWICE vanished.
 *   - canonical-wins alone: the restatement was dropped here instead, so the
 *     reveal control could not show it or the score that hid it — and a
 *     suppression nothing can reveal is the one thing this must never be.
 *
 * Keeping one of each is what satisfies both: the default view shows the
 * canonical, the toggle reveals the restatement beside it, and the count the
 * toggle offers is the number of rows it can actually produce. Among rows of
 * the same kind the first still wins, which is the newest, since these lists
 * are built newest-run first.
 */
export function collapseByKey<T extends MaybeRestated>(docs: T[], key: (doc: T) => string): T[] {
  const seen = new Set<string>();
  const out: T[] = [];
  for (const doc of docs) {
    // The kind is part of the identity here: a canonical and a restatement of
    // the same subject are two rows the reader may need, not one row twice.
    const k = (isRestated(doc) ? 'r:' : 'c:') + key(doc);
    if (seen.has(k)) {
      continue;
    }
    seen.add(k);
    out.push(doc);
  }
  return out;
}
