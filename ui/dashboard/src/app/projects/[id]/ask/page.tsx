'use client';

import { useEffect, useState } from 'react';
import { useParams, useSearchParams } from 'next/navigation';
import { useTranslations } from 'next-intl';
import Shell from '@/components/layout/AppShell';
import ChatPanel from '@/components/ask/ChatPanel';
import { api } from '@/lib/api';

// The Ask page keeps its breadcrumb / header / full-bleed chrome and renders the
// shared <ChatPanel> for the conversation itself — the exact same component the
// global chat drawer uses, so page and drawer never diverge.
export default function AskPage() {
  const { id } = useParams<{ id: string }>();
  const t = useTranslations('nav');
  const searchParams = useSearchParams();
  const initialQuestion = searchParams.get('q') || '';
  const [project, setProject] = useState<{ name: string } | null>(null);

  useEffect(() => {
    api.getProject(id).then(p => setProject({ name: p.name })).catch(() => {});
  }, [id]);

  return (
    <Shell fullWidth breadcrumb={project ? [{ label: project.name, href: `/projects/${id}` }, { label: t('askInsights') }] : undefined}>
      <div style={{ display: 'flex', flexDirection: 'column', height: 'calc(100vh - var(--db-topbar-height))', margin: '-24px -24px -24px -24px', overflow: 'hidden' }}>
        <div style={{ padding: '24px 24px 0', flexShrink: 0 }}>
          <h1 style={{ fontSize: 22, fontWeight: 600, color: 'var(--db-text-primary)', margin: '0 0 4px' }}>
            Ask Your Insights
          </h1>
          <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: 0 }}>
            AI-synthesized answers with conversation context
          </p>
        </div>
        <div style={{ flex: 1, minHeight: 0 }}>
          <ChatPanel projectId={id} initialQuestion={initialQuestion} showHistory />
        </div>
      </div>
    </Shell>
  );
}
