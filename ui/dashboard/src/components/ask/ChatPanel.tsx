'use client';

import { useEffect, useState, useRef } from 'react';
import { Loader, TextInput, ActionIcon } from '@mantine/core';
import { IconMessageCircle, IconSend, IconHistory, IconClock, IconPlus, IconTrash, IconBulb, IconStarFilled } from '@tabler/icons-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { useTranslations } from 'next-intl';
import CitationsFooter, { sourceHref } from '@/components/citations/CitationsFooter';
import { CitationLink } from '@/components/citations/CitationLink';
import { api, AskSession, SearchResultItem, SeedContext, askErrorMessage } from '@/lib/api';
import { useFormat } from '@/lib/format';

// ChatPanel is the single source of truth for the Ask conversation UI. It is
// rendered full-bleed by the Ask page (showHistory) and inside the global chat
// drawer (showHistory={false}); both share this exact component so behaviour and
// styling never diverge. seedContext, when set, anchors the conversation to one
// insight / recommendation and is sent on the first turn (then persisted on the
// session server-side). initialQuestion auto-asks on mount (the /ask ?q= hook
// and clicked suggested questions both use it).
export interface ChatPanelProps {
  projectId: string;
  seedContext?: SeedContext;
  initialQuestion?: string;
  showHistory?: boolean;
}

interface DisplayMessage {
  question: string;
  answer: string;
  sources: SearchResultItem[];
  model: string;
  input_tokens?: number;
  output_tokens?: number;
  timestamp: string;
}

