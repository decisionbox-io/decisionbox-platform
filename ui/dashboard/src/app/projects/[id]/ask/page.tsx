'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import { Loader, TextInput, Button, ActionIcon } from '@mantine/core';
import { IconMessageCircle, IconSend, IconBulb, IconStarFilled } from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { SeverityBadge, AreaBadge, EmptyState } from '@/components/common/UIComponents';
import { api, AskResponse, SearchHistoryEntry } from '@/lib/api';

interface ConversationEntry {
  question: string;
  response: AskResponse;
}

export default function AskPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<{ name: string } | null>(null);
  const [question, setQuestion] = useState('');
  const [loading, setLoading] = useState(false);
  const [conversation, setConversation] = useState<ConversationEntry[]>([]);
  const [history, setHistory] = useState<SearchHistoryEntry[]>([]);

  useEffect(() => {
    api.getProject(id).then(p => setProject({ name: p.name })).catch(() => {});
    api.listSearchHistory(id, 10).then(setHistory).catch(() => {});
  }, [id]);

  const handleAsk = async (q?: string) => {
    const text = (q || question).trim();
    if (!text || loading) return;
    setLoading(true);
    setQuestion('');
    try {
      const resp = await api.askInsights(id, { question: text, limit: 5 });
      setConversation(prev => [...prev, { question: text, response: resp }]);
      api.listSearchHistory(id, 10).then(setHistory).catch(() => {});
    } catch {
      setConversation(prev => [...prev, {
        question: text,
        response: { answer: 'Sorry, I could not answer this question. Make sure embedding is configured for this project.', sources: [], model: '' },
      }]);
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk();
  };

  const pastAsks = history.filter(h => h.type === 'ask');

  return (
    <Shell breadcrumb={project ? [{ label: project.name, href: `/projects/${id}` }, { label: 'Ask Insights' }] : undefined}>
      <div style={{ maxWidth: 'var(--db-content-max-width)', margin: '0 auto', display: 'flex', flexDirection: 'column', minHeight: 'calc(100vh - 120px)' }}>
        <h1 style={{ fontSize: 22, fontWeight: 600, color: 'var(--db-text-primary)', margin: '0 0 4px' }}>
          Ask Your Insights
        </h1>
        <p style={{ fontSize: 14, color: 'var(--db-text-secondary)', margin: '0 0 20px' }}>
          Ask questions about your data and get answers synthesized from discovery insights.
        </p>

        {/* Conversation area */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 20, marginBottom: 20 }}>
          {conversation.length === 0 && !loading && (
            <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 16, padding: 40 }}>
              <IconMessageCircle size={40} color="var(--db-text-tertiary)" strokeWidth={1.2} />
              <p style={{ fontSize: 15, color: 'var(--db-text-tertiary)', textAlign: 'center', maxWidth: 400 }}>
                Ask a question about your discovery insights. For example: &ldquo;What are the main causes of churn?&rdquo;
              </p>
              {pastAsks.length > 0 && (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, justifyContent: 'center', marginTop: 8 }}>
                  {pastAsks.slice(0, 4).map(h => (
                    <button
                      key={h.id}
                      onClick={() => { setQuestion(h.query); handleAsk(h.query); }}
                      style={{
                        background: 'var(--db-bg-muted)', border: '1px solid var(--db-border-default)',
                        borderRadius: 16, padding: '4px 12px', fontSize: 13, cursor: 'pointer',
                        color: 'var(--db-text-secondary)',
                      }}
                    >
                      {h.query.length > 50 ? h.query.slice(0, 50) + '...' : h.query}
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}

          {conversation.map((entry, i) => (
            <div key={i} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {/* User question */}
              <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                <div style={{
                  background: 'var(--db-blue-bg)', color: 'var(--db-blue-text)',
                  padding: '10px 14px', borderRadius: '12px 12px 2px 12px',
                  fontSize: 14, maxWidth: '70%',
                }}>
                  {entry.question}
                </div>
              </div>

              {/* AI answer */}
              <div style={{
                background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
                borderRadius: 'var(--db-radius-lg)', padding: 16,
              }}>
                <div style={{
                  fontSize: 14, color: 'var(--db-text-primary)', lineHeight: 1.7,
                  whiteSpace: 'pre-wrap',
                }}>
                  {entry.response.answer}
                </div>

                {entry.response.sources.length > 0 && (
                  <div style={{ marginTop: 14, paddingTop: 14, borderTop: '1px solid var(--db-border-default)' }}>
                    <h4 style={{ fontSize: 12, fontWeight: 600, color: 'var(--db-text-tertiary)', marginBottom: 8 }}>
                      Sources ({entry.response.sources.length})
                    </h4>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                      {entry.response.sources.map((src, j) => (
                        <Link
                          key={src.id}
                          href={`/projects/${id}/discoveries/${src.discovery_id}`}
                          style={{
                            display: 'flex', alignItems: 'center', gap: 8,
                            fontSize: 13, color: 'var(--db-text-link)', textDecoration: 'none',
                            padding: '4px 8px', borderRadius: 6,
                            background: j % 2 === 0 ? 'var(--db-bg-muted)' : 'transparent',
                          }}
                        >
                          {src.type === 'insight'
                            ? <IconBulb size={14} color="var(--db-amber-text)" />
                            : <IconStarFilled size={14} color="var(--db-purple-text)" />
                          }
                          <span style={{ flex: 1 }}>[{j + 1}] {src.name || src.title}</span>
                          {src.severity && <SeverityBadge severity={src.severity} type="severity" />}
                          {src.analysis_area && <AreaBadge area={src.analysis_area} />}
                          <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                            {Math.round(src.score * 100)}%
                          </span>
                        </Link>
                      ))}
                    </div>
                  </div>
                )}

                {entry.response.model && (
                  <p style={{ fontSize: 11, color: 'var(--db-text-tertiary)', marginTop: 8, marginBottom: 0 }}>
                    Answered by {entry.response.model}
                  </p>
                )}
              </div>
            </div>
          ))}

          {loading && (
            <div style={{
              background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
              borderRadius: 'var(--db-radius-lg)', padding: 24,
              display: 'flex', alignItems: 'center', gap: 10,
            }}>
              <Loader size="xs" />
              <span style={{ fontSize: 14, color: 'var(--db-text-secondary)' }}>Thinking...</span>
            </div>
          )}
        </div>

        {/* Input */}
        <form onSubmit={handleSubmit} style={{
          position: 'sticky', bottom: 0, background: 'var(--db-bg-page)',
          paddingTop: 12, paddingBottom: 12,
          display: 'flex', gap: 8,
        }}>
          <TextInput
            placeholder="Ask a question about your insights..."
            value={question}
            onChange={e => setQuestion(e.currentTarget.value)}
            style={{ flex: 1 }}
            size="md"
            disabled={loading}
          />
          <ActionIcon type="submit" size="lg" variant="filled" loading={loading} style={{ height: 42, width: 42 }}>
            <IconSend size={18} />
          </ActionIcon>
        </form>
      </div>
    </Shell>
  );
}
