'use client';

import { isRestated, MaybeRestated } from '@/lib/restated';

/**
 * Marks a row the duplicate check judged a rewording of something already
 * reported, and says how sure it was.
 *
 * Only rendered where restatements are being shown — which is on request, so
 * the reader has asked to see them and the badge is what tells them why these
 * rows were not there a moment ago. Showing the score matters: the suppression
 * is a similarity judgement and it can be wrong, and a reader who can see 0.88
 * can disagree with it.
 */
export default function RestatedBadge({ doc }: { doc: MaybeRestated }) {
  if (!isRestated(doc)) return null;
  const score = typeof doc.similarity_score === 'number'
    ? ` ${doc.similarity_score.toFixed(2)}`
    : '';
  return (
    <span
      title={`Judged a restatement of ${doc.duplicate_of}${score ? `, similarity${score}` : ''}`}
      style={{
        fontSize: 11, padding: '1px 6px', marginLeft: 6,
        border: '0.5px solid var(--db-border-strong)',
        borderRadius: 'var(--db-radius)',
        background: 'var(--db-bg-subtle, var(--db-bg-white))',
        color: 'var(--db-text-secondary)',
        whiteSpace: 'nowrap',
      }}
    >
      restated{score}
    </span>
  );
}
