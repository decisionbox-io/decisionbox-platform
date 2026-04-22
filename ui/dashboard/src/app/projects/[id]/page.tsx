'use client';

import { useEffect, useState, useCallback, useRef } from 'react';
import { useParams } from 'next/navigation';
import {
  Checkbox, Collapse, Loader, Menu, NumberInput,
  ScrollArea, Text,
} from '@mantine/core';
import { useDisclosure } from '@mantine/hooks';
import { notifications } from '@mantine/notifications';
import {
  IconAlertTriangle, IconBulb, IconChartBar, IconCheck,
  IconDatabase, IconPlayerPlay, IconShieldCheck, IconStack2, IconX,
} from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { api, CostEstimate, DiscoveryResult, DiscoveryRunStatus, Project, RunStep } from '@/lib/api';

export default function ProjectPage() {
  const { id } = useParams<{ id: string }>();
  const [project, setProject] = useState<Project | null>(null);
  const [discoveries, setDiscoveries] = useState<DiscoveryResult[]>([]);
  const [run, setRun] = useState<DiscoveryRunStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [triggering, setTriggering] = useState(false);
  const [analysisAreas, setAnalysisAreas] = useState<{ id: string; name: string }[]>([]);
  const [selectedAreas, setSelectedAreas] = useState<string[]>([]);
  const [maxSteps, setMaxSteps] = useState(100);
  // minSteps rejects premature completion from the LLM — important for
  // reasoning models that tend to terminate exploration too early. The
  // value auto-tracks 60% of maxSteps until the user edits it; after that
  // it stays wherever the user put it (minStepsTouched flips to true).
  // Sending `undefined` to the API omits the field so the server applies
  // its own default; sending 0 explicitly disables the floor.
  const [minSteps, setMinSteps] = useState<number>(60);
  const [minStepsTouched, setMinStepsTouched] = useState(false);
  const [estimate, setEstimate] = useState<CostEstimate | null>(null);
  const [estimating, setEstimating] = useState(false);
  const [pendingAreas, setPendingAreas] = useState<string[] | undefined>(undefined);
  const [estimateFirst, setEstimateFirst] = useState(false);
  const dismissedRunId = useRef<string | null>(null);

  useEffect(() => {
    Promise.all([
      api.getProject(id).then((p) => {
        setProject(p);
        return api.getAnalysisAreas(p.domain, p.category)
          .then((areas) => setAnalysisAreas((areas || []).map((a) => ({ id: a.id, name: a.name }))));
      }),
      api.listDiscoveries(id).then((d) => setDiscoveries(d || [])).catch(() => setDiscoveries([])),
    ])
      .catch((e) => notifications.show({ title: 'Error', message: e.message, color: 'red' }))
      .finally(() => setLoading(false));
  }, [id]);

  const pollStatus = useCallback(async () => {
    try {
      const status = await api.getProjectStatus(id);
      if (status?.run) {
        const newRun = status.run as unknown as DiscoveryRunStatus;
        if (dismissedRunId.current === newRun.id) return;
        const wasRunning = run && (run.status === 'running' || run.status === 'pending');
        const nowDone = newRun.status === 'completed' || newRun.status === 'failed';
        setRun(newRun);
        if (wasRunning && nowDone) {
          api.listDiscoveries(id).then((d) => setDiscoveries(d || [])).catch(() => {});
        }
      }
    } catch { /* ignore */ }
  }, [id, run]);

  useEffect(() => {
    if (!run) return;
    if (run.status !== 'running' && run.status !== 'pending') return;
    const interval = setInterval(pollStatus, 2000);
    return () => clearInterval(interval);
  }, [run, pollStatus]);

  useEffect(() => { pollStatus(); }, [pollStatus]);

  const handleRun = (areas?: string[]) => {
    if (estimateFirst) handleEstimate(areas);
    else handleTrigger(areas);
  };

  const handleEstimate = async (areas?: string[]) => {
    setEstimating(true);
    setPendingAreas(areas);
    try {
      const opts: { areas?: string[]; max_steps?: number } = {};
      if (areas && areas.length > 0) opts.areas = areas;
      opts.max_steps = maxSteps;
      // Cost estimation doesn't care about min_steps — it only depends on
      // max_steps and selected areas. Keep the call minimal.
      const est = await api.estimateCost(id, opts);
      setEstimate(est);
    } catch (e: unknown) {
      notifications.show({ title: 'Estimation failed', message: (e as Error).message, color: 'orange' });
    } finally {
      setEstimating(false);
    }
  };

  const handleTrigger = async (areas?: string[]) => {
    setTriggering(true);
    setEstimate(null);
    try {
      const opts: { areas?: string[]; max_steps?: number; min_steps?: number } = {};
      if (areas && areas.length > 0) opts.areas = areas;
      if (maxSteps !== 100) opts.max_steps = maxSteps;
      // Only send min_steps when the user actively touched the field. If
      // untouched, the server computes the 60%-of-max_steps default — so
      // the default stays in one place and bumping max_steps on the server
      // automatically adjusts the floor for untouched clients.
      if (minStepsTouched) opts.min_steps = minSteps;
      const result = await api.triggerDiscovery(id, Object.keys(opts).length > 0 ? opts : undefined);
      if (result.run_id) {
        const newRun = await api.getRun(result.run_id);
        setRun(newRun);
      }
      const floor = minStepsTouched ? minSteps : Math.floor(maxSteps * 0.6);
      notifications.show({
        title: 'Discovery started',
        message: `${maxSteps} steps (min ${floor})`,
        color: 'blue',
      });
    } catch (e: unknown) {
      notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
    } finally {
      setTriggering(false);
      setSelectedAreas([]);
    }
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (!project) return <Shell><Text>Project not found</Text></Shell>;

  const isRunning = run && (run.status === 'running' || run.status === 'pending');
  const justFinished = run && (run.status === 'completed' || run.status === 'failed' || run.status === 'cancelled');
  const showRunPanel = isRunning || justFinished;

  // Aggregate stats
  const totalRuns = discoveries.length;
  const totalInsights = discoveries.reduce((sum, d) => sum + (d.summary?.total_insights || 0), 0);
  const totalRecs = discoveries.reduce((sum, d) => sum + (d.summary?.total_recommendations || 0), 0);
  const criticalCount = discoveries.reduce((sum, d) =>
    sum + (d.insights?.filter(i => i.severity === 'critical' || i.severity === 'high').length || 0), 0);
  const latestAgo = discoveries.length > 0
    ? formatTimeAgo(new Date(discoveries[0].discovery_date))
    : null;

  const breadcrumb = [
    { label: 'Projects', href: '/' },
    { label: project.name },
  ];

  const topBarActions = (
    <Menu shadow="md" width={280} disabled={!!isRunning}>
      <Menu.Target>
        <button style={{
          display: 'inline-flex', alignItems: 'center', gap: 6,
          background: 'var(--db-text-primary)', color: '#fff',
          border: 'none', borderRadius: 6, padding: '6px 14px',
          fontSize: 13, fontWeight: 500, cursor: 'pointer',
          fontFamily: 'inherit', opacity: isRunning ? 0.5 : 1,
          transition: 'background 120ms ease',
        }}
        onMouseEnter={e => { if (!isRunning) e.currentTarget.style.background = '#333'; }}
        onMouseLeave={e => { e.currentTarget.style.background = 'var(--db-text-primary)'; }}
        >
          <IconPlayerPlay size={14} />
          {isRunning ? 'Running...' : 'Run discovery'}
        </button>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>Exploration steps</Menu.Label>
        <div style={{ padding: '4px 12px 8px' }}>
          <NumberInput size="xs" value={maxSteps}
            onChange={(v) => {
              const next = Number(v) || 100;
              setMaxSteps(next);
              // Auto-track 60% of max_steps until the user customises the floor.
              if (!minStepsTouched) setMinSteps(Math.floor(next * 0.6));
            }}
            min={5} max={500} step={5} description="More steps = more comprehensive" />
        </div>
        <Menu.Label>Minimum steps</Menu.Label>
        <div style={{ padding: '4px 12px 8px' }}>
          <NumberInput size="xs" value={minSteps}
            onChange={(v) => {
              const next = Number(v);
              setMinSteps(Number.isFinite(next) && next >= 0 ? next : 0);
              setMinStepsTouched(true);
            }}
            min={0} max={maxSteps} step={5}
            error={minSteps > maxSteps ? `Cannot exceed ${maxSteps}` : undefined}
            description={minStepsTouched
              ? "Rejects premature done — 0 disables"
              : `Default: 60% of max (${Math.floor(maxSteps * 0.6)})`} />
        </div>
        <Menu.Item closeMenuOnClick={false}>
          <Checkbox label="Estimate cost before running" size="xs"
            checked={estimateFirst} onChange={(e) => setEstimateFirst(e.currentTarget.checked)} />
        </Menu.Item>
        <Menu.Divider />
        <Menu.Item onClick={() => handleRun()}>Run All Areas</Menu.Item>
        <Menu.Divider />
        <Menu.Label>Select areas</Menu.Label>
        {analysisAreas.map((area) => (
          <Menu.Item key={area.id} closeMenuOnClick={false}>
            <Checkbox label={area.name} checked={selectedAreas.includes(area.id)}
              onChange={(e) => {
                if (e.currentTarget.checked) setSelectedAreas([...selectedAreas, area.id]);
                else setSelectedAreas(selectedAreas.filter((a) => a !== area.id));
              }} />
          </Menu.Item>
        ))}
        {selectedAreas.length > 0 && (
          <>
            <Menu.Divider />
            <Menu.Item color="blue" onClick={() => handleRun(selectedAreas)}>
              Run Selected ({selectedAreas.length})
            </Menu.Item>
          </>
        )}
      </Menu.Dropdown>
    </Menu>
  );

  return (
    <Shell breadcrumb={breadcrumb} actions={topBarActions}>
      {/* Aggregate Stats Row */}
      {totalRuns > 0 && (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(4, 1fr)',
          gap: 12,
          marginBottom: 24,
        }}>
          <StatCard label="Total Runs" value={totalRuns} subtitle={latestAgo ? `Latest: ${latestAgo}` : undefined} />
          <StatCard label="Total Insights" value={totalInsights} subtitle={criticalCount > 0 ? `${criticalCount} critical or high` : undefined} />
          <StatCard label="Recommendations" value={totalRecs} valueColor="var(--db-green-text)" />
          <StatCard label="Queries Executed" value={discoveries.reduce((sum, d) => sum + (d.summary?.queries_executed || 0), 0)} />
        </div>
      )}

      {/* Cost Estimation */}
      {(estimating || estimate) && (
        <div style={{
          background: 'var(--db-bg-white)',
          border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius-lg)',
          padding: '16px 20px',
          marginBottom: 20,
        }}>
          {estimating ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <Loader size="sm" />
              <span style={{ fontSize: 13, color: 'var(--db-text-secondary)' }}>Estimating cost...</span>
            </div>
          ) : estimate && (
            <>
              <div style={{ fontSize: 15, fontWeight: 500, marginBottom: 12 }}>Cost Estimate</div>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, marginBottom: 16 }}>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4 }}>
                    LLM ({estimate.llm.provider})
                  </div>
                  <div style={{ fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums' }}>${estimate.llm.cost_usd.toFixed(4)}</div>
                  <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
                    ~{(estimate.llm.estimated_input_tokens / 1000).toFixed(0)}K in + {(estimate.llm.estimated_output_tokens / 1000).toFixed(0)}K out
                  </div>
                </div>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4 }}>
                    Warehouse ({estimate.warehouse.provider})
                  </div>
                  <div style={{ fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums' }}>${estimate.warehouse.cost_usd.toFixed(4)}</div>
                  <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
                    ~{estimate.warehouse.estimated_queries} queries, {(estimate.warehouse.estimated_bytes_scanned / (1024 * 1024)).toFixed(0)} MB
                  </div>
                </div>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4 }}>Total</div>
                  <div style={{ fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums', color: 'var(--db-blue-text)' }}>${estimate.total_cost_usd.toFixed(4)}</div>
                </div>
              </div>
              <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
                <GhostButton onClick={() => { setEstimate(null); setPendingAreas(undefined); }}>Cancel</GhostButton>
                <PrimaryButton onClick={() => handleTrigger(pendingAreas)} disabled={triggering}>
                  {triggering ? 'Starting...' : 'Confirm & Run'}
                </PrimaryButton>
              </div>
            </>
          )}
        </div>
      )}

      {/* Live Run Panel */}
      {showRunPanel && run && (
        <LiveRunPanel run={run} onCancel={async () => {
          if (justFinished) {
            dismissedRunId.current = run.id;
            setRun(null);
            return;
          }
          try {
            await api.cancelRun(run.id);
            setRun({ ...run, status: 'cancelled' });
            notifications.show({ title: 'Cancelled', message: 'Discovery cancelled', color: 'orange' });
          } catch (e: unknown) {
            notifications.show({ title: 'Error', message: (e as Error).message, color: 'red' });
          }
        }} />
      )}

      {/* Discovery Runs Section */}
      {discoveries.length > 0 && (
        <>
          <SectionHeader title="Discovery runs" count={discoveries.length} />
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {discoveries.map((d) => (
              <DiscoveryRunCard key={d.id} discovery={d} projectId={id} />
            ))}
          </div>
        </>
      )}

      {/* Empty State */}
      {!discoveries.length && !isRunning && !estimating && !estimate && (
        <div style={{
          background: 'var(--db-bg-white)',
          border: '2px dashed var(--db-border-strong)',
          borderRadius: 'var(--db-radius-lg)',
          padding: 48,
          textAlign: 'center',
        }}>
          <IconChartBar size={32} style={{ opacity: 0.3, marginBottom: 8 }} />
          <div style={{ fontSize: 15, fontWeight: 500, color: 'var(--db-text-secondary)', marginBottom: 4 }}>
            No discoveries yet
          </div>
          <div style={{ fontSize: 13, color: 'var(--db-text-tertiary)', marginBottom: 16 }}>
            Run your first discovery to see insights.
          </div>
          <PrimaryButton onClick={() => handleRun()}>Run your first discovery</PrimaryButton>
        </div>
      )}
    </Shell>
  );
}

