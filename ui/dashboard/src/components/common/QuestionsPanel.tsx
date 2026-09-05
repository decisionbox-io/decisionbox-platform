'use client';

import { ReactNode } from 'react';
import { useTranslations } from 'next-intl';
import { SectionHeader } from '@/components/common/UIComponents';
import { DiscoveryQuestion } from '@/lib/api';
import QuestionCard from '@/components/common/QuestionCard';

interface QuestionsPanelProps {
  projectId: string;
  questions: DiscoveryQuestion[];
  onResolved: (id: string) => void;
  // hideHeader suppresses the built-in SectionHeader — used inside QuestionsDrawer,
  // which renders its own header + collapse control.
  hideHeader?: boolean;
  // intro overrides the default helper line; pass false to hide it entirely.
  intro?: ReactNode;
}

// QuestionsPanel renders the "Questions to answer" list. It renders nothing when
// there are no questions, so it's safe to always mount (community builds, or a
// clean run, simply show nothing).
export default function QuestionsPanel({
  projectId, questions, onResolved, hideHeader, intro,
}: QuestionsPanelProps) {
  const t = useTranslations('commonUi');
  if (!questions || questions.length === 0) return null;
  const introContent = intro === undefined ? t('questionsIntro') : intro;
  return (
    <div style={{ marginBottom: 20 }}>
      {!hideHeader && <SectionHeader title={t('questionsToAnswer')} count={questions.length} />}
      {introContent && (
        <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginBottom: 10 }}>
          {introContent}
        </div>
      )}
      {questions.map((qn) => (
        <QuestionCard key={qn.id} projectId={projectId} question={qn} onResolved={onResolved} />
      ))}
    </div>
  );
}
