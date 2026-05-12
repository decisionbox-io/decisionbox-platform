'use client';

import Link from 'next/link';
import { IconBulb, IconStarFilled } from '@tabler/icons-react';
import { SeverityBadge } from '@/components/common/UIComponents';
import type { SearchResultItem } from '@/lib/api';

/**
 * sourceHref builds the deep link from a cited insight or recommendation back
 * to its detail page inside the project. Mirrors the helper that used to live
 * inline in the Ask page so both the Ask reply card and the upcoming
 * executive-summary newspaper resolve the same URL shape.
 *
 * Sources that are missing a discovery_id (e.g. cross-project search hits)
 * fall back to the project-level list page — better than 404 when the user
 * clicks through.
 */
export function sourceHref(projectId: string, src: SearchResultItem): string {
  const type = src.type === 'insight' ? 'insights' : 'recommendations';
  if (src.discovery_id) {
    return `/projects/${projectId}/discoveries/${src.discovery_id}/${type}/${src.id}`;
  }
  return `/projects/${projectId}/${type}`;
}

export interface CitationsFooterProps {
  projectId: string;
  sources: SearchResultItem[];
  /**
   * Optional heading label. Defaults to "Sources" — override when the
   * surrounding component already says "Sources" elsewhere on the page.
   */
  heading?: string;
  /**
   * Score precision toggle. When false (default for newspaper layouts), the
   * relevance percentage is hidden — citations there are inline and don't
   * carry a search-relevance score. Ask reply cards keep it visible.
   */
  showScore?: boolean;
}

/**
 * CitationsFooter renders a deduplicated, ordered list of cited insights
 * and recommendations as a footer block under an answer or article.
 *
 * Number assignment follows the order the items appear in `sources` and
 * dedupes by `(type, id)` so the same insight referenced twice in the
 * body still resolves to a single numbered entry. Empty source lists
 * render `null` so consumers can drop this component into a layout
 * without conditional rendering at the call site.
 */
export default function CitationsFooter({
  projectId,
  sources,
  heading = 'Sources',
  showScore = true,
}: CitationsFooterProps) {
  const numbered = numberSources(sources);
  if (numbered.length === 0) return null;
  return (
    <div
      style={{
        marginTop: 14,
        paddingTop: 14,
        borderTop: '1px solid var(--db-border-default)',
      }}
      data-testid="citations-footer"
    >
      <h4
        style={{
          fontSize: 12,
          fontWeight: 600,
          color: 'var(--db-text-tertiary)',
          marginBottom: 8,
        }}
      >
        {heading}
      </h4>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
        {numbered.map(({ src, number }, j) => (
          <Link
            key={`${src.type}-${src.id}`}
            href={sourceHref(projectId, src)}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              fontSize: 12,
              color: 'var(--db-text-link)',
              textDecoration: 'none',
              padding: '3px 6px',
              borderRadius: 4,
              background: j % 2 === 0 ? 'var(--db-bg-muted)' : 'transparent',
            }}
            data-testid="citations-footer-item"
          >
            {src.type === 'insight' ? (
              <IconBulb size={12} color="var(--db-amber-text)" />
            ) : (
              <IconStarFilled size={12} color="var(--db-purple-text)" />
            )}
            <span
              style={{
                flex: 1,
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                whiteSpace: 'nowrap',
              }}
            >
              [{number}] {src.name || src.title}
            </span>
            {src.severity && <SeverityBadge severity={src.severity} type="severity" />}
            {showScore && (
              <span style={{ fontSize: 10, color: 'var(--db-text-tertiary)' }}>
                {Math.round(src.score * 100)}%
              </span>
            )}
          </Link>
        ))}
      </div>
    </div>
  );
}

interface NumberedSource {
  src: SearchResultItem;
  number: number;
}

/**
 * numberSources walks a flat source list and assigns a citation number to
 * each unique (type, id) pair in first-seen order. Duplicates collapse to
 * the existing entry rather than getting a new number — matters when a
 * paragraph cites the same insight twice or two paragraphs share a
 * citation. Exported for tests and for future reuse by the newspaper
 * inline-token resolver.
 */
export function numberSources(sources: SearchResultItem[]): NumberedSource[] {
  const seen = new Map<string, NumberedSource>();
  const ordered: NumberedSource[] = [];
  for (const src of sources) {
    const key = `${src.type}::${src.id}`;
    if (seen.has(key)) continue;
    const entry: NumberedSource = { src, number: ordered.length + 1 };
    seen.set(key, entry);
    ordered.push(entry);
  }
  return ordered;
}
