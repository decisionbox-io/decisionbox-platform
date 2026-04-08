'use client';

import { useEffect, useState, useRef } from 'react';
import { useParams, useSearchParams } from 'next/navigation';
import { Loader, TextInput, ActionIcon } from '@mantine/core';
import { IconMessageCircle, IconSend, IconBulb, IconStarFilled, IconHistory, IconClock, IconPlus, IconTrash } from '@tabler/icons-react';
import Link from 'next/link';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import Shell from '@/components/layout/AppShell';
import { SeverityBadge } from '@/components/common/UIComponents';
import { api, AskSession, SearchResultItem } from '@/lib/api';

interface DisplayMessage {
  question: string;
  answer: string;
  sources: SearchResultItem[];
  model: string;
  timestamp: string;
}

function sourceHref(projectId: string, src: SearchResultItem): string {
  const type = src.type === 'insight' ? 'insights' : 'recommendations';
  if (src.discovery_id) {
    return `/projects/${projectId}/discoveries/${src.discovery_id}/${type}/${src.id}`;
  }
  return `/projects/${projectId}/${type}`;
}

export default function AskPage() {
  const { id } = useParams<{ id: string }>();
  const searchParams = useSearchParams();
  const initialQuestion = searchParams.get('q') || '';
  const [project, setProject] = useState<{ name: string } | null>(null);
  const [question, setQuestion] = useState('');
  const [loading, setLoading] = useState(false);
  const [messages, setMessages] = useState<DisplayMessage[]>([]);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [sessions, setSessions] = useState<AskSession[]>([]);
  const [showHistory, setShowHistory] = useState(true);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api.getProject(id).then(p => setProject({ name: p.name })).catch(() => {});
    loadSessions();
    if (initialQuestion) handleAsk(initialQuestion);
  }, [id]); // eslint-disable-line react-hooks/exhaustive-deps

  const loadSessions = () => {
    api.listAskSessions(id, 30).then(s => setSessions(s || [])).catch(() => {});
  };

  useEffect(() => {
    if (messages.length > 0) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [messages.length, loading]);

  const handleAsk = async (q?: string) => {
    const text = (q || question).trim();
    if (!text || loading) return;
    setLoading(true);
    setQuestion('');
    try {
      const resp = await api.askInsights(id, {
        question: text,
        limit: 5,
        session_id: sessionId || undefined,
      });
      // Update session ID (first message creates a new session)
      if (!sessionId && resp.session_id) {
        setSessionId(resp.session_id);
      }
      setMessages(prev => [...prev, {
        question: text,
        answer: resp.answer,
        sources: resp.sources,
        model: resp.model,
        timestamp: new Date().toISOString(),
      }]);
      loadSessions();
    } catch {
      setMessages(prev => [...prev, {
        question: text,
        answer: 'Sorry, I could not answer this question. Make sure embedding is configured for this project.',
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
  };

  const loadSession = async (session: AskSession) => {
    try {
      const full = await api.getAskSession(id, session.id);
      setSessionId(full.id);
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
        timestamp: m.created_at,
      })));
    } catch {
      // Failed to load — start fresh with this question
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

  return (
    <Shell fullWidth breadcrumb={project ? [{ label: project.name, href: `/projects/${id}` }, { label: 'Ask Insights' }] : undefined}>
      <div style={{ display: 'flex', gap: 0, minHeight: 'calc(100vh - var(--db-topbar-height))', margin: '-24px -24px -24px -24px' }}>

        {/* Left — Chat */}
        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', minWidth: 0, padding: '24px 24px 0' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <div>
              <h1 style={{ fontSize: 22, fontWeight: 600, color: 'var(--db-text-primary)', margin: '0 0 4px' }}>
                Ask Your Insights
              </h1>
              <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: 0 }}>
                AI-synthesized answers with conversation context{messages.length > 0 ? ` — ${messages.length} message${messages.length !== 1 ? 's' : ''}` : ''}
              </p>
            </div>
            {messages.length > 0 && (
              <button onClick={startNewChat} style={{
                display: 'inline-flex', alignItems: 'center', gap: 4,
                fontSize: 12, color: 'var(--db-text-link)', background: 'none',
                border: '1px solid var(--db-border-default)', borderRadius: 6,
                padding: '4px 10px', cursor: 'pointer', fontFamily: 'inherit',
              }}>
                <IconPlus size={12} /> New chat
              </button>
            )}
          </div>

          {/* Messages */}
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 20, marginBottom: 12, overflowY: 'auto' }}>
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
                    Ask anything about your insights
                  </p>
                  <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: 0 }}>
                    Get answers backed by evidence from your discovery runs. Follow-up questions use conversation context.
                  </p>
                </div>
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

                  {entry.sources.length > 0 && (
                    <div style={{ marginTop: 14, paddingTop: 14, borderTop: '1px solid var(--db-border-default)' }}>
                      <h4 style={{ fontSize: 12, fontWeight: 600, color: 'var(--db-text-tertiary)', marginBottom: 8 }}>Sources</h4>
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                        {entry.sources.map((src, j) => (
                          <Link key={src.id} href={sourceHref(id, src)}
                            style={{
                              display: 'flex', alignItems: 'center', gap: 6, fontSize: 12,
                              color: 'var(--db-text-link)', textDecoration: 'none',
                              padding: '3px 6px', borderRadius: 4,
                              background: j % 2 === 0 ? 'var(--db-bg-muted)' : 'transparent',
                            }}>
                            {src.type === 'insight' ? <IconBulb size={12} color="var(--db-amber-text)" /> : <IconStarFilled size={12} color="var(--db-purple-text)" />}
                            <span style={{ flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>[{j + 1}] {src.name || src.title}</span>
                            {src.severity && <SeverityBadge severity={src.severity} type="severity" />}
                            <span style={{ fontSize: 10, color: 'var(--db-text-tertiary)' }}>{Math.round(src.score * 100)}%</span>
                          </Link>
                        ))}
                      </div>
                    </div>
                  )}

                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 8 }}>
                    {entry.model && <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>{entry.model}</span>}
                    {entry.timestamp && <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>{new Date(entry.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>}
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
                <span style={{ fontSize: 14, color: 'var(--db-text-secondary)' }}>Thinking...</span>
              </div>
            )}

            <div ref={bottomRef} />
          </div>

          {/* Input bar */}
          <form onSubmit={handleSubmit} style={{
            position: 'sticky', bottom: 0, background: 'var(--db-bg-page)',
            paddingTop: 12, paddingBottom: 12, display: 'flex', gap: 8, alignItems: 'center',
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
            <ActionIcon
              size="lg"
              variant={showHistory ? 'light' : 'subtle'}
              color={showHistory ? 'blue' : 'gray'}
              onClick={() => setShowHistory(!showHistory)}
              style={{ height: 42, width: 42 }}
              title={showHistory ? 'Hide history' : 'Show history'}
            >
              <IconHistory size={18} />
            </ActionIcon>
          </form>
        </div>

        {/* Right — Sessions panel */}
        {showHistory && (
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
              <span style={{ fontSize: 14, fontWeight: 600, color: 'var(--db-text-primary)' }}>Conversations</span>
              <button onClick={startNewChat} style={{
                fontSize: 11, color: 'var(--db-text-link)', background: 'none',
                border: 'none', cursor: 'pointer', fontFamily: 'inherit',
                display: 'flex', alignItems: 'center', gap: 3,
              }}>
                <IconPlus size={12} /> New
              </button>
            </div>

            <div style={{ flex: 1, overflowY: 'auto' }}>
              {sessions.length === 0 && (
                <div style={{ padding: 24, textAlign: 'center' }}>
                  <IconClock size={24} color="var(--db-text-tertiary)" style={{ marginBottom: 8 }} />
                  <p style={{ fontSize: 13, color: 'var(--db-text-tertiary)', margin: 0 }}>
                    Your conversations will appear here.
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
                      {formatRelativeTime(s.updated_at || s.created_at)}
                      <span>·</span>
                      {s.message_count || 0} msg{(s.message_count || 0) !== 1 ? 's' : ''}
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
    </Shell>
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
        .cite-ref { position: relative; display: inline; }
        .cite-ref .cite-badge {
          display: inline-flex; align-items: center; justify-content: center;
          font-size: 10px; font-weight: 700; color: var(--db-blue-text);
          background: var(--db-blue-bg); border-radius: 4px; padding: 0 4px;
          min-width: 16px; height: 16px; cursor: pointer; vertical-align: super;
          line-height: 1; text-decoration: none; transition: background 100ms ease; margin: 0 1px;
        }
        .cite-ref .cite-badge:hover { background: var(--db-blue-text); color: white; }
        .cite-ref .cite-tooltip {
          display: none; position: absolute; bottom: calc(100% + 6px); left: 50%;
          transform: translateX(-50%); background: var(--db-text-primary); color: white;
          padding: 8px 12px; border-radius: 8px; font-size: 12px; line-height: 1.4;
          width: 280px; z-index: 50; pointer-events: none; box-shadow: 0 4px 12px rgba(0,0,0,0.2);
        }
        .cite-ref .cite-tooltip::after {
          content: ''; position: absolute; top: 100%; left: 50%; transform: translateX(-50%);
          border: 5px solid transparent; border-top-color: var(--db-text-primary);
        }
        .cite-ref:hover .cite-tooltip { display: block; }
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
              const idx = num - 1;
              const src = sources[idx];
              if (!src) return <span key={j} className="cite-ref"><span className="cite-badge">{num}</span></span>;
              const name = src.name || src.title || '';
              const href = sourceHref(src.project_id || projectId, src);
              return (
                <span key={j} className="cite-ref">
                  <Link href={href} className="cite-badge">{num}</Link>
                  <span className="cite-tooltip">
                    <strong>{name.length > 80 ? name.slice(0, 80) + '...' : name}</strong>
                    {src.severity && <span style={{ marginLeft: 6, opacity: 0.7 }}>{src.severity}</span>}
                    {src.description && (
                      <span style={{ display: 'block', marginTop: 4, opacity: 0.8, fontSize: 11 }}>
                        {src.description.slice(0, 120)}{src.description.length > 120 ? '...' : ''}
                      </span>
                    )}
                  </span>
                </span>
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

function formatRelativeTime(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'Just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  return new Date(iso).toLocaleDateString();
}
