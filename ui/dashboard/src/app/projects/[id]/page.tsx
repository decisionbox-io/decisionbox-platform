'use client';

import { useEffect, useState, useCallback, useRef } from 'react';
import { useParams } from 'next/navigation';
import { useTranslations } from 'next-intl';
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
import { useFormat } from '@/lib/format';
import { SchemaIndexPanel } from '@/components/SchemaIndexPanel';
import { RunErrorIndicator } from '@/components/common/RunErrorIndicator';
import { UpcomingInvestigation } from '@/components/projects/UpcomingInvestigation';
import {
  api, ApiError, CostEstimate, DebugLogEntry, DiscoveryResult, DiscoveryRunStatus, Project, RunStep, SchemaIndexStatus,
  PROJECT_STATE_READY,
} from '@/lib/api';

// On DecisionBox Cloud, usage is billed in credits (not dollars), so the USD
// cost-estimate preview is hidden. The cloud tenant sets
// NEXT_PUBLIC_HIDE_COST_ESTIMATE=1; self-hosted leaves it unset and keeps the
// dollar estimate.
const HIDE_COST_ESTIMATE = process.env.NEXT_PUBLIC_HIDE_COST_ESTIMATE === '1';

// pollQuestionsNudge polls for clarifying questions after a run completes and
// toasts once when some appear. Question generation runs after the status flip
// (bounded by DISCOVERY_QUESTIONS_TIMEOUT), so a single immediate check usually
// races ahead of the insert; poll across the window and stop on the first hit.
async function pollQuestionsNudge(
  projectId: string,
  t: ReturnType<typeof useTranslations<'projectDetail'>>,
) {
  const MAX_ATTEMPTS = 20;
  const DELAY_MS = 10000;
  for (let i = 0; i < MAX_ATTEMPTS; i++) {
    try {
      const qs = await api.listProjectQuestions(projectId, { status: 'pending' });
      const n = qs?.length || 0;
      if (n > 0) {
        notifications.show({
          title: t('questionsAwaitTitle', { count: n }),
          message: t('questionsAwaitMessage'),
          color: 'blue',
        });
        return;
      }
    } catch (e) {
      // Community builds have no questions endpoint — a 404 is permanent, so
      // stop rather than retrying for the whole window.
      if (e instanceof ApiError && e.status === 404) return;
    }
    await new Promise((r) => setTimeout(r, DELAY_MS));
  }
}

