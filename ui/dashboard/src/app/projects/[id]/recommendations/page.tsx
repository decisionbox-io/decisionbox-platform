'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import { Loader } from '@mantine/core';
import { IconChevronDown, IconStack2 } from '@tabler/icons-react';
import { useDisclosure } from '@mantine/hooks';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import {
  SectionHeader, Pill, EmptyState, SearchInput, Pagination, normalizeConfidence,
} from '@/components/common/UIComponents';
import { api, Feedback, Insight, Recommendation } from '@/lib/api';

interface RecWithContext extends Recommendation {
  discoveryId: string;
  discoveryDate: string;
  discoveryInsights: Insight[];
}

export default function RecommendationsListPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<{ name: string } | null>(null);
  const [allRecs, setAllRecs] = useState<RecWithContext[]>([]);
  const [feedbackMap, setFeedbackMap] = useState<Record<string, Feedback>>({});
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');
  const [sortBy, setSortBy] = useState('Priority');
  const [page, setPage] = useState(1);
  const pageSize = 10;

  useEffect(() => {
    Promise.all([
      api.getProject(id).then(p => setProject({ name: p.name })),
      api.listDiscoveries(id).then(discoveries => {
        const recs: RecWithContext[] = [];
        const seen = new Set<string>();

        for (const d of (discoveries || [])) {
          api.listFeedback(d.id).then(fb => {
            for (const f of (fb || [])) {
              setFeedbackMap(prev => ({ ...prev, [`${f.target_type}:${f.target_id}:${f.discovery_id}`]: f }));
            }
          }).catch(() => {});

          for (const rec of (d.recommendations || [])) {
            // Deduplicate by title (show latest)
            if (seen.has(rec.title)) continue;
            seen.add(rec.title);

            recs.push({
              ...rec,
              discoveryId: d.id,
              discoveryDate: d.discovery_date,
              discoveryInsights: d.insights || [],
            });
          }
        }
        setAllRecs(recs);
      }),
    ])
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [id]);

  if (loading) return <Shell><Loader /></Shell>;

  // Filter
  let filtered = allRecs;
  if (search) {
    const q = search.toLowerCase();
    filtered = filtered.filter(r =>
      r.title.toLowerCase().includes(q) ||
      r.description?.toLowerCase().includes(q) ||
      r.category?.toLowerCase().includes(q) ||
      r.target_segment?.toLowerCase().includes(q)
    );
  }

  // Sort
  filtered = [...filtered].sort((a, b) => {
    switch (sortBy) {
      case 'Priority': return a.priority - b.priority;
      case 'Confidence': return (b.confidence || 0) - (a.confidence || 0);
      case 'Segment size': return (b.segment_size || 0) - (a.segment_size || 0);
      case 'Date': return new Date(b.discoveryDate).getTime() - new Date(a.discoveryDate).getTime();
      default: return a.priority - b.priority;
    }
  });

  const totalPages = Math.ceil(filtered.length / pageSize);
  const paged = filtered.slice((page - 1) * pageSize, page * pageSize);

  const breadcrumb = [
    { label: 'Projects', href: '/' },
    { label: project?.name || '...', href: `/projects/${id}` },
    { label: 'Recommendations' },
  ];

  const effortColors: Record<string, { bg: string; color: string }> = {
    low: { bg: '#EAF3DE', color: '#3B6D11' },
    medium: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
    high: { bg: '#FAECE7', color: '#993C1D' },
  };

  return (
    <Shell breadcrumb={breadcrumb}>
      <SectionHeader title="All Recommendations" count={filtered.length} right={
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <SearchInput value={search} onChange={setSearch} placeholder="Search recommendations..." />
          <SortDropdown value={sortBy} onChange={setSortBy}
            options={['Priority', 'Confidence', 'Segment size', 'Date']} />
        </div>
      } />

      {filtered.length > 0 ? (<>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {paged.map((rec, idx) => {
            const effort = rec.priority <= 1 ? 'low' : rec.priority <= 3 ? 'medium' : 'high';
            const effortStyle = effortColors[effort] || effortColors.medium;
            const relatedInsights = (rec.related_insight_ids || [])
              .map(rid => rec.discoveryInsights.find(i => i.id === rid))
              .filter(Boolean) as Insight[];

            return (
              <div key={idx} style={{
                background: 'var(--db-bg-white)',
                border: '1px solid var(--db-border-default)',
                borderRadius: 'var(--db-radius-lg)',
                padding: '16px 20px',
              }}>
                {/* Title row */}
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 8, marginBottom: 6 }}>
                  <div style={{ fontSize: 14, fontWeight: 500, flex: 1 }}>{rec.title}</div>
                  <FeedbackButtons projectId={id} discoveryId={rec.discoveryId} targetType="recommendation"
                    targetId={String(idx)}
                    feedback={feedbackMap[`recommendation:${idx}:${rec.discoveryId}`]} />
                </div>

                {/* Pills row */}
                <div style={{ display: 'flex', gap: 6, marginBottom: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                  <Pill bg={effortStyle.bg} color={effortStyle.color}>
                    {effort.charAt(0).toUpperCase() + effort.slice(1)} effort
                  </Pill>
                  {rec.expected_impact?.estimated_improvement && (
                    rec.expected_impact.estimated_improvement.length > 30
                      ? <span style={{ fontSize: 12, color: 'var(--db-green-text)' }}>
                          {rec.expected_impact.estimated_improvement}
                        </span>
                      : <Pill bg="var(--db-green-bg)" color="var(--db-green-text)">
                          {rec.expected_impact.estimated_improvement}
                        </Pill>
                  )}
                  <Link href={`/projects/${id}/discoveries/${rec.discoveryId}`}
                    style={{
                      fontSize: 11, padding: '2px 8px', borderRadius: 'var(--db-radius)',
                      background: 'var(--db-bg-muted)', color: 'var(--db-text-tertiary)',
                      textDecoration: 'none', transition: 'color 120ms ease',
                    }}
                    onMouseEnter={e => { e.currentTarget.style.color = 'var(--db-text-link)'; }}
                    onMouseLeave={e => { e.currentTarget.style.color = 'var(--db-text-tertiary)'; }}
                  >
                    {new Date(rec.discoveryDate).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}
                  </Link>
                </div>

                {/* Metadata */}
                <div style={{ display: 'flex', gap: 16, fontSize: 12, color: 'var(--db-text-tertiary)', marginBottom: 8, flexWrap: 'wrap' }}>
                  {rec.target_segment && (
                    <span>{rec.segment_size?.toLocaleString() || ''} {rec.target_segment}</span>
                  )}
                  {rec.expected_impact?.metric && <span>{rec.expected_impact.metric}</span>}
                  {rec.confidence > 0 && <span>Confidence: {normalizeConfidence(rec.confidence)}%</span>}
                </div>

                {/* Related insights */}
                {relatedInsights.length > 0 && (
                  <div style={{ display: 'flex', gap: 6, marginBottom: 8, flexWrap: 'wrap', alignItems: 'center' }}>
                    <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)', fontWeight: 500 }}>Addresses:</span>
                    {relatedInsights.map((insight, i) => (
                      <Link key={i} href={`/projects/${id}/discoveries/${rec.discoveryId}/insights/${insight.id}`}
                        style={{
                          fontSize: 11, padding: '1px 8px', borderRadius: 'var(--db-radius)',
                          background: 'var(--db-blue-bg)', color: 'var(--db-blue-text)',
                          textDecoration: 'none',
                        }}
                      >
                        {insight.name}
                      </Link>
                    ))}
                  </div>
                )}

                {/* Description */}
                {rec.description && (
                  <div style={{ fontSize: 13, color: 'var(--db-text-secondary)', lineHeight: 1.6 }}>
                    {rec.description}
                  </div>
                )}

                {/* Action steps */}
                {rec.actions && rec.actions.length > 0 && (
                  <div style={{ marginTop: 10, paddingTop: 10, borderTop: '1px solid var(--db-border-default)' }}>
                    <div style={{
                      fontSize: 11, fontWeight: 500, color: 'var(--db-text-tertiary)',
                      textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 6,
                    }}>Action steps</div>
                    {rec.actions.map((action, i) => (
                      <div key={i} style={{
                        display: 'flex', gap: 6, fontSize: 13, color: 'var(--db-text-secondary)',
                        lineHeight: 1.6, marginBottom: 4,
                      }}>
                        <span style={{ color: 'var(--db-text-tertiary)', fontWeight: 500, flexShrink: 0 }}>{i + 1}</span>
                        <span>{action}</span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
        <Pagination page={page} totalPages={totalPages} onChange={p => { setPage(p); window.scrollTo(0, 0); }} />
      </>) : (
        <EmptyState icon={<IconStack2 size={32} />} title="No recommendations found"
          description={search ? 'No recommendations match your search.' : 'Run a discovery to get recommendations.'} />
      )}
    </Shell>
  );
}

/* ========== Sort Dropdown ========== */

function SortDropdown({ value, onChange, options }: { value: string; onChange: (v: string) => void; options: string[] }) {
  const [open, { toggle, close }] = useDisclosure(false);
  return (
    <div style={{ position: 'relative' }}>
      <button onClick={toggle} style={{
        fontSize: 12, color: 'var(--db-text-tertiary)',
        background: 'none', border: 'none', cursor: 'pointer',
        fontFamily: 'inherit', display: 'flex', alignItems: 'center', gap: 4,
      }}>
        Sort: {value} <IconChevronDown size={12} />
      </button>
      {open && (
        <div style={{
          position: 'absolute', right: 0, top: '100%', marginTop: 4,
          background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius)', boxShadow: '0 4px 12px rgba(0,0,0,0.08)',
          zIndex: 10, minWidth: 160,
        }}>
          {options.map(opt => (
            <div key={opt} onClick={() => { onChange(opt); close(); }}
              style={{
                padding: '6px 12px', fontSize: 12, cursor: 'pointer',
                background: opt === value ? 'var(--db-bg-muted)' : 'transparent',
                fontWeight: opt === value ? 500 : 400,
                transition: 'background 120ms ease',
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
              onMouseLeave={e => { if (opt !== value) e.currentTarget.style.background = 'transparent'; }}
            >{opt}</div>
          ))}
        </div>
      )}
    </div>
  );
}
