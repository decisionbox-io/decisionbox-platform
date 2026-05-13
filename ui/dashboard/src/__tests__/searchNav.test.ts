/**
 * Unit tests for the searchResultHref URL builder.
 *
 * The function powers two click paths — the Spotlight autocomplete and
 * the full-results page — that were previously dropping the result's
 * own id and landing the user on the discovery overview.
 *
 * The API contract (services/api/internal/handler/search.go) guarantees
 * `id`, `type`, and `discovery_id` are populated. Only `project_id` is
 * optional (omitted by same-project search, set by cross-project search),
 * so those are the cases we pin.
 */

import { searchResultHref } from '@/lib/searchNav';
import type { SearchResultItem } from '@/lib/api';

const baseInsight = (over: Partial<SearchResultItem> = {}): SearchResultItem => ({
  id: 'ins-1',
  type: 'insight',
  score: 0.91,
  name: 'High churn at L45',
  description: 'desc',
  discovery_id: 'run-1',
  discovered_at: '2026-05-13T00:00:00Z',
  project_id: 'proj-1',
  ...over,
});

const baseRec = (over: Partial<SearchResultItem> = {}): SearchResultItem => ({
  id: 'rec-7',
  type: 'recommendation',
  score: 0.88,
  name: 'Reduce friction at sign-up',
  description: 'desc',
  discovery_id: 'run-2',
  discovered_at: '2026-05-13T00:00:00Z',
  project_id: 'proj-1',
  ...over,
});

describe('searchResultHref', () => {
  it('builds the insight detail URL', () => {
    expect(searchResultHref(baseInsight(), 'fallback-proj')).toBe(
      '/projects/proj-1/discoveries/run-1/insights/ins-1',
    );
  });

  it('builds the recommendation detail URL with the plural segment', () => {
    // The detail route uses /recommendations/, not /recommendation/, so
    // this guards the singular-vs-plural mapping.
    expect(searchResultHref(baseRec(), 'fallback-proj')).toBe(
      '/projects/proj-1/discoveries/run-2/recommendations/rec-7',
    );
  });

  it('falls back to the route project id when item.project_id is omitted', () => {
    // Same-project search responses omit project_id; the per-project
    // route supplies it via fallbackProjectId.
    const item = baseInsight({ project_id: undefined });
    expect(searchResultHref(item, 'route-proj')).toBe(
      '/projects/route-proj/discoveries/run-1/insights/ins-1',
    );
  });

  it('prefers the item project id when both are set (cross-project search)', () => {
    // Cross-project search returns items whose project_id differs from
    // the route — clicking must navigate to the result's project, not
    // the page's project, or the detail page will 404.
    const item = baseInsight({ project_id: 'other-proj' });
    expect(searchResultHref(item, 'route-proj')).toBe(
      '/projects/other-proj/discoveries/run-1/insights/ins-1',
    );
  });
});