export default function ProjectPage() {
  const t = useTranslations('projectDetail');
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
  // schemaIndexStatus is refreshed by SchemaIndexPanel's poll loop. When
  // status is not "ready" we disable the Run Discovery button because the
  // agent will 409 anyway (plan §8.4 — discovery is gated on the index).
  const [schemaIndexStatus, setSchemaIndexStatus] = useState<SchemaIndexStatus | null>(null);
  const [estimate, setEstimate] = useState<CostEstimate | null>(null);
  const [estimating, setEstimating] = useState(false);
  const [pendingAreas, setPendingAreas] = useState<string[] | undefined>(undefined);
  const [estimateFirst, setEstimateFirst] = useState(false);
  const dismissedRunId = useRef<string | null>(null);

  useEffect(() => {
    Promise.all([
      api.getProject(id).then((p) => {
        setProject(p);
        // Skip analysis-area lookup when the project has no
        // domain/category yet (a plugin-managed lifecycle state
        // may leave them empty). Without this guard the request
        // hits /api/v1/domains//categories//areas, which the Go
        // mux 301-redirects to a non-JSON HTML body and explodes
        // with "Unexpected non-whitespace character after JSON at
        // position 4".
        if (!p.domain || !p.category) return;
        return api.getAnalysisAreas(p.domain, p.category)
          .then((areas) => setAnalysisAreas((areas || []).map((a) => ({ id: a.id, name: a.name }))));
      }),
      api.listDiscoveries(id).then((d) => setDiscoveries(d || [])).catch(() => setDiscoveries([])),
    ])
      .catch((e) => notifications.show({ title: t('errorTitle'), message: e.message, color: 'red' }))
      .finally(() => setLoading(false));
  }, [id, t]);

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
          // Nudge the analyst if the run left clarifying questions to answer.
          // Generation is a best-effort step that runs AFTER completion (bounded
          // by DISCOVERY_QUESTIONS_TIMEOUT), so poll for a while rather than
          // firing one immediate query that would usually race ahead of the
          // insert and silently show nothing.
          if (newRun.status === 'completed') void pollQuestionsNudge(id, t);
        }
      }
    } catch { /* ignore */ }
  }, [id, run, t]);

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
      notifications.show({ title: t('estimationFailedTitle'), message: (e as Error).message, color: 'orange' });
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
        title: t('discoveryStartedTitle'),
        message: t('discoveryStartedMessage', { maxSteps, floor }),
        color: 'blue',
      });
    } catch (e: unknown) {
      notifications.show({ title: t('errorTitle'), message: (e as Error).message, color: 'red' });
    } finally {
      setTriggering(false);
      setSelectedAreas([]);
    }
  };

  if (loading) return <Shell><Loader /></Shell>;
  if (!project) return <Shell><Text>{t('projectNotFound')}</Text></Shell>;

  // Projects in any non-"ready" plugin-managed state hide the
  // discovery UI and show a placeholder. A plugin dashboard overlay
  // that owns the state can replace this whole page with its own
  // implementation.
  const projectReady = !project.state || project.state === PROJECT_STATE_READY;
  if (!projectReady) {
    const breadcrumbPg = [
      { label: t('breadcrumbProjects'), href: '/' },
      { label: project.name },
    ];
    return (
      <Shell breadcrumb={breadcrumbPg}>
        <Text>{t('pluginManagedState', { state: project.state ?? '' })}</Text>
      </Shell>
    );
  }

  const isRunning = run && (run.status === 'running' || run.status === 'pending');
  // Schema index must be "ready" (or legacy ready-by-default with a
  // warehouse configured) before discovery can proceed. Block the Run
  // button otherwise so users see the gate reason in the banner instead
  // of a 409 toast.
  const schemaReady = schemaIndexStatus
    ? schemaIndexStatus.status === 'ready'
    : (project.schema_index_status === 'ready' || project.schema_index_status === undefined);
  const triggerDisabled = !!isRunning || !schemaReady;
  const justFinished = run && (run.status === 'completed' || run.status === 'failed' || run.status === 'cancelled');
  const showRunPanel = isRunning || justFinished;

  // Aggregate stats
  const totalRuns = discoveries.length;
  const totalInsights = discoveries.reduce((sum, d) => sum + (d.summary?.total_insights || 0), 0);
  const totalRecs = discoveries.reduce((sum, d) => sum + (d.summary?.total_recommendations || 0), 0);
  const criticalCount = discoveries.reduce((sum, d) =>
    sum + (d.insights?.filter(i => i.severity === 'critical' || i.severity === 'high').length || 0), 0);
  const latestAgo = discoveries.length > 0
    ? formatTimeAgo(new Date(discoveries[0].discovery_date), t)
    : null;

  const breadcrumb = [
    { label: t('breadcrumbProjects'), href: '/' },
    { label: project.name },
  ];

  const topBarActions = (
    <Menu shadow="md" width={280} disabled={triggerDisabled}>
      <Menu.Target>
        <button style={{
          display: 'inline-flex', alignItems: 'center', gap: 6,
          background: 'var(--db-text-primary)', color: '#fff',
          border: 'none', borderRadius: 6, padding: '6px 14px',
          fontSize: 13, fontWeight: 500, cursor: 'pointer',
          fontFamily: 'inherit', opacity: triggerDisabled ? 0.5 : 1,
          transition: 'background 120ms ease',
        }}
        onMouseEnter={e => { if (!triggerDisabled) e.currentTarget.style.background = '#333'; }}
        onMouseLeave={e => { e.currentTarget.style.background = 'var(--db-text-primary)'; }}
        title={!schemaReady ? t('schemaNotReadyTooltip') : undefined}
        >
          <IconPlayerPlay size={14} />
          {isRunning ? t('runButtonRunning') : !schemaReady ? t('runButtonWaiting') : t('runButton')}
        </button>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{t('explorationStepsLabel')}</Menu.Label>
        <div style={{ padding: '4px 12px 8px' }}>
          <NumberInput size="xs" value={maxSteps}
            onChange={(v) => {
              const next = Number(v) || 100;
              setMaxSteps(next);
              // Auto-track 60% of max_steps until the user customises the floor.
              if (!minStepsTouched) setMinSteps(Math.floor(next * 0.6));
            }}
            min={5} max={500} step={5} description={t('explorationStepsDescription')} />
        </div>
        <Menu.Label>{t('minimumStepsLabel')}</Menu.Label>
        <div style={{ padding: '4px 12px 8px' }}>
          <NumberInput size="xs" value={minSteps}
            onChange={(v) => {
              const next = Number(v);
              setMinSteps(Number.isFinite(next) && next >= 0 ? next : 0);
              setMinStepsTouched(true);
            }}
            min={0} max={maxSteps} step={5}
            error={minSteps > maxSteps ? t('minStepsError', { max: maxSteps }) : undefined}
            description={minStepsTouched
              ? t('minStepsDescriptionTouched')
              : t('minStepsDescriptionDefault', { defaultValue: Math.floor(maxSteps * 0.6) })} />
        </div>
        <Menu.Item closeMenuOnClick={false}>
          <Checkbox label={t('estimateCostCheckbox')} size="xs"
            checked={estimateFirst} onChange={(e) => setEstimateFirst(e.currentTarget.checked)} />
        </Menu.Item>
        <Menu.Divider />
        <Menu.Item onClick={() => handleRun()}>{t('runAllAreas')}</Menu.Item>
        <Menu.Divider />
        <Menu.Label>{t('selectAreasLabel')}</Menu.Label>
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
              {t('runSelected', { count: selectedAreas.length })}
            </Menu.Item>
          </>
        )}
      </Menu.Dropdown>
    </Menu>
  );

  return (
    <Shell breadcrumb={breadcrumb} actions={topBarActions}>
      {/* Schema-index status banner — polls every 2s while indexing.
          hideWhenReady keeps the banner invisible on the discovery
          steady state (status=ready), since the panel only adds value
          when there's something to act on (indexing in flight,
          needs_reindex after Settings → Clear cache, failed /
          cancelled recovery). The Re-index entry point on the panel
          re-appears the moment status flips back to non-ready. */}
      <div style={{ marginBottom: 16 }}>
        <SchemaIndexPanel projectId={id} onStatusChange={setSchemaIndexStatus} hideWhenReady />
      </div>

      {/* Aggregate Stats Row */}
      {totalRuns > 0 && (
        <div style={{
          display: 'grid',
          gridTemplateColumns: 'repeat(4, 1fr)',
          gap: 12,
          marginBottom: 24,
        }}>
          <StatCard label={t('statTotalRuns')} value={totalRuns} subtitle={latestAgo ? t('statLatest', { ago: latestAgo }) : undefined} />
          <StatCard label={t('statTotalInsights')} value={totalInsights} subtitle={criticalCount > 0 ? t('statCriticalOrHigh', { count: criticalCount }) : undefined} />
          <StatCard label={t('statRecommendations')} value={totalRecs} valueColor="var(--db-green-text)" />
          <StatCard label={t('statQueriesExecuted')} value={discoveries.reduce((sum, d) => sum + (d.summary?.queries_executed || 0), 0)} />
        </div>
      )}

      {/* What's next — a compact preview of the open ledger threads +
          pending playbook changes carried into the next run. Renders
          nothing when the ledger is empty or the feature is off. */}
      <UpcomingInvestigation projectId={id} />

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
              <span style={{ fontSize: 13, color: 'var(--db-text-secondary)' }}>{t('estimatingCost')}</span>
            </div>
          ) : estimate && (
            <>
              {/* On DecisionBox Cloud usage is billed in credits, not dollars,
                  so the USD cost estimate is hidden (HIDE_COST_ESTIMATE) — the
                  managed pricing is the plan's per-operation credit price. */}
              {HIDE_COST_ESTIMATE ? (
                <div style={{ fontSize: 13, color: 'var(--db-text-secondary)', marginBottom: 16 }}>
                  {t('readyToRun')}
                </div>
              ) : (
                <>
                  <div style={{ fontSize: 15, fontWeight: 500, marginBottom: 12 }}>{t('costEstimate')}</div>
                  <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12, marginBottom: 16 }}>
                    <div>
                      <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4 }}>
                        {t('estimateLlmLabel', { provider: estimate.llm.provider })}
                      </div>
                      <div style={{ fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums' }}>${estimate.llm.cost_usd.toFixed(4)}</div>
                      <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
                        {t('estimateTokens', {
                          inTokens: (estimate.llm.estimated_input_tokens / 1000).toFixed(0),
                          outTokens: (estimate.llm.estimated_output_tokens / 1000).toFixed(0),
                        })}
                      </div>
                    </div>
                    <div>
                      <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4 }}>
                        {t('estimateWarehouseLabel', { provider: estimate.warehouse.provider })}
                      </div>
                      <div style={{ fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums' }}>${estimate.warehouse.cost_usd.toFixed(4)}</div>
                      <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>
                        {t('estimateQueries', {
                          queries: estimate.warehouse.estimated_queries,
                          mb: (estimate.warehouse.estimated_bytes_scanned / (1024 * 1024)).toFixed(0),
                        })}
                      </div>
                    </div>
                    <div>
                      <div style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 4 }}>{t('estimateTotalLabel')}</div>
                      <div style={{ fontSize: 22, fontWeight: 500, fontVariantNumeric: 'tabular-nums', color: 'var(--db-blue-text)' }}>${estimate.total_cost_usd.toFixed(4)}</div>
                    </div>
                  </div>
                </>
              )}
              <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
                <GhostButton onClick={() => { setEstimate(null); setPendingAreas(undefined); }}>{t('cancel')}</GhostButton>
                <PrimaryButton onClick={() => handleTrigger(pendingAreas)} disabled={triggering}>
                  {triggering ? t('starting') : t('confirmAndRun')}
                </PrimaryButton>
              </div>
            </>
          )}
        </div>
      )}

      {/* Live Run Panel — keyed on run.id so a new run cleanly remounts
          the panel (fresh steps state + cursor) instead of needing an
          in-component reset effect. */}
      {showRunPanel && run && (
        <LiveRunPanel key={run.id} run={run} onCancel={async () => {
          if (justFinished) {
            dismissedRunId.current = run.id;
            setRun(null);
            return;
          }
          try {
            await api.cancelRun(run.id);
            setRun({ ...run, status: 'cancelled' });
            notifications.show({ title: t('cancelledTitle'), message: t('cancelledMessage'), color: 'orange' });
          } catch (e: unknown) {
            notifications.show({ title: t('errorTitle'), message: (e as Error).message, color: 'red' });
          }
        }} />
      )}

      {/* Discovery Runs Section */}
      {discoveries.length > 0 && (
        <>
          <SectionHeader title={t('discoveryRunsTitle')} count={discoveries.length} />
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
            {t('emptyTitle')}
          </div>
          <div style={{ fontSize: 13, color: 'var(--db-text-tertiary)', marginBottom: 16 }}>
            {t('emptyDescription')}
          </div>
          <PrimaryButton onClick={() => handleRun()}>{t('runFirstDiscovery')}</PrimaryButton>
        </div>
      )}
    </Shell>
  );
}

