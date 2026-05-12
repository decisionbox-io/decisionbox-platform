'use client';

/**
 * CitationLink renders one citation number — a small badge that
 * links to the source and reveals a CSS-only hover/focus tooltip
 * with the source name, severity, and description.
 *
 * Two callers share it today:
 *
 *  - The Ask page, which scans markdown answers for `[1,2]` patterns
 *    and emits one CitationLink per matched number.
 *  - The enterprise executive-summary "newspaper" renderer, which
 *    emits one CitationLink per `{{I:id}}` / `{{R:id}}` token in
 *    prose and one per explicit Citation in card / stat / story /
 *    bar / action arrays.
 *
 * Owning the badge + tooltip in one place means both surfaces look,
 * hover, and link the same way, and a future change (mobile tap
 * support, palette tweak) updates everything.
 *
 * `href` is optional: when undefined (the source couldn't be
 * resolved in the project's insight/recommendation list) the badge
 * renders as a non-interactive `<span>` rather than a `<Link>` to
 * "#" — clicking a stale citation should never scroll the page to
 * the top.
 */

import React from 'react';
import Link from 'next/link';
import styles from './CitationLink.module.css';

export interface CitationLinkProps {
  /** The number shown inside the badge. 1-based, in reading order. */
  number: number;
  /**
   * Deep link the badge points at (insight / recommendation page).
   * Omit when the citation is unresolved — the badge renders as
   * static text with the same visual styling.
   */
  href?: string;
  /**
   * The source title shown bold in the tooltip. Falls back to
   * "Source N" when the citation can't be resolved against the
   * project's insight + recommendation lists.
   */
  name?: string;
  /** Optional severity chip text (e.g. "high", "medium"). */
  severity?: string;
  /** Optional description, truncated to 120 characters in the tooltip. */
  description?: string;
}

/**
 * Maximum lengths kept conservative so the 280px-wide tooltip stays
 * readable across all dashboard themes — clipping at the source.
 */
const NAME_MAX = 80;
const DESCRIPTION_MAX = 120;

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}...` : s;
}

export function CitationLink({
  number,
  href,
  name,
  severity,
  description,
}: CitationLinkProps): React.JSX.Element {
  const display = name ? truncate(name, NAME_MAX) : `Source ${number}`;
  const badge = href ? (
    <Link href={href} className={`${styles.citeBadge} ${styles.citeBadgeLink}`}>
      {number}
    </Link>
  ) : (
    // tabIndex=0 keeps the badge in the tab order so :focus-within
    // can reveal the tooltip for keyboard users. The "note" role
    // tells assistive tech the element is informational, not an
    // action surface.
    <span
      className={`${styles.citeBadge} ${styles.citeBadgeStatic}`}
      role="note"
      tabIndex={0}
    >
      {number}
    </span>
  );
  return (
    <span className={styles.citeRef}>
      {badge}
      <span className={styles.citeTooltip}>
        <strong>{display}</strong>
        {severity && <span className={styles.tooltipSeverity}>{severity}</span>}
        {description && (
          <span className={styles.tooltipDescription}>
            {truncate(description, DESCRIPTION_MAX)}
          </span>
        )}
      </span>
    </span>
  );
}
