'use client';

/**
 * CitationLink renders one citation number — a small badge that
 * links to the source and reveals a CSS-only hover tooltip with the
 * source name, severity, and description.
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
 * hover, and link the same way, and a future change to the tooltip
 * (mobile tap support, different palette) updates everything.
 */

import React from 'react';
import Link from 'next/link';
import './CitationLink.css';

export interface CitationLinkProps {
  /** The number shown inside the badge. 1-based, in reading order. */
  number: number;
  /** Deep link the badge points at (insight / recommendation page). */
  href: string;
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
  return (
    <span className="cite-ref">
      <Link href={href} className="cite-badge">
        {number}
      </Link>
      <span className="cite-tooltip">
        <strong>{display}</strong>
        {severity && (
          <span style={{ marginLeft: 6, opacity: 0.7 }}>{severity}</span>
        )}
        {description && (
          <span style={{ display: 'block', marginTop: 4, opacity: 0.8, fontSize: 11 }}>
            {truncate(description, DESCRIPTION_MAX)}
          </span>
        )}
      </span>
    </span>
  );
}