export default function ChatPanel({ projectId, seedContext, initialQuestion, showHistory = true }: ChatPanelProps) {
  const t = useTranslations('askUi');
  const fmt = useFormat();
  const id = projectId;
  const [question, setQuestion] = useState('');
  const [loading, setLoading] = useState(false);
  const [messages, setMessages] = useState<DisplayMessage[]>([]);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [sessions, setSessions] = useState<AskSession[]>([]);
  // priorSeedSessions are earlier conversations launched from THIS insight /
  // recommendation, shown in the seeded empty state so the user can resume one
  // instead of starting over.
  const [priorSeedSessions, setPriorSeedSessions] = useState<AskSession[]>([]);
  const [historyOpen, setHistoryOpen] = useState(true);
  // The seed is sent on the first turn only; after a session exists the server
  // has persisted it. seededRef guards against re-sending / losing it.
  const seededRef = useRef(false);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (showHistory) loadSessions();
    if (seedContext) {
      api.listAskSessions(id, 10, { type: seedContext.type, id: seedContext.id })
        .then(s => setPriorSeedSessions(s || []))
        .catch(() => {});
    }
    if (initialQuestion) handleAsk(initialQuestion);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const loadSessions = () => {
    api.listAskSessions(id, 30).then(s => setSessions(s || [])).catch(() => {});
  };

  useEffect(() => {
    if (messages.length > 0) bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages.length, loading]);

  const handleAsk = async (q?: string) => {
    const text = (q || question).trim();
    if (!text || loading) return;
    setLoading(true);
    setQuestion('');
    try {
      // Attach the seed only on the first turn of a fresh, seeded conversation.
      const sendSeed = !sessionId && !seededRef.current && !!seedContext;
      const resp = await api.askInsights(id, {
        question: text,
        limit: 5,
        session_id: sessionId || undefined,
        seed_context: sendSeed
          ? { type: seedContext!.type, id: seedContext!.id }
          : undefined,
      });
      if (sendSeed) seededRef.current = true;
      if (!sessionId && resp.session_id) setSessionId(resp.session_id);
      setMessages(prev => [...prev, {
        question: text,
        answer: resp.answer,
        sources: resp.sources,
        model: resp.model,
        input_tokens: resp.input_tokens,
        output_tokens: resp.output_tokens,
        timestamp: new Date().toISOString(),
      }]);
      if (showHistory) loadSessions();
    } catch (err) {
      setMessages(prev => [...prev, {
        question: text,
        answer: askErrorMessage(err),
        sources: [],
        model: '',
        timestamp: new Date().toISOString(),
      }]);
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    handleAsk();
  };

  const startNewChat = () => {
    setMessages([]);
    setSessionId(null);
    setQuestion('');
    // A manual new chat drops the seed anchor — a fresh generic conversation.
    seededRef.current = true;
  };

  const loadSession = async (session: AskSession) => {
    try {
      const full = await api.getAskSession(id, session.id);
      setSessionId(full.id);
      seededRef.current = true; // loaded session already carries any seed
      setMessages(full.messages.map(m => ({
        question: m.question,
        answer: m.answer,
        sources: m.sources.map(s => ({
          id: s.id, type: s.type as 'insight' | 'recommendation', name: s.name,
          score: s.score, severity: s.severity, analysis_area: s.analysis_area,
          description: s.description || '', discovery_id: s.discovery_id,
          discovered_at: '',
        })),
        model: m.model,
        input_tokens: m.input_tokens,
        output_tokens: m.output_tokens,
        timestamp: m.created_at,
      })));
    } catch {
      startNewChat();
      handleAsk(session.title);
    }
  };

  const deleteSession = async (e: React.MouseEvent, sessionToDelete: AskSession) => {
    e.stopPropagation();
    try {
      await api.deleteAskSession(id, sessionToDelete.id);
      if (sessionId === sessionToDelete.id) startNewChat();
      loadSessions();
    } catch { /* ignore */ }
  };

  // seedActive is true only while the seed still governs the conversation:
  // after "New chat" (seededRef flipped true) the next turn is unanchored, so
  // neither the empty-state heading nor the chip should claim the chat is about
  // the seed. setMessages([]) in startNewChat re-renders, so the ref reads fresh.
  const seedActive = !!seedContext && !seededRef.current;
  const showSeedChip = seedActive && messages.length === 0 && !sessionId;

  return (
    <div style={{ display: 'flex', gap: 0, height: '100%', minHeight: 0, overflow: 'hidden' }}>
      {/* Left — Chat */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, overflow: 'hidden', padding: '16px 16px 0' }}>
        {messages.length > 0 && (
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', marginBottom: 8 }}>
            <button onClick={startNewChat} style={{
              display: 'inline-flex', alignItems: 'center', gap: 4,
              fontSize: 12, color: 'var(--db-text-link)', background: 'none',
              border: '1px solid var(--db-border-default)', borderRadius: 6,
              padding: '4px 10px', cursor: 'pointer', fontFamily: 'inherit',
            }}>
              <IconPlus size={12} /> {t('newChat')}
            </button>
          </div>
        )}

        {/* Messages */}
        <div style={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', gap: 20, marginBottom: 12, overflowY: 'auto' }}>
          {messages.length === 0 && !loading && (
            <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 16, padding: 40 }}>
              <div style={{
                width: 56, height: 56, borderRadius: 16,
                background: 'linear-gradient(135deg, var(--db-purple-bg), var(--db-blue-bg))',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
              }}>
                <IconMessageCircle size={28} color="var(--db-purple-text)" strokeWidth={1.5} />
              </div>
              <div style={{ textAlign: 'center', maxWidth: 420 }}>
                <p style={{ fontSize: 15, fontWeight: 500, color: 'var(--db-text-primary)', margin: '0 0 4px' }}>
                  {seedActive ? t('askAboutThis', { type: seedContext!.type }) : t('askAnything')}
                </p>
                <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: 0 }}>
                  {t('askAnythingSubtitle')}
                </p>
              </div>
              {seedActive && priorSeedSessions.length > 0 && (
                <div style={{ width: '100%', maxWidth: 420, marginTop: 4 }}>
                  <p style={{ fontSize: 12, fontWeight: 600, color: 'var(--db-text-tertiary)', margin: '0 0 6px', textAlign: 'left' }}>
                    {t('previousConversations', { type: seedContext!.type })}
                  </p>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                    {priorSeedSessions.map(s => (
                      <button key={s.id} type="button" onClick={() => loadSession(s)} style={{
                        display: 'flex', alignItems: 'center', gap: 8, width: '100%', textAlign: 'left',
                        background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
                        borderRadius: 8, padding: '8px 10px', cursor: 'pointer', fontFamily: 'inherit',
                      }}
                        onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                        onMouseLeave={e => { e.currentTarget.style.background = 'var(--db-bg-white)'; }}
                      >
                        <IconClock size={13} color="var(--db-text-tertiary)" style={{ flexShrink: 0 }} />
                        <span style={{ flex: 1, fontSize: 13, color: 'var(--db-text-primary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.title}</span>
                        <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)', flexShrink: 0 }}>{t('messageCount', { count: s.message_count || 0 })}</span>
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          )}

          {messages.map((entry, i) => (
            <div key={i} style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                <div style={{
                  background: 'var(--db-blue-bg)', color: 'var(--db-blue-text)',
                  padding: '10px 14px', borderRadius: '12px 12px 2px 12px', fontSize: 14, maxWidth: '75%',
                }}>
                  {entry.question}
                </div>
              </div>

              <div style={{
                background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
                borderRadius: 'var(--db-radius-lg)', padding: 16,
              }}>
                <AnswerContent answer={entry.answer} sources={entry.sources} projectId={id} />
                <CitationsFooter projectId={id} sources={entry.sources} />
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 8 }}>
                  {entry.model && <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>{entry.model}</span>}
                  {((entry.input_tokens ?? 0) > 0 || (entry.output_tokens ?? 0) > 0) && (
                    <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                      {t('tokenUsage', { in: fmt.number(entry.input_tokens ?? 0), out: fmt.number(entry.output_tokens ?? 0) })}
                    </span>
                  )}
                  {entry.timestamp && <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>{fmt.dateTime(entry.timestamp, { hour: '2-digit', minute: '2-digit' })}</span>}
                </div>
              </div>
            </div>
          ))}

          {loading && (
            <div style={{
              background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
              borderRadius: 'var(--db-radius-lg)', padding: 24, display: 'flex', alignItems: 'center', gap: 10,
            }}>
              <Loader size="xs" />
              <span style={{ fontSize: 14, color: 'var(--db-text-secondary)' }}>{t('thinking')}</span>
            </div>
          )}

          <div ref={bottomRef} />
        </div>

        {/* Seed chip — shows the active context above the input on a fresh seeded chat */}
        {showSeedChip && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 6, marginBottom: 8,
            fontSize: 12, color: 'var(--db-text-secondary)',
          }}>
            {seedContext!.type === 'insight'
              ? <IconBulb size={13} color="var(--db-amber-text)" />
              : <IconStarFilled size={13} color="var(--db-purple-text)" />}
            <span>{t('askingAbout')} <strong style={{ color: 'var(--db-text-primary)' }}>{seedContext!.title}</strong></span>
          </div>
        )}

        {/* Input bar */}
        <form onSubmit={handleSubmit} style={{
          position: 'sticky', bottom: 0, background: 'var(--db-bg-page)',
          paddingTop: 12, paddingBottom: 12, display: 'flex', gap: 8, alignItems: 'center',
        }}>
          <TextInput
            placeholder={t('inputPlaceholder')}
            value={question}
            onChange={e => setQuestion(e.currentTarget.value)}
            style={{ flex: 1 }}
            size="md"
            disabled={loading}
          />
          <ActionIcon type="submit" size="lg" variant="filled" loading={loading} style={{ height: 42, width: 42 }}>
            <IconSend size={18} />
          </ActionIcon>
          {showHistory && (
            <ActionIcon
              size="lg"
              variant={historyOpen ? 'light' : 'subtle'}
              color={historyOpen ? 'blue' : 'gray'}
              onClick={() => setHistoryOpen(!historyOpen)}
              style={{ height: 42, width: 42 }}
              title={historyOpen ? t('hideHistory') : t('showHistory')}
            >
              <IconHistory size={18} />
            </ActionIcon>
          )}
        </form>
      </div>

      {/* Right — Sessions panel (page only) */}
      {showHistory && historyOpen && (
        <div style={{
          width: 280, flexShrink: 0,
          background: 'var(--db-bg-white)',
          borderLeft: '1px solid var(--db-border-default)',
          display: 'flex', flexDirection: 'column',
          overflow: 'hidden',
        }}>
          <div style={{
            display: 'flex', alignItems: 'center', justifyContent: 'space-between',
            padding: '14px 16px', borderBottom: '1px solid var(--db-border-default)',
            flexShrink: 0,
          }}>
            <span style={{ fontSize: 14, fontWeight: 600, color: 'var(--db-text-primary)' }}>{t('conversations')}</span>
            <button onClick={startNewChat} style={{
              fontSize: 11, color: 'var(--db-text-link)', background: 'none',
              border: 'none', cursor: 'pointer', fontFamily: 'inherit',
              display: 'flex', alignItems: 'center', gap: 3,
            }}>
              <IconPlus size={12} /> {t('new')}
            </button>
          </div>

          <div style={{ flex: 1, overflowY: 'auto' }}>
            {sessions.length === 0 && (
              <div style={{ padding: 24, textAlign: 'center' }}>
                <IconClock size={24} color="var(--db-text-tertiary)" style={{ marginBottom: 8 }} />
                <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: 0 }}>
                  {t('conversationsEmpty')}
                </p>
              </div>
            )}

            {sessions.map(s => (
              <div
                key={s.id}
                onClick={() => loadSession(s)}
                style={{
                  padding: '10px 16px', cursor: 'pointer',
                  borderBottom: '1px solid var(--db-border-default)',
                  background: s.id === sessionId ? 'var(--db-blue-bg)' : 'transparent',
                  transition: 'background 80ms ease',
                  display: 'flex', alignItems: 'flex-start', gap: 8,
                }}
                onMouseEnter={e => { if (s.id !== sessionId) e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
                onMouseLeave={e => { if (s.id !== sessionId) e.currentTarget.style.background = 'transparent'; }}
              >
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{
                    fontSize: 13, fontWeight: s.id === sessionId ? 600 : 500,
                    color: s.id === sessionId ? 'var(--db-blue-text)' : 'var(--db-text-primary)',
                    overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
                  }}>
                    {s.title}
                  </div>
                  <div style={{ fontSize: 10, color: 'var(--db-text-tertiary)', marginTop: 3, display: 'flex', alignItems: 'center', gap: 4 }}>
                    <IconClock size={10} />
                    {formatRelativeTime(s.updated_at || s.created_at, t, fmt)}
                    <span>·</span>
                    {t('messageCount', { count: s.message_count || 0 })}
                  </div>
                </div>
                <ActionIcon
                  size="xs" variant="subtle" color="gray"
                  onClick={(e) => deleteSession(e, s)}
                  style={{ marginTop: 2, opacity: 0.4 }}
                  onMouseEnter={e => { e.currentTarget.style.opacity = '1'; }}
                  onMouseLeave={e => { e.currentTarget.style.opacity = '0.4'; }}
                >
                  <IconTrash size={12} />
                </ActionIcon>
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

/** Renders markdown answer with interactive citation tooltips */
function AnswerContent({ answer, sources, projectId }: { answer: string; sources: SearchResultItem[]; projectId: string }) {
  return (
    <div className="ask-answer">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          p: ({ children }) => <p style={{ margin: '0 0 12px', lineHeight: 1.7 }}>{processChildren(children, sources, projectId)}</p>,
          li: ({ children }) => <li style={{ marginBottom: 4, lineHeight: 1.6 }}>{processChildren(children, sources, projectId)}</li>,
          h1: ({ children }) => <h3 style={{ fontSize: 16, fontWeight: 600, margin: '16px 0 8px', color: 'var(--db-text-primary)' }}>{children}</h3>,
          h2: ({ children }) => <h3 style={{ fontSize: 15, fontWeight: 600, margin: '14px 0 6px', color: 'var(--db-text-primary)' }}>{children}</h3>,
          h3: ({ children }) => <h4 style={{ fontSize: 14, fontWeight: 600, margin: '12px 0 6px', color: 'var(--db-text-primary)' }}>{children}</h4>,
          strong: ({ children }) => <strong style={{ fontWeight: 600, color: 'var(--db-text-primary)' }}>{children}</strong>,
          ul: ({ children }) => <ul style={{ margin: '8px 0', paddingLeft: 20 }}>{children}</ul>,
          ol: ({ children }) => <ol style={{ margin: '8px 0', paddingLeft: 20 }}>{children}</ol>,
          table: ({ children }) => (
            <div style={{ overflowX: 'auto', margin: '8px 0' }}>
              <table style={{ borderCollapse: 'collapse', fontSize: 13, width: '100%' }}>{children}</table>
            </div>
          ),
          th: ({ children }) => <th style={{ borderBottom: '2px solid var(--db-border-default)', padding: '6px 10px', textAlign: 'left', fontWeight: 600, fontSize: 12 }}>{children}</th>,
          td: ({ children }) => <td style={{ borderBottom: '1px solid var(--db-border-default)', padding: '6px 10px', fontSize: 13 }}>{children}</td>,
          code: ({ children, className }) => {
            const isBlock = className?.includes('language-');
            return isBlock ? (
              <pre style={{ background: 'var(--db-bg-muted)', borderRadius: 6, padding: 12, overflow: 'auto', fontSize: 12, margin: '8px 0' }}>
                <code>{children}</code>
              </pre>
            ) : (
              <code style={{ background: 'var(--db-bg-muted)', padding: '1px 5px', borderRadius: 4, fontSize: '0.9em' }}>{children}</code>
            );
          },
        }}
      >
        {answer}
      </ReactMarkdown>
      <style>{`
        .ask-answer { font-size: 14px; color: var(--db-text-primary); }
        .ask-answer > *:first-child { margin-top: 0; }
        .ask-answer > *:last-child { margin-bottom: 0; }
      `}</style>
    </div>
  );
}

function processChildren(children: React.ReactNode, sources: SearchResultItem[], projectId: string): React.ReactNode {
  const process = (child: React.ReactNode): React.ReactNode => {
    if (typeof child === 'string') {
      const parts = child.split(/(\[[\d,\s]+\](?:\[[\d,\s]+\])*)/g);
      if (parts.length === 1) return child;

      return parts.map((part, i) => {
        const nums = [...part.matchAll(/\d+/g)].map(m => parseInt(m[0], 10));
        if (nums.length === 0 || !part.match(/^\[[\d,\s\[\]]+\]$/)) return <span key={i}>{part}</span>;

        return (
          <span key={i}>
            {nums.map((num, j) => {
              const src = sources[num - 1];
              return (
                <CitationLink
                  key={j}
                  number={num}
                  href={src ? sourceHref(src.project_id || projectId, src) : undefined}
                  name={src ? (src.name || src.title || undefined) : undefined}
                  severity={src?.severity}
                  description={src?.description}
                />
              );
            })}
          </span>
        );
      });
    }
    if (Array.isArray(child)) return child.map((c, i) => <span key={i}>{process(c)}</span>);
    return child;
  };

  if (Array.isArray(children)) return children.map((c, i) => <span key={i}>{process(c)}</span>);
  return process(children);
}

function formatRelativeTime(
  iso: string,
  t: ReturnType<typeof useTranslations<'askUi'>>,
  fmt: ReturnType<typeof useFormat>,
): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return t('relativeJustNow');
  if (mins < 60) return t('relativeMinutes', { count: mins });
  const hours = Math.floor(mins / 60);
  if (hours < 24) return t('relativeHours', { count: hours });
  const days = Math.floor(hours / 24);
  if (days < 7) return t('relativeDays', { count: days });
  return fmt.dateTime(iso);
}
