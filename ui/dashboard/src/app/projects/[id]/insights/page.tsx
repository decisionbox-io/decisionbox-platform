'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import { Loader } from '@mantine/core';
import { IconBulb } from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import {
  SectionHeader, SeverityBadge, AreaBadge, ConfidenceBar, Th, EmptyState, SearchInput, Pagination,
} from '@/components/common/UIComponents';
import { api, Feedback, Insight, Project, SearchResultItem } from '@/lib/api';

const severityOrder: Record<string, number> = {
  critical: 0, high: 1, medium: 2, low: 3,
};

interface InsightWithContext extends Insight {
  discoveryId: string;
  discoveryDate: string;
}

export default function InsightsListPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [allInsights, setAllInsights] = useState<InsightWithContext[]>([]);
  const [feedbackMap, setFeedbackMap] = useState<Record<string, Feedback>>({});
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');
  const [severityFilter, setSeverityFilter] = useState('All');
  const [sortBy, setSortBy] = useState('Severity');
  const [page, setPage] = useState(1);
  const pageSize = 20;
  const [semanticResults, setSemanticResults] = useState<SearchResultItem[] | null>(null);
  const [searching, setSearching] = useState(false);
  const hasEmbedding = !!project?.embedding?.provider;

  useEffect(() => {
    Promise.all([
      api.getProject(id).then(p => setProject(p)),
      api.listDiscoveries(id).then(discoveries => {
        const insights: InsightWithContext[] = [];
        const seen = new Set<string>();

        for (const d of (discoveries || [])) {
          // Load feedback per discovery
          api.listFeedback(d.id).then(fb => {
            for (const f of (fb || [])) {
              setFeedbackMap(prev => ({ ...prev, [`${f.target_type}:${f.target_id}:${f.discovery_id}`]: f }));
            }
          }).catch(() => {});

          for (const insight of (d.insights || [])) {
            // Deduplicate by name (show latest version)
            const key = `${insight.analysis_area}:${insight.name}`;
            if (seen.has(key)) continue;
            seen.add(key);

            insights.push({
              ...insight,
              discoveryId: d.id,
              discoveryDate: d.discovery_date,
            });
          }
        }
        setAllInsights(insights);
      }),
    ])
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [id]);

  // Semantic search with debounce when embedding is configured
  useEffect(() => {
    const timer = setTimeout(() => {
      if (!hasEmbedding || !search.trim()) {
        setSemanticResults(null);
        return;
      }
      setSearching(true);
      api.searchInsights(id, {
        query: search.trim(),
        limit: 20,
        types: ['insight'],
        filters: severityFilter !== 'All' ? { severity: severityFilter.toLowerCase() } : undefined,
      })
        .then(resp => setSemanticResults(resp.results))
        .catch(() => setSemanticResults(null))
        .finally(() => setSearching(false));
    }, !hasEmbedding || !search.trim() ? 0 : 400);
    return () => clearTimeout(timer);
  }, [search, hasEmbedding, id, severityFilter]);

  if (loading) return <Shell><Loader /></Shell>;

  // Filter
  let filtered = allInsights;
  if (search) {
    const q = search.toLowerCase();
    filtered = filtered.filter(i =>
      i.name.toLowerCase().includes(q) ||
      i.description?.toLowerCase().includes(q) ||
      i.analysis_area.toLowerCase().includes(q)
    );
  }
  if (severityFilter !== 'All') {
    filtered = filtered.filter(i => i.severity.toLowerCase() === severityFilter.toLowerCase());
  }

  // Sort
  filtered = [...filtered].sort((a, b) => {
    switch (sortBy) {
      case 'Severity': return (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
      case 'Confidence': return (b.confidence || 0) - (a.confidence || 0);
      case 'Players affected': return (b.affected_count || 0) - (a.affected_count || 0);
      case 'Date': return new Date(b.discoveryDate).getTime() - new Date(a.discoveryDate).getTime();
      default: return (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
    }
  });

  // Reset page when filters change
  const totalPages = Math.ceil(filtered.length / pageSize);
  const paged = filtered.slice((page - 1) * pageSize, page * pageSize);

  const breadcrumb = [
    { label: 'Projects', href: '/' },
    { label: project?.name || '...', href: `/projects/${id}` },
    { label: 'Insights' },
  ];

  return (
    <Shell breadcrumb={breadcrumb}>
      <SectionHeader title="All Insights" count={semanticResults ? semanticResults.length : filtered.length} right={
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <SearchInput value={search} onChange={v => { setSearch(v); setPage(1); }}
            placeholder={hasEmbedding ? 'Semantic search insights...' : 'Filter insights...'} />
          {searching && <Loader size="xs" />}
        </div>
      } />

      {/* Filter bar */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap', alignItems: 'center' }}>
        {['All', 'Critical', 'High', 'Medium', 'Low'].map(sev => (
          <button key={sev} onClick={() => setSeverityFilter(sev)} style={{
            fontSize: 12, padding: '4px 12px',
            border: '0.5px solid var(--db-border-strong)',
            borderRadius: 'var(--db-radius)',
            background: severityFilter === sev ? 'var(--db-blue-bg)' : 'var(--db-bg-white)',
            color: severityFilter === sev ? 'var(--db-blue-text)' : 'var(--db-text-secondary)',
            cursor: 'pointer', fontFamily: 'inherit', transition: 'all 120ms ease',
          }}>{sev}</button>
        ))}
        <span style={{ flex: 1 }} />
        <SortDropdown value={sortBy} onChange={setSortBy}
          options={['Severity', 'Confidence', 'Players affected', 'Date']} />
      </div>

      {/* Semantic search results */}
      {semanticResults && semanticResults.length > 0 && (
        <div style={{
          background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius-lg)', overflow: 'hidden',
        }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
            <thead>
              <tr>
                <Th width="5%">Match</Th>
                <Th width="30%">Insight</Th>
                <Th>Severity</Th>
                <Th>Area</Th>
                <Th>Date</Th>
              </tr>
            </thead>
            <tbody>
              {semanticResults.map(r => (
                <tr key={r.id} style={{ borderBottom: '1px solid var(--db-border-default)' }}
                  onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
                >
                  <td style={{ padding: '10px 12px' }}>
                    <span style={{
                      fontSize: 11, fontWeight: 600, color: 'var(--db-blue-text)',
                      background: 'var(--db-blue-bg)', padding: '2px 6px', borderRadius: 8,
                    }}>
                      {Math.round(r.score * 100)}%
                    </span>
                  </td>
                  <td style={{ padding: '10px 12px' }}>
                    <Link href={`/projects/${id}/discoveries/${r.discovery_id}`}
                      style={{ fontSize: 13, fontWeight: 500, color: 'var(--db-text-link)', textDecoration: 'none' }}>
                      {r.name}
                    </Link>
                  </td>
                  <td style={{ padding: '10px 12px' }}>
                    {r.severity && <SeverityBadge severity={r.severity} type="severity" />}
                  </td>
                  <td style={{ padding: '10px 12px' }}>
                    {r.analysis_area && <AreaBadge area={r.analysis_area} />}
                  </td>
                  <td style={{ padding: '10px 12px', fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                    {r.discovered_at ? new Date(r.discovered_at).toLocaleDateString('en-US', { month: 'short', day: 'numeric' }) : ''}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {semanticResults && semanticResults.length === 0 && !searching && (
        <EmptyState icon={<IconBulb size={32} />} title="No results"
          description={`No insights matched "${search}". Try different keywords.`} />
      )}

      {/* Client-side filtered results (when no semantic search) */}
      {!semanticResults && filtered.length > 0 ? (<>
        <div style={{
          background: 'var(--db-bg-white)',
          border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius-lg)',
          overflow: 'hidden',
        }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
            <thead>
              <tr>
                <Th width="30%">Insight</Th>
                <Th>Severity</Th>
                <Th>Area</Th>
                <Th align="right">Affected</Th>
                <Th>Confidence</Th>
                <Th>Discovery</Th>
                <Th width="70px">Feedback</Th>
              </tr>
            </thead>
            <tbody>
              {paged.map((insight, idx) => (
                <tr key={idx} style={{ borderBottom: '1px solid var(--db-border-default)' }}
                  onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
                >
                  <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
                    <Link href={`/projects/${id}/discoveries/${insight.discoveryId}/insights/${insight.id || idx}`}
                      style={{
                        fontSize: 13, fontWeight: 500, color: 'var(--db-text-link)',
                        textDecoration: 'none', display: 'block', maxWidth: 300,
                      }}
                      onMouseEnter={e => { e.currentTarget.style.textDecoration = 'underline'; }}
                      onMouseLeave={e => { e.currentTarget.style.textDecoration = 'none'; }}
                    >
                      {insight.name}
                    </Link>
                  </td>
                  <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
                    <SeverityBadge severity={insight.severity} type="severity" />
                  </td>
                  <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
                    <AreaBadge area={insight.analysis_area} />
                  </td>
                  <td style={{ padding: '10px 12px', textAlign: 'right', verticalAlign: 'top', fontVariantNumeric: 'tabular-nums' }}>
                    {insight.affected_count > 0 ? insight.affected_count.toLocaleString() : '—'}
                  </td>
                  <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
                    <ConfidenceBar confidence={insight.confidence} />
                  </td>
                  <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
                    <Link href={`/projects/${id}/discoveries/${insight.discoveryId}`}
                      style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textDecoration: 'none' }}
                      onMouseEnter={e => { e.currentTarget.style.color = 'var(--db-text-link)'; }}
                      onMouseLeave={e => { e.currentTarget.style.color = 'var(--db-text-tertiary)'; }}
                    >
                      {new Date(insight.discoveryDate).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}
                    </Link>
                  </td>
                  <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
                    <FeedbackButtons projectId={id} discoveryId={insight.discoveryId}
                      targetType="insight" targetId={String(insight.id || idx)}
                      feedback={feedbackMap[`insight:${insight.id || idx}:${insight.discoveryId}`]} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Pagination page={page} totalPages={totalPages} onChange={p => { setPage(p); window.scrollTo(0, 0); }} />
      </>) : !semanticResults ? (
        <EmptyState icon={<IconBulb size={32} />} title="No insights found"
          description={search ? 'No insights match your search.' : 'Run a discovery to see insights.'} />
      ) : null}
    </Shell>
  );
}

/* ========== Sort Dropdown ========== */

import { useDisclosure } from '@mantine/hooks';
import { IconChevronDown } from '@tabler/icons-react';

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
