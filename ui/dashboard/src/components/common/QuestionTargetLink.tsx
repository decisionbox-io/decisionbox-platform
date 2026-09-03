'use client';

import { useEffect, useState } from 'react';
import Link from 'next/link';
import citeStyles from '@/components/citations/CitationLink.module.css';
import { api, DiscoveryQuestion, DiscoveryResult } from '@/lib/api';

// Map the linked-target kind to the detail-route segment. Only insight /
// recommendation targets have a detail page; table / area targets don't, so
// QuestionTargetLink renders nothing for them.
const SEGMENT: Partial<Record<DiscoveryQuestion['linked_target']['type'], 'insights' | 'recommendations'>> = {
  insight: 'insights',
  recommendation: 'recommendations',
};

// Module-level cache: the review page can show many questions drawn from a
// handful of runs, so resolve each discovery's findings at most once for the
// hover previews rather than re-fetching per card.
const discoveryCache = new Map<string, Promise<DiscoveryResult | null>>();
function loadDiscovery(discoveryId: string): Promise<DiscoveryResult | null> {
  let p = discoveryCache.get(discoveryId);
  if (!p) {
    p = api.getDiscoveryById(discoveryId).catch(() => null);
    discoveryCache.set(discoveryId, p);
  }
  return p;
}

interface Preview { name?: string; chip?: string; description?: string }

function truncate(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n)}…` : s;
}

// QuestionTargetLink renders the "view insight / recommendation" jump under a
// question's rationale. It navigates to the finding's detail page and reveals a
// CSS-only hover/focus tooltip previewing that finding — the same reference
// tooltip pattern the Ask answers use (CitationLink), so the two look and behave
// alike. The preview (name + severity/priority + description) is resolved from
// the finding's discovery run, lazily and cached.
export default function QuestionTargetLink({ projectId, discoveryId, target }: {
  projectId: string;
  discoveryId: string;
  target: DiscoveryQuestion['linked_target'];
}) {
  const [preview, setPreview] = useState<Preview | null>(null);
  const segment = target ? SEGMENT[target.type] : undefined;
  const resolvable = Boolean(segment && discoveryId && target?.id);

  useEffect(() => {
    if (!resolvable) return;
    let alive = true;
    loadDiscovery(discoveryId).then((d) => {
      if (!alive || !d) return;
      if (target.type === 'insight') {
        const it = (d.insights || []).find((x) => x.id === target.id);
        if (it) setPreview({ name: it.name, chip: it.severity, description: it.description });
      } else {
        const it = (d.recommendations || []).find((x) => x.id === target.id);
        if (it) setPreview({ name: it.title, chip: it.priority ? `priority ${it.priority}` : undefined, description: it.description });
      }
    });
    return () => { alive = false; };
  }, [resolvable, discoveryId, target?.id, target?.type]);

  if (!resolvable) return null;
  const href = `/projects/${projectId}/discoveries/${discoveryId}/${segment}/${target.id}`;

  return (
    <>
      {' · '}
      <span className={citeStyles.citeRef}>
        <Link
          href={href}
          style={{ fontSize: 12, color: 'var(--db-text-link)', textDecoration: 'none', cursor: 'pointer' }}
        >
          view {target.type}
        </Link>
        <span className={citeStyles.citeTooltip}>
          <strong>{preview?.name ? truncate(preview.name, 80) : `Open ${target.type}`}</strong>
          {preview?.chip && <span className={citeStyles.tooltipSeverity}>{preview.chip}</span>}
          {preview?.description && (
            <span className={citeStyles.tooltipDescription}>{truncate(preview.description, 120)}</span>
          )}
        </span>
      </span>
    </>
  );
}
