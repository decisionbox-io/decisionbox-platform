'use client';

import { useEffect, useState, useCallback } from 'react';
import { useParams, useSearchParams } from 'next/navigation';
import { Loader, TextInput, Button, SegmentedControl, Select } from '@mantine/core';
import { IconSearch, IconBulb, IconStarFilled } from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { SeverityBadge, AreaBadge, EmptyState } from '@/components/common/UIComponents';
import { api, SearchResultItem, SearchHistoryEntry } from '@/lib/api';

export default function SearchPage() {
  const { id } = useParams<{ id: string }>();
  const searchParams = useSearchParams();
  const initialQuery = searchParams.get('q') || '';

  const [project, setProject] = useState<{ name: string } | null>(null);
  const [query, setQuery] = useState(initialQuery);
  const [results, setResults] = useState<SearchResultItem[]>([]);
  const [embeddingModel, setEmbeddingModel] = useState('');
  const [loading, setLoading] = useState(false);
  const [searched, setSearched] = useState(false);
  const [typeFilter, setTypeFilter] = useState('all');
  const [severityFilter, setSeverityFilter] = useState<string | null>(null);
  const [history, setHistory] = useState<SearchHistoryEntry[]>([]);

  useEffect(() => {
    api.getProject(id).then(p => setProject({ name: p.name })).catch(() => {});
    api.listSearchHistory(id, 10).then(h => setHistory(h || [])).catch(() => {});
  }, [id]);

  const runSearch = useCallback(async (q: string) => {
    if (!q.trim()) return;
    setLoading(true);
    setSearched(true);
    try {
      const resp = await api.searchInsights(id, {
        query: q.trim(),
        limit: 20,
        types: typeFilter === 'all' ? undefined : [typeFilter],
        filters: severityFilter ? { severity: severityFilter } : undefined,
      });
      setResults(resp.results);
      setEmbeddingModel(resp.embedding_model);
      // Refresh history
      api.listSearchHistory(id, 10).then(h => setHistory(h || [])).catch(() => {});
    } catch {
      setResults([]);
      setEmbeddingModel('');
    } finally {
      setLoading(false);
    }
  }, [id, typeFilter, severityFilter]);

  useEffect(() => {
    if (initialQuery) {
      setQuery(initialQuery);
      runSearch(initialQuery);
    }
  }, [initialQuery]); // eslint-disable-line react-hooks/exhaustive-deps

  // Re-run search when filters change (if already searched)
  useEffect(() => {
    if (searched && query.trim()) {
      runSearch(query);
    }
  }, [typeFilter, severityFilter]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    runSearch(query);
  };

  const insights = results.filter(r => r.type === 'insight');
  const recommendations = results.filter(r => r.type === 'recommendation');

  return (
    <Shell breadcrumb={project ? [{ label: project.name, href: `/projects/${id}` }, { label: 'Search' }] : undefined}>
      <div style={{ maxWidth: 'var(--db-content-max-width)', margin: '0 auto' }}>
        <h1 style={{ fontSize: 22, fontWeight: 600, color: 'var(--db-text-primary)', margin: '0 0 20px' }}>
          Search Insights
        </h1>

        {/* Search form */}
        <form onSubmit={handleSubmit} style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
          <TextInput
            placeholder="Search insights and recommendations..."
            value={query}
            onChange={e => setQuery(e.currentTarget.value)}
            leftSection={<IconSearch size={16} />}
            style={{ flex: 1 }}
            size="md"
          />
          <Button type="submit" loading={loading} size="md">Search</Button>
        </form>

        {/* Filters */}
        <div style={{ display: 'flex', gap: 12, marginBottom: 20, alignItems: 'center' }}>
          <SegmentedControl
            value={typeFilter}
            onChange={v => setTypeFilter(v)}
            data={[
              { label: 'All', value: 'all' },
              { label: 'Insights', value: 'insight' },
              { label: 'Recommendations', value: 'recommendation' },
            ]}
            size="xs"
          />
          <Select
            placeholder="Severity"
            value={severityFilter}
            onChange={v => setSeverityFilter(v)}
            data={[
              { label: 'All severities', value: '' },
              { label: 'Critical', value: 'critical' },
              { label: 'High', value: 'high' },
              { label: 'Medium', value: 'medium' },
              { label: 'Low', value: 'low' },
            ]}
            clearable
            size="xs"
            style={{ width: 140 }}
          />
          {embeddingModel && (
            <span style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginLeft: 'auto' }}>
              Model: {embeddingModel}
            </span>
          )}
        </div>

        {/* Recent searches (before first search) */}
        {!searched && history.length > 0 && (
          <div style={{ marginBottom: 24 }}>
            <h3 style={{ fontSize: 14, fontWeight: 600, color: 'var(--db-text-secondary)', marginBottom: 8 }}>
              Recent searches
            </h3>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {history.filter(h => h.type === 'search').slice(0, 8).map(h => (
                <button
                  key={h.id}
                  onClick={() => { setQuery(h.query); runSearch(h.query); }}
                  style={{
                    background: 'var(--db-bg-muted)', border: '1px solid var(--db-border-default)',
                    borderRadius: 16, padding: '4px 12px', fontSize: 13, cursor: 'pointer',
                    color: 'var(--db-text-secondary)',
                  }}
                >
                  {h.query}
                </button>
              ))}
            </div>
          </div>
        )}

        {/* Loading */}
        {loading && (
          <div style={{ display: 'flex', justifyContent: 'center', padding: 60 }}>
            <Loader size="sm" />
          </div>
        )}

        {/* Results */}
        {searched && !loading && results.length === 0 && (
          <EmptyState
            icon={<IconSearch size={32} />}
            title="No results found"
            description={`No insights or recommendations matched "${query}". Try different keywords.`}
          />
        )}

        {!loading && results.length > 0 && (
          <>
            <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', marginBottom: 16 }}>
              {results.length} result{results.length !== 1 ? 's' : ''} for &ldquo;{query}&rdquo;
            </p>

            {/* Insights section */}
            {insights.length > 0 && (
              <div style={{ marginBottom: 28 }}>
                <h3 style={{
                  fontSize: 14, fontWeight: 600, color: 'var(--db-text-secondary)',
                  marginBottom: 10, display: 'flex', alignItems: 'center', gap: 6,
                }}>
                  <IconBulb size={16} /> Insights ({insights.length})
                </h3>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                  {insights.map(r => (
                    <ResultCard key={r.id} item={r} projectId={id} />
                  ))}
                </div>
              </div>
            )}

            {/* Recommendations section */}
            {recommendations.length > 0 && (
              <div style={{ marginBottom: 28 }}>
                <h3 style={{
                  fontSize: 14, fontWeight: 600, color: 'var(--db-text-secondary)',
                  marginBottom: 10, display: 'flex', alignItems: 'center', gap: 6,
                }}>
                  <IconStarFilled size={16} /> Recommendations ({recommendations.length})
                </h3>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                  {recommendations.map(r => (
                    <ResultCard key={r.id} item={r} projectId={id} />
                  ))}
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </Shell>
  );
}

function ResultCard({ item, projectId }: { item: SearchResultItem; projectId: string }) {
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
        href={`/projects/${item.project_id || projectId}/discoveries/${item.discovery_id}`}
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
