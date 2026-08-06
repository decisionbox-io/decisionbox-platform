import type { SearchResultItem } from '@/lib/api';

// Maps a SearchResultItem's discriminator to the URL segment used by the
// detail routes under /projects/[id]/discoveries/[runId]/.
//
// 'source_chunk' is absent deliberately: a knowledge-source citation has
// no discovery run and therefore no detail route under it. Search never
// returns that type — it queries discovery findings — but the shared
// result type admits it, so searchResultHref handles it explicitly
// rather than building a nonsense /discoveries/undefined/... URL.
const DETAIL_SEGMENT: Partial<Record<SearchResultItem['type'], 'insights' | 'recommendations'>> = {
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
  if (!segment) {
    // Not a discovery finding — send the user to the project's knowledge
    // sources rather than a detail route that cannot exist.
    return `/projects/${projectId}/sources`;
  }
  return `/projects/${projectId}/discoveries/${item.discovery_id}/${segment}/${item.id}`;
}