/* ========== Stat Card ========== */

function StatCard({ label, value, subtitle, valueColor }: {
  label: string; value: number | string; subtitle?: string; valueColor?: string;
}) {
  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      padding: 16,
    }}>
      <div style={{
        fontSize: 11, fontWeight: 500, textTransform: 'uppercase',
        letterSpacing: '0.5px', color: 'var(--db-text-tertiary)', marginBottom: 4,
      }}>{label}</div>
      <div style={{
        fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums',
        color: valueColor || 'var(--db-text-primary)', lineHeight: 1.3,
      }}>{typeof value === 'number' ? value.toLocaleString() : value}</div>
      {subtitle && (
        <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginTop: 2 }}>{subtitle}</div>
      )}
    </div>
  );
}

/* ========== Section Header ========== */

function SectionHeader({ title, count }: { title: string; count?: number }) {
  return (
    <div style={{
      display: 'flex', alignItems: 'center', justifyContent: 'space-between',
      marginBottom: 12, marginTop: 8,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
        <span style={{ fontSize: 15, fontWeight: 500, color: 'var(--db-text-primary)' }}>{title}</span>
        {count !== undefined && (
          <span style={{ fontSize: 13, color: 'var(--db-text-tertiary)' }}>{count}</span>
        )}
      </div>
    </div>
  );
}

/* ========== Discovery Run Card ========== */

function DiscoveryRunCard({ discovery: d, projectId }: { discovery: DiscoveryResult; projectId: string }) {
  const insights = d.insights || [];
  const criticalCount = insights.filter(i => i.severity === 'critical').length;
  const highCount = insights.filter(i => i.severity === 'high').length;
  const topInsights = insights.slice(0, 3);

  return (
    <Link href={`/projects/${projectId}/discoveries/${d.id}`} style={{ textDecoration: 'none', color: 'inherit' }}>
      <div style={{
        background: 'var(--db-bg-white)',
        border: '1px solid var(--db-border-default)',
        borderRadius: 'var(--db-radius-lg)',
        padding: '16px 20px',
        cursor: 'pointer',
        transition: 'border-color 120ms ease, box-shadow 120ms ease',
      }}
      onMouseEnter={e => {
        e.currentTarget.style.borderColor = 'var(--db-border-strong)';
        e.currentTarget.style.boxShadow = '0 1px 3px rgba(0,0,0,0.04)';
      }}
      onMouseLeave={e => {
        e.currentTarget.style.borderColor = 'var(--db-border-default)';
        e.currentTarget.style.boxShadow = 'none';
      }}
      >
        {/* Row 1: Date + badges */}
        <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
          <div>
            <div style={{ fontSize: 14, fontWeight: 500 }}>
              {new Date(d.discovery_date).toLocaleDateString('en-US', {
                month: 'long', day: 'numeric', year: 'numeric',
              })} · {new Date(d.discovery_date).toLocaleTimeString('en-US', {
                hour: 'numeric', minute: '2-digit',
              })}
            </div>
            <div style={{ display: 'flex', gap: 4, marginTop: 4, alignItems: 'center', flexWrap: 'wrap' }}>
              <StatusBadge status={d.run_type === 'failed' ? 'Failed' : d.run_type === 'partial' ? 'Partial' : 'Complete'} />
              {d.areas_requested?.map(a => <AreaBadge key={a} area={a} />)}
              <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                {d.total_steps} queries · {d.duration || '—'}
              </span>
            </div>
          </div>
        </div>

        {/* Row 2: Stats */}
        <div style={{ display: 'flex', gap: 24, fontSize: 12, color: 'var(--db-text-secondary)' }}>
          <StatDot color="var(--db-blue-text)" text={`${d.summary?.total_insights || 0} insights`} />
          {criticalCount > 0 && <StatDot color="var(--db-red-text)" text={`${criticalCount} critical`} />}
          {highCount > 0 && <StatDot color="var(--db-severity-high-text)" text={`${highCount} high`} />}
          <StatDot color="var(--db-purple-text)" text={`${d.summary?.total_recommendations || 0} recommendations`} />
        </div>

        {/* Row 3: Preview */}
        {topInsights.length > 0 && (
          <div style={{ marginTop: 10, paddingTop: 10, borderTop: '1px solid var(--db-border-default)' }}>
            {topInsights.map((insight, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '2px 0', fontSize: 12, color: 'var(--db-text-secondary)' }}>
                <SeverityDot severity={insight.severity} />
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{insight.name}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </Link>
  );
}

/* ========== Live Run Panel ========== */

function LiveRunPanel({ run, onCancel }: { run: DiscoveryRunStatus; onCancel: () => void }) {
  const steps = run.steps || [];
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const userScrolledUp = useRef(false);
  const prevStepCount = useRef(0);

  useEffect(() => {
    if (steps.length > prevStepCount.current && !userScrolledUp.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
    prevStepCount.current = steps.length;
  }, [steps.length]);

  const isDone = run.status === 'completed' || run.status === 'failed' || run.status === 'cancelled';
  const phaseLabel: Record<string, string> = {
    init: 'Initializing', schema_discovery: 'schema_discovery',
    exploration: 'exploration', analysis: 'analysis',
    validation: 'validation', recommendations: 'recommendations',
    saving: 'saving', complete: 'complete',
  };

  const elapsed = run.started_at
    ? Math.round((new Date(run.updated_at || run.started_at).getTime() - new Date(run.started_at).getTime()) / 1000)
    : 0;

  return (
    <div style={{
      background: 'var(--db-bg-white)',
      border: '1px solid var(--db-border-default)',
      borderRadius: 'var(--db-radius-lg)',
      overflow: 'hidden',
      marginBottom: 20,
    }}>
      {/* Header */}
      <div style={{ padding: '16px 20px 0' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            {!isDone && (
              <span style={{
                width: 8, height: 8, borderRadius: '50%',
                background: 'var(--db-green-text)',
                animation: 'pulse-dot 1.5s ease-in-out infinite',
              }} />
            )}
            {isDone && run.status === 'completed' && <IconCheck size={16} color="var(--db-green-text)" />}
            {isDone && run.status === 'failed' && <IconX size={16} color="var(--db-red-text)" />}
            {isDone && run.status === 'cancelled' && <IconAlertTriangle size={16} color="var(--db-amber-text)" />}
            <span style={{ fontSize: 14, fontWeight: 500 }}>
              {isDone
                ? (run.status === 'completed' ? 'Discovery complete' : run.status === 'failed' ? 'Discovery failed' : 'Discovery cancelled')
                : 'Discovery running'}
            </span>
            <span style={{
              fontSize: 11, fontWeight: 500, padding: '2px 8px',
              borderRadius: 'var(--db-radius)',
              background: isDone
                ? (run.status === 'completed' ? 'var(--db-green-bg)' : 'var(--db-red-bg)')
                : 'var(--db-green-bg)',
              color: isDone
                ? (run.status === 'completed' ? 'var(--db-green-text)' : 'var(--db-red-text)')
                : 'var(--db-green-text)',
            }}>
              {phaseLabel[run.phase] || run.phase}
            </span>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>{run.progress}%</span>
            {!isDone && <GhostButton onClick={onCancel} small>Cancel</GhostButton>}
            {isDone && <GhostButton onClick={onCancel} small>Dismiss</GhostButton>}
          </div>
        </div>

        {/* Progress bar */}
        <div style={{
          height: 3, background: 'var(--db-bg-muted)', borderRadius: 2,
          marginTop: 10, overflow: 'hidden',
        }}>
          <div style={{
            height: '100%', borderRadius: 2,
            width: `${run.progress}%`,
            background: isDone
              ? (run.status === 'completed' ? 'var(--db-green-text)' : 'var(--db-red-text)')
              : 'var(--db-green-text)',
            transition: 'width 0.5s ease',
          }} />
        </div>

        {/* Stats row */}
        <div style={{
          display: 'flex', gap: 20, fontSize: 12, color: 'var(--db-text-secondary)',
          padding: '10px 0 14px', flexWrap: 'wrap',
        }}>
          <span>{run.total_queries} queries</span>
          <span>{run.insights_found} insights</span>
          <span>{formatElapsed(elapsed)}</span>
          <span style={{ color: 'var(--db-text-tertiary)' }}>
            Started: {new Date(run.started_at).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })}
          </span>
          {run.updated_at && (
            <span style={{ color: 'var(--db-text-tertiary)' }}>
              Updated: {new Date(run.updated_at).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })}
            </span>
          )}
          {isDone && run.completed_at && (
            <span style={{ color: 'var(--db-text-tertiary)' }}>
              Completed: {new Date(run.completed_at).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })}
            </span>
          )}
        </div>

        {run.error && (
          <div style={{ fontSize: 12, color: 'var(--db-red-text)', paddingBottom: 10 }}>{run.error}</div>
        )}
      </div>

      {/* Step list */}
      {steps.length > 0 && (
        <div style={{ borderTop: '1px solid var(--db-border-default)' }}>
          <ScrollArea h={400} type="auto" viewportRef={(el) => { scrollRef.current = el; }}
            onScrollPositionChange={() => {
              const el = scrollRef.current;
              if (!el) return;
              userScrolledUp.current = el.scrollHeight - el.scrollTop - el.clientHeight > 40;
            }}>
            {steps.map((step, idx) => (
              <StepRow key={idx} step={step} index={idx + 1} isLast={idx === steps.length - 1}
                isActive={!isDone && idx === steps.length - 1} />
            ))}
          </ScrollArea>
        </div>
      )}
    </div>
  );
}

/* ========== Step Row ========== */

function StepRow({ step, index, isLast, isActive }: {
  step: RunStep; index: number; isLast: boolean; isActive: boolean;
}) {
  const [opened, { toggle }] = useDisclosure(false);
  const isDone = !isActive;
  const hasDetails = isDone && (step.query || (step.llm_thinking && step.llm_thinking.length > 40));

  const stepTypeIcon = () => {
    if (step.type === 'insight') return <IconStack2 size={16} color="var(--db-green-text)" />;
    if (step.type === 'analysis') return <IconChartBar size={16} color="var(--db-blue-text)" />;
    if (step.type === 'recommendation') return <IconBulb size={16} color="var(--db-amber-text)" />;
    if (step.type === 'validation') return <IconShieldCheck size={16} color="var(--db-blue-text)" />;
    return <IconDatabase size={16} color="var(--db-blue-text)" />;
  };

  // Number circle colors
  const circleStyle = isActive
    ? { background: 'var(--db-blue-bg)', color: 'var(--db-blue-text)' }
    : isDone
      ? { background: 'var(--db-green-bg)', color: 'var(--db-green-text)' }
      : { background: 'var(--db-bg-muted)', color: 'var(--db-text-tertiary)' };

  const thinking = step.llm_thinking || '';
  const stepText = step.type === 'insight'
    ? (step.insight_name || step.message)
    : (thinking.length > 120 ? thinking.slice(0, 120) + '...' : thinking || step.message);

  return (
    <div style={{ borderBottom: isLast ? 'none' : '1px solid var(--db-border-default)' }}>
      <div style={{
        display: 'flex', alignItems: 'center', gap: 10,
        padding: '10px 20px', minHeight: 42,
        cursor: hasDetails ? 'pointer' : 'default',
        transition: 'background 120ms ease',
      }}
      onClick={hasDetails ? toggle : undefined}
      onMouseEnter={e => { if (hasDetails) e.currentTarget.style.background = 'var(--db-bg-muted)'; }}
      onMouseLeave={e => { e.currentTarget.style.background = 'transparent'; }}
      >
        {/* Expand arrow */}
        {hasDetails ? (
          <span style={{
            fontSize: 10, color: 'var(--db-text-tertiary)', width: 16, textAlign: 'center',
            transform: opened ? 'rotate(90deg)' : 'none', transition: 'transform 150ms',
            display: 'inline-block',
          }}>▶</span>
        ) : (
          <span style={{ width: 16 }} />
        )}

        {/* Number circle */}
        <span style={{
          width: 20, height: 20, borderRadius: '50%',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 11, fontWeight: 600, flexShrink: 0,
          ...circleStyle,
        }}>{index}</span>

        {/* Type icon */}
        <span style={{ flexShrink: 0, display: 'flex' }}>{stepTypeIcon()}</span>

        {/* Step text */}
        <span style={{
          flex: 1, fontSize: 13, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          color: isActive ? 'var(--db-text-primary)' : 'var(--db-text-secondary)',
          fontWeight: isActive ? 500 : 400,
        }}>
          {stepText}
        </span>

        {/* Right badges */}
        <div style={{ display: 'flex', gap: 4, marginLeft: 'auto', flexShrink: 0 }}>
          {isActive && <ResultBadge type="running">Running…</ResultBadge>}
          {isDone && step.row_count > 0 && <ResultBadge type="rows">{step.row_count} rows</ResultBadge>}
          {isDone && step.query_time_ms > 0 && <ResultBadge type="duration">{(step.query_time_ms / 1000).toFixed(2)}s</ResultBadge>}
          {isDone && step.type === 'insight' && step.insight_severity && (
            <ResultBadge type="insight">{step.insight_severity}</ResultBadge>
          )}
          {step.error && <ResultBadge type="error">Error</ResultBadge>}
        </div>
      </div>

      {/* Active step indicator */}
      {isActive && (
        <div style={{ padding: '0 20px 14px 66px', display: 'flex', alignItems: 'center', gap: 6 }}>
          <span style={{ display: 'flex', gap: 2 }}>
            {[0, 1, 2].map(i => (
              <span key={i} style={{
                width: 4, height: 4, borderRadius: '50%',
                background: 'var(--db-text-tertiary)',
                animation: `typing 1.2s infinite ${i * 0.2}s`,
              }} />
            ))}
          </span>
          <span style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
            {step.type === 'recommendation' ? 'Generating recommendations…' : 'Querying data warehouse…'}
          </span>
        </div>
      )}

      {/* Expanded detail */}
      {hasDetails && (
        <Collapse in={opened}>
          <div style={{ padding: '0 20px 14px 66px', fontSize: 13, lineHeight: 1.6, color: 'var(--db-text-secondary)' }}>
            {/* Step metadata */}
            <div style={{ display: 'flex', gap: 16, fontSize: 11, color: 'var(--db-text-tertiary)', marginBottom: 6 }}>
              {step.timestamp && (
                <span>At: {new Date(step.timestamp).toLocaleTimeString('en-US', { hour: 'numeric', minute: '2-digit', second: '2-digit' })}</span>
              )}
              {step.query_time_ms > 0 && <span>Query: {step.query_time_ms}ms</span>}
              {step.row_count > 0 && <span>Rows: {step.row_count}</span>}
              {step.query_fixed && <span style={{ color: 'var(--db-amber-text)' }}>Auto-fixed</span>}
            </div>
            {thinking.length > 40 && (
              <div style={{ fontStyle: 'italic', color: 'var(--db-text-tertiary)', marginBottom: 6 }}>{thinking}</div>
            )}
            {step.query && (
              <div style={{
                background: 'var(--db-bg-muted)', borderRadius: 6, padding: '10px 12px',
                fontFamily: 'SF Mono, Fira Code, monospace', fontSize: 12,
                whiteSpace: 'pre-wrap', wordBreak: 'break-all', marginTop: 6,
                maxHeight: 200, overflow: 'auto',
              }}>
                {step.query}
              </div>
            )}
            {step.query_result && (
              <div style={{ marginTop: 8, fontSize: 12 }}>{step.query_result}</div>
            )}
          </div>
        </Collapse>
      )}
    </div>
  );
}

/* ========== Small UI Components ========== */

function ResultBadge({ type, children }: { type: 'rows' | 'duration' | 'insight' | 'running' | 'error'; children: React.ReactNode }) {
  const styles: Record<string, { bg: string; color: string }> = {
    rows: { bg: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)' },
    duration: { bg: 'var(--db-bg-muted)', color: 'var(--db-text-tertiary)' },
    insight: { bg: 'var(--db-green-bg)', color: 'var(--db-green-text)' },
    running: { bg: 'var(--db-blue-bg)', color: 'var(--db-blue-text)' },
    error: { bg: 'var(--db-red-bg)', color: 'var(--db-red-text)' },
  };
  const s = styles[type];
  return (
    <span style={{
      fontSize: 10, fontWeight: 500, padding: '2px 7px', borderRadius: 4,
      background: s.bg, color: s.color, fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap',
    }}>{children}</span>
  );
}

function StatusBadge({ status }: { status: string }) {
  const map: Record<string, { bg: string; color: string }> = {
    Complete: { bg: 'var(--db-green-bg)', color: 'var(--db-green-text)' },
    Partial: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
    Failed: { bg: 'var(--db-red-bg)', color: 'var(--db-red-text)' },
  };
  const s = map[status] || map.Complete;
  return (
    <span style={{
      fontSize: 11, fontWeight: 500, padding: '1px 7px',
      borderRadius: 'var(--db-radius)', background: s.bg, color: s.color,
    }}>{status}</span>
  );
}

function AreaBadge({ area }: { area: string }) {
  return (
    <span style={{
      fontSize: 11, padding: '1px 7px', borderRadius: 'var(--db-radius)',
      background: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)',
    }}>{area}</span>
  );
}

function StatDot({ color, text }: { color: string; text: string }) {
  return (
    <span style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
      <span style={{ width: 6, height: 6, borderRadius: '50%', background: color, flexShrink: 0 }} />
      {text}
    </span>
  );
}

function SeverityDot({ severity }: { severity: string }) {
  const colors: Record<string, string> = {
    critical: 'var(--db-severity-critical-text)',
    high: 'var(--db-severity-high-text)',
    medium: 'var(--db-severity-medium-text)',
    low: 'var(--db-severity-low-text)',
  };
  return (
    <span style={{
      width: 6, height: 6, borderRadius: '50%', flexShrink: 0,
      background: colors[severity] || 'var(--db-text-tertiary)',
    }} />
  );
}

function PrimaryButton({ onClick, children, disabled }: { onClick: () => void; children: React.ReactNode; disabled?: boolean }) {
  return (
    <button onClick={onClick} disabled={disabled} style={{
      display: 'inline-flex', alignItems: 'center', gap: 6,
      background: 'var(--db-text-primary)', color: '#fff',
      border: 'none', borderRadius: 6, padding: '6px 14px',
      fontSize: 13, fontWeight: 500, cursor: disabled ? 'default' : 'pointer',
      fontFamily: 'inherit', opacity: disabled ? 0.5 : 1,
      transition: 'background 120ms ease',
    }}
    onMouseEnter={e => { if (!disabled) e.currentTarget.style.background = '#333'; }}
    onMouseLeave={e => { e.currentTarget.style.background = 'var(--db-text-primary)'; }}
    >
      {children}
    </button>
  );
}

function GhostButton({ onClick, children, small }: { onClick: () => void; children: React.ReactNode; small?: boolean }) {
  return (
    <button onClick={onClick} style={{
      display: 'inline-flex', alignItems: 'center', gap: 6,
      background: 'transparent', color: 'var(--db-text-secondary)',
      border: '1px solid var(--db-border-strong)', borderRadius: 6,
      padding: small ? '4px 10px' : '6px 14px',
      fontSize: small ? 12 : 13, fontWeight: 500, cursor: 'pointer',
      fontFamily: 'inherit', transition: 'all 120ms ease',
    }}
    onMouseEnter={e => {
      e.currentTarget.style.background = 'var(--db-bg-muted)';
      e.currentTarget.style.color = 'var(--db-text-primary)';
    }}
    onMouseLeave={e => {
      e.currentTarget.style.background = 'transparent';
      e.currentTarget.style.color = 'var(--db-text-secondary)';
    }}
    >
      {children}
    </button>
  );
}

/* ========== Helpers ========== */

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s elapsed`;
  const min = Math.floor(seconds / 60);
  const sec = seconds % 60;
  if (min < 60) return `${min}m ${sec}s elapsed`;
  const hr = Math.floor(min / 60);
  const remainMin = min % 60;
  return `${hr}h ${remainMin}m elapsed`;
}

function formatTimeAgo(date: Date): string {
  const diff = Date.now() - date.getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return 'Just now';
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}
