'use client';

import { useEffect, useState } from 'react';
import { useParams } from 'next/navigation';
import {
  Accordion, Code, Loader, Text,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import {
  IconAlertCircle, IconBulb, IconChevronDown, IconClipboardX, IconDatabase, IconSearch,
} from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import FeedbackButtons from '@/components/common/FeedbackButtons';
import {
  StatCard, SectionHeader, Th, SeverityBadge, AreaBadge, ConfidenceBar, Pill, EmptyState, normalizeConfidence,
} from '@/components/common/UIComponents';
import { api, DiscoveryResult, Feedback, Insight, Recommendation } from '@/lib/api';

const severityOrder: Record<string, number> = {
  critical: 0, high: 1, medium: 2, low: 3,
};

export default function DiscoveryDetailPage() {
  const { id, runId } = useParams<{ id: string; runId: string }>();
  const [discovery, setDiscovery] = useState<DiscoveryResult | null>(null);
  const [project, setProject] = useState<{ name: string; domain: string; category: string } | null>(null);
  const [feedbackMap, setFeedbackMap] = useState<Record<string, Feedback>>({});
  const [loading, setLoading] = useState(true);
  const [severityFilter, setSeverityFilter] = useState<string>('All');
  const [sortBy, setSortBy] = useState<string>('Severity');
  const [showAllRecs, setShowAllRecs] = useState(false);

  useEffect(() => {
    Promise.all([
      api.getDiscoveryById(runId).then(setDiscovery),
      api.getProject(id).then(p => setProject({ name: p.name, domain: p.domain, category: p.category })),
      api.listFeedback(runId).then((fb) => {
        const map: Record<string, Feedback> = {};
        (fb || []).forEach((f) => { map[`${f.target_type}:${f.target_id}`] = f; });
        setFeedbackMap(map);
      }).catch(() => {}),
    ])
      .catch(() => null)
      .finally(() => setLoading(false));
  }, [id, runId]);

  const handleFeedbackUpdate = (targetType: string, targetId: string, fb: Feedback | null) => {
    const key = `${targetType}:${targetId}`;
    setFeedbackMap((prev) => {
      const next = { ...prev };
      if (fb) next[key] = fb;
      else delete next[key];
      return next;
    });
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (!discovery) return <Shell><Text>Discovery not found</Text></Shell>;

  const insights = discovery.insights || [];
  const recommendations = [...(discovery.recommendations || [])].sort((a, b) => a.priority - b.priority);

  // Filter + sort insights
  let filtered = insights;
  if (severityFilter !== 'All') {
    filtered = filtered.filter(i => i.severity.toLowerCase() === severityFilter.toLowerCase());
  }
  filtered = [...filtered].sort((a, b) => {
    switch (sortBy) {
      case 'Severity': return (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
      case 'Confidence': return b.confidence - a.confidence;
      case 'Players affected': return (b.affected_count || 0) - (a.affected_count || 0);
      case 'Area': return a.analysis_area.localeCompare(b.analysis_area);
      default: return (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
    }
  });

  // Aggregate stats
  const totalInsights = insights.length;
  const criticalCount = insights.filter(i => i.severity === 'critical').length;
  const avgConfidenceRaw = totalInsights > 0
    ? insights.reduce((sum, i) => sum + (i.confidence || 0), 0) / totalInsights
    : 0;
  const avgConfidence = normalizeConfidence(avgConfidenceRaw);

  const durationSec = discovery.duration ? (discovery.duration / 1000000000).toFixed(2) : '—';

  const breadcrumb = [
    { label: 'Projects', href: '/' },
    { label: project?.name || '...', href: `/projects/${id}` },
    { label: new Date(discovery.discovery_date).toLocaleDateString('en-US', { month: 'short', day: 'numeric' }) },
  ];

  const visibleRecs = showAllRecs ? recommendations : recommendations.slice(0, 3);
  const hiddenRecCount = recommendations.length - 3;

  return (
    <Shell breadcrumb={breadcrumb}>
      {/* Run Header */}
      <div style={{ marginBottom: 20 }}>
        {project && (
          <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', marginBottom: 4 }}>
            {project.name} · {project.category || project.domain}
          </div>
        )}
        <div style={{ fontSize: 18, fontWeight: 500, marginBottom: 6 }}>
          Discovery run · {new Date(discovery.discovery_date).toLocaleDateString('en-US', {
            month: 'long', day: 'numeric', year: 'numeric',
          })}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <SeverityBadge severity={discovery.run_type === 'partial' ? 'Partial' : 'Complete'}
            type="status" />
          {discovery.areas_requested?.map(a => (
            <span key={a} style={{
              fontSize: 11, padding: '1px 7px', borderRadius: 'var(--db-radius)',
              background: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)',
            }}>{a}</span>
          ))}
          <span style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
            {discovery.total_steps} queries · completed in {durationSec}s
          </span>
        </div>
      </div>

      {/* Errors banner */}
      {discovery.summary?.errors && discovery.summary.errors.length > 0 && (
        <div style={{
          background: 'var(--db-red-bg)', border: '1px solid var(--db-severity-critical-text)',
          borderRadius: 'var(--db-radius-lg)', padding: '12px 16px', marginBottom: 16,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4 }}>
            <IconAlertCircle size={16} color="var(--db-red-text)" />
            <span style={{ fontSize: 13, fontWeight: 500, color: 'var(--db-red-text)' }}>
              {discovery.summary.errors.length === 1 ? '1 area failed' : `${discovery.summary.errors.length} areas failed`} during analysis
            </span>
          </div>
          {discovery.summary.errors.map((err, i) => (
            <div key={i} style={{ fontSize: 12, color: 'var(--db-red-text)', paddingLeft: 22 }}>{err}</div>
          ))}
        </div>
      )}

      {/* Hero KPI Cards */}
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12, marginBottom: 24,
      }}>
        <StatCard label="Total Insights" value={totalInsights} />
        <StatCard label="Critical Findings" value={criticalCount}
          subtitle={`Of ${totalInsights} total insights`}
          valueColor={criticalCount > 0 ? 'var(--db-red-text)' : undefined} />
        <StatCard label="Recommendations" value={recommendations.length} />
        <StatCard label="Avg. Confidence" value={`${avgConfidence}%`}
          subtitle={`${discovery.summary?.queries_executed || 0} queries executed`} />
      </div>

      {/* Insights Section */}
      <SectionHeader title="Insights" count={filtered.length} />

      {insights.length > 0 ? (
        <>
          {/* Filter bar */}
          <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap', alignItems: 'center' }}>
            {['All', 'Critical', 'High', 'Medium', 'Low'].map(sev => (
              <button key={sev} onClick={() => setSeverityFilter(sev)} style={{
                fontSize: 12, padding: '4px 12px',
                border: `0.5px solid var(--db-border-strong)`,
                borderRadius: 'var(--db-radius)',
                background: severityFilter === sev ? 'var(--db-blue-bg)' : 'var(--db-bg-white)',
                color: severityFilter === sev ? 'var(--db-blue-text)' : 'var(--db-text-secondary)',
                cursor: 'pointer', fontFamily: 'inherit', transition: 'all 120ms ease',
              }}>{sev}</button>
            ))}
            <span style={{ flex: 1 }} />
            <SortDropdown value={sortBy} onChange={setSortBy} />
          </div>

          {/* Insights Table */}
          <div style={{
            background: 'var(--db-bg-white)',
            border: '1px solid var(--db-border-default)',
            borderRadius: 'var(--db-radius-lg)',
            overflow: 'hidden',
            marginBottom: 24,
          }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
              <thead>
                <tr>
                  <Th width="35%">Insight</Th>
                  <Th>Severity</Th>
                  <Th>Area</Th>
                  <Th align="right">Players affected</Th>
                  <Th>Confidence</Th>
                  <Th width="70px">Feedback</Th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((insight, idx) => (
                  <InsightRow key={idx} insight={insight} projectId={id} runId={runId} idx={idx}
                    feedback={feedbackMap[`insight:${insight.id || idx}`]}
                    onFeedbackUpdate={(fb) => handleFeedbackUpdate('insight', String(insight.id || idx), fb)} />
                ))}
              </tbody>
            </table>
          </div>
        </>
      ) : (
        <EmptyState icon={<IconBulb size={32} />} title="No insights found"
          description="No issues were detected in this discovery run." />
      )}

      {/* Recommendations Section */}
      <div style={{ marginTop: '2.5rem' }}>
        <SectionHeader title="Recommendations" count={recommendations.length} />
      </div>

      {recommendations.length > 0 ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {visibleRecs.map((rec, idx) => (
            <RecommendationCard key={idx} rec={rec} projectId={id} discoveryId={runId} idx={idx}
              insights={insights}
              feedback={feedbackMap[`recommendation:${idx}`]}
              onFeedbackUpdate={(fb) => handleFeedbackUpdate('recommendation', String(idx), fb)} />
          ))}
          {!showAllRecs && hiddenRecCount > 0 && (
            <div onClick={() => setShowAllRecs(true)} style={{
              background: 'var(--db-bg-white)',
              border: '1px dashed var(--db-border-strong)',
              borderRadius: 'var(--db-radius-lg)',
              padding: '16px 20px',
              cursor: 'pointer',
              opacity: 0.7,
              textAlign: 'center',
              transition: 'opacity 120ms ease',
            }}
            onMouseEnter={e => { e.currentTarget.style.opacity = '1'; }}
            onMouseLeave={e => { e.currentTarget.style.opacity = '0.7'; }}
            >
              <span style={{ fontSize: 14, fontWeight: 500, color: 'var(--db-text-secondary)' }}>
                + {hiddenRecCount} more recommendations
              </span>
            </div>
          )}
        </div>
      ) : (
        <EmptyState icon={<IconClipboardX size={32} />} title="No recommendations available"
          description="No actionable recommendations for the insights found." />
      )}

      {/* Transparency: How the AI Found This */}
      {((discovery.exploration_log && discovery.exploration_log.length > 0) ||
        (discovery.analysis_log && discovery.analysis_log.length > 0)) && (
        <div style={{ marginTop: '2.5rem' }}>
          <SectionHeader title="How the AI Found This" />
          <Accordion variant="separated" styles={{
            item: { borderColor: 'var(--db-border-default)' },
            control: { fontSize: 13 },
          }}>
            {discovery.exploration_log && discovery.exploration_log.length > 0 && (
              <Accordion.Item value="exploration">
                <Accordion.Control>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <IconDatabase size={16} />
                    <span style={{ fontWeight: 500 }}>Exploration Steps ({discovery.exploration_log.length})</span>
                  </span>
                </Accordion.Control>
                <Accordion.Panel>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {discovery.exploration_log.map((step, idx) => (
                      <div key={idx} style={{
                        border: '1px solid var(--db-border-default)',
                        borderRadius: 'var(--db-radius)',
                        padding: '10px 12px',
                      }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                          <span style={{ fontSize: 12, fontWeight: 600 }}>Step {step.step}</span>
                          <span style={{ display: 'flex', gap: 4 }}>
                            {step.row_count > 0 && <MicroBadge>{step.row_count} rows</MicroBadge>}
                            {step.execution_time_ms > 0 && <MicroBadge>{step.execution_time_ms}ms</MicroBadge>}
                            {step.fixed && <MicroBadge color="amber">auto-fixed</MicroBadge>}
                            {step.error && <MicroBadge color="red">error</MicroBadge>}
                            {step.query && !step.error && (
                              <FeedbackButtons projectId={id} discoveryId={runId} targetType="exploration_step"
                                targetId={String(step.step)}
                                feedback={feedbackMap[`exploration_step:${step.step}`]}
                                onUpdate={(fb) => handleFeedbackUpdate('exploration_step', String(step.step), fb)} />
                            )}
                          </span>
                        </div>
                        {step.thinking && (
                          <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginBottom: 4 }}>{step.thinking}</div>
                        )}
                        {step.query && (
                          <Code block style={{ fontSize: 11, maxHeight: 80, overflow: 'auto' }}>{step.query}</Code>
                        )}
                        {step.error && <div style={{ fontSize: 12, color: 'var(--db-red-text)', marginTop: 4 }}>{step.error}</div>}
                      </div>
                    ))}
                  </div>
                </Accordion.Panel>
              </Accordion.Item>
            )}

            {discovery.analysis_log && discovery.analysis_log.length > 0 && (
              <Accordion.Item value="analysis">
                <Accordion.Control>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <IconBulb size={16} />
                    <span style={{ fontWeight: 500 }}>Analysis by Area ({discovery.analysis_log.length})</span>
                  </span>
                </Accordion.Control>
                <Accordion.Panel>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {discovery.analysis_log.map((step, idx) => (
                      <div key={idx} style={{
                        border: '1px solid var(--db-border-default)',
                        borderRadius: 'var(--db-radius)',
                        padding: '10px 12px',
                      }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between' }}>
                          <span style={{ fontSize: 13, fontWeight: 500 }}>{step.area_name || step.area_id}</span>
                          <span style={{ display: 'flex', gap: 4 }}>
                            <MicroBadge>{step.relevant_queries} queries</MicroBadge>
                            <MicroBadge>{step.tokens_in + step.tokens_out} tokens</MicroBadge>
                            {step.duration_ms > 0 && <MicroBadge>{(step.duration_ms / 1000).toFixed(1)}s</MicroBadge>}
                            {step.error && <MicroBadge color="red">error</MicroBadge>}
                          </span>
                        </div>
                        {step.error && <div style={{ fontSize: 12, color: 'var(--db-red-text)', marginTop: 4 }}>{step.error}</div>}
                      </div>
                    ))}
                  </div>
                </Accordion.Panel>
              </Accordion.Item>
            )}

            {discovery.validation_log && discovery.validation_log.length > 0 && (
              <Accordion.Item value="validation">
                <Accordion.Control>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <IconSearch size={16} />
                    <span style={{ fontWeight: 500 }}>Validation ({discovery.validation_log.length})</span>
                  </span>
                </Accordion.Control>
                <Accordion.Panel>
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {discovery.validation_log.map((v, idx) => (
                      <div key={idx} style={{
                        border: '1px solid var(--db-border-default)',
                        borderRadius: 'var(--db-radius)',
                        padding: '10px 12px',
                      }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                          <span style={{ fontSize: 12 }}>{v.analysis_area}</span>
                          <SeverityBadge severity={v.status} type="validation" />
                        </div>
                        {v.claimed_count > 0 && (
                          <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
                            Claimed: {v.claimed_count.toLocaleString()} → Verified: {v.verified_count.toLocaleString()}
                          </div>
                        )}
                        <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>{v.reasoning}</div>
                      </div>
                    ))}
                  </div>
                </Accordion.Panel>
              </Accordion.Item>
            )}
          </Accordion>
        </div>
      )}
    </Shell>
  );
}

/* ========== Insight Table Row ========== */

function InsightRow({ insight, projectId, runId, idx, feedback, onFeedbackUpdate }: {
  insight: Insight; projectId: string; runId: string; idx: number;
  feedback?: Feedback; onFeedbackUpdate: (fb: Feedback | null) => void;
}) {
  return (
    <tr style={{ borderBottom: '1px solid var(--db-border-default)' }}
      onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
      onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
    >
      <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
        <Link href={`/projects/${projectId}/discoveries/${runId}/insights/${insight.id || idx}`}
          style={{
            fontSize: 13, fontWeight: 500, color: 'var(--db-text-link)',
            textDecoration: 'none', cursor: 'pointer', maxWidth: 320, display: 'block',
          }}
          onMouseEnter={e => { e.currentTarget.style.textDecoration = 'underline'; }}
          onMouseLeave={e => { e.currentTarget.style.textDecoration = 'none'; }}
        >
          {insight.name}
        </Link>
      </td>
      <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
        <SeverityBadge severity={insight.severity} type="severity" />
      </td>
      <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
        <AreaBadge area={insight.analysis_area} />
      </td>
      <td style={{ padding: '10px 12px', textAlign: 'right', verticalAlign: 'top', fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' }}>
        {insight.affected_count > 0 ? insight.affected_count.toLocaleString() : '—'}
      </td>
      <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
        <ConfidenceBar confidence={insight.confidence} />
      </td>
      <td style={{ padding: '10px 12px', verticalAlign: 'top' }}>
        <FeedbackButtons projectId={projectId} discoveryId={runId} targetType="insight"
          targetId={String(insight.id || idx)}
          feedback={feedback} onUpdate={onFeedbackUpdate} />
      </td>
    </tr>
  );
}

/* ========== Recommendation Card ========== */

function RecommendationCard({ rec, projectId, discoveryId, idx, insights, feedback, onFeedbackUpdate }: {
  rec: Recommendation; projectId: string; discoveryId: string; idx: number;
  insights: Insight[];
  feedback?: Feedback | null; onFeedbackUpdate?: (fb: Feedback | null) => void;
}) {
  const effortColors: Record<string, { bg: string; color: string }> = {
    low: { bg: '#EAF3DE', color: '#3B6D11' },
    medium: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
    high: { bg: '#FAECE7', color: '#993C1D' },
  };

  // Derive effort from priority
  const effort = rec.priority <= 1 ? 'low' : rec.priority <= 3 ? 'medium' : 'high';
  const effortStyle = effortColors[effort] || effortColors.medium;

  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      padding: '16px 20px',
    }}>
      {/* Title row */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 8, marginBottom: 6 }}>
        <Link href={`/projects/${projectId}/discoveries/${discoveryId}/recommendations/${rec.id || idx}`}
          style={{ fontSize: 14, fontWeight: 500, flex: 1, color: 'var(--db-text-primary)', textDecoration: 'none' }}
          onMouseEnter={e => { e.currentTarget.style.color = 'var(--db-text-link)'; }}
          onMouseLeave={e => { e.currentTarget.style.color = 'var(--db-text-primary)'; }}
        >{rec.title}</Link>
        <FeedbackButtons projectId={projectId} discoveryId={discoveryId} targetType="recommendation"
          targetId={String(idx)} feedback={feedback} onUpdate={onFeedbackUpdate} />
      </div>

      {/* Pills + impact row */}
      <div style={{ display: 'flex', gap: 6, marginBottom: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <Pill bg={effortStyle.bg} color={effortStyle.color}>
          {effort.charAt(0).toUpperCase() + effort.slice(1)} effort
        </Pill>
        {rec.expected_impact?.estimated_improvement && (
          rec.expected_impact.estimated_improvement.length > 30
            ? <span style={{ fontSize: 12, color: 'var(--db-green-text)' }}>
                {rec.expected_impact.estimated_improvement}
              </span>
            : <Pill bg="var(--db-green-bg)" color="var(--db-green-text)">
                {rec.expected_impact.estimated_improvement}
              </Pill>
        )}
      </div>

      {/* Metadata */}
      <div style={{ display: 'flex', gap: 16, fontSize: 12, color: 'var(--db-text-tertiary)', marginBottom: 8, flexWrap: 'wrap' }}>
        {rec.target_segment && (
          <span>{rec.segment_size?.toLocaleString() || ''} {rec.target_segment}</span>
        )}
        {rec.expected_impact?.metric && (
          <span>{rec.expected_impact.metric}</span>
        )}
        {rec.confidence > 0 && <span>Confidence: {normalizeConfidence(rec.confidence)}%</span>}
      </div>

      {/* Related Insights */}
      {rec.related_insight_ids && rec.related_insight_ids.length > 0 && (() => {
        const relatedInsights = rec.related_insight_ids
          .map(rid => insights.find(i => i.id === rid))
          .filter(Boolean) as Insight[];
        if (relatedInsights.length === 0) return null;
        return (
          <div style={{ display: 'flex', gap: 6, marginBottom: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)', fontWeight: 500 }}>Addresses:</span>
            {relatedInsights.map((insight, i) => (
              <Link key={i} href={`/projects/${projectId}/discoveries/${discoveryId}/insights/${insight.id}`}
                style={{
                  fontSize: 11, padding: '1px 8px', borderRadius: 'var(--db-radius)',
                  background: 'var(--db-blue-bg)', color: 'var(--db-blue-text)',
                  textDecoration: 'none', transition: 'opacity 120ms ease',
                }}
                onMouseEnter={e => { e.currentTarget.style.opacity = '0.8'; }}
                onMouseLeave={e => { e.currentTarget.style.opacity = '1'; }}
              >
                {insight.name}
              </Link>
            ))}
          </div>
        );
      })()}

      {/* Description */}
      {rec.description && (
        <div style={{ fontSize: 13, color: 'var(--db-text-secondary)', lineHeight: 1.6, marginBottom: rec.actions?.length ? 0 : undefined }}>
          {rec.description}
        </div>
      )}

      {/* Action Steps */}
      {rec.actions && rec.actions.length > 0 && (
        <div style={{ marginTop: 10, paddingTop: 10, borderTop: '1px solid var(--db-border-default)' }}>
          <div style={{
            fontSize: 11, fontWeight: 500, color: 'var(--db-text-tertiary)',
            textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 6,
          }}>Action steps</div>
          {rec.actions.map((action, i) => (
            <div key={i} style={{
              display: 'flex', gap: 6, fontSize: 13, color: 'var(--db-text-secondary)',
              lineHeight: 1.6, marginBottom: 4,
            }}>
              <span style={{ color: 'var(--db-text-tertiary)', fontWeight: 500, flexShrink: 0 }}>{i + 1}</span>
              <span>{action}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/* ========== Page-Specific Components ========== */

function MicroBadge({ children, color }: { children: React.ReactNode; color?: 'red' | 'amber' }) {
  const bg = color === 'red' ? 'var(--db-red-bg)' : color === 'amber' ? 'var(--db-amber-bg)' : 'var(--db-bg-muted)';
  const textColor = color === 'red' ? 'var(--db-red-text)' : color === 'amber' ? 'var(--db-amber-text)' : 'var(--db-text-tertiary)';
  return (
    <span style={{
      fontSize: 10, fontWeight: 500, padding: '1px 6px', borderRadius: 4,
      background: bg, color: textColor,
    }}>{children}</span>
  );
}

function SortDropdown({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [open, { toggle, close }] = useDisclosure(false);
  const options = ['Severity', 'Confidence', 'Players affected', 'Area'];
  return (
    <div style={{ position: 'relative' }}>
      <button onClick={toggle} style={{
        fontSize: 12, color: 'var(--db-text-tertiary)',
        background: 'none', border: 'none', cursor: 'pointer',
        fontFamily: 'inherit', display: 'flex', alignItems: 'center', gap: 4,
      }}>
        Sort: {value} <IconChevronDown size={12} />
      </button>
      {open && (
        <div style={{
          position: 'absolute', right: 0, top: '100%', marginTop: 4,
          background: 'var(--db-bg-white)', border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius)', boxShadow: '0 4px 12px rgba(0,0,0,0.08)',
          zIndex: 10, minWidth: 140,
        }}>
          {options.map(opt => (
            <div key={opt} onClick={() => { onChange(opt); close(); }}
              style={{
                padding: '6px 12px', fontSize: 12, cursor: 'pointer',
                background: opt === value ? 'var(--db-bg-muted)' : 'transparent',
                fontWeight: opt === value ? 500 : 400,
                transition: 'background 120ms ease',
              }}
              onMouseEnter={e => { e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
              onMouseLeave={e => { if (opt !== value) e.currentTarget.style.background = 'transparent'; }}
            >{opt}</div>
          ))}
        </div>
      )}
    </div>
  );
}

