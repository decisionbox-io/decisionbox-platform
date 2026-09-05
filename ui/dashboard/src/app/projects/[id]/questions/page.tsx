'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import { useTranslations } from 'next-intl';
import { Loader } from '@mantine/core';
import { IconHelpCircle } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import QuestionCard from '@/components/common/QuestionCard';
import { SectionHeader, EmptyState } from '@/components/common/UIComponents';
import { api, ApiError, DiscoveryQuestion, Project } from '@/lib/api';

type StatusFilter = 'all' | 'pending' | 'answered' | 'dismissed';

const FILTER_KEYS: StatusFilter[] = ['all', 'pending', 'answered', 'dismissed'];

// QuestionsReviewPage is the standing home for every clarifying question the
// agent has raised — pending ones the analyst can answer, plus the answered /
// dismissed history. It complements the run-detail drawer, which shows only the
// still-open questions for a given run.
export default function QuestionsReviewPage() {
  const t = useTranslations('systemSearch');
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [questions, setQuestions] = useState<DiscoveryQuestion[]>([]);
  const [loading, setLoading] = useState(true);
  // unsupported = the questions endpoint 404s (community build without the
  // enterprise plugin). Distinguished from "supported but empty" so the copy is
  // honest rather than implying the analyst simply has no questions.
  const [unsupported, setUnsupported] = useState(false);
  const [filter, setFilter] = useState<StatusFilter>('all');

  const load = () => {
    api.listProjectQuestions(id)
      .then((qs) => { setQuestions(qs || []); setUnsupported(false); })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setUnsupported(true);
        setQuestions([]);
      })
      .finally(() => setLoading(false));
  };

  useEffect(() => {
    api.getProject(id).then(setProject).catch(() => {});
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const counts = {
    all: questions.length,
    pending: questions.filter((q) => q.status === 'pending').length,
    answered: questions.filter((q) => q.status === 'answered').length,
    dismissed: questions.filter((q) => q.status === 'dismissed').length,
  };

  // Pending first, then most-recently-updated — so open work is at the top and
  // the resolved history reads newest-first below it.
  const visible = questions
    .filter((q) => filter === 'all' || q.status === filter)
    .sort((a, b) => {
      if (a.status === 'pending' && b.status !== 'pending') return -1;
      if (b.status === 'pending' && a.status !== 'pending') return 1;
      return (b.updated_at || b.created_at || '').localeCompare(a.updated_at || a.created_at || '');
    });

  const breadcrumb = [
    { label: t('projects'), href: '/' },
    { label: project?.name || t('project'), href: `/projects/${id}` },
    { label: t('questions') },
  ];

  return (
    <Shell breadcrumb={breadcrumb}>
      <SectionHeader title={t('clarifyingQuestions')} count={loading ? undefined : counts.all} />
      <div style={{ fontSize: 13, color: 'var(--db-text-tertiary)', marginBottom: 16, maxWidth: 640 }}>
        {t('questionsHelp')}
      </div>

      {/* Status filter tabs */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        {FILTER_KEYS.map((key) => {
          const active = filter === key;
          return (
            <button
              key={key}
              type="button"
              onClick={() => setFilter(key)}
              style={{
                fontSize: 13, padding: '5px 12px', borderRadius: 'var(--db-radius)', cursor: 'pointer',
                border: '1px solid var(--db-border-default)',
                background: active ? 'var(--db-blue-bg, #eaf1ff)' : 'transparent',
                color: active ? 'var(--db-blue-text, #2563eb)' : 'var(--db-text-secondary)',
                fontWeight: active ? 500 : 400,
              }}
            >
              {t(`filterStatus_${key}`)}
              <span style={{ marginLeft: 6, opacity: 0.7 }}>{counts[key]}</span>
            </button>
          );
        })}
      </div>

      {loading ? (
        <Loader />
      ) : visible.length === 0 ? (
        <EmptyState
          icon={<IconHelpCircle size={40} />}
          title={unsupported ? t('emptyUnsupportedTitle')
            : filter === 'all' ? t('emptyAllTitle')
              : t('emptyFilteredTitle', { status: t(`filterStatus_${filter}`) })}
          description={unsupported
            ? t('emptyUnsupportedBody')
            : filter === 'all'
              ? t('emptyAllBody')
              : t('emptyFilteredBody')}
        />
      ) : (
        <div style={{ maxWidth: 720 }}>
          {visible.map((q) => (
            <QuestionCard
              key={q.id}
              projectId={id}
              question={q}
              // A resolved card refetches so its status/answer reflect the server.
              onResolved={() => load()}
            />
          ))}
        </div>
      )}
    </Shell>
  );
}
