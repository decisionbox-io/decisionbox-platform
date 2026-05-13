'use client';

import Link from 'next/link';
import { SeverityBadge, AreaBadge } from '@/components/common/UIComponents';
import { searchResultHref } from '@/lib/searchNav';
import type { SearchResultItem } from '@/lib/api';

/**
 * Renders a single search result card with a Link to the detail page.
 *
 * The href is built by `searchResultHref` so the autocomplete dropdown
 * and the full-results page stay in sync — if the URL shape ever
 * changes again we only update one place.
 */
export function ResultCard({ item, projectId }: { item: SearchResultItem; projectId: string }) {
  const name = item.type === 'insight' ? item.name : (item.name || item.title || '');
  const score = Math.round(item.score * 100);

  return (
    <div style={{
      background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius)', padding: '12px 16px',
      display: 'flex', flexDirection: 'column', gap: 6,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span style={{
          fontSize: 11, fontWeight: 600, color: 'var(--db-blue-text)',
          background: 'var(--db-blue-bg)', padding: '2px 8px', borderRadius: 10,
        }}>
          {score}% match
        </span>
        {item.severity && <SeverityBadge severity={item.severity} type="severity" />}
        {item.analysis_area && <AreaBadge area={item.analysis_area} />}
        {item.project_name && (
          <span style={{
            fontSize: 11, color: 'var(--db-purple-text)', background: 'var(--db-purple-bg)',
            padding: '2px 8px', borderRadius: 10,
          }}>
            {item.project_name}
          </span>
        )}
        <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)', marginLeft: 'auto' }}>
          {item.discovered_at ? new Date(item.discovered_at).toLocaleDateString() : ''}
        </span>
      </div>
      <Link
        href={searchResultHref(item, projectId)}
        style={{ fontSize: 14, fontWeight: 500, color: 'var(--db-text-primary)', textDecoration: 'none' }}
      >
        {name}
      </Link>
      <p style={{ fontSize: 13, color: 'var(--db-text-secondary)', margin: 0, lineHeight: 1.5 }}>
        {item.description?.slice(0, 200)}{item.description?.length > 200 ? '...' : ''}
      </p>
    </div>
  );
}
