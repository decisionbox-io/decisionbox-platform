'use client';

import { SectionHeader } from '@/components/common/UIComponents';
import { DiscoveryQuestion } from '@/lib/api';
import QuestionCard from '@/components/common/QuestionCard';

interface QuestionsPanelProps {
  projectId: string;
  questions: DiscoveryQuestion[];
  onResolved: (id: string) => void;
  onLinkClick?: (target: DiscoveryQuestion['linked_target']) => void;
}

// QuestionsPanel renders the run-detail "Questions to answer" block. It renders
// nothing when there are no pending questions, so it's safe to always mount
// (community builds, or a clean run, simply show nothing).
export default function QuestionsPanel({ projectId, questions, onResolved, onLinkClick }: QuestionsPanelProps) {
  if (!questions || questions.length === 0) return null;
  return (
    <div style={{ marginBottom: 20 }}>
      <SectionHeader title="Questions to answer" count={questions.length} />
      <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginBottom: 10 }}>
        The analysis was unsure about a few things. Your answers are saved as notes and sharpen the next run.
      </div>
      {questions.map((q) => (
        <QuestionCard key={q.id} projectId={projectId} question={q} onResolved={onResolved} onLinkClick={onLinkClick} />
      ))}
    </div>
  );
}
