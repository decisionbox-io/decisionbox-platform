import type { SearchResultItem } from '@/lib/api';

// Maps a SearchResultItem's discriminator to the URL segment used by the
// detail routes under /projects/[id]/discoveries/[runId]/.
const DETAIL_SEGMENT: Record<SearchResultItem['type'], 'insights' | 'recommendations'> = {
  insight: 'insights',
  recommendation: 'recommendations',
};

/**
 * Build the dashboard URL for a single search result.
 *
 * The search API returns the standalone insight/recommendation `_id`
 * as `item.id` and the parent run as `item.discovery_id` — together with
 * `item.type` they uniquely address the detail page:
 *
 *   /projects/{projectId}/discoveries/{discoveryId}/{insights|recommendations}/{id}
 *
 * `project_id` is the only optional field in the response: cross-project
 * search populates it explicitly; the per-project search omits it because
 * the route already supplies the project context. `fallbackProjectId` is
 * the project from the current URL and is used only when `item.project_id`
 * is absent.
 */
export function searchResultHref(
  item: SearchResultItem,
  fallbackProjectId: string,
): string {
  const projectId = item.project_id || fallbackProjectId;
  const segment = DETAIL_SEGMENT[item.type];
  return `/projects/${projectId}/discoveries/${item.discovery_id}/${segment}/${item.id}`;
}