/* ========== Stat Card ========== */

function StatCard({ label, value, subtitle, valueColor }: {
  label: string; value: number | string; subtitle?: string; valueColor?: string;
}) {
  const fmt = useFormat();
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
      }}>{typeof value === 'number' ? fmt.number(value) : value}</div>
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
  const t = useTranslations('projectDetail');
  const fmt = useFormat();
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
              {fmt.dateTime(d.discovery_date, {
                month: 'long', day: 'numeric', year: 'numeric',
              })} · {fmt.dateTime(d.discovery_date, {
                hour: 'numeric', minute: '2-digit',
              })}
            </div>
            <div style={{ display: 'flex', gap: 4, marginTop: 4, alignItems: 'center', flexWrap: 'wrap' }}>
              <StatusBadge status={d.run_type === 'failed' ? 'Failed' : d.run_type === 'partial' ? 'Partial' : 'Complete'} />
              {d.areas_requested?.map(a => <AreaBadge key={a} area={a} />)}
              <span style={{ fontSize: 11, color: 'var(--db-text-tertiary)' }}>
                {t('cardQueriesAndDuration', { queries: d.total_steps, duration: d.duration || '—' })}
              </span>
            </div>
          </div>
        </div>

        {/* Row 2: Stats */}
        <div style={{ display: 'flex', gap: 24, fontSize: 12, color: 'var(--db-text-secondary)' }}>
          <StatDot color="var(--db-blue-text)" text={t('dotInsights', { count: d.summary?.total_insights || 0 })} />
          {criticalCount > 0 && <StatDot color="var(--db-red-text)" text={t('dotCritical', { count: criticalCount })} />}
          {highCount > 0 && <StatDot color="var(--db-severity-high-text)" text={t('dotHigh', { count: highCount })} />}
          <StatDot color="var(--db-purple-text)" text={t('dotRecommendations', { count: d.summary?.total_recommendations || 0 })} />
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
  const t = useTranslations('projectDetail');
  const fmt = useFormat();
  // Per-step rows are no longer embedded in the run doc — they live in
  // discovery_run_steps and are streamed via api.listRunSteps with an
  // opaque ObjectID cursor (the last `id` we have). We poll while the
  // run is live and stop after it terminates. Polling is self-scheduling
  // (await-then-schedule) rather than setInterval, so a slow network
  // can never overlap two `since`-equal requests and produce duplicate
  // rows.
  const [steps, setSteps] = useState<RunStep[]>([]);
  const lastIDRef = useRef<string>('');
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const userScrolledUp = useRef(false);
  const prevStepCount = useRef(0);

  // The parent renders <LiveRunPanel key={run.id}> so a new run
  // remounts this component with fresh `steps` / `lastIDRef` state
  // automatically — no in-component reset effect needed (which the
  // react-hooks/set-state-in-effect lint rule rightly flags as a
  // cascading-render anti-pattern).

  useEffect(() => {
    let cancelled = false;
    let timer: number | null = null;
    const isTerminal = (s: string) => s === 'completed' || s === 'failed' || s === 'cancelled';
    const tick = async () => {
      try {
        const next = await api.listRunSteps(run.id, lastIDRef.current || undefined);
        if (cancelled) return;
        if (next.length) {
          lastIDRef.current = next[next.length - 1].id;
          setSteps(prev => prev.concat(next));
        }
      } catch {
        // Network blips are tolerable — the next tick will retry.
      }
      if (cancelled) return;
      // Self-scheduling: only arm the next poll AFTER this one
      // resolves, so we never have two listRunSteps calls in flight
      // sharing the same cursor.
      if (!isTerminal(run.status)) {
        timer = window.setTimeout(tick, 1500);
      }
    };
    tick();
    return () => {
      cancelled = true;
      if (timer !== null) window.clearTimeout(timer);
    };
  }, [run.id, run.status]);

  useEffect(() => {
    if (steps.length > prevStepCount.current && !userScrolledUp.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
    prevStepCount.current = steps.length;
  }, [steps.length]);

  const isDone = run.status === 'completed' || run.status === 'failed' || run.status === 'cancelled';
  const phaseLabel: Record<string, string> = {
    init: t('phaseInit'), schema_discovery: t('phaseSchemaDiscovery'),
    exploration: t('phaseExploration'), analysis: t('phaseAnalysis'),
    validation: t('phaseValidation'), recommendations: t('phaseRecommendations'),
    saving: t('phaseSaving'), complete: t('phaseComplete'),
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
                ? (run.status === 'completed' ? t('discoveryComplete') : run.status === 'failed' ? t('discoveryFailed') : t('discoveryCancelled'))
                : t('discoveryRunning')}
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
            {/* Errored run: collapse the raw error to a compact warning icon
                next to the status. Clicking it expands the full text; the icon
                itself stays put and survives refresh (it is derived from
                run.error, which the backend keeps on the run). */}
            <RunErrorIndicator errors={run.error} label={t('discoveryRunError')} />
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 12, color: 'var(--db-text-tertiary)' }}>{run.progress}%</span>
            {!isDone && <GhostButton onClick={onCancel} small>{t('cancel')}</GhostButton>}
            {isDone && <GhostButton onClick={onCancel} small>{t('dismiss')}</GhostButton>}
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
          <span>{t('statsQueries', { count: run.total_queries })}</span>
          <span>{t('statsInsights', { count: run.insights_found })}</span>
          <span>{formatElapsed(elapsed, t)}</span>
          <span style={{ color: 'var(--db-text-tertiary)' }}>
            {t('startedAt', { time: fmt.dateTime(run.started_at, { hour: 'numeric', minute: '2-digit', second: '2-digit' }) })}
          </span>
          {run.updated_at && (
            <span style={{ color: 'var(--db-text-tertiary)' }}>
              {t('updatedAt', { time: fmt.dateTime(run.updated_at, { hour: 'numeric', minute: '2-digit', second: '2-digit' }) })}
            </span>
          )}
          {isDone && run.completed_at && (
            <span style={{ color: 'var(--db-text-tertiary)' }}>
              {t('completedAt', { time: fmt.dateTime(run.completed_at, { hour: 'numeric', minute: '2-digit', second: '2-digit' }) })}
            </span>
          )}
        </div>
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

      {/* Debug log tail — rendered only when the per-project preference is
          on (set under Project Settings → Advanced). The high-level step
          list above only advances when the agent finishes a macro step,
          which can be minutes apart during schema discovery; this panel
          tails every LLM call and SQL execution in near-real time. */}
      <DebugLogsPanel projectId={run.project_id} runId={run.id} isDone={isDone} />
    </div>
  );
}

