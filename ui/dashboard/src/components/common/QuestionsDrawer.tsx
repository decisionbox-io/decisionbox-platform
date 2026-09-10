'use client';

import { useState } from 'react';
import Link from 'next/link';
import { IconChevronRight, IconHelpCircle } from '@tabler/icons-react';
import { DiscoveryQuestion } from '@/lib/api';
import QuestionsPanel from '@/components/common/QuestionsPanel';

interface QuestionsDrawerProps {
  projectId: string;
  questions: DiscoveryQuestion[];
  onResolved: (id: string) => void;
  title?: string;
  // storageKey persists the collapsed choice across navigations so the analyst
  // isn't forced to re-collapse it on every page. Distinct keys let the run and
  // insight surfaces remember independently.
  storageKey?: string;
  // viewAllHref, when set, adds a "View all" link in the header to the full
  // questions review page.
  viewAllHref?: string;
}

const DRAWER_WIDTH = 380;

// QuestionsDrawer floats the pending clarifying questions in a collapsible panel
// pinned to the right edge, so they're prominent without consuming the main
// column's width. Default open; collapses to a slim tab (with a count badge)
// that reopens on click. Renders nothing when there are no questions, so it's
// safe to always mount.
export default function QuestionsDrawer({
  projectId, questions, onResolved, title = 'Questions to answer',
  storageKey = 'dbx-questions-drawer', viewAllHref,
}: QuestionsDrawerProps) {
  // Default open; a lazy initializer restores the last collapse choice so the
  // analyst isn't forced to re-collapse it on every navigation. Guarded for SSR
  // (window undefined → open), mirroring the repo's other localStorage-backed
  // UI toggles (SchemaIndexPanel, the debug-logs panel).
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false;
    try { return window.localStorage.getItem(storageKey) === '1'; } catch { return false; }
  });

  const setCollapsedPersistent = (next: boolean) => {
    setCollapsed(next);
    try { localStorage.setItem(storageKey, next ? '1' : '0'); } catch { /* ignore */ }
  };

  if (!questions || questions.length === 0) return null;

  // Collapsed → a slim vertical tab pinned to the right edge.
  if (collapsed) {
    return (
      <button
        type="button"
        onClick={() => setCollapsedPersistent(false)}
        title={`${questions.length} question${questions.length > 1 ? 's' : ''} to answer`}
        style={{
          position: 'fixed', top: 'calc(var(--db-topbar-height) + 16px)', right: 0, zIndex: 30,
          display: 'flex', alignItems: 'center', gap: 6,
          background: 'var(--db-blue-bg, #eaf1ff)', color: 'var(--db-blue-text, #2563eb)',
          border: '1px solid var(--db-border-default)', borderRight: 'none',
          borderRadius: '8px 0 0 8px', padding: '8px 10px', cursor: 'pointer',
          boxShadow: '0 2px 8px rgba(0,0,0,0.08)', fontSize: 13, fontWeight: 500,
        }}
      >
        <IconHelpCircle size={16} />
        <span>{questions.length}</span>
      </button>
    );
  }

  return (
    <aside
      aria-label="Clarifying questions"
      style={{
        position: 'fixed', top: 'var(--db-topbar-height)', right: 0, bottom: 0, zIndex: 30,
        width: DRAWER_WIDTH, maxWidth: '92vw',
        background: 'var(--db-bg-white)', borderLeft: '1px solid var(--db-border-default)',
        boxShadow: '-4px 0 16px rgba(0,0,0,0.06)',
        display: 'flex', flexDirection: 'column',
      }}
    >
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8, padding: '12px 14px',
        borderBottom: '1px solid var(--db-border-default)',
      }}>
        <IconHelpCircle size={18} style={{ color: 'var(--db-blue-text, #2563eb)', flexShrink: 0 }} />
        <span style={{ fontSize: 14, fontWeight: 600, flex: 1 }}>
          {title}
          <span style={{
            marginLeft: 8, fontSize: 12, fontWeight: 500, background: 'var(--db-blue-bg, #eaf1ff)',
            color: 'var(--db-blue-text, #2563eb)', padding: '0 7px', borderRadius: 10, lineHeight: '18px',
          }}>{questions.length}</span>
        </span>
        {viewAllHref && (
          <Link href={viewAllHref} style={{ fontSize: 12, color: 'var(--db-blue-text, #2563eb)', textDecoration: 'none' }}>
            View all
          </Link>
        )}
        <button
          type="button"
          onClick={() => setCollapsedPersistent(true)}
          title="Collapse"
          aria-label="Collapse questions"
          style={{
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            background: 'transparent', border: 'none', cursor: 'pointer',
            color: 'var(--db-text-tertiary)', padding: 2, borderRadius: 4,
          }}
        >
          <IconChevronRight size={18} />
        </button>
      </div>

      <div style={{ overflowY: 'auto', padding: '12px 14px', flex: 1 }}>
        <QuestionsPanel
          projectId={projectId}
          questions={questions}
          onResolved={onResolved}
          hideHeader
        />
      </div>
    </aside>
  );
}
