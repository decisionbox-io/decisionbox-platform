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
 * Collapses rows that share a key, keeping the one a reader should be sent to.
 *
 * These lists show one row per subject across runs, keyed by title or by
 * area+name, and they kept whichever came first. That is wrong once
 * restatements are hidden: if the first row for a subject is a restatement and
 * the canonical it points at carries the same key, the canonical was discarded
 * here and the restatement was then hidden by the split — so the subject
 * vanished from the list entirely, having been reported twice.
 *
 * A canonical row therefore wins over a restatement for the same key.
 * Otherwise the first still wins, which is the previous behaviour and is the
 * newest row, since these lists are built newest-run first.
 */
export function collapseByKey<T extends MaybeRestated>(docs: T[], key: (doc: T) => string): T[] {
  const at = new Map<string, number>();
  const out: T[] = [];
  for (const doc of docs) {
    const k = key(doc);
    const seen = at.get(k);
    if (seen === undefined) {
      at.set(k, out.length);
      out.push(doc);
      continue;
    }
    // Replace a kept restatement with the canonical for the same subject.
    if (isRestated(out[seen]) && !isRestated(doc)) {
      out[seen] = doc;
    }
  }
  return out;
}
