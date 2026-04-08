'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useParams, useRouter } from 'next/navigation';
import {
  IconSearch, IconBulb, IconStarFilled, IconClock, IconSparkles,
  IconArrowRight, IconMessageCircle, IconTrendingUp,
} from '@tabler/icons-react';
import { SeverityBadge } from './UIComponents';
import { api, SearchResultItem, SearchHistoryEntry } from '@/lib/api';

const EXAMPLE_QUERIES = [
  { text: 'Why are users churning?', icon: IconTrendingUp },
  { text: 'Revenue optimization opportunities', icon: IconBulb },
  { text: 'What are the most critical issues?', icon: IconSparkles },
];

export default function SpotlightSearch() {
  const { id: projectId } = useParams<{ id?: string }>();
  const router = useRouter();
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SearchResultItem[]>([]);
  const [history, setHistory] = useState<SearchHistoryEntry[]>([]);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [selectedIdx, setSelectedIdx] = useState(-1);
  const [isMac, setIsMac] = useState(false);
  const [mode, setMode] = useState<'search' | 'ask'>('search');
  const inputRef = useRef<HTMLInputElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const timer = setTimeout(() => setIsMac(/Mac/.test(navigator.userAgent)), 0);
    return () => clearTimeout(timer);
  }, []);

  // Load recent history when dropdown opens
  useEffect(() => {
    if (!open || !projectId) return;
    api.listSearchHistory(projectId, 8).then(h => setHistory(h || [])).catch(() => {});
  }, [open, projectId]);

  // Debounced semantic search
  useEffect(() => {
    const timer = setTimeout(() => {
      if (!projectId || !query.trim()) {
        setResults([]);
        return;
      }
      setLoading(true);
      api.searchInsights(projectId, { query: query.trim(), limit: 6 })
        .then(resp => { setResults(resp?.results || []); setSelectedIdx(-1); })
        .catch(() => setResults([]))
        .finally(() => setLoading(false));
    }, !projectId || !query.trim() ? 0 : 300);
    return () => clearTimeout(timer);
  }, [query, projectId]);

  // Close on click outside
  useEffect(() => {
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false);
      }
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);

  // Keyboard shortcut: Cmd+K / Ctrl+K
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault();
        setOpen(true);
        inputRef.current?.focus();
      }
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('keydown', handler);
    return () => document.removeEventListener('keydown', handler);
  }, []);

  const navigate = useCallback((item: SearchResultItem) => {
    setOpen(false);
    setQuery('');
    router.push(`/projects/${item.project_id || projectId}/discoveries/${item.discovery_id}`);
  }, [router, projectId]);

  const goToSearch = useCallback((q: string) => {
    setOpen(false);
    setQuery('');
    router.push(`/projects/${projectId}/search?q=${encodeURIComponent(q)}`);
  }, [router, projectId]);

  const goToAsk = useCallback((q: string) => {
    setOpen(false);
    setQuery('');
    router.push(`/projects/${projectId}/ask?q=${encodeURIComponent(q)}`);
  }, [router, projectId]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    const allItems = getAllItems();
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSelectedIdx(i => Math.min(i + 1, allItems.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSelectedIdx(i => Math.max(i - 1, -1));
    } else if (e.key === 'Enter') {
      if (selectedIdx >= 0 && allItems[selectedIdx]) {
        allItems[selectedIdx].action();
      } else if (query.trim() && projectId) {
        if (mode === 'ask') {
          setOpen(false);
          router.push(`/projects/${projectId}/ask?q=${encodeURIComponent(query.trim())}`);
          setQuery('');
        } else {
          goToSearch(query.trim());
        }
      }
    } else if (e.key === 'Tab') {
      e.preventDefault();
      setMode(m => m === 'search' ? 'ask' : 'search');
    }
  };

  // Build a unified selectable list for keyboard nav
  const getAllItems = (): { id: string; action: () => void }[] => {
    const items: { id: string; action: () => void }[] = [];
    if (results.length > 0) {
      results.forEach(r => items.push({ id: r.id, action: () => navigate(r) }));
      items.push({ id: 'view-all', action: () => goToSearch(query.trim()) });
    }
    if (!query.trim()) {
      history.forEach(h => items.push({ id: h.id, action: () => setQuery(h.query) }));
    }
    return items;
  };

  const recentSearches = history.filter(h => h.type === 'search');
  const recentAsks = history.filter(h => h.type === 'ask');
  const showDropdown = open && projectId;

  return (
    <div ref={containerRef} style={{ position: 'relative', flex: '0 1 420px', maxWidth: 420 }}>
      {/* Input */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8,
        background: open ? 'var(--db-bg-white)' : 'var(--db-bg-muted)',
        borderRadius: 10,
        padding: '0 12px', height: 34,
        border: open ? '1px solid var(--db-blue-text)' : '1px solid transparent',
        boxShadow: open ? '0 0 0 3px rgba(24,95,165,0.08)' : 'none',
        transition: 'all 150ms ease',
      }}>
        {mode === 'search'
          ? <IconSearch size={15} color={open ? 'var(--db-blue-text)' : 'var(--db-text-tertiary)'} style={{ flexShrink: 0 }} />
          : <IconMessageCircle size={15} color={open ? 'var(--db-purple-text)' : 'var(--db-text-tertiary)'} style={{ flexShrink: 0 }} />
        }
        <input
          ref={inputRef}
          value={query}
          onChange={e => { setQuery(e.target.value); setOpen(true); }}
          onFocus={() => setOpen(true)}
          onKeyDown={handleKeyDown}
          placeholder={projectId
            ? (mode === 'search' ? 'Search insights...' : 'Ask a question...')
            : 'Select a project first'
          }
          disabled={!projectId}
          role="combobox"
          aria-expanded={!!showDropdown}
          aria-controls="spotlight-results"
          aria-haspopup="listbox"
          aria-autocomplete="list"
          style={{
            flex: 1, border: 'none', background: 'transparent', outline: 'none',
            fontSize: 13, color: 'var(--db-text-primary)', fontFamily: 'inherit',
          }}
        />
        {loading && (
          <div style={{
            width: 14, height: 14, border: '2px solid var(--db-border-default)',
            borderTopColor: 'var(--db-blue-text)', borderRadius: '50%',
            animation: 'spin 600ms linear infinite', flexShrink: 0,
          }} />
        )}
        {/* Mode toggle */}
        <button
          onClick={() => setMode(m => m === 'search' ? 'ask' : 'search')}
          title={`Switch to ${mode === 'search' ? 'Ask' : 'Search'} mode (Tab)`}
          style={{
            display: 'flex', alignItems: 'center', gap: 3,
            fontSize: 10, fontWeight: 600, flexShrink: 0,
            background: mode === 'ask' ? 'var(--db-purple-bg)' : 'var(--db-blue-bg)',
            color: mode === 'ask' ? 'var(--db-purple-text)' : 'var(--db-blue-text)',
            border: 'none', borderRadius: 4, padding: '2px 6px',
            cursor: 'pointer', fontFamily: 'inherit', lineHeight: '16px',
            transition: 'all 120ms ease',
          }}
        >
          {mode === 'search' ? 'Search' : 'Ask'}
        </button>
        <kbd style={{
          fontSize: 10, color: 'var(--db-text-tertiary)',
          background: 'var(--db-bg-muted)', border: '1px solid var(--db-border-default)',
          borderRadius: 4, padding: '1px 5px', lineHeight: '16px', flexShrink: 0,
        }}>
          {isMac ? '⌘K' : 'Ctrl+K'}
        </kbd>
      </div>

      {/* Dropdown */}
      {showDropdown && (
        <div id="spotlight-results" role="listbox" style={{
          position: 'absolute', top: '100%', left: '50%', transform: 'translateX(-50%)',
          width: 460, marginTop: 6,
          background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
          borderRadius: 12, boxShadow: '0 12px 32px rgba(0,0,0,0.12), 0 2px 8px rgba(0,0,0,0.06)',
          zIndex: 100, maxHeight: 440, overflowY: 'auto',
        }}>
          {/* Search results */}
          {results.length > 0 && (
            <div style={{ padding: '8px 0 4px' }}>
              <SectionLabel>Results</SectionLabel>
              {results.map((r, i) => (
                <DropdownRow
                  key={r.id}
                  selected={i === selectedIdx}
                  onClick={() => navigate(r)}
                  onHover={() => setSelectedIdx(i)}
                >
                  {r.type === 'insight'
                    ? <IconBulb size={15} color="var(--db-amber-text)" style={{ flexShrink: 0, marginTop: 1 }} />
                    : <IconStarFilled size={15} color="var(--db-purple-text)" style={{ flexShrink: 0, marginTop: 1 }} />
                  }
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{
                      fontSize: 13, fontWeight: 500, color: 'var(--db-text-primary)',
                      overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                    }}>
                      {r.name || r.title}
                    </div>
                    {r.description && (
                      <div style={{
                        fontSize: 11, color: 'var(--db-text-tertiary)',
                        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                        marginTop: 1,
                      }}>
                        {r.description.slice(0, 80)}
                      </div>
                    )}
                  </div>
                  <div style={{ display: 'flex', gap: 4, alignItems: 'center', flexShrink: 0 }}>
                    {r.severity && <SeverityBadge severity={r.severity} type="severity" />}
                    <span style={{
                      fontSize: 10, color: 'var(--db-blue-text)', background: 'var(--db-blue-bg)',
                      padding: '1px 6px', borderRadius: 8, fontWeight: 600,
                    }}>
                      {Math.round(r.score * 100)}%
                    </span>
                  </div>
                </DropdownRow>
              ))}
              {/* View all */}
              <div
                onClick={() => goToSearch(query.trim())}
                style={{
                  display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6,
                  padding: '10px 14px', fontSize: 12, fontWeight: 500, color: 'var(--db-text-link)',
                  cursor: 'pointer', borderTop: '1px solid var(--db-border-default)',
                  margin: '4px 0 0',
                }}
                onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
              >
                View all results <IconArrowRight size={13} />
              </div>
            </div>
          )}

          {/* No results for query */}
          {query.trim() && !loading && results.length === 0 && (
            <div style={{ padding: '20px 14px', textAlign: 'center' }}>
              <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: '0 0 10px' }}>
                No results for &ldquo;{query}&rdquo;
              </p>
              <div
                onClick={() => goToAsk(query.trim())}
                style={{
                  display: 'inline-flex', alignItems: 'center', gap: 6,
                  fontSize: 12, color: 'var(--db-text-link)', cursor: 'pointer',
                  fontWeight: 500,
                }}
              >
                <IconMessageCircle size={14} /> Ask AI about this instead →
              </div>
            </div>
          )}

          {/* Empty state: recent + suggestions */}
          {!query.trim() && (
            <div style={{ padding: '8px 0' }}>
              {/* Recent searches */}
              {recentSearches.length > 0 && (
                <>
                  <SectionLabel>Recent Searches</SectionLabel>
                  {recentSearches.slice(0, 4).map(h => (
                    <DropdownRow key={h.id} onClick={() => setQuery(h.query)}>
                      <IconClock size={14} color="var(--db-text-tertiary)" style={{ flexShrink: 0 }} />
                      <span style={{
                        flex: 1, fontSize: 13, color: 'var(--db-text-secondary)',
                        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }}>
                        {h.query}
                      </span>
                      <span style={{ fontSize: 10, color: 'var(--db-text-tertiary)', flexShrink: 0 }}>
                        {h.results_count} result{h.results_count !== 1 ? 's' : ''}
                      </span>
                    </DropdownRow>
                  ))}
                </>
              )}

              {/* Recent asks */}
              {recentAsks.length > 0 && (
                <>
                  <SectionLabel>Recent Questions</SectionLabel>
                  {recentAsks.slice(0, 3).map(h => (
                    <DropdownRow key={h.id} onClick={() => {
                      setOpen(false);
                      router.push(`/projects/${projectId}/ask`);
                    }}>
                      <IconMessageCircle size={14} color="var(--db-purple-text)" style={{ flexShrink: 0 }} />
                      <span style={{
                        flex: 1, fontSize: 13, color: 'var(--db-text-secondary)',
                        overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                      }}>
                        {h.query}
                      </span>
                    </DropdownRow>
                  ))}
                </>
              )}

              {/* Try searching */}
              <SectionLabel>Try Searching</SectionLabel>
              {EXAMPLE_QUERIES.map(ex => (
                <DropdownRow key={ex.text} onClick={() => setQuery(ex.text)}>
                  <ex.icon size={14} color="var(--db-blue-text)" style={{ flexShrink: 0 }} />
                  <span style={{ fontSize: 13, color: 'var(--db-text-secondary)' }}>{ex.text}</span>
                </DropdownRow>
              ))}

              {/* Quick links */}
              <div style={{
                display: 'flex', gap: 0, borderTop: '1px solid var(--db-border-default)',
                marginTop: 4,
              }}>
                <div
                  onClick={() => { setOpen(false); router.push(`/projects/${projectId}/search`); }}
                  style={{
                    flex: 1, padding: '10px 14px', fontSize: 12, fontWeight: 500,
                    color: 'var(--db-text-link)', cursor: 'pointer', textAlign: 'center',
                    borderRight: '1px solid var(--db-border-default)',
                  }}
                  onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
                >
                  <IconSearch size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
                  Advanced Search
                </div>
                <div
                  onClick={() => { setOpen(false); router.push(`/projects/${projectId}/ask`); }}
                  style={{
                    flex: 1, padding: '10px 14px', fontSize: 12, fontWeight: 500,
                    color: 'var(--db-purple-text)', cursor: 'pointer', textAlign: 'center',
                  }}
                  onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                  onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
                >
                  <IconMessageCircle size={12} style={{ marginRight: 4, verticalAlign: -1 }} />
                  Ask Insights
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div style={{
      fontSize: 10, fontWeight: 600, textTransform: 'uppercase',
      letterSpacing: '0.6px', color: 'var(--db-text-tertiary)',
      padding: '8px 16px 4px',
    }}>
      {children}
    </div>
  );
}

function DropdownRow({ children, selected, onClick, onHover }: {
  children: React.ReactNode;
  selected?: boolean;
  onClick: () => void;
  onHover?: () => void;
}) {
  return (
    <div
      role="option"
      aria-selected={selected || false}
      onClick={onClick}
      onMouseEnter={e => {
        e.currentTarget.style.background = 'var(--db-bg-muted)';
        onHover?.();
      }}
      onMouseLeave={e => {
        if (!selected) e.currentTarget.style.background = 'transparent';
      }}
      style={{
        display: 'flex', alignItems: 'center', gap: 10,
        padding: '7px 16px', cursor: 'pointer',
        background: selected ? 'var(--db-bg-muted)' : 'transparent',
        transition: 'background 80ms ease',
      }}
    >
      {children}
    </div>
  );
}
