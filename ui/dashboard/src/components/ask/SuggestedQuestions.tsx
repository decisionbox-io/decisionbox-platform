'use client';

import { useEffect, useState } from 'react';
import { Loader } from '@mantine/core';
import { IconSparkles, IconMessageCircle } from '@tabler/icons-react';
import { useTranslations } from 'next-intl';
import { api, SeedContext } from '@/lib/api';
import { useChatDrawer } from '@/components/ask/ChatDrawerProvider';

// SuggestedQuestions shows up to three LLM-generated starter questions about one
// insight / recommendation, plus an "Ask about this" button. A thinking
// animation runs while they generate. Clicking a chip opens the seeded chat
// drawer and auto-asks that question; the button opens a generic seeded chat.
// It renders nothing when the feature is off / unavailable, on error, or when
// there are no suggestions — so it's safe to always place on the page.
export default function SuggestedQuestions({ projectId, seed }: { projectId: string; seed: SeedContext }) {
  const t = useTranslations('askUi');
  const ctx = useChatDrawer();
  const [questions, setQuestions] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    // loading initialises to true, so the first render already shows the
    // thinking state without a synchronous setState here (which the
    // react-hooks/set-state-in-effect rule forbids). SuggestedQuestions is
    // remounted per page, so a stale-across-seed flash is not a concern.
    let cancelled = false;
    api.getAskSuggestions(projectId, { type: seed.type, id: seed.id })
      .then(resp => { if (!cancelled) setQuestions(resp.questions || []); })
      .catch(() => { if (!cancelled) setQuestions([]); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [projectId, seed.type, seed.id]);

  // Nothing to show once we know there are no suggestions (feature off / empty).
  if (!ctx) return null;
  if (!loading && (!questions || questions.length === 0)) {
    // Still offer the plain "Ask about this" entry point even without chips.
    return (
      <div style={{ marginTop: 12 }}>
        <AskButton onClick={() => ctx.openWithSeed(projectId, seed)} kind={seed.type} />
      </div>
    );
  }

  return (
    <div style={{
      marginTop: 12, padding: 12, borderRadius: 'var(--db-radius-lg)',
      border: '1px solid var(--db-border-default)', background: 'var(--db-bg-white)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8 }}>
        <IconSparkles size={14} color="var(--db-purple-text)" />
        <span style={{ fontSize: 12, fontWeight: 600, color: 'var(--db-text-tertiary)' }}>{t('suggestedQuestions')}</span>
      </div>

      {loading ? (
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--db-text-secondary)', fontSize: 13, padding: '4px 0' }}>
          <Loader size="xs" />
          <span>{t('thinkingOfQuestions')}</span>
        </div>
      ) : (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          {questions!.map((q, i) => (
            <button
              key={i}
              type="button"
              onClick={() => ctx.openWithSeed(projectId, seed, q)}
              style={{
                display: 'inline-flex', alignItems: 'center', gap: 6,
                fontSize: 13, color: 'var(--db-text-primary)', background: 'var(--db-bg-muted)',
                border: '1px solid var(--db-border-default)', borderRadius: 999,
                padding: '6px 12px', cursor: 'pointer', fontFamily: 'inherit', textAlign: 'left',
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-blue-bg)'; }}
              onMouseLeave={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
            >
              <IconMessageCircle size={13} color="var(--db-text-link)" />
              {q}
            </button>
          ))}
        </div>
      )}

      {!loading && (
        <div style={{ marginTop: 10 }}>
          <AskButton onClick={() => ctx.openWithSeed(projectId, seed)} kind={seed.type} />
        </div>
      )}
    </div>
  );
}

function AskButton({ onClick, kind }: { onClick: () => void; kind: string }) {
  const t = useTranslations('askUi');
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        display: 'inline-flex', alignItems: 'center', gap: 6,
        fontSize: 13, fontWeight: 600, color: 'var(--db-text-link)',
        background: 'none', border: '1px solid var(--db-border-default)',
        borderRadius: 8, padding: '6px 12px', cursor: 'pointer', fontFamily: 'inherit',
      }}
    >
      <IconMessageCircle size={14} /> {t('askAboutThis', { type: kind })}
    </button>
  );
}