/* ========== Debug Logs Panel ========== */

// localStorage key for the per-project "show debug logs" preference. The
// toggle UI lives in Project Settings → Advanced; this panel just reads
// the value. Keyed by project ID so different projects can keep different
// defaults (e.g. the one you're debugging has it on, production is off).
export const debugLogsVisibleKey = (projectId: string) => `db:showDebugLogs:${projectId}`;

function DebugLogsPanel({ projectId, runId, isDone }: { projectId: string; runId: string; isDone: boolean }) {
  const t = useTranslations('projectDetail');
  // Read the preference fresh on mount. If it flips while the panel is
  // open (user toggled it in another tab), the `storage` event below
  // picks it up.
  const [visible, setVisible] = useState<boolean>(() => {
    if (typeof window === 'undefined') return false;
    return window.localStorage.getItem(debugLogsVisibleKey(projectId)) === '1';
  });
  const [entries, setEntries] = useState<DebugLogEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  // Polling uses the newest rendered `created_at` as the `since` cursor
  // on each request. We read it through a ref instead of including
  // `entries` in the effect deps — otherwise every successful poll
  // updates `entries`, which would re-run the effect, tear down the old
  // interval, and fire a fresh immediate poll, doubling the effective
  // rate.
  const sinceRef = useRef<string | undefined>(undefined);
  // Cap retained entries to keep the DOM small on long runs.
  const MAX_ENTRIES = 500;

  // Re-read the preference when the Settings tab (in a different browser
  // tab, or same tab after navigation) updates it. `storage` fires on
  // OTHER tabs; within the same tab we poll the key on focus.
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const onStorage = (e: StorageEvent) => {
      if (e.key === debugLogsVisibleKey(projectId)) {
        setVisible(e.newValue === '1');
        if (e.newValue !== '1') { setEntries([]); setError(null); sinceRef.current = undefined; }
      }
    };
    const onFocus = () => {
      const next = window.localStorage.getItem(debugLogsVisibleKey(projectId)) === '1';
      setVisible((prev) => {
        if (prev !== next) {
          if (!next) { setEntries([]); setError(null); sinceRef.current = undefined; }
        }
        return next;
      });
    };
    window.addEventListener('storage', onStorage);
    window.addEventListener('focus', onFocus);
    return () => {
      window.removeEventListener('storage', onStorage);
      window.removeEventListener('focus', onFocus);
    };
  }, [projectId]);

  useEffect(() => {
    if (!visible) {
      sinceRef.current = undefined;
      return;
    }
    let cancelled = false;

    const poll = async () => {
      // The server filters with $gt on created_at, so passing the newest
      // timestamp we already rendered gives us only what's new since the
      // last tick — safe to repeat as often as we like.
      try {
        const next = await api.getDebugLogs(runId, sinceRef.current, 200);
        if (cancelled) return;
        if (next && next.length > 0) {
          sinceRef.current = next[next.length - 1].created_at;
          setEntries((prev) => {
            const merged = [...prev, ...next];
            return merged.length > MAX_ENTRIES ? merged.slice(merged.length - MAX_ENTRIES) : merged;
          });
        }
        setError(null);
      } catch (e) {
        if (!cancelled) setError((e as Error).message);
      }
    };

    poll();
    // Stop polling once the run is terminal — no new events will arrive,
    // and a live run panel is typically dismissed soon after. Still allow
    // one final fetch above to pick up any trailing entries.
    if (isDone) return () => { cancelled = true; };

    const timer = setInterval(poll, 2000);
    return () => { cancelled = true; clearInterval(timer); };
  }, [visible, runId, isDone]);

  // Auto-scroll to latest unless the user scrolled up.
  const userScrolledUp = useRef(false);
  useEffect(() => {
    if (!visible) return;
    const el = scrollRef.current;
    if (el && !userScrolledUp.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [entries, visible]);

  if (!visible) return null;

  return (
    <div style={{ borderTop: '1px solid var(--db-border-default)', padding: '10px 20px' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
        <span style={{ fontSize: 12, fontWeight: 500, color: 'var(--db-text-secondary)' }}>
          {t('debugLogs')}
          <span style={{ color: 'var(--db-text-tertiary)', fontWeight: 400, marginLeft: 6 }}>
            {t('debugLogsSubtitle')}
          </span>
        </span>
        <Link href={`/projects/${projectId}/settings#advanced`}
          style={{ fontSize: 11, color: 'var(--db-text-tertiary)', textDecoration: 'none' }}>
          {t('hideInSettings')}
        </Link>
      </div>
      {error && (
        <div style={{ fontSize: 12, color: 'var(--db-red-text)', marginBottom: 6 }}>
          {t('debugLoadFailed', { error })}
        </div>
      )}
      <div
        ref={scrollRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          userScrolledUp.current = el.scrollHeight - el.scrollTop - el.clientHeight > 40;
        }}
        style={{
          maxHeight: 480,
          overflowY: 'auto',
          background: 'var(--db-bg-muted)',
          border: '1px solid var(--db-border-default)',
          borderRadius: 'var(--db-radius)',
          fontFamily: 'var(--db-font-mono, ui-monospace, SFMono-Regular, monospace)',
          fontSize: 11,
          lineHeight: 1.5,
        }}
      >
        {entries.length === 0 ? (
          <div style={{ padding: '10px 12px', color: 'var(--db-text-tertiary)' }}>
            {isDone ? t('noDebugEntries') : t('waitingForFirstEvent')}
          </div>
        ) : (
          entries.map((d) => <DebugLogRow key={d.id} entry={d} />)
        )}
      </div>
    </div>
  );
}

function DebugLogRow({ entry }: { entry: DebugLogEntry }) {
  const t = useTranslations('projectDetail');
  const fmt = useFormat();
  const [expanded, setExpanded] = useState(false);

  const ts = fmt.dateTime(entry.created_at, {
    hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit',
  });
  const err = entry.query_error || entry.error_message;
  const ok = entry.success && !err;
  const statusColor = ok ? 'var(--db-green-text)' : 'var(--db-red-text)';

  // Headline summary — one line, no wrap. Expanding reveals full text.
  const summary = (() => {
    if (err) return err;
    if (entry.operation === 'execute_query') {
      return entry.query_purpose || shortSQL(entry.sql_query) || t('queryFallback');
    }
    if (entry.operation === 'create_message') {
      const tokens = entry.llm_input_tokens || entry.llm_output_tokens
        ? `${entry.llm_input_tokens || 0}→${entry.llm_output_tokens || 0} tok`
        : '';
      const preview = firstLine(entry.llm_response);
      const model = entry.llm_model ? shortModel(entry.llm_model) : '';
      return [model, tokens, preview].filter(Boolean).join(' · ');
    }
    return `${entry.component || ''}${entry.phase ? ' / ' + entry.phase : ''}`;
  })();

  const hasDetails =
    (entry.llm_response && entry.llm_response.length > 0)
    || (entry.sql_query && entry.sql_query.length > 0)
    || (entry.sql_query_fixed && entry.sql_query_fixed.length > 0)
    || (err && err.length > 200);

  return (
    <div style={{ borderBottom: '1px solid var(--db-border-default)' }}>
      <div
        onClick={() => hasDetails && setExpanded((x) => !x)}
        style={{
          padding: '3px 12px',
          display: 'grid',
          gridTemplateColumns: 'auto 10px 14px auto auto 1fr auto',
          gap: 8,
          alignItems: 'baseline',
          cursor: hasDetails ? 'pointer' : 'default',
        }}
      >
        <span style={{ color: 'var(--db-text-tertiary)' }}>{ts}</span>
        <span style={{ color: 'var(--db-text-tertiary)', fontSize: 9 }}>
          {hasDetails ? (expanded ? '▾' : '▸') : ''}
        </span>
        <span style={{ color: statusColor }}>{ok ? '✓' : '✗'}</span>
        <span style={{ color: 'var(--db-text-secondary)', fontWeight: 500 }}>{entry.operation}</span>
        {entry.duration_ms !== undefined && entry.duration_ms > 0 ? (
          <span style={{ color: 'var(--db-text-tertiary)' }}>{entry.duration_ms}ms</span>
        ) : <span />}
        <span style={{
          color: err ? 'var(--db-red-text)' : 'var(--db-text-primary)',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
          whiteSpace: 'nowrap',
        }}>
          {summary}
        </span>
        {entry.row_count && entry.row_count > 0 ? (
          <span style={{ color: 'var(--db-text-tertiary)' }}>{t('rowsCount', { count: entry.row_count })}</span>
        ) : <span />}
      </div>
      {expanded && (
        <div style={{
          padding: '4px 12px 10px 34px',
          color: 'var(--db-text-secondary)',
          fontSize: 11,
          whiteSpace: 'pre-wrap',
          wordBreak: 'break-word',
        }}>
          {entry.sql_query && (
            <details open style={{ marginBottom: 6 }}>
              <summary style={{ cursor: 'pointer', color: 'var(--db-text-tertiary)', fontSize: 10, marginBottom: 2 }}>
                {entry.sql_query_fixed ? t('sqlOriginalRewritten') : t('sqlLabel')}
              </summary>
              <div style={{ background: 'var(--db-bg-white)', padding: 6, borderRadius: 3, border: '1px solid var(--db-border-default)' }}>
                {entry.sql_query}
              </div>
            </details>
          )}
          {entry.sql_query_fixed && (
            <details open style={{ marginBottom: 6 }}>
              <summary style={{ cursor: 'pointer', color: 'var(--db-text-tertiary)', fontSize: 10, marginBottom: 2 }}>
                {t('sqlExecutedAfterFix', { attempts: entry.fix_attempts || 0 })}
              </summary>
              <div style={{ background: 'var(--db-bg-white)', padding: 6, borderRadius: 3, border: '1px solid var(--db-border-default)' }}>
                {entry.sql_query_fixed}
              </div>
            </details>
          )}
          {entry.llm_response && (
            <details open>
              <summary style={{ cursor: 'pointer', color: 'var(--db-text-tertiary)', fontSize: 10, marginBottom: 2 }}>
                {entry.llm_model ? t('llmResponseWithModel', { model: entry.llm_model }) : t('llmResponse')}
              </summary>
              <div style={{ background: 'var(--db-bg-white)', padding: 6, borderRadius: 3, border: '1px solid var(--db-border-default)' }}>
                {entry.llm_response}
              </div>
            </details>
          )}
          {err && err.length > 200 && (
            <details open>
              <summary style={{ cursor: 'pointer', color: 'var(--db-red-text)', fontSize: 10, marginBottom: 2 }}>{t('errorLabel')}</summary>
              <div style={{ background: 'var(--db-red-bg)', padding: 6, borderRadius: 3, color: 'var(--db-red-text)' }}>
                {err}
              </div>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

// firstLine returns the first non-empty line of a string, trimmed and
// length-capped so it fits in a single row. We show it next to create_message
// entries so users can see what the LLM said without expanding every row.
function firstLine(s?: string): string {
  if (!s) return '';
  const line = s.split('\n').map(l => l.trim()).find(Boolean) || '';
  return line.length > 200 ? line.slice(0, 200) + '…' : line;
}

// shortSQL compresses whitespace to fit the SQL into one row when a purpose
// field isn't available. Full SQL is still shown when the row is expanded.
function shortSQL(s?: string): string {
  if (!s) return '';
  return s.replace(/\s+/g, ' ').trim().slice(0, 200);
}

// shortModel strips the provider-specific version prefix so model IDs like
// `global.anthropic.claude-opus-4-6-v1` render as just `claude-opus-4-6`.
function shortModel(m: string): string {
  const parts = m.split(/[.\/]/);
  return parts[parts.length - 1].replace(/-v\d+$/, '');
}

/* ========== Step Row ========== */

function StepRow({ step, index, isLast, isActive }: {
  step: RunStep; index: number; isLast: boolean; isActive: boolean;
}) {
  const t = useTranslations('projectDetail');
  const fmt = useFormat();
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
          {isActive && <ResultBadge type="running">{t('badgeRunning')}</ResultBadge>}
          {isDone && step.row_count > 0 && <ResultBadge type="rows">{t('rowsCount', { count: step.row_count })}</ResultBadge>}
          {isDone && step.query_time_ms > 0 && <ResultBadge type="duration">{t('badgeSeconds', { seconds: (step.query_time_ms / 1000).toFixed(2) })}</ResultBadge>}
          {isDone && ((step.input_tokens ?? 0) > 0 || (step.output_tokens ?? 0) > 0) && (
            <ResultBadge type="duration">
              {t('badgeInOutTokens', { in: step.input_tokens ?? 0, out: step.output_tokens ?? 0 })}
            </ResultBadge>
          )}
          {isDone && step.type === 'insight' && step.insight_severity && (
            <ResultBadge type="insight">{step.insight_severity}</ResultBadge>
          )}
          {step.error && <ResultBadge type="error">{t('errorLabel')}</ResultBadge>}
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
            {step.type === 'recommendation' ? t('generatingRecommendations') : t('queryingWarehouse')}
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
                <span>{t('metaAt', { time: fmt.dateTime(step.timestamp, { hour: 'numeric', minute: '2-digit', second: '2-digit' }) })}</span>
              )}
              {step.query_time_ms > 0 && <span>{t('metaQuery', { ms: step.query_time_ms })}</span>}
              {step.row_count > 0 && <span>{t('metaRows', { count: step.row_count })}</span>}
              {step.query_fixed && <span style={{ color: 'var(--db-amber-text)' }}>{t('metaAutoFixed')}</span>}
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
  const t = useTranslations('projectDetail');
  const map: Record<string, { bg: string; color: string }> = {
    Complete: { bg: 'var(--db-green-bg)', color: 'var(--db-green-text)' },
    Partial: { bg: 'var(--db-amber-bg)', color: 'var(--db-amber-text)' },
    Failed: { bg: 'var(--db-red-bg)', color: 'var(--db-red-text)' },
  };
  const labels: Record<string, string> = {
    Complete: t('statusComplete'),
    Partial: t('statusPartial'),
    Failed: t('statusFailed'),
  };
  const s = map[status] || map.Complete;
  return (
    <span style={{
      fontSize: 11, fontWeight: 500, padding: '1px 7px',
      borderRadius: 'var(--db-radius)', background: s.bg, color: s.color,
    }}>{labels[status] || labels.Complete}</span>
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

function formatElapsed(
  seconds: number,
  t: ReturnType<typeof useTranslations<'projectDetail'>>,
): string {
  if (seconds < 60) return t('elapsedSeconds', { seconds });
  const min = Math.floor(seconds / 60);
  const sec = seconds % 60;
  if (min < 60) return t('elapsedMinutes', { min, sec });
  const hr = Math.floor(min / 60);
  const remainMin = min % 60;
  return t('elapsedHours', { hr, remainMin });
}

function formatTimeAgo(
  date: Date,
  t: ReturnType<typeof useTranslations<'projectDetail'>>,
): string {
  const diff = Date.now() - date.getTime();
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return t('agoJustNow');
  if (minutes < 60) return t('agoMinutes', { minutes });
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return t('agoHours', { hours });
  const days = Math.floor(hours / 24);
  return t('agoDays', { days });
}
